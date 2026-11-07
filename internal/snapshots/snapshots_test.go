package snapshots

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
	"time"
)

const siteA = "aaaaaaaaaaaaaaaaaaaaaaaaaa"
const siteB = "bbbbbbbbbbbbbbbbbbbbbbbbbb"

func TestKindOf(t *testing.T) {
	for name, want := range map[string]string{
		"hourly-2026-11-07T0917Z":             "hourly",
		"nightly-2026-11-07T0320Z":            "nightly",
		"predeploy-mcp-docs-2026-11-07T0432Z": "predeploy",
		"sitebin-2026-11-07":                  "archive",
	} {
		if got := KindOf(name); got != want {
			t.Errorf("KindOf(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestValidIDs(t *testing.T) {
	if !ValidViewID(siteA) || ValidViewID("AAAAAAAAAAAAAAAAAAAAAAAAAA") || ValidViewID("short") || ValidViewID(siteA+"/..") {
		t.Error("ValidViewID accepts or refuses the wrong ids")
	}
	for _, ok := range []string{"hourly-2026-11-07T0917Z", "sitebin-2026-11-07", "a.b_c-1"} {
		if !ValidSnapshotID(ok) {
			t.Errorf("ValidSnapshotID(%q) = false", ok)
		}
	}
	for _, bad := range []string{"", "-rf", "../x", "a/b", "a b", "::x", ".hidden"} {
		if ValidSnapshotID(bad) {
			t.Errorf("ValidSnapshotID(%q) = true", bad)
		}
	}
}

// The fingerprint is a set's: the order an archive lists its files in does
// not matter, and a nanosecond mtime (borg create) and the same second (a
// tar from sitebin backup) agree.
func TestFingerprintIgnoresOrderAndSubSeconds(t *testing.T) {
	t0 := time.Date(2026, 11, 7, 9, 17, 1, 0, time.UTC)
	var a, b Fingerprinter
	a.Add("index.html", 10, t0.Add(123456789))
	a.Add("app/app.js", 20, t0)
	b.Add("app/app.js", 20, t0.Add(999))
	b.Add("index.html", 10, t0)
	if a.Sum() != b.Sum() {
		t.Fatal("same files, different fingerprints")
	}
	var c Fingerprinter
	c.Add("index.html", 11, t0)
	c.Add("app/app.js", 20, t0)
	if c.Sum() == a.Sum() {
		t.Fatal("a size change did not change the fingerprint")
	}
	var empty Fingerprinter
	if empty.Sum() == a.Sum() || len(empty.Sum()) != 64 {
		t.Fatalf("empty fingerprint %q", empty.Sum())
	}
}

func TestVersionsGroupsEqualRunsNewestFirst(t *testing.T) {
	at := func(h int) time.Time { return time.Date(2026, 11, 7, h, 0, 0, 0, time.UTC) }
	snaps := []Snapshot{
		{ID: "hourly-5", At: at(5), Kind: "hourly"},
		{ID: "hourly-4", At: at(4), Kind: "hourly"},
		{ID: "nightly-3", At: at(3), Kind: "nightly"},
		{ID: "hourly-2", At: at(2), Kind: "hourly"},
		{ID: "hourly-1", At: at(1), Kind: "hourly"},
		{ID: "hourly-0", At: at(0), Kind: "hourly"},
	}
	fp := map[string]string{"hourly-5": "C", "hourly-4": "B", "nightly-3": "B", "hourly-2": "A", "hourly-0": "A"}
	idx := func(id string) (map[string]SiteSummary, bool) {
		if id == "hourly-1" {
			return nil, false // not indexed yet: left out, does not split a run
		}
		f, ok := fp[id]
		if !ok {
			return map[string]SiteSummary{}, true
		}
		return map[string]SiteSummary{siteA: {Files: len(f), Bytes: 1, Fingerprint: f}}, true
	}
	got := Versions(snaps, idx, siteA)
	var ids []string
	for _, v := range got {
		ids = append(ids, v.Snapshot+":"+v.Fingerprint+":"+v.First.Format("15"))
	}
	want := []string{"hourly-5:C:05", "hourly-4:B:03", "hourly-2:A:00"}
	if !reflect.DeepEqual(ids, want) {
		t.Fatalf("versions = %v, want %v", ids, want)
	}
	if got[1].Kind != "hourly" || !got[1].At.Equal(at(4)) {
		t.Errorf("a run is named by its newest snapshot: %+v", got[1])
	}
	if Versions(snaps, idx, siteB) != nil && len(Versions(snaps, idx, siteB)) != 0 {
		t.Error("a site in no snapshot has versions")
	}
}

func TestIndexPutGetPrune(t *testing.T) {
	x := Index{Dir: t.TempDir()}
	m := map[string]SiteSummary{siteA: {Files: 2, Bytes: 30, Fingerprint: "ff"}}
	if err := x.Put("hourly-1", m); err != nil {
		t.Fatal(err)
	}
	x.Put("hourly-2", m)
	if got, ok := x.Get("hourly-1"); !ok || got[siteA].Bytes != 30 {
		t.Fatalf("Get = %v %v", got, ok)
	}
	if _, ok := x.Get("nope"); ok {
		t.Fatal("Get of an unknown snapshot")
	}
	x.Prune([]string{"hourly-2"})
	if _, ok := x.Get("hourly-1"); ok {
		t.Error("the index of a snapshot that is gone was kept")
	}
	if _, ok := x.Get("hourly-2"); !ok {
		t.Error("a kept snapshot's index was deleted")
	}
}

// ---- the dir source: a folder of `sitebin backup` archives ----

type entry struct {
	name, body string
	link       string
	mtime      time.Time
}

func writeArchive(t *testing.T, path string, gz bool, entries []entry) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var w io.Writer = f
	var zw *gzip.Writer
	if gz {
		zw = gzip.NewWriter(f)
		w = zw
	}
	tw := tar.NewWriter(w)
	for _, e := range entries {
		h := &tar.Header{Name: e.name, Mode: 0o644, ModTime: e.mtime}
		switch {
		case e.link != "":
			h.Typeflag, h.Linkname = tar.TypeSymlink, e.link
		case e.name[len(e.name)-1] == '/':
			h.Typeflag, h.Mode = tar.TypeDir, 0o755
		default:
			h.Typeflag, h.Size = tar.TypeReg, int64(len(e.body))
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if h.Typeflag == tar.TypeReg {
			tw.Write([]byte(e.body))
		}
	}
	tw.Close()
	if zw != nil {
		zw.Close()
	}
}

func dirFixture(t *testing.T) (*DirSource, time.Time) {
	t.Helper()
	dir := t.TempDir()
	t0 := time.Date(2026, 11, 6, 3, 20, 0, 0, time.UTC)
	writeArchive(t, filepath.Join(dir, "sitebin-2026-11-06.tar.gz"), true, []entry{
		{name: "sites/", mtime: t0},
		{name: "sites/" + siteA + "/meta.json", body: `{"mode":"webserver"}`, mtime: t0},
		{name: "sites/" + siteA + "/files/index.html", body: "old", mtime: t0},
		{name: "edit-index/e1", link: "../sites/" + siteA, mtime: t0},
	})
	writeArchive(t, filepath.Join(dir, "sitebin-2026-11-07.tar"), false, []entry{
		{name: "sites/" + siteA + "/meta.json", body: `{"mode":"viewer"}`, mtime: t0},
		{name: "sites/" + siteA + "/files/_raw/doc.md", body: "new", mtime: t0.Add(24 * time.Hour)},
		{name: "sites/" + siteA + "/files/index.html", body: "wrapper", mtime: t0},
		{name: "sites/" + siteB + "/meta.json", body: `{}`, mtime: t0},
		{name: "sites/" + siteB + "x/files/stray", body: "no meta", mtime: t0},
	})
	os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("not an archive"), 0o644)
	os.Chtimes(filepath.Join(dir, "sitebin-2026-11-06.tar.gz"), t0, t0)
	os.Chtimes(filepath.Join(dir, "sitebin-2026-11-07.tar"), t0.Add(24*time.Hour), t0.Add(24*time.Hour))
	return &DirSource{Dir: dir}, t0
}

func TestDirSourceListsArchivesNewestFirst(t *testing.T) {
	src, t0 := dirFixture(t)
	got, err := src.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != "sitebin-2026-11-07" || got[1].ID != "sitebin-2026-11-06" {
		t.Fatalf("List = %+v", got)
	}
	if !got[1].At.Equal(t0) || got[1].Kind != "archive" {
		t.Errorf("snapshot = %+v", got[1])
	}
}

func TestDirSourceWalkReadAndTar(t *testing.T) {
	src, _ := dirFixture(t)
	ctx := context.Background()
	var paths []string
	err := src.Walk(ctx, "sitebin-2026-11-07", "sites/", func(it Item) error {
		if it.Regular {
			paths = append(paths, it.Path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(paths)
	if len(paths) != 5 || paths[0] != "sites/"+siteA+"/files/_raw/doc.md" {
		t.Fatalf("Walk = %v", paths)
	}
	b, err := src.ReadFile(ctx, "sitebin-2026-11-07", "sites/"+siteA+"/meta.json", 1<<20)
	if err != nil || string(b) != `{"mode":"viewer"}` {
		t.Fatalf("ReadFile = %q %v", b, err)
	}
	if _, err := src.ReadFile(ctx, "sitebin-2026-11-07", "sites/nothing/meta.json", 1<<20); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing file: %v", err)
	}
	if _, err := src.ReadFile(ctx, "sitebin-2026-11-07", "sites/"+siteA+"/meta.json", 3); err == nil {
		t.Fatal("ReadFile ignored max")
	}
	if err := src.Tar(ctx, "nope", nil, io.Discard); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown snapshot: %v", err)
	}
	var buf bytes.Buffer
	if err := src.Tar(ctx, "sitebin-2026-11-06", []string{"sites/" + siteA + "/files"}, &buf); err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(&buf)
	var names []string
	for {
		h, err := tr.Next()
		if err != nil {
			break
		}
		names = append(names, h.Name)
	}
	if !reflect.DeepEqual(names, []string{"sites/" + siteA + "/files/index.html"}) {
		t.Fatalf("Tar filtered to %v", names)
	}
}

func TestSummarizeCountsOnlySitesWithMeta(t *testing.T) {
	src, _ := dirFixture(t)
	m, err := Summarize(context.Background(), src, "sitebin-2026-11-07")
	if err != nil {
		t.Fatal(err)
	}
	if len(m) != 2 {
		t.Fatalf("sites = %v", m)
	}
	a := m[siteA]
	if a.Files != 2 || a.Bytes != int64(len("new")+len("wrapper")) {
		t.Errorf("site A = %+v", a)
	}
	var f Fingerprinter
	t0 := time.Date(2026, 11, 6, 3, 20, 0, 0, time.UTC)
	f.Add("_raw/doc.md", 3, t0.Add(24*time.Hour))
	f.Add("index.html", 7, t0)
	if a.Fingerprint != f.Sum() {
		t.Error("the summary's fingerprint is not over the paths relative to files/")
	}
	if m[siteB].Files != 0 {
		t.Errorf("site B = %+v", m[siteB])
	}
}
