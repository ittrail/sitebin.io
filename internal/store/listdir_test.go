package store

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func entryNames(entries []DirEntry) string {
	var b strings.Builder
	for i, e := range entries {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(e.Name)
		if e.Dir {
			b.WriteString("/")
		}
	}
	return b.String()
}

func TestListDirShowsOneFolderFoldersFirst(t *testing.T) {
	s := newTestStore(t)
	site, _, _ := s.Create()
	for _, p := range []string{"index.html", "B.txt", "a.txt", "assets/app.js", "assets/img/logo.png", "Docs/x.md"} {
		if err := s.SaveFile(site, p, strings.NewReader("12345")); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.SetTrusted(site, true); err != nil {
		t.Fatal(err)
	}
	entries, truncated, err := s.ListDir(site, "")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := entryNames(entries), "assets/,Docs/,a.txt,B.txt,index.html"; got != want {
		t.Errorf("root = %s, want %s (folders first, case-insensitive, no markers)", got, want)
	}
	if truncated {
		t.Error("a small folder reported truncated")
	}
	for _, e := range entries {
		if !e.Dir && e.Size != 5 {
			t.Errorf("%s: size %d, want 5", e.Name, e.Size)
		}
	}
	entries, _, err = s.ListDir(site, "assets")
	if err != nil {
		t.Fatal(err)
	}
	if got := entryNames(entries); got != "img/,app.js" {
		t.Errorf("assets = %s", got)
	}
}

func TestListDirRefusesBadAndMissingPaths(t *testing.T) {
	s := newTestStore(t)
	site, _, _ := s.Create()
	s.SaveFile(site, "index.html", strings.NewReader("x"))
	if _, _, err := s.ListDir(site, "../other"); !errors.Is(err, ErrBadPath) {
		t.Errorf("traversal: %v, want ErrBadPath", err)
	}
	if _, _, err := s.ListDir(site, "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing folder: %v, want ErrNotFound", err)
	}
	if _, _, err := s.ListDir(site, "index.html"); !errors.Is(err, ErrNotFound) {
		t.Errorf("a file as a folder: %v, want ErrNotFound", err)
	}
}

func TestListDirInViewerModeListsTheRawContent(t *testing.T) {
	s := newTestStore(t)
	site, _, _ := s.Create()
	if err := s.Update(site, func(m *Meta) error { m.Mode = ModeViewer; return nil }); err != nil {
		t.Fatal(err)
	}
	s.SaveFile(site, "doc.md", strings.NewReader("# hi"))
	entries, _, err := s.ListDir(site, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := entryNames(entries); got != "doc.md" {
		t.Errorf("viewer root = %s", got)
	}
}

// Links a container left are not content, and the listing never follows one
// out of the site.
func TestListDirSkipsLinksAndNeverLeavesTheSite(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("links are the production platform's")
	}
	s := newTestStore(t)
	site, _, _ := s.Create()
	s.SaveFile(site, "index.html", strings.NewReader("x"))
	outside := t.TempDir()
	os.WriteFile(filepath.Join(outside, "secret"), []byte("s"), 0o644)
	if err := os.Symlink(outside, filepath.Join(site.ContentDir(), "out")); err != nil {
		t.Fatal(err)
	}
	entries, _, err := s.ListDir(site, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := entryNames(entries); got != "index.html" {
		t.Errorf("root = %s, want the link left out", got)
	}
	if _, _, err := s.ListDir(site, "out"); err == nil {
		t.Error("the listing followed a link out of the site")
	}
}

func TestListDirCapsAHugeFolder(t *testing.T) {
	s := newTestStore(t)
	site, _, _ := s.Create()
	dir := filepath.Join(site.ContentDir(), "many")
	os.MkdirAll(dir, 0o755)
	for i := 0; i < 30; i++ {
		os.WriteFile(filepath.Join(dir, strings.Repeat("f", 1)+string(rune('a'+i%26))+strings.Repeat("x", i)), []byte("x"), 0o644)
	}
	old := maxDirEntries
	maxDirEntries = 10
	defer func() { maxDirEntries = old }()
	entries, truncated, err := s.ListDir(site, "many")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 10 || !truncated {
		t.Errorf("%d entries, truncated=%v; want 10, true", len(entries), truncated)
	}
}

// A name the API could not address (here a top-level meta.json a container
// wrote into the content root) is not offered: the page could not open or
// delete it.
func TestListDirHidesNamesTheAPICannotAddress(t *testing.T) {
	s := newTestStore(t)
	site, _, _ := s.Create()
	s.SaveFile(site, "index.html", strings.NewReader("x"))
	os.WriteFile(filepath.Join(site.ContentDir(), "meta.json"), []byte("{}"), 0o644)
	entries, _, err := s.ListDir(site, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := entryNames(entries); got != "index.html" {
		t.Errorf("root = %s, want meta.json left out", got)
	}
}

// A folder a container made unreadable is ErrUnreadable (a 403), not an
// internal error.
func TestListDirOfAnUnreadableFolder(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("permission bits bind a non-root user on the production platform")
	}
	s := newTestStore(t)
	site, _, _ := s.Create()
	s.SaveFile(site, "locked/a.txt", strings.NewReader("x"))
	locked := filepath.Join(site.ContentDir(), "locked")
	os.Chmod(locked, 0)
	defer os.Chmod(locked, 0o755)
	if _, _, err := s.ListDir(site, "locked"); !errors.Is(err, ErrUnreadable) {
		t.Errorf("unreadable folder: %v, want ErrUnreadable", err)
	}
}
