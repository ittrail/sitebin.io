package store

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

func stagingDirs(t *testing.T, site *Site) []string {
	t.Helper()
	m, err := filepath.Glob(filepath.Join(site.Dir(), replaceStagingPrefix+"*"))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// fileNames lists a site's content as "a,b/c", sorted.
func fileNames(t *testing.T, s *Store, site *Site) string {
	t.Helper()
	files, err := s.ListFiles(site)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(files))
	for _, f := range files {
		names = append(names, f.Path)
	}
	sort.Strings(names)
	return strings.Join(names, ",")
}

func TestReplaceCommitSwapsTheContent(t *testing.T) {
	s := newTestStore(t)
	site, _, _ := s.Create()
	s.SaveFile(site, "old.txt", strings.NewReader("old"))
	s.SaveFile(site, "dir/older.txt", strings.NewReader("older"))
	if err := s.SetTrusted(site, true); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(site.ContentDir())
	if err != nil {
		t.Fatal(err)
	}

	rep, err := s.BeginReplace(site)
	if err != nil {
		t.Fatal(err)
	}
	defer rep.Abort()
	if err := rep.SaveFile("index.html", strings.NewReader("new")); err != nil {
		t.Fatal(err)
	}
	if got := fileNames(t, s, site); got != "dir/older.txt,old.txt" {
		t.Fatalf("the site changed before Commit: %s", got)
	}
	if err := rep.Commit(); err != nil {
		t.Fatal(err)
	}

	if got := fileNames(t, s, site); got != "index.html" {
		t.Fatalf("after Commit: %s", got)
	}
	if !s.Trusted(site) {
		t.Error("the trust marker did not survive the replace")
	}
	after, err := os.Stat(site.ContentDir())
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) {
		t.Error("the content directory was replaced; a container's bind mount would lose it")
	}
	if d := stagingDirs(t, site); len(d) != 0 {
		t.Errorf("staging left behind: %v", d)
	}
}

func TestReplaceAbortLeavesTheSiteAsItWas(t *testing.T) {
	s := newTestStore(t)
	site, _, _ := s.Create()
	s.SaveFile(site, "old.txt", strings.NewReader("old"))
	rep, err := s.BeginReplace(site)
	if err != nil {
		t.Fatal(err)
	}
	rep.SaveFile("new.txt", strings.NewReader("new"))
	rep.Abort()
	rep.Abort() // twice is harmless
	if got := fileNames(t, s, site); got != "old.txt" {
		t.Fatalf("after Abort: %s", got)
	}
	if d := stagingDirs(t, site); len(d) != 0 {
		t.Errorf("staging left behind: %v", d)
	}
	if err := rep.Commit(); err == nil {
		t.Error("Commit after Abort succeeded")
	}
}

func TestReplaceCountsOnlyTheNewContent(t *testing.T) {
	s, err := New(t.TempDir(), "sitebin.example", 10, 100)
	if err != nil {
		t.Fatal(err)
	}
	site, _, _ := s.Create()
	if err := s.SaveFile(site, "old.txt", strings.NewReader("12345678")); err != nil {
		t.Fatal(err)
	}
	rep, _ := s.BeginReplace(site)
	defer rep.Abort()
	// 9 bytes on top of the old 8 would break the cap of 10; on their own they fit.
	if err := rep.SaveFile("new.txt", strings.NewReader("123456789")); err != nil {
		t.Fatalf("the replaced content was counted against the replacement: %v", err)
	}
	if err := rep.SaveFile("more.txt", strings.NewReader("12")); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("over the cap within the replacement: %v", err)
	}
}

func TestReplaceWithAnOverQuotaZipLeavesTheSiteIntact(t *testing.T) {
	s, err := New(t.TempDir(), "sitebin.example", 50, 100)
	if err != nil {
		t.Fatal(err)
	}
	site, _, _ := s.Create()
	s.SaveFile(site, "index.html", strings.NewReader("old"))
	data := makeZip(t, map[string]string{"big.txt": strings.Repeat("A", 500)}, false)
	rep, _ := s.BeginReplace(site)
	if err := rep.ExtractZip(bytes.NewReader(data), int64(len(data))); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("zip over the cap: %v", err)
	}
	rep.Abort()
	if got := fileNames(t, s, site); got != "index.html" {
		t.Fatalf("after a failed replace: %s", got)
	}
}

func TestReplaceWithACorruptZipLeavesTheSiteIntact(t *testing.T) {
	s := newTestStore(t)
	site, _, _ := s.Create()
	s.SaveFile(site, "index.html", strings.NewReader("old"))
	rep, _ := s.BeginReplace(site)
	junk := []byte("this is not a zip archive")
	if err := rep.ExtractZip(bytes.NewReader(junk), int64(len(junk))); err == nil {
		t.Fatal("a corrupt zip was accepted")
	}
	rep.Abort()
	if got := fileNames(t, s, site); got != "index.html" {
		t.Fatalf("after a failed replace: %s", got)
	}
}

func TestReplaceZipCommits(t *testing.T) {
	s := newTestStore(t)
	site, _, _ := s.Create()
	s.SaveFile(site, "old.txt", strings.NewReader("old"))
	data := makeZip(t, map[string]string{"index.html": "<p>", "assets/app.js": "js"}, false)
	rep, _ := s.BeginReplace(site)
	defer rep.Abort()
	if err := rep.ExtractZip(bytes.NewReader(data), int64(len(data))); err != nil {
		t.Fatal(err)
	}
	if err := rep.Commit(); err != nil {
		t.Fatal(err)
	}
	if got := fileNames(t, s, site); got != "assets/app.js,index.html" {
		t.Fatalf("after Commit: %s", got)
	}
}

func TestReplaceWithNothingEmptiesTheSite(t *testing.T) {
	s := newTestStore(t)
	site, _, _ := s.Create()
	s.SaveFile(site, "old.txt", strings.NewReader("old"))
	rep, _ := s.BeginReplace(site)
	if err := rep.Commit(); err != nil {
		t.Fatal(err)
	}
	if got := fileNames(t, s, site); got != "" {
		t.Fatalf("after an empty replace: %s", got)
	}
}

func TestReplaceInViewerModeLandsInTheRawDir(t *testing.T) {
	s := newTestStore(t)
	site, _, _ := s.Create()
	if err := s.Update(site, func(m *Meta) error { m.Mode = ModeViewer; return nil }); err != nil {
		t.Fatal(err)
	}
	rep, _ := s.BeginReplace(site)
	defer rep.Abort()
	if err := rep.SaveFile("doc.md", strings.NewReader("# hi")); err != nil {
		t.Fatal(err)
	}
	if err := rep.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(site.ContentDir(), "doc.md")); err != nil {
		t.Fatalf("doc.md is not in the viewer's content dir: %v", err)
	}
}

func TestBeginReplaceRemovesStaleStaging(t *testing.T) {
	s := newTestStore(t)
	site, _, _ := s.Create()
	stale := filepath.Join(site.Dir(), replaceStagingPrefix+"stale")
	fresh := filepath.Join(site.Dir(), replaceStagingPrefix+"fresh")
	for _, d := range []string{stale, fresh} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}
	rep, err := s.BeginReplace(site)
	if err != nil {
		t.Fatal(err)
	}
	defer rep.Abort()
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Error("a staging directory a crash left two hours ago survived")
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Error("a staging directory another upload may still be using was removed")
	}
}
