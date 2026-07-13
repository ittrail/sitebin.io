package store

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/ittrail/sitebin/internal/auth"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := New(t.TempDir(), "sitebin.example", 1<<20, 100)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s
}

func TestCreate(t *testing.T) {
	s := newTestStore(t)
	site, pw, err := s.Create()
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if site.ViewID == "" || site.EditID == "" || pw == "" {
		t.Fatal("missing identifiers")
	}
	if site.ViewID == site.EditID {
		t.Error("view and edit ids must differ")
	}
	if site.Meta.Mode != ModeWebserver {
		t.Errorf("default mode = %q", site.Meta.Mode)
	}
	if site.Meta.EntryFile != "index.html" {
		t.Errorf("entry file = %q", site.Meta.EntryFile)
	}
	if !auth.VerifyPassword(site.Meta.EditPasswordHash, pw) {
		t.Error("edit password hash does not verify")
	}
	if site.Meta.EditPasswordHash == pw {
		t.Error("plaintext stored")
	}
	if fi, err := os.Stat(site.FilesDir()); err != nil || !fi.IsDir() {
		t.Errorf("files dir missing: %v", err)
	}
	// created_at set, custom_domains serializes as [] not null
	if site.Meta.CreatedAt.IsZero() || site.Meta.CustomDomains == nil {
		t.Error("meta defaults incomplete")
	}
}

func TestLookups(t *testing.T) {
	s := newTestStore(t)
	site, _, _ := s.Create()

	got, err := s.ByViewID(site.ViewID)
	if err != nil || got.EditID != site.EditID {
		t.Fatalf("ByViewID: %v", err)
	}
	got, err = s.ByEditID(site.EditID)
	if err != nil || got.ViewID != site.ViewID {
		t.Fatalf("ByEditID: %v", err)
	}
	if _, err := s.ByViewID("zzzzzzzzzzzzzzzzzzzzzzzzzz"); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing view id: %v", err)
	}
	if _, err := s.ByEditID("../../../../etc/passwd"); !errors.Is(err, ErrNotFound) {
		t.Errorf("invalid edit id must be ErrNotFound, got %v", err)
	}
	if _, err := s.ByDomain("nope.example.org"); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing domain: %v", err)
	}
}

func TestCleanRelPath(t *testing.T) {
	ok := map[string]string{
		"a.txt":        "a.txt",
		"dir/a.txt":    "dir/a.txt",
		"./a.txt":      "a.txt",
		"a/./b":        "a/b",
		"a/../b.txt":   "b.txt",
		"deep/x/y/z":   "deep/x/y/z",
		"_rawish/file": "_rawish/file",
	}
	for in, want := range ok {
		got, err := CleanRelPath(in)
		if err != nil || got != want {
			t.Errorf("CleanRelPath(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	bad := []string{
		"", ".", "..", "../x", "a/../../x", "/abs", "//x",
		`C:\x`, `a\b`, "_raw/x", "_RAW/x", "_sitebin/x", "_sitebin",
		"meta.json", "META.JSON", "a\x00b", "a\nb",
		strings.Repeat("x", 300),
	}
	for _, in := range bad {
		if got, err := CleanRelPath(in); err == nil {
			t.Errorf("CleanRelPath(%q) = %q; want error", in, got)
		}
	}
	// nested reserved names are fine (only top level is reserved)
	if _, err := CleanRelPath("docs/meta.json"); err != nil {
		t.Errorf("nested meta.json should be allowed: %v", err)
	}
}

func TestSaveListDelete(t *testing.T) {
	s := newTestStore(t)
	site, _, _ := s.Create()

	if err := s.SaveFile(site, "index.html", strings.NewReader("<h1>hi</h1>")); err != nil {
		t.Fatalf("SaveFile: %v", err)
	}
	if err := s.SaveFile(site, "css/style.css", strings.NewReader("body{}")); err != nil {
		t.Fatalf("SaveFile nested: %v", err)
	}
	files, err := s.ListFiles(site)
	if err != nil || len(files) != 2 {
		t.Fatalf("ListFiles = %v, %v", files, err)
	}
	if files[0].Path != "css/style.css" && files[1].Path != "css/style.css" {
		t.Errorf("nested path missing: %+v", files)
	}
	// overwrite shrinks, doesn't double-count
	if err := s.SaveFile(site, "index.html", strings.NewReader("x")); err != nil {
		t.Fatalf("overwrite: %v", err)
	}
	b, _ := os.ReadFile(filepath.Join(site.FilesDir(), "index.html"))
	if string(b) != "x" {
		t.Errorf("overwrite content = %q", b)
	}
	if err := s.DeleteFile(site, "css/style.css"); err != nil {
		t.Fatalf("DeleteFile: %v", err)
	}
	if _, err := os.Stat(filepath.Join(site.FilesDir(), "css")); !os.IsNotExist(err) {
		t.Error("empty parent dir not pruned")
	}
	if err := s.DeleteFile(site, "../meta.json"); err == nil {
		t.Error("traversal delete allowed")
	}
}

func TestLimits(t *testing.T) {
	s, err := New(t.TempDir(), "sitebin.example", 100, 2)
	if err != nil {
		t.Fatal(err)
	}
	site, _, _ := s.Create()
	if err := s.SaveFile(site, "a", strings.NewReader(strings.Repeat("x", 60))); err != nil {
		t.Fatalf("first: %v", err)
	}
	if err := s.SaveFile(site, "b", strings.NewReader(strings.Repeat("x", 60))); !errors.Is(err, ErrTooLarge) {
		t.Errorf("byte cap: %v", err)
	}
	if err := s.SaveFile(site, "b", strings.NewReader("ok")); err != nil {
		t.Fatalf("second small: %v", err)
	}
	if err := s.SaveFile(site, "c", strings.NewReader("x")); !errors.Is(err, ErrTooManyFiles) {
		t.Errorf("file cap: %v", err)
	}
}

func makeZip(t *testing.T, entries map[string]string, withSymlink bool) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for name, content := range entries {
		f, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if withSymlink {
		h := &zip.FileHeader{Name: "evil-link"}
		h.SetMode(os.ModeSymlink | 0o777)
		f, err := w.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write([]byte("/etc/passwd")); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestExtractZip(t *testing.T) {
	s := newTestStore(t)
	site, _, _ := s.Create()
	data := makeZip(t, map[string]string{
		"index.html":     "<html></html>",
		"assets/app.js":  "console.log(1)",
		"assets/app.css": "body{}",
	}, false)
	if err := s.ExtractZip(site, bytes.NewReader(data), int64(len(data))); err != nil {
		t.Fatalf("ExtractZip: %v", err)
	}
	files, _ := s.ListFiles(site)
	if len(files) != 3 {
		t.Fatalf("extracted %d files: %+v", len(files), files)
	}
	b, _ := os.ReadFile(filepath.Join(site.FilesDir(), "assets", "app.js"))
	if string(b) != "console.log(1)" {
		t.Errorf("content = %q", b)
	}
}

func TestExtractZipRejectsSymlink(t *testing.T) {
	s := newTestStore(t)
	site, _, _ := s.Create()
	data := makeZip(t, map[string]string{"ok.txt": "fine"}, true)
	if err := s.ExtractZip(site, bytes.NewReader(data), int64(len(data))); err == nil {
		t.Fatal("zip with symlink entry accepted")
	}
}

func TestExtractZipTraversal(t *testing.T) {
	s := newTestStore(t)
	site, _, _ := s.Create()
	data := makeZip(t, map[string]string{"../escape.txt": "boom"}, false)
	if err := s.ExtractZip(site, bytes.NewReader(data), int64(len(data))); err == nil {
		t.Fatal("zip traversal accepted")
	}
	if _, err := os.Stat(filepath.Join(site.Dir(), "escape.txt")); !os.IsNotExist(err) {
		t.Fatal("file escaped files/ root")
	}
}

func TestExtractZipBudget(t *testing.T) {
	s, _ := New(t.TempDir(), "sitebin.example", 50, 100)
	site, _, _ := s.Create()
	data := makeZip(t, map[string]string{"big.txt": strings.Repeat("A", 500)}, false)
	if err := s.ExtractZip(site, bytes.NewReader(data), int64(len(data))); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("zip bomb not stopped: %v", err)
	}
}

func TestDomains(t *testing.T) {
	s := newTestStore(t)
	site, _, _ := s.Create()

	if err := s.AddDomain(site, "Client.Example.ORG"); err != nil {
		t.Fatalf("AddDomain: %v", err)
	}
	if got := site.Meta.CustomDomains; len(got) != 1 || got[0] != "client.example.org" {
		t.Errorf("domains = %v", got)
	}
	if got, err := s.ByDomain("client.example.org"); err != nil || got.ViewID != site.ViewID {
		t.Fatalf("ByDomain: %v", err)
	}
	for _, bad := range []string{
		"", "no-dot", "-bad.example.org", "bad-.example.org", "ex ample.org",
		"sitebin.example", "sub.sitebin.example", "a..b.org", "x.y!z.org",
		strings.Repeat("a", 64) + ".org",
	} {
		if err := s.AddDomain(site, bad); err == nil {
			t.Errorf("AddDomain(%q) accepted", bad)
		}
	}
	other, _, _ := s.Create()
	if err := s.AddDomain(other, "client.example.org"); !errors.Is(err, ErrDomainTaken) {
		t.Errorf("duplicate domain: %v", err)
	}
	if err := s.RemoveDomain(site, "client.example.org"); err != nil {
		t.Fatalf("RemoveDomain: %v", err)
	}
	if _, err := s.ByDomain("client.example.org"); !errors.Is(err, ErrNotFound) {
		t.Errorf("domain still resolves after removal: %v", err)
	}
	if len(site.Meta.CustomDomains) != 0 {
		t.Errorf("meta still lists domain: %v", site.Meta.CustomDomains)
	}
}

func TestDeleteSite(t *testing.T) {
	s := newTestStore(t)
	site, _, _ := s.Create()
	if err := s.AddDomain(site, "gone.example.org"); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(site); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := os.Stat(site.Dir()); !os.IsNotExist(err) {
		t.Error("site dir survives delete")
	}
	if _, err := s.ByEditID(site.EditID); !errors.Is(err, ErrNotFound) {
		t.Errorf("edit index survives delete: %v", err)
	}
	if _, err := s.ByDomain("gone.example.org"); !errors.Is(err, ErrNotFound) {
		t.Errorf("domain index survives delete: %v", err)
	}
}

func TestContentDirFollowsMode(t *testing.T) {
	s := newTestStore(t)
	site, _, _ := s.Create()
	if site.ContentDir() != site.FilesDir() {
		t.Error("webserver mode should serve files/ directly")
	}
	if err := s.Update(site, func(m *Meta) error { m.Mode = ModeViewer; return nil }); err != nil {
		t.Fatal(err)
	}
	if site.ContentDir() != filepath.Join(site.FilesDir(), "_raw") {
		t.Error("viewer mode content dir should be files/_raw")
	}
}

func TestUpdateConcurrent(t *testing.T) {
	s := newTestStore(t)
	site, _, _ := s.Create()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			// each goroutine works on its own handle, as HTTP handlers would
			h, err := s.ByViewID(site.ViewID)
			if err != nil {
				t.Errorf("lookup: %v", err)
				return
			}
			err = s.Update(h, func(m *Meta) error {
				m.CustomDomains = append(m.CustomDomains, fmt.Sprintf("d%d.example.org", n))
				return nil
			})
			if err != nil {
				t.Errorf("update: %v", err)
			}
		}(i)
	}
	wg.Wait()
	got, _ := s.ByViewID(site.ViewID)
	if len(got.Meta.CustomDomains) != 50 {
		t.Errorf("lost updates: %d/50 domains", len(got.Meta.CustomDomains))
	}
	if got.Meta.UpdatedAt.Before(got.Meta.CreatedAt) {
		t.Error("updated_at not bumped")
	}
}
