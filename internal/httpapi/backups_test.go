package httpapi

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ittrail/sitebin.io/internal/snapshots"
	"github.com/ittrail/sitebin.io/internal/store"
)

// snapFile is one file of a site in a test snapshot, its path relative to
// the site's files/.
type snapFile struct {
	path, body string
	mtime      time.Time
	link       string // a symlink to this target
	mode       int64  // permission bits; 0644 when zero
	dir        bool   // an (empty) folder
}

// writeSnapshot writes <dir>/<id>.tar holding site view (mode) with files.
func writeSnapshot(t *testing.T, dir, id string, at time.Time, view, mode string, files []snapFile) {
	t.Helper()
	p := filepath.Join(dir, id+".tar")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	tw := tar.NewWriter(f)
	meta := `{"mode":"` + mode + `"}`
	tw.WriteHeader(&tar.Header{Name: "sites/" + view + "/meta.json", Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(meta)), ModTime: at})
	tw.Write([]byte(meta))
	for _, x := range files {
		h := &tar.Header{Name: "sites/" + view + "/files/" + x.path, Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(x.body)), ModTime: x.mtime}
		if x.mode != 0 {
			h.Mode = x.mode
		}
		switch {
		case x.link != "":
			h.Typeflag, h.Linkname, h.Size = tar.TypeSymlink, x.link, 0
		case x.dir:
			h.Typeflag, h.Mode, h.Size, h.Name = tar.TypeDir, 0o755, 0, h.Name+"/"
		}
		tw.WriteHeader(h)
		if h.Typeflag == tar.TypeReg {
			tw.Write([]byte(x.body))
		}
	}
	tw.Close()
	f.Close()
	os.Chtimes(p, at, at)
}

// liveFiles is the site's files/ as a snapshot of it now would hold them.
func liveFiles(t *testing.T, site *store.Site) []snapFile {
	t.Helper()
	var out []snapFile
	root := site.FilesDir()
	filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() {
			return err
		}
		fi, _ := d.Info()
		b, _ := os.ReadFile(p)
		rel, _ := filepath.Rel(root, p)
		out = append(out, snapFile{path: filepath.ToSlash(rel), body: string(b), mtime: fi.ModTime()})
		return nil
	})
	return out
}

type backupEnv struct {
	*env
	editID, pw string
	site       *store.Site
	dir        string
	backups    atomic.Int32
	failBackup atomic.Bool
	// during runs inside the snapshot taken before a restore: what changes
	// while it runs, which can take minutes.
	during func()
	agent  *snapshots.Agent
}

var (
	tOld = time.Date(2026, 11, 6, 3, 20, 0, 0, time.UTC)
	tMid = time.Date(2026, 11, 7, 3, 20, 0, 0, time.UTC)
)

// newBackupEnv: a site with index.html "now" and the snapshots of it — an
// older one (index.html "v1" plus old.txt) and one of its files as they are.
func newBackupEnv(t *testing.T) *backupEnv {
	t.Helper()
	e := newEnv(t, map[string]string{"SITEBIN_SNAPSHOTS_AGENT": "/unused.sock"})
	c := e.createSite(t, nil, map[string]string{"index.html": "now"})
	site, err := e.st.ByViewID(c.ID)
	if err != nil {
		t.Fatal(err)
	}
	b := &backupEnv{env: e, editID: editIDFrom(t, c.EditURL), pw: c.EditPassword, site: site, dir: t.TempDir()}
	writeSnapshot(t, b.dir, "nightly-old", tOld, site.ViewID, "webserver", []snapFile{
		{path: "index.html", body: "v1", mtime: tOld}, {path: "old.txt", body: "old", mtime: tOld}, {path: ".sitebin-spa", body: "", mtime: tOld},
	})
	writeSnapshot(t, b.dir, "hourly-mid", tMid, site.ViewID, "webserver", liveFiles(t, site))
	b.agent = snapshots.NewAgent(snapshots.AgentConfig{
		Source: &snapshots.DirSource{Dir: b.dir}, SourceID: "dir", StateDir: t.TempDir(),
		DataDir: t.TempDir(), Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/backup-now", func(w http.ResponseWriter, r *http.Request) {
		if b.failBackup.Load() {
			http.Error(w, `{"error":"the snapshot could not be taken"}`, 502)
			return
		}
		if b.during != nil {
			b.during()
		}
		n := b.backups.Add(1)
		at := time.Now().UTC().Truncate(time.Second).Add(time.Duration(n) * time.Second)
		id := "hourly-now" + string(rune('0'+n))
		s, _ := e.st.ByViewID(site.ViewID)
		writeSnapshot(t, b.dir, id, at, site.ViewID, s.Meta.Mode, liveFiles(t, s))
		b.agent.IndexNow(r.Context())
		json.NewEncoder(w).Encode(snapshots.Snapshot{ID: id, At: at, Kind: "hourly"})
	})
	mux.HandleFunc("GET /v1/snapshots/broken/sites/{view}/files", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("this is not the tar it promised to be, and it ends here"))
	})
	mux.Handle("/", b.agent.Handler())
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	e.api.snap = snapshots.NewClientURL(srv.URL, srv.Client())
	b.agent.IndexNow(context.Background())
	return b
}

type backupList struct {
	Available bool `json:"available"`
	Versions  []struct {
		Snapshot string    `json:"snapshot"`
		At       time.Time `json:"at"`
		Kind     string    `json:"kind"`
		Files    int       `json:"files"`
		Current  bool      `json:"current"`
	} `json:"versions"`
}

func (b *backupEnv) list(t *testing.T) backupList {
	t.Helper()
	w := b.call(t, "GET", "/api/sites/"+b.editID+"/backups", b.pw, nil)
	if w.Code != 200 {
		t.Fatalf("list: %d %s", w.Code, w.Body)
	}
	var l backupList
	json.Unmarshal(w.Body.Bytes(), &l)
	return l
}

func (b *backupEnv) restore(t *testing.T, snap string) *httptest.ResponseRecorder {
	t.Helper()
	return b.call(t, "POST", "/api/sites/"+b.editID+"/backups/"+snap+"/restore", b.pw, nil)
}

// siteFiles is the site's files/ as path=body, sorted.
func (b *backupEnv) siteFiles(t *testing.T) string {
	t.Helper()
	s, _ := b.st.ByViewID(b.site.ViewID)
	var out []string
	for _, f := range liveFiles(t, s) {
		if !snapshots.IsMarker(f.path) { // the site's own markers stay, as on every replace
			out = append(out, f.path+"="+f.body)
		}
	}
	sort.Strings(out)
	return strings.Join(out, " ")
}

func TestSnapshotMarkersAreTheStores(t *testing.T) {
	if strings.Join(snapshots.Markers, ",") != store.SPAMarker+","+store.TrustedMarker {
		t.Fatalf("snapshots.Markers = %v, the store's are %s and %s", snapshots.Markers, store.SPAMarker, store.TrustedMarker)
	}
}

func TestBackupsAreOffWithoutAnAgent(t *testing.T) {
	e := newEnv(t, nil)
	c := e.createSite(t, nil, map[string]string{"index.html": "x"})
	id := editIDFrom(t, c.EditURL)
	if w := e.call(t, "GET", "/api/sites/"+id+"/backups", c.EditPassword, nil); w.Code != 404 {
		t.Errorf("list without an agent: %d", w.Code)
	}
	if w := e.call(t, "POST", "/api/sites/"+id+"/backups/x/restore", c.EditPassword, nil); w.Code != 404 {
		t.Errorf("restore without an agent: %d", w.Code)
	}
	w := e.call(t, "GET", "/api/sites/"+id, c.EditPassword, nil)
	if !strings.Contains(w.Body.String(), `"backups_available":false`) {
		t.Errorf("payload: %s", w.Body)
	}
}

func TestBackupsListTheVersionsAndMarkTheCurrentOne(t *testing.T) {
	b := newBackupEnv(t)
	l := b.list(t)
	if !l.Available || len(l.Versions) != 2 {
		t.Fatalf("list = %+v", l)
	}
	if l.Versions[0].Snapshot != "hourly-mid" || !l.Versions[0].Current || l.Versions[1].Current || l.Versions[1].Files != 2 {
		t.Errorf("versions = %+v", l.Versions)
	}
	if w := b.call(t, "GET", "/api/sites/"+b.editID+"/backups", "wrong-password", nil); w.Code != 401 && w.Code != 403 {
		t.Errorf("a wrong password listed the backups: %d", w.Code)
	}
	if w := b.call(t, "GET", "/api/sites/"+b.editID, b.pw, nil); !strings.Contains(w.Body.String(), `"backups_available":true`) {
		t.Errorf("payload lacks backups_available: %s", w.Body)
	}
}

func TestRestoreReplacesTheFilesTakesASnapshotFirstAndRecords(t *testing.T) {
	b := newBackupEnv(t)
	w := b.restore(t, "nightly-old")
	if w.Code != 200 {
		t.Fatalf("restore: %d %s", w.Code, w.Body)
	}
	if got := b.siteFiles(t); got != "index.html=v1 old.txt=old" {
		t.Errorf("files after restore = %q (the snapshot's marker must not come back)", got)
	}
	if b.backups.Load() != 1 {
		t.Errorf("snapshots taken first: %d", b.backups.Load())
	}
	if !strings.Contains(w.Body.String(), `"restored"`) {
		t.Errorf("response: %s", w.Body)
	}
	// the snapshot's modification times come back with its files, so the
	// restored version is the current one, and the replaced files are the
	// newest version
	l := b.list(t)
	cur := ""
	for _, v := range l.Versions {
		if v.Current {
			cur = v.Snapshot
		}
	}
	if cur != "nightly-old" || l.Versions[0].Snapshot != "hourly-now1" {
		t.Errorf("after restore: current %q, versions %+v", cur, l.Versions)
	}
	entries, _ := b.st.Provenance(b.site)
	found := false
	for _, x := range entries {
		if x.Action == "restore" && strings.Contains(x.Detail, "2026-11-06 03:20 UTC") {
			found = true
		}
	}
	if !found {
		t.Errorf("no restore in the site's log: %+v", entries)
	}
	if w := b.restore(t, "no-such-snapshot"); w.Code != 404 {
		t.Errorf("unknown snapshot: %d", w.Code)
	}
}

func TestRestoreRefusesWhenNoSnapshotCanBeTakenFirst(t *testing.T) {
	b := newBackupEnv(t)
	b.failBackup.Store(true)
	if w := b.restore(t, "nightly-old"); w.Code != 503 {
		t.Fatalf("restore: %d %s", w.Code, w.Body)
	}
	if got := b.siteFiles(t); got != "index.html=now" {
		t.Errorf("files changed: %q", got)
	}
}

func TestRestoreOfABrokenStreamLeavesTheSiteAsItWas(t *testing.T) {
	b := newBackupEnv(t)
	writeSnapshot(t, b.dir, "broken", tOld.Add(time.Hour), b.site.ViewID, "webserver", []snapFile{{path: "a.txt", body: "a", mtime: tOld}})
	b.agent.IndexNow(context.Background())
	if w := b.restore(t, "broken"); w.Code < 500 {
		t.Fatalf("restore of a broken stream: %d %s", w.Code, w.Body)
	}
	if got := b.siteFiles(t); got != "index.html=now" {
		t.Errorf("files changed: %q", got)
	}
}

func TestRestoreOfAViewerSnapshotIntoAStaticSiteTakesTheFilesNotTheWrapper(t *testing.T) {
	b := newBackupEnv(t)
	writeSnapshot(t, b.dir, "nightly-viewer", tOld.Add(2*time.Hour), b.site.ViewID, "viewer", []snapFile{
		{path: "_raw/doc.md", body: "# doc", mtime: tOld}, {path: "index.html", body: "<wrapper>", mtime: tOld},
	})
	b.agent.IndexNow(context.Background())
	if w := b.restore(t, "nightly-viewer"); w.Code != 200 {
		t.Fatalf("restore: %d %s", w.Code, w.Body)
	}
	if got := b.siteFiles(t); got != "doc.md=# doc" {
		t.Errorf("files = %q", got)
	}
}

func TestRestoreRefusesAContainerSnapshotForAStaticSite(t *testing.T) {
	b := newBackupEnv(t)
	writeSnapshot(t, b.dir, "nightly-app", tOld.Add(3*time.Hour), b.site.ViewID, "container", []snapFile{
		{path: store.ComposeFile, body: "services: {}", mtime: tOld}, {path: "data/app.db", body: "secret rows", mtime: tOld},
	})
	b.agent.IndexNow(context.Background())
	w := b.restore(t, "nightly-app")
	if w.Code != 409 || !strings.Contains(w.Body.String(), "Container") {
		t.Fatalf("restore: %d %s", w.Code, w.Body)
	}
	if b.backups.Load() != 0 || b.siteFiles(t) != "index.html=now" {
		t.Error("a refused restore took a snapshot or changed files")
	}
}

func TestBackupsFromBeforeTheSiteChangedHandsAreNotOffered(t *testing.T) {
	b := newBackupEnv(t)
	since := tOld.Add(time.Hour)
	if err := b.st.Update(b.site, func(m *store.Meta) error { m.OwnerSince = &since; return nil }); err != nil {
		t.Fatal(err)
	}
	l := b.list(t)
	if len(l.Versions) != 1 || l.Versions[0].Snapshot != "hourly-mid" {
		t.Fatalf("versions = %+v", l.Versions)
	}
	if w := b.restore(t, "nightly-old"); w.Code != 404 {
		t.Errorf("restore from before the transfer: %d", w.Code)
	}
	if w := b.call(t, "GET", "/api/sites/"+b.editID+"/backups/nightly-old/download", b.pw, nil); w.Code != 404 {
		t.Errorf("download from before the transfer: %d", w.Code)
	}
}

func TestRestoreOfAContainerSiteStopsTheProjectAndStartsItAgain(t *testing.T) {
	rt := &fakeRuntime{}
	withContainers(t, rt)
	b := newBackupEnv(t)
	if err := b.st.Update(b.site, func(m *store.Meta) error {
		m.Mode = store.ModeContainer
		m.OwnerAccountID = "acct-1"
		m.Container = &store.ContainerMeta{Enabled: true, RestartSeq: 4}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	writeSnapshot(t, b.dir, "nightly-app", tOld.Add(3*time.Hour), b.site.ViewID, "container", []snapFile{
		{path: store.ComposeFile, body: "services: {}", mtime: tOld}, {path: "data/app.db", body: "rows", mtime: tOld},
	})
	b.agent.IndexNow(context.Background())
	if w := b.restore(t, "nightly-app"); w.Code != 200 {
		t.Fatalf("restore: %d %s", w.Code, w.Body)
	}
	if len(rt.stopped) != 1 || len(rt.kicked) != 1 {
		t.Errorf("runtime: stopped %v kicked %v", rt.stopped, rt.kicked)
	}
	s, _ := b.st.ByViewID(b.site.ViewID)
	if c := s.Meta.Container; c == nil || !c.Enabled || c.RestartSeq != 5 {
		t.Errorf("container after restore = %+v", c)
	}
	// a failed restore starts the project again too
	writeSnapshot(t, b.dir, "broken", tOld.Add(4*time.Hour), b.site.ViewID, "container", []snapFile{{path: "a", body: "a", mtime: tOld}})
	b.agent.IndexNow(context.Background())
	b.restore(t, "broken")
	s, _ = b.st.ByViewID(b.site.ViewID)
	if c := s.Meta.Container; c == nil || !c.Enabled {
		t.Errorf("after a failed restore the project stays stopped: %+v", c)
	}
}

func TestDownloadIsAZipOfTheVersionsFiles(t *testing.T) {
	b := newBackupEnv(t)
	w := b.call(t, "GET", "/api/sites/"+b.editID+"/backups/nightly-old/download", b.pw, nil)
	if w.Code != 200 || w.Header().Get("Content-Type") != "application/zip" {
		t.Fatalf("download: %d %s", w.Code, w.Header())
	}
	zr, err := zip.NewReader(bytes.NewReader(w.Body.Bytes()), int64(w.Body.Len()))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, f := range zr.File {
		names = append(names, f.Name)
	}
	sort.Strings(names)
	if strings.Join(names, ",") != "index.html,old.txt" {
		t.Errorf("zip holds %v", names)
	}
}

// What a restore decided at its start is checked again after its snapshot
// (which can take minutes) and under the site lock at the commit.
func TestRestoreRechecksTheSiteAfterItsSnapshot(t *testing.T) {
	for name, change := range map[string]func(b *backupEnv){
		"became a container": func(b *backupEnv) {
			b.st.Update(b.site, func(m *store.Meta) error { m.Mode = store.ModeContainer; return nil })
		},
		"changed hands": func(b *backupEnv) {
			now := time.Now()
			b.st.Update(b.site, func(m *store.Meta) error { m.OwnerAccountID = "someone-else"; m.OwnerSince = &now; return nil })
		},
		"edit password rotated": func(b *backupEnv) {
			if _, err := b.st.SetEditPassword(b.site); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			b := newBackupEnv(t)
			b.during = func() { change(b) }
			w := b.restore(t, "nightly-old")
			if w.Code != 409 {
				t.Fatalf("restore: %d %s", w.Code, w.Body)
			}
			if got := b.siteFiles(t); got != "index.html=now" {
				t.Errorf("files changed: %q", got)
			}
		})
	}
}

// Every restore runs a snapshot of the whole instance: an owner cannot make
// it run without end.
func TestRestoresAndDownloadsAreRateLimitedPerSite(t *testing.T) {
	b := newBackupEnv(t)
	codes := []int{}
	for i := 0; i < 4; i++ {
		codes = append(codes, b.restore(t, "nightly-old").Code)
	}
	if codes[3] != 429 {
		t.Fatalf("restore codes = %v, want the 4th refused", codes)
	}
	last := 0
	for i := 0; i < 11; i++ {
		last = b.call(t, "GET", "/api/sites/"+b.editID+"/backups/nightly-old/download", b.pw, nil).Code
	}
	if last != 429 {
		t.Fatalf("the 11th download answered %d", last)
	}
}

// A container site's restore keeps what its app installed: executables,
// links inside the tree and empty folders. A link out of the tree is
// skipped; a static site gets plain files only.
func TestRestoreOfAContainerSiteKeepsLinksModesAndEmptyFolders(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("links and mode bits need a unix filesystem")
	}
	rt := &fakeRuntime{}
	withContainers(t, rt)
	b := newBackupEnv(t)
	b.st.Update(b.site, func(m *store.Meta) error {
		m.Mode, m.OwnerAccountID = store.ModeContainer, "acct-1"
		m.Container = &store.ContainerMeta{Enabled: true}
		return nil
	})
	writeSnapshot(t, b.dir, "nightly-app", tOld.Add(3*time.Hour), b.site.ViewID, "container", []snapFile{
		{path: store.ComposeFile, body: "services: {}", mtime: tOld},
		{path: "app/node_modules/next/dist/bin/next", body: "#!/usr/bin/env node", mtime: tOld, mode: 0o755},
		{path: "app/node_modules/.bin/next", mtime: tOld, link: "../next/dist/bin/next"},
		{path: "app/evil", mtime: tOld, link: "../../../../../etc"},
		{path: "data/empty", mtime: tOld, dir: true},
	})
	b.agent.IndexNow(context.Background())
	if w := b.restore(t, "nightly-app"); w.Code != 200 {
		t.Fatalf("restore: %d %s", w.Code, w.Body)
	}
	dir := b.site.FilesDir()
	if fi, err := os.Stat(filepath.Join(dir, "app/node_modules/next/dist/bin/next")); err != nil || fi.Mode().Perm()&0o111 == 0 {
		t.Errorf("executable bits lost: %v %v", fi, err)
	}
	if l, err := os.Readlink(filepath.Join(dir, "app/node_modules/.bin/next")); err != nil || l != "../next/dist/bin/next" {
		t.Errorf(".bin link = %q %v", l, err)
	}
	if _, err := os.Lstat(filepath.Join(dir, "app/evil")); err == nil {
		t.Error("a link out of the tree was restored")
	}
	if fi, err := os.Stat(filepath.Join(dir, "data/empty")); err != nil || !fi.IsDir() {
		t.Errorf("empty folder lost: %v", err)
	}
}

func TestRestoreOfAStaticSiteTakesPlainFilesOnly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("links need a unix filesystem")
	}
	b := newBackupEnv(t)
	writeSnapshot(t, b.dir, "nightly-links", tOld.Add(4*time.Hour), b.site.ViewID, "webserver", []snapFile{
		{path: "index.html", body: "x", mtime: tOld, mode: 0o755},
		{path: "l", mtime: tOld, link: "index.html"},
	})
	b.agent.IndexNow(context.Background())
	if w := b.restore(t, "nightly-links"); w.Code != 200 {
		t.Fatalf("restore: %d %s", w.Code, w.Body)
	}
	if _, err := os.Lstat(filepath.Join(b.site.FilesDir(), "l")); err == nil {
		t.Error("a static site got a link")
	}
	if fi, _ := os.Stat(filepath.Join(b.site.FilesDir(), "index.html")); fi != nil && fi.Mode().Perm()&0o111 != 0 {
		t.Error("a static site got an executable")
	}
}

// attachBackups gives e a snapshots agent holding one snapshot, nightly-kit,
// of site viewID with files; a snapshot before a restore always succeeds.
func attachBackups(t *testing.T, e *env, viewID string, files map[string]string) {
	t.Helper()
	dir := t.TempDir()
	var fs []snapFile
	for p, b := range files {
		fs = append(fs, snapFile{path: p, body: b, mtime: tOld})
	}
	writeSnapshot(t, dir, "nightly-kit", tOld, viewID, "webserver", fs)
	ag := snapshots.NewAgent(snapshots.AgentConfig{Source: &snapshots.DirSource{Dir: dir}, SourceID: "dir",
		StateDir: t.TempDir(), DataDir: t.TempDir(), Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/backup-now", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(snapshots.Snapshot{ID: "hourly-now", At: time.Now().UTC(), Kind: "hourly"})
	})
	mux.Handle("/", ag.Handler())
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	e.api.snap = snapshots.NewClientURL(srv.URL, srv.Client())
	ag.IndexNow(context.Background())
}
