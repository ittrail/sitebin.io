package snapshots

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeBackup is the backup-now command in tests: a new archive, one second
// newer than the newest in dir.
func fakeBackup(dir string) int {
	newest := time.Time{}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if fi, err := e.Info(); err == nil && fi.ModTime().After(newest) {
			newest = fi.ModTime()
		}
	}
	at := newest.Add(time.Second)
	p := filepath.Join(dir, "hourly-now.tar")
	f, err := os.Create(p)
	if err != nil {
		return 1
	}
	tw := tar.NewWriter(f)
	body := []byte("now")
	tw.WriteHeader(&tar.Header{Name: "sites/" + siteA + "/meta.json", Typeflag: tar.TypeReg, Mode: 0o644, Size: 2, ModTime: at})
	tw.Write([]byte("{}"))
	tw.WriteHeader(&tar.Header{Name: "sites/" + siteA + "/files/now.txt", Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(body)), ModTime: at})
	tw.Write(body)
	tw.Close()
	f.Close()
	os.Chtimes(p, at, at)
	return 0
}

// extractForTest is a minimal Extract: regular files and folders.
func extractForTest(dst string, r io.Reader) error {
	tr := tar.NewReader(r)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		p := filepath.Join(dst, filepath.FromSlash(entryName(h)))
		switch h.Typeflag {
		case tar.TypeDir:
			os.MkdirAll(p, 0o755)
		case tar.TypeReg:
			os.MkdirAll(filepath.Dir(p), 0o755)
			b, _ := io.ReadAll(tr)
			if err := os.WriteFile(p, b, 0o644); err != nil {
				return err
			}
		}
	}
}

type fakeDocker struct {
	mu     sync.Mutex
	calls  []string
	health func() string
}

func (d *fakeDocker) note(s string)                           { d.mu.Lock(); d.calls = append(d.calls, s); d.mu.Unlock() }
func (d *fakeDocker) Stop(_ context.Context, n string) error  { d.note("stop " + n); return nil }
func (d *fakeDocker) Start(_ context.Context, n string) error { d.note("start " + n); return nil }
func (d *fakeDocker) Health(context.Context, string) (string, error) {
	return d.health(), nil
}
func (d *fakeDocker) RemoveManaged(context.Context) (int, error) {
	d.note("remove-managed")
	return 2, nil
}
func (d *fakeDocker) Calls() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return strings.Join(d.calls, ", ")
}

type agentFixture struct {
	a      *Agent
	srv    *httptest.Server
	base   string
	data   string
	docker *fakeDocker
}

func newAgentFixture(t *testing.T, health string) *agentFixture {
	t.Helper()
	src, _ := dirFixture(t)
	base := t.TempDir()
	data := filepath.Join(base, "data")
	os.MkdirAll(filepath.Join(data, "sites"), 0o755)
	os.WriteFile(filepath.Join(data, "old.txt"), []byte("before"), 0o644)
	d := &fakeDocker{health: func() string { return health }}
	a := NewAgent(AgentConfig{
		Source: src, SourceID: "dir", StateDir: filepath.Join(base, "state"), DataDir: data,
		BackupNow: []string{os.Args[0]}, Container: "sitebin", Docker: d, Extract: extractForTest,
		HealthWait: 300 * time.Millisecond, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	srv := httptest.NewServer(a.Handler())
	t.Cleanup(srv.Close)
	t.Setenv("FAKE_BACKUP_DIR", src.Dir)
	a.IndexNow(context.Background())
	return &agentFixture{a: a, srv: srv, base: base, data: data, docker: d}
}

func (f *agentFixture) get(t *testing.T, path string, v any) int {
	t.Helper()
	res, err := http.Get(f.srv.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if v != nil {
		json.NewDecoder(res.Body).Decode(v)
	}
	return res.StatusCode
}

func (f *agentFixture) post(t *testing.T, path, body string) (int, map[string]any) {
	t.Helper()
	res, err := http.Post(f.srv.URL+path, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var m map[string]any
	json.NewDecoder(res.Body).Decode(&m)
	return res.StatusCode, m
}

func TestAgentListsSnapshotsAndASitesVersions(t *testing.T) {
	f := newAgentFixture(t, "healthy")
	var rows []snapshotRow
	if code := f.get(t, "/v1/snapshots", &rows); code != 200 || len(rows) != 2 || !rows[0].Indexed || rows[0].Sites != 2 {
		t.Fatalf("snapshots %d %+v", code, rows)
	}
	var vs struct {
		Versions []Version `json:"versions"`
		Pending  int       `json:"pending"`
	}
	if code := f.get(t, "/v1/sites/"+siteA+"/versions", &vs); code != 200 || len(vs.Versions) != 2 || vs.Pending != 0 {
		t.Fatalf("versions %d %+v", code, vs)
	}
	if vs.Versions[0].Snapshot != "sitebin-2026-11-07" || vs.Versions[1].Files != 1 {
		t.Errorf("versions = %+v", vs.Versions)
	}
	if code := f.get(t, "/v1/sites/../versions", nil); code == 200 {
		t.Error("a path that is no site id was answered")
	}
	if code := f.get(t, "/v1/sites/NOTANID/versions", nil); code != 400 {
		t.Errorf("bad id: %d", code)
	}
}

func TestAgentAnswersASitesMetaAndFiles(t *testing.T) {
	f := newAgentFixture(t, "healthy")
	res, err := http.Get(f.srv.URL + "/v1/snapshots/sitebin-2026-11-07/sites/" + siteA + "/meta")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 || string(b) != `{"mode":"viewer"}` {
		t.Fatalf("meta %d %q", res.StatusCode, b)
	}
	if code := f.get(t, "/v1/snapshots/nope/sites/"+siteA+"/meta", nil); code != 404 {
		t.Errorf("unknown snapshot: %d", code)
	}
	if code := f.get(t, "/v1/snapshots/sitebin-2026-11-07/sites/"+siteB+"x/meta", nil); code != 400 {
		t.Errorf("bad site id: %d", code)
	}
	res, _ = http.Get(f.srv.URL + "/v1/snapshots/sitebin-2026-11-07/sites/" + siteA + "/files")
	b, _ = io.ReadAll(res.Body)
	res.Body.Close()
	tr := tar.NewReader(bytes.NewReader(b))
	var names []string
	for {
		h, err := tr.Next()
		if err != nil {
			break
		}
		names = append(names, h.Name)
	}
	if strings.Join(names, ",") != "sites/"+siteA+"/files/_raw/doc.md,sites/"+siteA+"/files/index.html" {
		t.Fatalf("files tar = %v", names)
	}
}

func TestAgentBackupNowAnswersTheNewSnapshotIndexed(t *testing.T) {
	f := newAgentFixture(t, "healthy")
	code, m := f.post(t, "/v1/backup-now", "")
	if code != 200 || m["id"] != "hourly-now" || m["kind"] != "hourly" {
		t.Fatalf("backup-now %d %v", code, m)
	}
	if _, ok := f.a.idx.Get("hourly-now"); !ok {
		t.Error("the new snapshot was not indexed at once")
	}
	t.Setenv("FAKE_BACKUP_FAIL", "1")
	if code, _ := f.post(t, "/v1/backup-now", ""); code != 502 {
		t.Errorf("a failing backup command answered %d", code)
	}
}

func waitRestore(t *testing.T, a *Agent) RestoreState {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if s := a.restoreState(); !s.running() {
			return s
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the restore did not end")
	return RestoreState{}
}

func TestInstanceRestoreSwapsTheDataAndKeepsTheOldAsACopy(t *testing.T) {
	f := newAgentFixture(t, "healthy")
	code, m := f.post(t, "/v1/instance/restore", `{"snapshot":"sitebin-2026-11-06"}`)
	if code != 202 || m["state"] != "preparing" {
		t.Fatalf("restore %d %v", code, m)
	}
	st := waitRestore(t, f.a)
	if st.State != "done" || !strings.HasPrefix(st.Previous, "data.before-restore-") {
		t.Fatalf("state = %+v", st)
	}
	if got := f.docker.Calls(); got != "stop sitebin, remove-managed, start sitebin" {
		t.Errorf("docker calls = %q", got)
	}
	if b, err := os.ReadFile(filepath.Join(f.data, "sites", siteA, "files", "index.html")); err != nil || string(b) != "old" {
		t.Fatalf("restored data: %q %v", b, err)
	}
	if _, err := os.Stat(filepath.Join(f.data, "old.txt")); err == nil {
		t.Error("the data from before is still in place")
	}
	if b, _ := os.ReadFile(filepath.Join(f.base, st.Previous, "old.txt")); string(b) != "before" {
		t.Error("the data from before was not kept")
	}
	var copies []Copy
	f.get(t, "/v1/instance/copies", &copies)
	if len(copies) != 1 || copies[0].Kind != "before-restore" {
		t.Fatalf("copies = %+v", copies)
	}
	// the state survives the agent
	if again := NewAgent(f.a.cfg).restoreState(); again.State != "done" {
		t.Errorf("state after a restart of the agent = %+v", again)
	}

	// switching back to the copy is a restore too
	code, _ = f.post(t, "/v1/instance/restore", `{"copy":"`+copies[0].Name+`"}`)
	if code != 202 {
		t.Fatalf("switch back %d", code)
	}
	st = waitRestore(t, f.a)
	if st.State != "done" {
		t.Fatalf("switch back state = %+v", st)
	}
	if b, _ := os.ReadFile(filepath.Join(f.data, "old.txt")); string(b) != "before" {
		t.Error("switching back did not bring the old data")
	}
	f.get(t, "/v1/instance/copies", &copies)
	if len(copies) != 1 || copies[0].Name == st.Copy {
		t.Fatalf("after switching back, copies = %+v (the restored tree must be the copy now)", copies)
	}
	if code, _ := f.post(t, "/v1/instance/restore", `{"copy":"../../etc"}`); code != 404 {
		t.Errorf("a copy that is not one: %d", code)
	}
	req, _ := http.NewRequest("DELETE", f.srv.URL+"/v1/instance/copies/"+copies[0].Name, nil)
	res, _ := http.DefaultClient.Do(req)
	res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("delete copy %d", res.StatusCode)
	}
	f.get(t, "/v1/instance/copies", &copies)
	if len(copies) != 0 {
		t.Errorf("copies after delete = %+v", copies)
	}
}

func TestInstanceRestoreSwitchesBackWhenNotHealthy(t *testing.T) {
	f := newAgentFixture(t, "unhealthy")
	f.post(t, "/v1/instance/restore", `{"snapshot":"sitebin-2026-11-07"}`)
	st := waitRestore(t, f.a)
	if st.State != "failed" {
		// unhealthy after switching back too: failed, with the old data in place
		t.Logf("state = %+v", st)
	}
	if b, _ := os.ReadFile(filepath.Join(f.data, "old.txt")); string(b) != "before" {
		t.Fatal("the previous data is not back in place")
	}
	var copies []Copy
	f.get(t, "/v1/instance/copies", &copies)
	if len(copies) != 1 || copies[0].Kind != "failed-restore" {
		t.Fatalf("copies = %+v", copies)
	}

	// healthy again after switching back: rolled-back
	f2 := newAgentFixture(t, "")
	n := 0
	f2.docker.health = func() string {
		n++
		if strings.Count(f2.docker.Calls(), "start") >= 2 {
			return "healthy"
		}
		return "unhealthy"
	}
	f2.post(t, "/v1/instance/restore", `{"snapshot":"sitebin-2026-11-07"}`)
	if st := waitRestore(t, f2.a); st.State != "rolled-back" || !strings.Contains(st.Message, "failed-restore") {
		t.Fatalf("state = %+v", st)
	}
	if b, _ := os.ReadFile(filepath.Join(f2.data, "old.txt")); string(b) != "before" {
		t.Fatal("rolled back, but the previous data is not in place")
	}
}

func TestInstanceRestoreRefusesWithoutSpaceAndChangesNothing(t *testing.T) {
	f := newAgentFixture(t, "healthy")
	old := diskFree
	diskFree = func(string) (uint64, error) { return 1 << 20, nil }
	defer func() { diskFree = old }()
	f.post(t, "/v1/instance/restore", `{"snapshot":"sitebin-2026-11-07"}`)
	st := waitRestore(t, f.a)
	if st.State != "failed" || !strings.Contains(st.Message, "Not enough space") {
		t.Fatalf("state = %+v", st)
	}
	if f.docker.Calls() != "" {
		t.Errorf("docker was asked: %s", f.docker.Calls())
	}
	if code, _ := f.post(t, "/v1/instance/restore", `{}`); code != 400 {
		t.Errorf("empty body: %d", code)
	}
	if code, _ := f.post(t, "/v1/instance/restore", `{"snapshot":"nope"}`); code != 404 {
		t.Errorf("unknown snapshot: %d", code)
	}
}

func TestCopiesAreRemovedAfterThirtyDays(t *testing.T) {
	f := newAgentFixture(t, "healthy")
	now := time.Date(2026, 12, 10, 12, 0, 0, 0, time.UTC)
	f.a.cfg.Now = func() time.Time { return now }
	old := "data.before-restore-" + now.Add(-31*24*time.Hour).Format(stampLayout)
	young := "data.failed-restore-" + now.Add(-29*24*time.Hour).Format(stampLayout)
	staging := "data.restore-" + now.Add(-2*24*time.Hour).Format(stampLayout)
	for _, d := range []string{old, young, staging} {
		os.MkdirAll(filepath.Join(f.base, d), 0o755)
	}
	f.a.pruneCopies()
	for d, want := range map[string]bool{old: false, young: true, staging: false} {
		_, err := os.Stat(filepath.Join(f.base, d))
		if got := err == nil; got != want {
			t.Errorf("%s kept = %v, want %v", d, got, want)
		}
	}
}

func TestAnInterruptedRestoreIsReportedFailed(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "restore.json"), []byte(`{"state":"swapping","snapshot":"x"}`), 0o600)
	a := NewAgent(AgentConfig{StateDir: dir, DataDir: filepath.Join(dir, "data"), Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if st := a.restoreState(); st.State != "failed" || !strings.Contains(st.Message, "stopped during") {
		t.Fatalf("state = %+v", st)
	}
}
