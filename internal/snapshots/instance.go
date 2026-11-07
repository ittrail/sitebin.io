package snapshots

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ittrail/sitebin.io/internal/analytics"
)

// RestoreState is where the instance restore is. It is kept on disk, so the
// instance — which the restore stops — reads it once it runs again.
type RestoreState struct {
	State    string    `json:"state"` // idle preparing extracting stopping swapping starting done failed rolled-back
	Snapshot string    `json:"snapshot,omitempty"`
	Copy     string    `json:"copy,omitempty"` // restoring a kept copy instead of a snapshot
	Started  time.Time `json:"started,omitzero"`
	Finished time.Time `json:"finished,omitzero"`
	Message  string    `json:"message,omitempty"`
	// Previous is the copy the data from before the restore was kept as.
	Previous string `json:"previous,omitempty"`
}

func (s RestoreState) running() bool {
	switch s.State {
	case "preparing", "extracting", "stopping", "swapping", "starting":
		return true
	}
	return false
}

// diskFree is analytics.DiskFree, swappable for a test.
var diskFree = analytics.DiskFree

// restoreHeadroom is the free space a restore keeps beyond the snapshot.
const restoreHeadroom = 1 << 30

func (a *Agent) restorePath() string { return filepath.Join(a.cfg.StateDir, "restore.json") }

func (a *Agent) saveRestore() {
	os.MkdirAll(a.cfg.StateDir, 0o700)
	b, _ := json.MarshalIndent(a.restore, "", "  ")
	tmp := a.restorePath() + ".tmp"
	if os.WriteFile(tmp, b, 0o600) == nil {
		os.Rename(tmp, a.restorePath())
	}
}

func (a *Agent) restoreState() RestoreState {
	a.restoreMu.Lock()
	defer a.restoreMu.Unlock()
	return a.restore
}

// step moves the restore to state and keeps it.
func (a *Agent) step(state, msg string) {
	a.restoreMu.Lock()
	defer a.restoreMu.Unlock()
	a.restore.State = state
	if msg != "" {
		a.restore.Message = msg
	}
	if !a.restore.running() {
		a.restore.Finished = a.cfg.Now().UTC()
	}
	a.saveRestore()
	a.cfg.Log.Info("instance restore", "state", state, "msg", msg)
}

// Copy is a kept data tree: the data before a restore, or a restored tree
// that did not come up.
type Copy struct {
	Name string    `json:"name"`
	Kind string    `json:"kind"` // before-restore, failed-restore
	At   time.Time `json:"at"`
}

func (a *Agent) parent() (string, string) {
	return filepath.Dir(a.cfg.DataDir), filepath.Base(a.cfg.DataDir)
}

const stampLayout = "20060102T150405Z"

// freeName answers <base>.<kind>-<stamp> for the first second from t on that
// names nothing in dir yet: two restores within one second (a switch back
// right after a restore) must not collide on a copy's name.
func freeName(dir, base, kind string, t time.Time) string {
	for i := 0; ; i++ {
		n := base + "." + kind + "-" + t.Add(time.Duration(i)*time.Second).UTC().Format(stampLayout)
		if _, err := os.Lstat(filepath.Join(dir, n)); errors.Is(err, os.ErrNotExist) || i >= 3600 {
			return n
		}
	}
}

// copies lists the kept trees beside the data directory, newest first.
func (a *Agent) copies() []Copy {
	dir, base := a.parent()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []Copy
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		for _, kind := range []string{"before-restore", "failed-restore"} {
			stamp, ok := strings.CutPrefix(e.Name(), base+"."+kind+"-")
			if !ok {
				continue
			}
			at, err := time.Parse(stampLayout, stamp)
			if err != nil {
				continue
			}
			out = append(out, Copy{Name: e.Name(), Kind: kind, At: at})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].At.After(out[j].At) })
	return out
}

func (a *Agent) findCopy(name string) (Copy, bool) {
	for _, c := range a.copies() {
		if c.Name == name {
			return c, true
		}
	}
	return Copy{}, false
}

// pruneCopies removes kept trees older than copyKeep, and staging trees a
// crashed extraction left more than a day ago. Never while a restore runs.
func (a *Agent) pruneCopies() {
	if a.restoreState().running() {
		return
	}
	now := a.cfg.Now()
	dir, base := a.parent()
	for _, c := range a.copies() {
		if now.Sub(c.At) > copyKeep {
			if err := os.RemoveAll(filepath.Join(dir, c.Name)); err == nil {
				a.cfg.Log.Info("instance restore: removed an expired copy", "copy", c.Name)
			}
		}
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		stamp, ok := strings.CutPrefix(e.Name(), base+".restore-")
		if !ok || !e.IsDir() {
			continue
		}
		if at, err := time.Parse(stampLayout, stamp); err == nil && now.Sub(at) > 24*time.Hour {
			os.RemoveAll(filepath.Join(dir, e.Name()))
		}
	}
}

func (a *Agent) handleCopies(w http.ResponseWriter, r *http.Request) {
	c := a.copies()
	if c == nil {
		c = []Copy{}
	}
	writeJSON(w, 200, c)
}

func (a *Agent) handleDeleteCopy(w http.ResponseWriter, r *http.Request) {
	c, ok := a.findCopy(r.PathValue("name"))
	if !ok {
		writeErr(w, 404, "no such copy")
		return
	}
	if a.restoreState().running() {
		writeErr(w, 409, "a restore is running")
		return
	}
	dir, _ := a.parent()
	if err := os.RemoveAll(filepath.Join(dir, c.Name)); err != nil {
		a.cfg.Log.Error("instance restore: delete copy", "copy", c.Name, "err", err)
		writeErr(w, 500, "the copy could not be deleted")
		return
	}
	a.cfg.Log.Info("instance restore: copy deleted", "copy", c.Name)
	writeJSON(w, 200, map[string]string{"status": "deleted"})
}

func (a *Agent) handleRestoreState(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, a.restoreState())
}

func (a *Agent) handleInstanceRestore(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Snapshot string `json:"snapshot"`
		Copy     string `json:"copy"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&body); err != nil || (body.Snapshot == "") == (body.Copy == "") {
		writeErr(w, 400, `body must be {"snapshot": "<id>"} or {"copy": "<name>"}`)
		return
	}
	var src Snapshot
	var cp Copy
	if body.Snapshot != "" {
		s, err := a.find(r.Context(), body.Snapshot)
		if err != nil {
			a.failed(w, err)
			return
		}
		src = s
	} else {
		c, ok := a.findCopy(body.Copy)
		if !ok {
			writeErr(w, 404, "no such copy")
			return
		}
		cp = c
	}
	a.restoreMu.Lock()
	if a.restore.running() {
		a.restoreMu.Unlock()
		writeErr(w, 409, "a restore is already running")
		return
	}
	a.restore = RestoreState{State: "preparing", Snapshot: src.ID, Copy: cp.Name, Started: a.cfg.Now().UTC()}
	a.saveRestore()
	st := a.restore
	a.restoreMu.Unlock()
	go a.runRestore(context.Background(), src, cp)
	writeJSON(w, 202, st)
}

// runRestore restores the instance to snapshot src, or to the kept copy cp:
// the new tree is made beside the data directory, the instance is stopped
// with every container of its sites, the two trees swap names, and the
// instance starts again — switching back when it does not become healthy.
func (a *Agent) runRestore(ctx context.Context, src Snapshot, cp Copy) {
	dir, base := a.parent()
	now := a.cfg.Now()
	var incoming string // the tree that becomes the data directory
	if cp.Name != "" {
		incoming = filepath.Join(dir, cp.Name)
	} else {
		var size int64
		if m, ok := a.idx.Get(src.ID); ok {
			for _, s := range m {
				size += s.Bytes
			}
		} else {
			a.cfg.Source.Walk(ctx, src.ID, "", func(it Item) error { size += it.Size; return nil })
		}
		if free, err := diskFree(dir); err == nil && free < uint64(size)+restoreHeadroom {
			a.step("failed", fmt.Sprintf("Not enough space: the snapshot needs about %d MB, %d MB are free. Nothing was changed.", (size+restoreHeadroom)>>20, free>>20))
			return
		}
		a.step("extracting", "")
		incoming = filepath.Join(dir, freeName(dir, base, "restore", now))
		if err := a.extract(ctx, src.ID, incoming); err != nil {
			os.RemoveAll(incoming)
			a.cfg.Log.Error("instance restore: extract", "err", err)
			a.step("failed", "The snapshot could not be read: "+err.Error()+". Nothing was changed.")
			return
		}
	}
	unlock, err := lockFile(a.cfg.BackupLock, 30*time.Minute)
	if err != nil {
		if cp.Name == "" {
			os.RemoveAll(incoming)
		}
		a.step("failed", "A backup run held its lock for too long: "+err.Error()+". Nothing was changed.")
		return
	}
	defer unlock()

	a.step("stopping", "")
	if err := a.cfg.Docker.Stop(ctx, a.cfg.Container); err != nil {
		if cp.Name == "" {
			os.RemoveAll(incoming)
		}
		a.cfg.Docker.Start(ctx, a.cfg.Container)
		a.step("failed", "The instance could not be stopped: "+err.Error()+". Nothing was changed.")
		return
	}
	if n, err := a.cfg.Docker.RemoveManaged(ctx); err != nil {
		a.cfg.Log.Warn("instance restore: removing site containers", "err", err, "removed", n)
	}

	a.step("swapping", "")
	previous := freeName(dir, base, "before-restore", now)
	if err := os.Rename(a.cfg.DataDir, filepath.Join(dir, previous)); err != nil {
		a.cfg.Docker.Start(ctx, a.cfg.Container)
		a.step("failed", "The data directory could not be moved aside: "+err.Error()+". The instance runs on its data as before.")
		return
	}
	if err := os.Rename(incoming, a.cfg.DataDir); err != nil {
		os.Rename(filepath.Join(dir, previous), a.cfg.DataDir)
		a.cfg.Docker.Start(ctx, a.cfg.Container)
		a.step("failed", "The restored tree could not be put in place: "+err.Error()+". The instance runs on its data as before.")
		return
	}
	a.restoreMu.Lock()
	a.restore.Previous = previous
	a.restoreMu.Unlock()

	a.step("starting", "")
	if err := a.startHealthy(ctx); err == nil {
		what := "the snapshot of " + src.At.UTC().Format("2 Jan 2006, 15:04 UTC")
		if cp.Name != "" {
			what = "the copy " + cp.Name
		}
		a.step("done", "The instance runs on "+what+". The data from before is kept as "+previous+" for 30 days.")
		return
	} else {
		a.cfg.Log.Error("instance restore: not healthy, switching back", "err", err)
		a.cfg.Docker.Stop(ctx, a.cfg.Container)
		a.cfg.Docker.RemoveManaged(ctx)
		failed := freeName(dir, base, "failed-restore", now)
		os.Rename(a.cfg.DataDir, filepath.Join(dir, failed))
		if err2 := os.Rename(filepath.Join(dir, previous), a.cfg.DataDir); err2 != nil {
			a.step("failed", "The restored instance did not become healthy ("+err.Error()+"), and switching back failed: "+err2.Error()+". The previous data is "+previous+".")
			return
		}
		a.restoreMu.Lock()
		a.restore.Previous = ""
		a.restoreMu.Unlock()
		if err3 := a.startHealthy(ctx); err3 != nil {
			a.step("failed", "The restored instance did not become healthy, and after switching back the instance does not either: "+err3.Error()+".")
			return
		}
		a.step("rolled-back", "The restored instance did not become healthy ("+err.Error()+"), so the previous data is back in place. The restored tree is kept as "+failed+" for 30 days.")
	}
}

// extract writes snapshot id's whole tree into dst.
func (a *Agent) extract(ctx context.Context, id, dst string) error {
	pr, pw := io.Pipe()
	errc := make(chan error, 1)
	go func() {
		err := a.cfg.Source.Tar(ctx, id, nil, pw)
		pw.CloseWithError(err)
		errc <- err
	}()
	exErr := a.cfg.Extract(dst, pr)
	pr.CloseWithError(errors.New("extraction ended"))
	tarErr := <-errc
	if tarErr != nil {
		return tarErr
	}
	return exErr
}

// startHealthy starts the instance and waits for Docker to call it healthy.
func (a *Agent) startHealthy(ctx context.Context) error {
	if err := a.cfg.Docker.Start(ctx, a.cfg.Container); err != nil {
		return err
	}
	deadline := a.cfg.Now().Add(a.cfg.HealthWait)
	poll := min(3*time.Second, a.cfg.HealthWait/10+time.Millisecond)
	last := ""
	for {
		h, err := a.cfg.Docker.Health(ctx, a.cfg.Container)
		if err == nil {
			last = h
			if h == "healthy" || h == "running" {
				return nil
			}
		}
		if !a.cfg.Now().Before(deadline) {
			if last == "" {
				last = "no answer"
			}
			return fmt.Errorf("health %q after %s", last, a.cfg.HealthWait)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(poll):
		}
	}
}
