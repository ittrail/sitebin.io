package snapshots

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// AgentConfig is what `sitebin agent` runs with, on the instance's host.
type AgentConfig struct {
	Source   Source
	SourceID string // "borg" or "dir", for the status
	StateDir string // the index and the instance restore's state
	DataDir  string // the host path of the instance's data directory
	// BackupNow is the command that takes a snapshot now (argv); nil when a
	// restore may not ask for one (the API then answers 501).
	BackupNow []string
	// BackupLock is the file the backup runs flock; an instance restore
	// holds it while the tree is swapped. "" for none.
	BackupLock string
	// StatusDir holds the backup runs' last-<mode>.json notes. "" for none.
	StatusDir string
	Container string // the Sitebin container
	Docker    DockerAPI
	// Extract restores a tar stream into dst: the core's restore, with the
	// owner of the data directory.
	Extract    func(dst string, r io.Reader) error
	Now        func() time.Time
	HealthWait time.Duration // how long a restored instance has to be healthy
	Log        *slog.Logger
}

// Agent serves the instance's snapshots to the instance and restores it.
type Agent struct {
	cfg  AgentConfig
	idx  Index
	wake chan struct{}

	mu       sync.Mutex
	snaps    []Snapshot
	listedAt time.Time

	backupMu sync.Mutex // one backup-now at a time
	indexMu  sync.Mutex // one indexer at a time

	restoreMu sync.Mutex
	restore   RestoreState
}

// copyKeep is how long a pre-restore copy, and a staging tree a crash left,
// is kept: the backups' own 30 days.
const copyKeep = 30 * 24 * time.Hour

// NewAgent answers an agent over cfg. It reads the last instance restore's
// state; one the agent did not live to finish is reported as failed.
func NewAgent(cfg AgentConfig) *Agent {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.HealthWait == 0 {
		cfg.HealthWait = 3 * time.Minute
	}
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	a := &Agent{cfg: cfg, idx: Index{Dir: filepath.Join(cfg.StateDir, "index")}, wake: make(chan struct{}, 1)}
	a.restore = RestoreState{State: "idle"}
	if b, err := os.ReadFile(a.restorePath()); err == nil {
		json.Unmarshal(b, &a.restore)
		if a.restore.running() {
			a.restore.State = "failed"
			a.restore.Message = "The agent stopped during this restore. Check that " + cfg.DataDir + " holds the data you want and that the instance runs."
			a.restore.Finished = cfg.Now().UTC()
			a.saveRestore()
		}
	}
	return a
}

// Handler is the agent's API.
func (a *Agent) Handler() http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("GET /v1/status", a.handleStatus)
	m.HandleFunc("GET /v1/snapshots", a.handleSnapshots)
	m.HandleFunc("GET /v1/sites/{view}/versions", a.handleVersions)
	m.HandleFunc("GET /v1/snapshots/{id}/sites/{view}/meta", a.handleMeta)
	m.HandleFunc("GET /v1/snapshots/{id}/sites/{view}/files", a.handleFiles)
	m.HandleFunc("POST /v1/backup-now", a.handleBackupNow)
	m.HandleFunc("POST /v1/instance/restore", a.handleInstanceRestore)
	m.HandleFunc("GET /v1/instance/restore", a.handleRestoreState)
	m.HandleFunc("GET /v1/instance/copies", a.handleCopies)
	m.HandleFunc("DELETE /v1/instance/copies/{name}", a.handleDeleteCopy)
	return m
}

// Run indexes new snapshots, forgets gone ones and removes copies past
// their time, every minute and whenever woken, until ctx ends.
func (a *Agent) Run(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		a.IndexNow(ctx)
		a.pruneCopies()
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-a.wake:
		}
	}
}

// IndexNow indexes every snapshot not indexed yet, newest first, and deletes
// the index of every snapshot that is gone.
func (a *Agent) IndexNow(ctx context.Context) {
	a.indexMu.Lock()
	defer a.indexMu.Unlock()
	snaps, err := a.list(ctx, true)
	if err != nil {
		a.cfg.Log.Warn("snapshots: list failed", "err", err)
		return
	}
	ids := make([]string, 0, len(snaps))
	for _, s := range snaps {
		ids = append(ids, s.ID)
	}
	a.idx.Prune(ids)
	for _, s := range snaps {
		if ctx.Err() != nil {
			return
		}
		if _, ok := a.idx.Get(s.ID); ok {
			continue
		}
		m, err := Summarize(ctx, a.cfg.Source, s.ID)
		if err != nil {
			if !errors.Is(err, ErrNotFound) {
				a.cfg.Log.Warn("snapshots: index failed", "snapshot", s.ID, "err", err)
			}
			continue
		}
		if err := a.idx.Put(s.ID, m); err != nil {
			a.cfg.Log.Warn("snapshots: index not saved", "snapshot", s.ID, "err", err)
		}
	}
}

// list answers the snapshots, from a cache at most 30 seconds old unless
// fresh is asked for.
func (a *Agent) list(ctx context.Context, fresh bool) ([]Snapshot, error) {
	a.mu.Lock()
	if !fresh && a.snaps != nil && a.cfg.Now().Sub(a.listedAt) < 30*time.Second {
		s := a.snaps
		a.mu.Unlock()
		return s, nil
	}
	a.mu.Unlock()
	s, err := a.cfg.Source.List(ctx)
	if err != nil {
		return nil, err
	}
	a.mu.Lock()
	a.snaps, a.listedAt = s, a.cfg.Now()
	a.mu.Unlock()
	return s, nil
}

// find answers the snapshot id names, listing afresh once when the cache
// does not hold it (it may be minutes old).
func (a *Agent) find(ctx context.Context, id string) (Snapshot, error) {
	if !ValidSnapshotID(id) {
		return Snapshot{}, ErrNotFound
	}
	for _, fresh := range []bool{false, true} {
		snaps, err := a.list(ctx, fresh)
		if err != nil {
			return Snapshot{}, err
		}
		for _, s := range snaps {
			if s.ID == id {
				return s, nil
			}
		}
	}
	return Snapshot{}, ErrNotFound
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func (a *Agent) failed(w http.ResponseWriter, err error) {
	if errors.Is(err, ErrNotFound) {
		writeErr(w, 404, "not found")
		return
	}
	a.cfg.Log.Error("snapshots: request failed", "err", err)
	writeErr(w, 502, "the backups could not be read")
}

func (a *Agent) handleStatus(w http.ResponseWriter, r *http.Request) {
	out := map[string]any{"source": a.cfg.SourceID, "backup_now": len(a.cfg.BackupNow) > 0}
	if snaps, err := a.list(r.Context(), false); err == nil {
		indexed := 0
		for _, s := range snaps {
			if _, ok := a.idx.Get(s.ID); ok {
				indexed++
			}
		}
		out["snapshots"], out["indexed"] = len(snaps), indexed
		if len(snaps) > 0 {
			out["newest"], out["oldest"] = snaps[0].At, snaps[len(snaps)-1].At
		}
	} else {
		out["error"] = "the backups could not be listed"
	}
	if a.cfg.StatusDir != "" {
		runs := map[string]json.RawMessage{}
		for _, mode := range []string{"hourly", "nightly", "prune", "check", "check-data"} {
			if b, err := os.ReadFile(filepath.Join(a.cfg.StatusDir, "last-"+mode+".json")); err == nil && json.Valid(b) {
				runs[mode] = b
			}
		}
		out["runs"] = runs
	}
	out["restore"] = a.restoreState()
	writeJSON(w, 200, out)
}

// snapshotRow is a snapshot as the operator's list shows it.
type snapshotRow struct {
	Snapshot
	Indexed bool  `json:"indexed"`
	Sites   int   `json:"sites"`
	Bytes   int64 `json:"bytes"`
}

func (a *Agent) handleSnapshots(w http.ResponseWriter, r *http.Request) {
	snaps, err := a.list(r.Context(), r.URL.Query().Get("fresh") == "1")
	if err != nil {
		a.failed(w, err)
		return
	}
	rows := make([]snapshotRow, 0, len(snaps))
	for _, s := range snaps {
		row := snapshotRow{Snapshot: s}
		if m, ok := a.idx.Get(s.ID); ok {
			row.Indexed, row.Sites = true, len(m)
			for _, x := range m {
				row.Bytes += x.Bytes
			}
		}
		rows = append(rows, row)
	}
	writeJSON(w, 200, rows)
}

func (a *Agent) handleVersions(w http.ResponseWriter, r *http.Request) {
	view := r.PathValue("view")
	if !ValidViewID(view) {
		writeErr(w, 400, "not a site id")
		return
	}
	snaps, err := a.list(r.Context(), false)
	if err != nil {
		a.failed(w, err)
		return
	}
	pending := 0
	for _, s := range snaps {
		if _, ok := a.idx.Get(s.ID); !ok {
			pending++
		}
	}
	vs := Versions(snaps, a.idx.Get, view)
	if vs == nil {
		vs = []Version{}
	}
	writeJSON(w, 200, map[string]any{"versions": vs, "pending": pending})
}

// siteRequest answers the snapshot and site a per-site route names.
func (a *Agent) siteRequest(w http.ResponseWriter, r *http.Request) (Snapshot, string, bool) {
	view := r.PathValue("view")
	if !ValidViewID(view) {
		writeErr(w, 400, "not a site id")
		return Snapshot{}, "", false
	}
	s, err := a.find(r.Context(), r.PathValue("id"))
	if err != nil {
		a.failed(w, err)
		return Snapshot{}, "", false
	}
	return s, view, true
}

func (a *Agent) handleMeta(w http.ResponseWriter, r *http.Request) {
	s, view, ok := a.siteRequest(w, r)
	if !ok {
		return
	}
	b, err := a.cfg.Source.ReadFile(r.Context(), s.ID, "sites/"+view+"/meta.json", 4<<20)
	if err != nil {
		a.failed(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(b)
}

func (a *Agent) handleFiles(w http.ResponseWriter, r *http.Request) {
	s, view, ok := a.siteRequest(w, r)
	if !ok {
		return
	}
	cw := &countWriter{w: w}
	if err := a.cfg.Source.Tar(r.Context(), s.ID, []string{"sites/" + view + "/files"}, cw); err != nil {
		if cw.n == 0 {
			a.failed(w, err)
			return
		}
		// Once the stream has begun a failure can only cut it short; the
		// caller reads a truncated tar as the error it is.
		a.cfg.Log.Error("snapshots: tar failed", "snapshot", s.ID, "site", view, "err", err)
		panic(http.ErrAbortHandler)
	}
}

// countWriter sets the tar response's headers on its first write and counts.
type countWriter struct {
	w http.ResponseWriter
	n int64
}

func (c *countWriter) Write(p []byte) (int, error) {
	if c.n == 0 && len(p) > 0 {
		c.w.Header().Set("Content-Type", "application/x-tar")
		c.w.Header().Set("Cache-Control", "no-store")
	}
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}

func (a *Agent) handleBackupNow(w http.ResponseWriter, r *http.Request) {
	if len(a.cfg.BackupNow) == 0 {
		writeErr(w, 501, "this agent takes no snapshot on request")
		return
	}
	a.backupMu.Lock()
	defer a.backupMu.Unlock()
	before, _ := a.list(r.Context(), true)
	// The run must end even when the instance gives up waiting.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 30*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, a.cfg.BackupNow[0], a.cfg.BackupNow[1:]...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		tail := strings.TrimSpace(string(out))
		if len(tail) > 400 {
			tail = tail[len(tail)-400:]
		}
		a.cfg.Log.Error("snapshots: backup now failed", "err", err, "output", tail)
		writeErr(w, 502, "the snapshot could not be taken")
		return
	}
	snaps, err := a.list(ctx, true)
	if err != nil || len(snaps) == 0 {
		writeErr(w, 502, "the snapshot was taken but cannot be listed")
		return
	}
	newest := snaps[0]
	if len(before) > 0 && before[0].ID == newest.ID {
		writeErr(w, 502, "the backup command made no new snapshot")
		return
	}
	if m, err := Summarize(ctx, a.cfg.Source, newest.ID); err == nil {
		a.idx.Put(newest.ID, m)
	}
	select {
	case a.wake <- struct{}{}:
	default:
	}
	writeJSON(w, 200, newest)
}
