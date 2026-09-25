package store

import (
	"archive/zip"
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// stagingDirs lists every replace staging directory a site can have: the
// upload's own under <root>/tmp, and one a commit moved into the site folder.
func stagingDirs(t *testing.T, s *Store, site *Site) []string {
	t.Helper()
	var all []string
	for _, pattern := range []string{
		filepath.Join(s.Root(), "tmp", "replace-"+site.ViewID+"-*"),
		filepath.Join(site.Dir(), ".replace-*"),
	} {
		m, err := filepath.Glob(pattern)
		if err != nil {
			t.Fatal(err)
		}
		all = append(all, m...)
	}
	return all
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
		t.Error("the content directory was replaced; container bind mounts point into it (files/<folder>), so a commit must empty and refill it, never rename it")
	}
	if d := stagingDirs(t, s, site); len(d) != 0 {
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
	if d := stagingDirs(t, s, site); len(d) != 0 {
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
	tmp := filepath.Join(s.Root(), "tmp")
	// Uploads stage under <root>/tmp, for any site, so only stale ones go
	// there. A site's own .replace-* directories are left by a crash or a
	// failed commit, and the one-replace claim proves no commit of the site
	// is running now: they all go, whatever their age.
	staleTmp := filepath.Join(tmp, "replace-othersite-1")
	freshTmp := filepath.Join(tmp, "replace-othersite-2")
	staleCommit := filepath.Join(site.Dir(), ".replace-commit-1")
	freshCommit := filepath.Join(site.Dir(), ".replace-commit-2")
	for _, d := range []string{staleTmp, freshTmp, staleCommit, freshCommit} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-2 * time.Hour)
	for _, d := range []string{staleTmp, staleCommit} {
		if err := os.Chtimes(d, old, old); err != nil {
			t.Fatal(err)
		}
	}
	rep, err := s.BeginReplace(site)
	if err != nil {
		t.Fatal(err)
	}
	defer rep.Abort()
	for _, d := range []string{staleTmp, staleCommit, freshCommit} {
		if _, err := os.Stat(d); !os.IsNotExist(err) {
			t.Errorf("%s survived the next replace of its site", d)
		}
	}
	if _, err := os.Stat(freshTmp); err != nil {
		t.Errorf("%s, which another site's upload may still be using, was removed", freshTmp)
	}
}

func TestReplaceStagesOutsideTheSiteFolder(t *testing.T) {
	s := newTestStore(t)
	site, _, _ := s.Create()
	rep, err := s.BeginReplace(site)
	if err != nil {
		t.Fatal(err)
	}
	defer rep.Abort()
	if err := rep.SaveFile("index.html", strings.NewReader("new")); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(site.Dir())
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".replace-") {
			t.Errorf("a staging directory sits in the site folder while the upload streams: %s", e.Name())
		}
	}
	m, err := filepath.Glob(filepath.Join(s.Root(), "tmp", "replace-"+site.ViewID+"-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(m) != 1 {
		t.Fatalf("staging directories under <root>/tmp: %v", m)
	}
	if _, err := os.Stat(filepath.Join(m[0], "index.html")); err != nil {
		t.Errorf("the staged file is not in %s: %v", m[0], err)
	}
	if err := rep.Commit(); err != nil {
		t.Fatal(err)
	}
	if d := stagingDirs(t, s, site); len(d) != 0 {
		t.Errorf("staging left behind: %v", d)
	}
}

// A site deleted between the last staged write and Commit: the commit must
// neither write into what is left of it nor bring its folder back.
func TestReplaceCommitOnADeletedSiteIsNotFound(t *testing.T) {
	s := newTestStore(t)
	site, _, _ := s.Create()
	s.SaveFile(site, "old.txt", strings.NewReader("old"))
	rep, err := s.BeginReplace(site)
	if err != nil {
		t.Fatal(err)
	}
	defer rep.Abort()
	if err := rep.SaveFile("new.txt", strings.NewReader("new")); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(site); err != nil {
		t.Errorf("Delete while a replacement is staging: %v", err)
	}
	if err := rep.Commit(); !errors.Is(err, ErrNotFound) {
		t.Errorf("Commit on a deleted site = %v, want ErrNotFound", err)
	}
	if _, err := os.Stat(site.Dir()); !os.IsNotExist(err) {
		t.Errorf("the deleted site's folder exists after Commit (stat: %v)", err)
	}
	if d := stagingDirs(t, s, site); len(d) != 0 {
		t.Errorf("staging left behind: %v", d)
	}
}

func TestReplaceCommitAfterAFailedWriteIsRefused(t *testing.T) {
	s, err := New(t.TempDir(), "sitebin.example", 10, 100)
	if err != nil {
		t.Fatal(err)
	}
	site, _, _ := s.Create()
	s.SaveFile(site, "index.html", strings.NewReader("old"))
	rep, err := s.BeginReplace(site)
	if err != nil {
		t.Fatal(err)
	}
	defer rep.Abort()
	if err := rep.SaveFile("big.txt", strings.NewReader(strings.Repeat("a", 30))); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("over the cap: %v", err)
	}
	if err := rep.Commit(); err == nil {
		t.Error("a replacement with a failed write was committed")
	}
	if got := fileNames(t, s, site); got != "index.html" {
		t.Fatalf("after a refused commit: %s", got)
	}
	if b, err := s.ReadContentFile(site, "index.html"); err != nil || string(b) != "old" {
		t.Fatalf("index.html = %q, %v", b, err)
	}
	if d := stagingDirs(t, s, site); len(d) != 0 {
		t.Errorf("staging left behind: %v", d)
	}
}

func TestReplaceCommitAfterAFailedExtractIsRefused(t *testing.T) {
	s := newTestStore(t)
	site, _, _ := s.Create()
	s.SaveFile(site, "index.html", strings.NewReader("old"))
	rep, err := s.BeginReplace(site)
	if err != nil {
		t.Fatal(err)
	}
	defer rep.Abort()
	junk := []byte("this is not a zip archive")
	if err := rep.ExtractZip(bytes.NewReader(junk), int64(len(junk))); err == nil {
		t.Fatal("a corrupt zip was accepted")
	}
	if err := rep.Commit(); err == nil {
		t.Error("a replacement with a failed extract was committed")
	}
	if got := fileNames(t, s, site); got != "index.html" {
		t.Fatalf("after a refused commit: %s", got)
	}
}

// Old and new content sharing a directory: the old file in it goes, the new
// one arrives.
func TestReplaceIntoASharedDirectoryKeepsOnlyTheNewFiles(t *testing.T) {
	s := newTestStore(t)
	site, _, _ := s.Create()
	if err := s.SaveFile(site, "assets/a.js", strings.NewReader("a")); err != nil {
		t.Fatal(err)
	}
	rep, err := s.BeginReplace(site)
	if err != nil {
		t.Fatal(err)
	}
	defer rep.Abort()
	if err := rep.SaveFile("assets/b.js", strings.NewReader("b")); err != nil {
		t.Fatal(err)
	}
	if err := rep.Commit(); err != nil {
		t.Fatal(err)
	}
	if got := fileNames(t, s, site); got != "assets/b.js" {
		t.Fatalf("after Commit: %s", got)
	}
}

// The mode decides where the content lives, and it can change while an upload
// streams: the commit follows the site as it is when it lands.
func TestReplaceCommitFollowsAModeChangeDuringTheUpload(t *testing.T) {
	s := newTestStore(t)
	site, _, _ := s.Create()
	rep, err := s.BeginReplace(site)
	if err != nil {
		t.Fatal(err)
	}
	defer rep.Abort()
	if err := rep.SaveFile("doc.md", strings.NewReader("# hi")); err != nil {
		t.Fatal(err)
	}
	other, err := s.ByViewID(site.ViewID) // a second handle, as a concurrent PUT would hold
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Update(other, func(m *Meta) error { m.Mode = ModeViewer; return nil }); err != nil {
		t.Fatal(err)
	}
	if err := rep.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(site.FilesDir(), rawDirName, "doc.md")); err != nil {
		t.Fatalf("doc.md is not in the viewer's content dir: %v", err)
	}
	if site.Meta.Mode != ModeViewer {
		t.Errorf("the caller's handle still says %q", site.Meta.Mode)
	}
}

// beginBusy asserts BeginReplace refuses site because a replacement of it is
// in flight, cleaning up if it wrongly began one.
func beginBusy(t *testing.T, s *Store, site *Site, when string) {
	t.Helper()
	rep, err := s.BeginReplace(site)
	if rep != nil {
		rep.Abort()
	}
	if !errors.Is(err, ErrReplaceBusy) {
		t.Fatalf("BeginReplace %s = %v, want ErrReplaceBusy", when, err)
	}
}

func TestBeginReplaceAllowsOneReplacementPerSite(t *testing.T) {
	s := newTestStore(t)
	site, _, _ := s.Create()
	first, err := s.BeginReplace(site)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Abort()
	beginBusy(t, s, site, "while one is open")
	again, err := s.ByViewID(site.ViewID) // another request loads its own handle
	if err != nil {
		t.Fatal(err)
	}
	beginBusy(t, s, again, "through a second handle")

	other, _, _ := s.Create()
	o, err := s.BeginReplace(other)
	if err != nil {
		t.Fatalf("a replace of another site was refused: %v", err)
	}
	o.Abort()

	first.Abort()
	second, err := s.BeginReplace(site)
	if err != nil {
		t.Fatalf("BeginReplace after Abort: %v", err)
	}
	if err := second.Commit(); err != nil {
		t.Fatal(err)
	}
	second.Abort() // after Commit: must not release anything a second time
	third, err := s.BeginReplace(site)
	if err != nil {
		t.Fatalf("BeginReplace after Commit: %v", err)
	}
	beginBusy(t, s, site, "while the third is open") // the stray Abort released nothing
	third.SaveFile("../escape", strings.NewReader("x"))
	if err := third.Commit(); err == nil {
		t.Fatal("a failed replacement was committed")
	}
	fourth, err := s.BeginReplace(site)
	if err != nil {
		t.Fatalf("BeginReplace after a refused Commit: %v", err)
	}
	fourth.Abort()
}

// A commit that fails after the live content was cleared — a rename of one
// staged entry refusing — keeps the rest of the upload in the site folder
// for recovery instead of deleting it, and says where it is.
func TestReplaceCommitThatFailsMidwayKeepsTheRestOfTheUpload(t *testing.T) {
	s := newTestStore(t)
	site, _, _ := s.Create()
	s.SaveFile(site, "old.txt", strings.NewReader("old"))
	rep, err := s.BeginReplace(site)
	if err != nil {
		t.Fatal(err)
	}
	defer rep.Abort()
	rep.SaveFile("a.txt", strings.NewReader("a"))
	rep.SaveFile("b.txt", strings.NewReader("b"))

	calls := 0
	commitRename = func(from, to string) error {
		calls++
		if calls == 3 { // 1: the staging dir into the site folder, 2: first entry, 3: second entry
			return errors.New("injected rename failure")
		}
		return os.Rename(from, to)
	}
	defer func() { commitRename = os.Rename }()

	err = rep.Commit()
	if err == nil {
		t.Fatal("a commit whose rename failed reported success")
	}
	kept, _ := filepath.Glob(filepath.Join(site.Dir(), replaceCommitPrefix+"*"))
	if len(kept) != 1 {
		t.Fatalf("the rest of the upload was not kept: %v", kept)
	}
	if !strings.Contains(err.Error(), kept[0]) {
		t.Errorf("the error does not say where the rest of the upload is: %v", err)
	}
	left, _ := os.ReadDir(kept[0])
	if len(left) != 1 {
		t.Errorf("kept dir holds %d entries, want the 1 not yet moved", len(left))
	}
	commitRename = os.Rename // the recovery replace below runs without the injected failure
	// The site is not blocked: a new replacement can begin and finish.
	again, err := s.BeginReplace(site)
	if err != nil {
		t.Fatalf("BeginReplace after a failed commit: %v", err)
	}
	again.SaveFile("index.html", strings.NewReader("recovered"))
	if err := again.Commit(); err != nil {
		t.Fatal(err)
	}
	if got := fileNames(t, s, site); got != "index.html" {
		t.Errorf("after the recovery replace: %s", got)
	}
}

// A zip that is not a zip, or whose data does not match its checksum, is the
// uploader's mistake: ErrBadArchive, which the API answers with 400.
func TestCorruptZipIsErrBadArchive(t *testing.T) {
	s := newTestStore(t)
	site, _, _ := s.Create()
	junk := []byte("this is not a zip archive")
	if err := s.ExtractZip(site, bytes.NewReader(junk), int64(len(junk))); !errors.Is(err, ErrBadArchive) {
		t.Errorf("junk bytes: %v, want ErrBadArchive", err)
	}

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.CreateHeader(&zip.FileHeader{Name: "a.txt", Method: zip.Store})
	if err != nil {
		t.Fatal(err)
	}
	w.Write([]byte("hello, stored data"))
	zw.Close()
	data := buf.Bytes()
	i := bytes.Index(data, []byte("hello, stored data"))
	if i < 0 {
		t.Fatal("stored data not found in the archive")
	}
	data[i] = 'H' // the central directory still reads; the CRC no longer matches
	if err := s.ExtractZip(site, bytes.NewReader(data), int64(len(data))); !errors.Is(err, ErrBadArchive) {
		t.Errorf("checksum mismatch: %v, want ErrBadArchive", err)
	}
	rep, _ := s.BeginReplace(site)
	defer rep.Abort()
	if err := rep.ExtractZip(bytes.NewReader(data), int64(len(data))); !errors.Is(err, ErrBadArchive) {
		t.Errorf("checksum mismatch in a replace: %v, want ErrBadArchive", err)
	}
}

// A kept commit directory counts toward no quota, so failed commits must not
// pile up: the next replace of the site — which the one-replace claim proves
// is not racing a commit — removes it whatever its age. Two failures in a row
// leave at most one.
func TestFailedCommitsLeaveAtMostOneKeptDirectory(t *testing.T) {
	s := newTestStore(t)
	site, _, _ := s.Create()
	defer func() { commitRename = os.Rename }()
	for i := 0; i < 2; i++ {
		rep, err := s.BeginReplace(site)
		if err != nil {
			t.Fatal(err)
		}
		rep.SaveFile("a.txt", strings.NewReader("a"))
		rep.SaveFile("b.txt", strings.NewReader("b"))
		calls := 0
		commitRename = func(from, to string) error {
			calls++
			if calls == 3 {
				return errors.New("injected rename failure")
			}
			return os.Rename(from, to)
		}
		if err := rep.Commit(); err == nil {
			t.Fatal("the injected failure did not fail the commit")
		}
		commitRename = os.Rename
	}
	kept, _ := filepath.Glob(filepath.Join(site.Dir(), replaceCommitPrefix+"*"))
	if len(kept) != 1 {
		t.Fatalf("%d kept commit directories after two failures, want 1: %v", len(kept), kept)
	}
	rep, err := s.BeginReplace(site)
	if err != nil {
		t.Fatal(err)
	}
	defer rep.Abort()
	if kept, _ := filepath.Glob(filepath.Join(site.Dir(), replaceCommitPrefix+"*")); len(kept) != 0 {
		t.Errorf("the next replace left the kept directory: %v", kept)
	}
}
