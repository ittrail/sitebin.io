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
// instance — which the restore stops — reads it once it runs again, and so
// an agent that stopped mid-way can put things right when it starts.
type RestoreState struct {
	State    string    `json:"state"` // idle preparing extracting stopping swapping starting done failed rolled-back
	Snapshot string    `json:"snapshot,omitempty"`
	Copy     string    `json:"copy,omitempty"` // restoring a kept copy instead of a snapshot
	Started  time.Time `json:"started,omitzero"`
	Finished time.Time `json:"finished,omitzero"`
	Message  string    `json:"message,omitempty"`
	// Incoming is the tree that becomes the data directory (an extraction
	// beside it, or the kept copy), and Previous the name the data from
	// before is kept as. Both are recorded before the first rename, so an
	// interrupted swap can be undone.
	Incoming string `json:"incoming,omitempty"`
	Previous string `json:"previous,omitempty"`
	// IncomingOrigin is how old the incoming tree's contents are (the
	// snapshot's time, or the copy's own origin).
	IncomingOrigin time.Time `json:"incoming_origin,omitzero"`
	// LiveOrigin is how old the running data's contents are when a restore
	// put them there; zero when they are simply the instance's own.
	LiveOrigin time.Time `json:"live_origin,omitzero"`
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

// update changes the restore's state under its lock and keeps it.
func (a *Agent) update(fn func(*RestoreState)) {
	a.restoreMu.Lock()
	defer a.restoreMu.Unlock()
	fn(&a.restore)
	a.saveRestore()
}

// step moves the restore to state and keeps it.
func (a *Agent) step(state, msg string) {
	a.update(func(s *RestoreState) {
		s.State = state
		if msg != "" {
			s.Message = msg
		}
		if !s.running() {
			s.Finished = a.cfg.Now().UTC()
		}
	})
	a.cfg.Log.Info("instance restore", "state", state, "msg", msg)
}

// Copy is a kept data tree: the data before a restore, or a restored tree
// that did not come up.
type Copy struct {
	Name string    `json:"name"`
	Kind string    `json:"kind"` // before-restore, failed-restore
	At   time.Time `json:"at"`   // when it was set aside
	// Origin is how old its contents are: a copy is deleted 30 days after
	// that, not after it was set aside — a restored 29-day-old snapshot
	// kept as a copy must not live another 30 days.
	Origin time.Time `json:"origin"`
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

// ---- the copies' origins, kept beside the restore's state ----

func (a *Agent) originsPath() string { return filepath.Join(a.cfg.StateDir, "copies.json") }

func (a *Agent) origins() map[string]time.Time {
	m := map[string]time.Time{}
	if b, err := os.ReadFile(a.originsPath()); err == nil {
		json.Unmarshal(b, &m)
	}
	return m
}

// setOrigin records (or, with a zero time, forgets) a copy's origin.
func (a *Agent) setOrigin(name string, t time.Time) {
	m := a.origins()
	if t.IsZero() {
		delete(m, name)
	} else {
		m[name] = t.UTC()
	}
	os.MkdirAll(a.cfg.StateDir, 0o700)
	b, _ := json.Marshal(m)
	tmp := a.originsPath() + ".tmp"
	if os.WriteFile(tmp, b, 0o600) == nil {
		os.Rename(tmp, a.originsPath())
	}
}

// liveOrigin is how old the running data's contents are: the time of what
// a restore put in place, else now.
func (a *Agent) liveOrigin(now time.Time) time.Time {
	if o := a.restoreState().LiveOrigin; !o.IsZero() {
		return o
	}
	return now
}

// copies lists the kept trees beside the data directory, newest first.
func (a *Agent) copies() []Copy {
	dir, base := a.parent()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	origins := a.origins()
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
			origin := at
			if o, ok := origins[e.Name()]; ok && o.Before(at) {
				origin = o
			}
			out = append(out, Copy{Name: e.Name(), Kind: kind, At: at, Origin: origin})
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

// pruneCopies removes kept trees whose contents are older than copyKeep,
// and staging trees a crashed extraction left more than a day ago. Never
// while a restore runs.
func (a *Agent) pruneCopies() {
	if a.restoreState().running() {
		return
	}
	now := a.cfg.Now()
	dir, base := a.parent()
	for _, c := range a.copies() {
		if now.Sub(c.Origin) > copyKeep {
			if err := os.RemoveAll(filepath.Join(dir, c.Name)); err == nil {
				a.setOrigin(c.Name, time.Time{})
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
	a.setOrigin(c.Name, time.Time{})
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
	live := a.restore.LiveOrigin
	a.restore = RestoreState{State: "preparing", Snapshot: src.ID, Copy: cp.Name, Started: a.cfg.Now().UTC(), LiveOrigin: live}
	a.saveRestore()
	st := a.restore
	a.restoreWG.Add(1)
	a.restoreMu.Unlock()
	go func() {
		defer a.restoreWG.Done()
		a.runRestore(context.Background(), src, cp)
	}()
	writeJSON(w, 202, st)
}

// WaitRestore waits up to max for a running instance restore: the agent
// must not stop half-way through a swap (systemd's TimeoutStopSec allows it).
func (a *Agent) WaitRestore(max time.Duration) {
	done := make(chan struct{})
	go func() { a.restoreWG.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(max):
		a.cfg.Log.Error("instance restore: still running at shutdown; the next start puts it right")
	}
}

// removeManaged removes every site container, trying a few times: a
// survivor would keep its mounts into the tree being swapped out.
func (a *Agent) removeManaged(ctx context.Context) error {
	var err error
	for i := 0; i < 3; i++ {
		if _, err = a.cfg.Docker.RemoveManaged(ctx); err == nil {
			return nil
		}
		time.Sleep(time.Duration(i+1) * 500 * time.Millisecond)
	}
	return err
}

// runRestore restores the instance to snapshot src, or to the kept copy cp:
// the new tree is made beside the data directory, the instance is stopped
// with every container of its sites, the two trees swap names, and the
// instance starts again — switching back when it does not become healthy.
func (a *Agent) runRestore(ctx context.Context, src Snapshot, cp Copy) {
	dir, base := a.parent()
	now := a.cfg.Now()
	var incoming string // the tree that becomes the data directory
	origin := src.At
	if cp.Name != "" {
		incoming = filepath.Join(dir, cp.Name)
		origin = cp.Origin
	} else {
		// The whole snapshot's size — Git repositories, accounts and
		// analytics too, not only the sites' files the index sums.
		var size int64
		if err := a.cfg.Source.Walk(ctx, src.ID, "", func(it Item) error {
			if it.Regular {
				size += it.Size
			}
			return nil
		}); err != nil {
			a.step("failed", "The snapshot could not be read: "+err.Error()+". Nothing was changed.")
			return
		}
		if free, err := diskFree(dir); err == nil && free < uint64(size)+restoreHeadroom {
			a.step("failed", fmt.Sprintf("Not enough space: the snapshot needs about %d MB, %d MB are free. Nothing was changed.", (size+restoreHeadroom)>>20, free>>20))
			return
		}
		incoming = filepath.Join(dir, freeName(dir, base, "restore", now))
		a.update(func(s *RestoreState) { s.Incoming = filepath.Base(incoming) })
		a.step("extracting", "")
		if err := a.extract(ctx, src.ID, incoming); err != nil {
			os.RemoveAll(incoming)
			a.cfg.Log.Error("instance restore: extract", "err", err)
			a.step("failed", "The snapshot could not be read: "+err.Error()+". Nothing was changed.")
			return
		}
	}
	// a staging tree is ours to remove on a way out; a kept copy is not
	dropIncoming := func() {
		if cp.Name == "" {
			os.RemoveAll(incoming)
		}
	}
	unlock, err := lockFile(a.cfg.BackupLock, 30*time.Minute)
	if err != nil {
		dropIncoming()
		a.step("failed", "A backup run held its lock for too long: "+err.Error()+". Nothing was changed.")
		return
	}
	defer unlock()

	a.step("stopping", "")
	if err := a.cfg.Docker.Stop(ctx, a.cfg.Container); err != nil {
		dropIncoming()
		a.cfg.Docker.Start(ctx, a.cfg.Container)
		a.step("failed", "The instance could not be stopped: "+err.Error()+". Nothing was changed.")
		return
	}
	if err := a.removeManaged(ctx); err != nil {
		dropIncoming()
		a.cfg.Docker.Start(ctx, a.cfg.Container)
		a.step("failed", "The site containers could not be removed ("+err.Error()+"), so the data was not swapped. Nothing was changed; the instance runs again.")
		return
	}

	previous := freeName(dir, base, "before-restore", now)
	prevOrigin := a.liveOrigin(now)
	a.update(func(s *RestoreState) {
		s.Incoming, s.Previous, s.IncomingOrigin = filepath.Base(incoming), previous, origin
	})
	a.step("swapping", "")
	if err := os.Rename(a.cfg.DataDir, filepath.Join(dir, previous)); err != nil {
		dropIncoming()
		a.cfg.Docker.Start(ctx, a.cfg.Container)
		a.update(func(s *RestoreState) { s.Previous = "" })
		a.step("failed", "The data directory could not be moved aside: "+err.Error()+". The instance runs on its data as before.")
		return
	}
	a.setOrigin(previous, prevOrigin)
	if err := os.Rename(incoming, a.cfg.DataDir); err != nil {
		os.Rename(filepath.Join(dir, previous), a.cfg.DataDir)
		a.setOrigin(previous, time.Time{})
		dropIncoming()
		a.cfg.Docker.Start(ctx, a.cfg.Container)
		a.update(func(s *RestoreState) { s.Previous = "" })
		a.step("failed", "The restored tree could not be put in place: "+err.Error()+". The instance runs on its data as before.")
		return
	}
	if cp.Name != "" {
		a.setOrigin(cp.Name, time.Time{}) // it is the data now, no longer a copy
	}

	a.step("starting", "")
	if err := a.startHealthy(ctx); err == nil {
		what := "the snapshot of " + src.At.UTC().Format("2 Jan 2006, 15:04 UTC")
		if cp.Name != "" {
			what = "the copy " + cp.Name
		}
		a.update(func(s *RestoreState) { s.LiveOrigin = origin })
		a.step("done", "The instance runs on "+what+". The data from before is kept as "+previous+" for 30 days.")
		return
	} else {
		a.cfg.Log.Error("instance restore: not healthy, switching back", "err", err)
		a.rollBack(ctx, err.Error())
	}
}

// rollBack puts the data from before back after the restored tree did not
// come up, keeping the restored tree as a failed copy.
func (a *Agent) rollBack(ctx context.Context, why string) {
	dir, base := a.parent()
	st := a.restoreState()
	a.cfg.Docker.Stop(ctx, a.cfg.Container)
	note := ""
	if err := a.removeManaged(ctx); err != nil {
		note = " Some site containers could not be removed (" + err.Error() + "); restart their projects."
	}
	failed := freeName(dir, base, "failed-restore", a.cfg.Now())
	if err := os.Rename(a.cfg.DataDir, filepath.Join(dir, failed)); err == nil {
		a.setOrigin(failed, st.IncomingOrigin)
	}
	if err := os.Rename(filepath.Join(dir, st.Previous), a.cfg.DataDir); err != nil {
		a.step("failed", "The restored instance did not become healthy ("+why+"), and switching back failed: "+err.Error()+". The previous data is "+st.Previous+"."+note)
		return
	}
	a.setOrigin(st.Previous, time.Time{})
	a.update(func(s *RestoreState) { s.Previous = "" })
	if err := a.startHealthy(ctx); err != nil {
		a.step("failed", "The restored instance did not become healthy, and after switching back the instance does not either: "+err.Error()+"."+note)
		return
	}
	a.step("rolled-back", "The restored instance did not become healthy ("+why+"), so the previous data is back in place. The restored tree is kept as "+failed+" for 30 days."+note)
}

// Recover puts right an instance restore the agent did not live to finish
// (a restart, a crash): the data the instance needs is put back in place if
// a swap was cut in half, and the instance is started whatever the state.
// Run calls it first.
func (a *Agent) Recover(ctx context.Context) {
	st := a.restoreState()
	if !st.running() {
		return
	}
	dir, _ := a.parent()
	exists := func(name string) bool {
		if name == "" {
			return false
		}
		_, err := os.Lstat(filepath.Join(dir, name))
		return err == nil
	}
	_, dataErr := os.Lstat(a.cfg.DataDir)
	data := dataErr == nil
	msg := ""
	switch {
	case !data && exists(st.Previous):
		// cut between the two renames: the data from before goes back
		if err := os.Rename(filepath.Join(dir, st.Previous), a.cfg.DataDir); err != nil {
			msg = "The agent stopped while swapping, and the data could not be put back: " + err.Error() + ". The data from before is " + st.Previous + "."
			break
		}
		a.setOrigin(st.Previous, time.Time{})
		msg = "The agent stopped while swapping the data. The data from before is back in place; nothing was restored."
	case st.State == "swapping" && data && exists(st.Previous):
		// both renames done, never started: the data from before goes back
		a.cfg.Docker.Stop(ctx, a.cfg.Container)
		a.removeManaged(ctx)
		failed := freeName(dir, filepath.Base(a.cfg.DataDir), "failed-restore", a.cfg.Now())
		if err := os.Rename(a.cfg.DataDir, filepath.Join(dir, failed)); err == nil {
			a.setOrigin(failed, st.IncomingOrigin)
		}
		if err := os.Rename(filepath.Join(dir, st.Previous), a.cfg.DataDir); err != nil {
			msg = "The agent stopped after swapping, and the data from before could not be put back: " + err.Error() + ". It is " + st.Previous + "."
			break
		}
		a.setOrigin(st.Previous, time.Time{})
		msg = "The agent stopped after swapping the data, before the instance started. The data from before is back in place; the restored tree is kept as " + failed + "."
	case st.State == "starting":
		// in place and being started: it stays, and starts
		a.update(func(s *RestoreState) { s.LiveOrigin = s.IncomingOrigin })
		msg = "The agent stopped while the restored instance was starting. It runs on the restored data; the data from before is kept as " + st.Previous + "."
	default:
		// before any swap: a staging tree goes, nothing else changed
		if strings.Contains(st.Incoming, ".restore-") && exists(st.Incoming) {
			os.RemoveAll(filepath.Join(dir, st.Incoming))
		}
		msg = "The agent stopped during this restore, before anything was swapped. Nothing was changed."
	}
	if err := a.cfg.Docker.Start(ctx, a.cfg.Container); err != nil {
		msg += " The instance could not be started: " + err.Error() + "."
	}
	a.step("failed", msg)
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
	if exErr == nil {
		// A tar reader stops at the end-of-archive marker; Borg's export-tar
		// (Python's tarfile) pads the stream after it to a full record. The
		// rest is read and dropped, or the writer fails on a closed pipe and
		// a complete extraction would be reported as a broken one.
		io.Copy(io.Discard, pr)
	}
	pr.CloseWithError(errors.New("extraction ended"))
	tarErr := <-errc
	if exErr != nil {
		return exErr // the cause; the tar side then only saw the pipe close
	}
	return tarErr
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
