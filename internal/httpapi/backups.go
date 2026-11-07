package httpapi

import (
	"archive/tar"
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/ittrail/sitebin.io/internal/provenance"
	"github.com/ittrail/sitebin.io/internal/snapshots"
	"github.com/ittrail/sitebin.io/internal/store"
)

// A site's Backups tab: the versions of its files the instance's snapshots
// hold, each downloadable as a ZIP or restorable. The snapshots are read
// through the agent on the host (SITEBIN_SNAPSHOTS_AGENT); without one,
// every route here is a 404. See
// docs/superpowers/specs/2026-11-07-snapshots-and-restore-design.md.

// backupVersion is a version as the API answers it.
type backupVersion struct {
	Snapshot string    `json:"snapshot"`
	At       time.Time `json:"at"`
	First    time.Time `json:"first"`
	Kind     string    `json:"kind"`
	Files    int       `json:"files"`
	Bytes    int64     `json:"bytes"`
	// Current: the site's files now are these (same paths, sizes and times).
	Current bool `json:"current"`
}

func (a *API) backupsOff(w http.ResponseWriter) bool {
	if a.snap == nil {
		writeError(w, 404, "backups are not available on this instance")
		return true
	}
	return false
}

// ownerSince is when the site's current owner got it: Meta.OwnerSince, else
// (sites transferred before it existed) the newest transfer or adoption in
// its log. A log that cannot be read answers now — nothing is offered
// rather than something of the previous owner's.
func (a *API) ownerSince(site *store.Site) time.Time {
	if site.Meta.OwnerSince != nil {
		return *site.Meta.OwnerSince
	}
	entries, err := a.st.Provenance(site)
	if err != nil {
		a.log.Error("backups: provenance unreadable", "id", site.ViewID, "err", err)
		return time.Now()
	}
	var t time.Time
	for _, e := range entries {
		if e.Action != provenance.ActionTransfer && e.Action != provenance.ActionAdopt {
			continue
		}
		at := e.Time
		if e.Last != nil {
			at = *e.Last
		}
		if at.After(t) {
			t = at
		}
	}
	return t
}

// offered answers the versions of the site this caller may see: none from
// before its current owner got it.
func (a *API) offered(ctx context.Context, site *store.Site) ([]snapshots.Version, int, error) {
	vs, pending, err := a.snap.Versions(ctx, site.ViewID)
	if err != nil {
		return nil, 0, err
	}
	since := a.ownerSince(site)
	out := vs[:0]
	for _, v := range vs {
		if !v.At.Before(since) {
			out = append(out, v)
		}
	}
	return out, pending, nil
}

// offeredVersion answers the offered version whose snapshot is id.
func (a *API) offeredVersion(ctx context.Context, site *store.Site, id string) (snapshots.Version, error) {
	vs, _, err := a.offered(ctx, site)
	if err != nil {
		return snapshots.Version{}, err
	}
	for _, v := range vs {
		if v.Snapshot == id {
			return v, nil
		}
	}
	return snapshots.Version{}, snapshots.ErrNotFound
}

// liveFingerprint sums the site's files/ as the agent sums a snapshot's.
func liveFingerprint(site *store.Site) (string, error) {
	var f snapshots.Fingerprinter
	root, err := store.OpenServedRoot(site)
	if errors.Is(err, fs.ErrNotExist) {
		return f.Sum(), nil
	}
	if err != nil {
		return "", err
	}
	defer root.Close()
	err = fs.WalkDir(root.FS(), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.Type().IsRegular() || snapshots.IsMarker(p) {
			return nil
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		f.Add(p, fi.Size(), fi.ModTime())
		return nil
	})
	return f.Sum(), err
}

// agentFailed answers an error of the agent.
func (a *API) agentFailed(w http.ResponseWriter, site *store.Site, err error) {
	if errors.Is(err, snapshots.ErrNotFound) {
		writeError(w, 404, "no such version of this site")
		return
	}
	a.log.Error("backups: agent", "id", site.ViewID, "err", err)
	writeError(w, 502, "the backups cannot be read right now; try again in a minute")
}

func (a *API) listBackups(w http.ResponseWriter, r *http.Request, site *store.Site) {
	if a.backupsOff(w) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	vs, pending, err := a.offered(ctx, site)
	if err != nil {
		a.agentFailed(w, site, err)
		return
	}
	cur, err := liveFingerprint(site)
	if err != nil {
		a.log.Warn("backups: fingerprint", "id", site.ViewID, "err", err)
	}
	out := make([]backupVersion, 0, len(vs))
	for _, v := range vs {
		out = append(out, backupVersion{Snapshot: v.Snapshot, At: v.At, First: v.First, Kind: v.Kind,
			Files: v.Files, Bytes: v.Bytes, Current: cur != "" && v.Fingerprint == cur})
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, map[string]any{"available": true, "versions": out, "pending": pending})
}

// snapshotLayout reads the site's meta.json in the snapshot: the folder of
// files/ its content was in, and whether it ran in Container mode.
func (a *API) snapshotLayout(ctx context.Context, site *store.Site, id string) (root string, container bool, err error) {
	b, err := a.snap.Meta(ctx, id, site.ViewID)
	if err != nil {
		return "", false, err
	}
	var m struct {
		Mode string `json:"mode"`
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return "", false, fmt.Errorf("the snapshot's meta.json: %w", err)
	}
	root = "sites/" + site.ViewID + "/files/"
	if m.Mode == store.ModeViewer {
		root += "_raw/"
	}
	return root, m.Mode == store.ModeContainer, nil
}

// eachFile calls fn for every regular file of the version's tar under root,
// with its path relative to root — Sitebin's own markers left out (a site
// keeps its own, as on every replace). A stream cut short is an error.
func eachFile(tr *tar.Reader, root string, fn func(rel string, h *tar.Header, r io.Reader) error) error {
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("the snapshot's files: %w", err)
		}
		if h.Typeflag != tar.TypeReg {
			continue
		}
		name := strings.TrimPrefix(path.Clean("/" + strings.TrimPrefix(h.Name, "./"))[1:], "/")
		rel, ok := strings.CutPrefix(name, root)
		if !ok || rel == "" || snapshots.IsMarker(rel) {
			continue
		}
		if err := fn(rel, h, tr); err != nil {
			return err
		}
	}
}

func (a *API) downloadBackup(w http.ResponseWriter, r *http.Request, site *store.Site) {
	if a.backupsOff(w) {
		return
	}
	ctx := r.Context()
	id := r.PathValue("snapshot")
	if _, err := a.offeredVersion(ctx, site, id); err != nil {
		a.agentFailed(w, site, err)
		return
	}
	root, _, err := a.snapshotLayout(ctx, site, id)
	if err != nil {
		a.agentFailed(w, site, err)
		return
	}
	body, err := a.snap.Files(ctx, id, site.ViewID)
	if err != nil {
		a.agentFailed(w, site, err)
		return
	}
	defer body.Close()
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="`+site.ViewID+"-"+id+`.zip"`)
	w.Header().Set("Cache-Control", "no-store")
	zw := zip.NewWriter(w)
	err = eachFile(tar.NewReader(body), root, func(rel string, h *tar.Header, rd io.Reader) error {
		f, err := zw.CreateHeader(&zip.FileHeader{Name: rel, Method: zip.Deflate, Modified: h.ModTime})
		if err != nil {
			return err
		}
		_, err = io.Copy(f, rd)
		return err
	})
	if err == nil {
		err = zw.Close()
	}
	if err != nil {
		// The ZIP has begun; cut short, it is unreadable, not wrong.
		a.log.Error("backups: download", "id", site.ViewID, "snapshot", id, "err", err)
		panic(http.ErrAbortHandler)
	}
}

// restoreBackup replaces the site's files with a version's, through the
// staged replace every upload uses. The work runs on a context the client
// leaving does not end: a container project must be running again after it.
func (a *API) restoreBackup(w http.ResponseWriter, r *http.Request, site *store.Site) {
	if a.backupsOff(w) {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 45*time.Minute)
	defer cancel()
	id := r.PathValue("snapshot")
	v, err := a.offeredVersion(ctx, site, id)
	if err != nil {
		a.agentFailed(w, site, err)
		return
	}
	root, snapContainer, err := a.snapshotLayout(ctx, site, id)
	if err != nil {
		a.agentFailed(w, site, err)
		return
	}
	if isContainer := site.Meta.Mode == store.ModeContainer; snapContainer != isContainer {
		if snapContainer {
			writeError(w, 409, "This version is of the site in Container mode: its folders hold the app's data. Switch the site to Container mode first, then restore it.")
		} else {
			writeError(w, 409, "This version is of the site before it ran in Container mode. Switch the site to Static first, then restore it.")
		}
		return
	}
	// Every restore can be undone: the files it replaces become a snapshot
	// first. No snapshot, no restore.
	taken, err := a.snap.BackupNow(ctx)
	if err != nil {
		a.log.Error("backups: snapshot before restore", "id", site.ViewID, "err", err)
		writeError(w, 503, "A snapshot of the current files could not be taken first, so nothing was restored. Try again in a few minutes.")
		return
	}
	rep, err := a.st.BeginReplace(site)
	if err != nil {
		respondErr(w, err)
		return
	}
	defer rep.Abort()
	body, err := a.snap.Files(ctx, id, site.ViewID)
	if err != nil {
		a.agentFailed(w, site, err)
		return
	}
	times := map[string]time.Time{}
	err = eachFile(tar.NewReader(body), root, func(rel string, h *tar.Header, rd io.Reader) error {
		if err := rep.SaveFile(rel, rd); err != nil {
			return err
		}
		times[rel] = h.ModTime
		return nil
	})
	body.Close()
	if err != nil {
		a.log.Error("backups: restore stream", "id", site.ViewID, "snapshot", id, "err", err)
		if errors.Is(err, store.ErrTooLarge) || errors.Is(err, store.ErrTooManyFiles) || errors.Is(err, store.ErrBadPath) {
			respondErr(w, err)
			return
		}
		writeError(w, 502, "The version could not be read completely, so nothing was restored.")
		return
	}

	// A container project stops only now, right before its files change,
	// and runs again afterwards whatever happens.
	if site.Meta.Mode == store.ModeContainer {
		if rt, ok := containerRuntime(); ok {
			was := site.Meta.Container != nil && site.Meta.Container.Enabled
			if err := a.st.Update(site, func(m *store.Meta) error {
				if m.Container != nil {
					m.Container.Enabled = false
				}
				return nil
			}); err != nil {
				respondErr(w, err)
				return
			}
			if was {
				defer a.startAfterRestore(site, rt.Kick)
			}
			if err := rt.Stop(site.ViewID); err != nil {
				a.log.Error("backups: stop before restore", "id", site.ViewID, "err", err)
				writeError(w, 503, "The project could not be stopped, so nothing was restored.")
				return
			}
		}
	}
	if err := rep.Commit(); err != nil {
		noteOf(r).held = errors.Is(err, store.ErrHeld)
		respondErr(w, err)
		return
	}
	a.keepTimes(site, times)
	if err := a.syncViewerLayout(site); err != nil {
		respondErr(w, err)
		return
	}
	note := noteOf(r)
	note.files = len(times)
	note.detail = v.Kind + " snapshot of " + v.At.UTC().Format("2006-01-02 15:04 UTC")
	a.log.Info("site restored", "id", site.ViewID, "snapshot", id, "files", len(times), "before", taken.ID)
	resp := a.payloadFor(r, site)
	resp["restored"] = map[string]any{"snapshot": id, "at": v.At, "files": len(times), "before": taken.ID}
	writeJSON(w, 200, resp)
}

// startAfterRestore runs a container project again, as Start does — unless
// its plan no longer allows containers, which leaves it stopped.
func (a *API) startAfterRestore(site *store.Site, kick func(string)) {
	if err := containerModeAllowed(site); err != nil {
		a.log.Warn("backups: project stays stopped after restore", "id", site.ViewID, "err", err)
		return
	}
	err := a.st.Update(site, func(m *store.Meta) error {
		if m.Container == nil {
			m.Container = &store.ContainerMeta{}
		}
		m.Container.Enabled = true
		m.Container.RestartSeq++
		m.Container.Status = store.ContainerStarting
		m.Container.Message = ""
		return nil
	})
	if err != nil {
		a.log.Error("backups: start after restore", "id", site.ViewID, "err", err)
		return
	}
	kick(site.ViewID)
}

// keepTimes gives the restored files their times from the snapshot, so the
// version restored is the current one again. Best effort: a time that cannot
// be set only costs the "current" mark.
func (a *API) keepTimes(site *store.Site, times map[string]time.Time) {
	root, err := store.OpenContentRoot(site)
	if err != nil {
		return
	}
	defer root.Close()
	for rel, t := range times {
		root.Chtimes(rel, t, t)
	}
}
