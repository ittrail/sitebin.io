package store

import (
	"bytes"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// writeRepoFile puts n bytes at rel under the site's repository, as a push
// would have left them.
func writeRepoFile(t *testing.T, site *Site, rel string, n int) {
	t.Helper()
	p := filepath.Join(site.GitDir(), filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, bytes.Repeat([]byte("g"), n), 0o644); err != nil {
		t.Fatal(err)
	}
}

// capSite gives the site a byte cap of max.
func capSite(t *testing.T, s *Store, site *Site, max int64) {
	t.Helper()
	if err := s.Update(site, func(m *Meta) error { m.QuotaBytes = max; return nil }); err != nil {
		t.Fatal(err)
	}
}

func TestSiteUsageCountsRepository(t *testing.T) {
	s := newTestStore(t)
	site, _, _ := s.Create()
	if err := s.SaveFile(site, "index.html", strings.NewReader(strings.Repeat("f", 100))); err != nil {
		t.Fatal(err)
	}
	u, err := s.SiteUsage(site)
	if err != nil {
		t.Fatal(err)
	}
	if u != (Usage{FileBytes: 100, Files: 1}) || u.Bytes() != 100 {
		t.Fatalf("without a repository: %+v, Bytes %d", u, u.Bytes())
	}
	if site.GitDir() != filepath.Join(site.Dir(), "git") {
		t.Fatalf("GitDir = %s, want git/ beside files/", site.GitDir())
	}
	writeRepoFile(t, site, "objects/pack/pack-1.pack", 50)
	u, err = s.SiteUsage(site)
	if err != nil {
		t.Fatal(err)
	}
	if u.Bytes() != 150 || u.Files != 1 || u.FileBytes != 100 || u.GitBytes != 50 {
		t.Fatalf("usage = %+v, Bytes %d; want 100 + 50 = 150 bytes and 1 file", u, u.Bytes())
	}
	// The container runtime's stop rule keeps counting files only.
	if b, n, _ := s.Usage(site); b != 100 || n != 1 {
		t.Fatalf("Usage = %d bytes, %d files; want the files alone", b, n)
	}
}

// Only regular files count in the repository, as in files/: a link there is
// nothing anyone pushed, and is never followed.
func TestSiteUsageRepositoryCountsRegularFilesOnly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need elevation on windows")
	}
	s := newTestStore(t)
	site, _, _ := s.Create()
	writeRepoFile(t, site, "HEAD", 21)
	outside := filepath.Join(t.TempDir(), "big")
	if err := os.WriteFile(outside, bytes.Repeat([]byte("x"), 1000), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(site.GitDir(), "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Dir(outside), filepath.Join(site.GitDir(), "dirlink")); err != nil {
		t.Fatal(err)
	}
	u, err := s.SiteUsage(site)
	if err != nil {
		t.Fatal(err)
	}
	if u.GitBytes != 21 {
		t.Fatalf("GitBytes = %d, want 21 (links neither counted nor followed)", u.GitBytes)
	}
}

func TestSaveFileCountsRepository(t *testing.T) {
	s := newTestStore(t)
	site, _, _ := s.Create()
	capSite(t, s, site, 120)
	writeRepoFile(t, site, "objects/pack/pack-1.pack", 100)
	if err := s.SaveFile(site, "a.txt", strings.NewReader(strings.Repeat("x", 30))); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("30 bytes beside a 100-byte repository under a 120-byte cap: %v, want ErrTooLarge", err)
	}
	if err := s.SaveFile(site, "a.txt", strings.NewReader(strings.Repeat("x", 20))); err != nil {
		t.Fatalf("20 bytes fill the cap exactly: %v", err)
	}
}

func TestExtractZipCountsRepository(t *testing.T) {
	s := newTestStore(t)
	site, _, _ := s.Create()
	capSite(t, s, site, 120)
	writeRepoFile(t, site, "objects/pack/pack-1.pack", 100)
	data := makeZip(t, map[string]string{"a.txt": strings.Repeat("x", 30)}, false)
	if err := s.ExtractZip(site, bytes.NewReader(data), int64(len(data))); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("a 30-byte zip beside a 100-byte repository under a 120-byte cap: %v, want ErrTooLarge", err)
	}
	data = makeZip(t, map[string]string{"a.txt": strings.Repeat("x", 20)}, false)
	if err := s.ExtractZip(site, bytes.NewReader(data), int64(len(data))); err != nil {
		t.Fatalf("20 bytes fill the cap exactly: %v", err)
	}
}

func TestReplaceBudgetCountsRepository(t *testing.T) {
	s := newTestStore(t)
	site, _, _ := s.Create()
	capSite(t, s, site, 120)
	// The content a replace replaces counts for nothing; the repository stays.
	if err := s.SaveFile(site, "old.txt", strings.NewReader(strings.Repeat("o", 15))); err != nil {
		t.Fatal(err)
	}
	writeRepoFile(t, site, "objects/pack/pack-1.pack", 100)

	rep, err := s.BeginReplace(site)
	if err != nil {
		t.Fatal(err)
	}
	if err := rep.SaveFile("a.txt", strings.NewReader(strings.Repeat("x", 30))); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("a 30-byte replace beside a 100-byte repository under a 120-byte cap: %v, want ErrTooLarge", err)
	}
	rep.Abort()

	rep, err = s.BeginReplace(site)
	if err != nil {
		t.Fatal(err)
	}
	defer rep.Abort()
	if err := rep.SaveFile("a.txt", strings.NewReader(strings.Repeat("x", 20))); err != nil {
		t.Fatalf("20 bytes fill the cap exactly: %v", err)
	}
	if err := rep.Commit(); err != nil {
		t.Fatal(err)
	}
	if u, _ := s.SiteUsage(site); u.Bytes() != 120 || u.Files != 1 {
		t.Fatalf("after the replace: %+v", u)
	}
}

// byteChecks is every place that checks a write against a site's byte cap,
// and the test proving it counts the repository too (files and repository
// share one cap; SiteUsage is the one function that counts them). A new way
// to put bytes into a site joins this table with its test. The last row is
// the one deliberate exception: the container stop rule counts files only.
var byteChecks = []struct {
	check string
	dir   string // the package holding the test, from the repository root
	test  string
}{
	{"Store.SaveFile (API, MCP, upload tokens)", "internal/store", "TestSaveFileCountsRepository"},
	{"Store.ExtractZip (zip uploads)", "internal/store", "TestExtractZipCountsRepository"},
	{"Replacement (staged replace: budget fixed at BeginReplace)", "internal/store", "TestReplaceBudgetCountsRepository"},
	{"WebDAV PUT", "internal/httpapi", "TestWebDAVPutCountsRepository"},
	{"FTP transfers (Session.UsedOutside)", "internal/ftp", "TestFTPCountsRepository"},
	{"FTP login hands over the repository's bytes", "internal/httpapi", "TestFTPAuthReportsRepository"},
	{"EXCEPTION: container stop rule keeps counting files only", "ee/containers", "TestEnforceStorageIgnoresRepository"},
}

func TestByteChecksCountRepository(t *testing.T) {
	tests := map[string]map[string]bool{} // dir -> test funcs
	for _, c := range byteChecks {
		if tests[c.dir] == nil {
			tests[c.dir] = testFuncs(t, c.dir)
		}
		if !tests[c.dir][c.test] {
			t.Errorf("byte check %s: its test %s is not in %s", c.check, c.test, c.dir)
		}
	}
}

// testFuncs lists the Test functions declared in a package's _test.go files,
// whatever their build tags.
func testFuncs(t *testing.T, dir string) map[string]bool {
	t.Helper()
	files, err := filepath.Glob(filepath.Join("..", "..", filepath.FromSlash(dir), "*_test.go"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no tests in %s: %v", dir, err)
	}
	out := map[string]bool{}
	for _, f := range files {
		af, err := parser.ParseFile(token.NewFileSet(), f, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range af.Decls {
			if fd, ok := d.(*ast.FuncDecl); ok && fd.Recv == nil && strings.HasPrefix(fd.Name.Name, "Test") {
				out[fd.Name.Name] = true
			}
		}
	}
	return out
}
