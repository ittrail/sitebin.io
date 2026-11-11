package store

import (
	"errors"
	"strings"
	"testing"
)

// An atomic upload (BeginMerge) adds and overwrites files like a live upload,
// but none of them is visible until all arrived and Commit put them in.

func TestMergeCommitAddsAndOverwritesKeepingTheRest(t *testing.T) {
	s := newTestStore(t)
	site, _, _ := s.Create()
	s.SaveFile(site, "index.html", strings.NewReader("home"))
	s.SaveFile(site, "stable/latest.yml", strings.NewReader("version: 0.9"))

	m, err := s.BeginMerge(site)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Abort()
	if err := m.SaveFile("stable/app-1.0.exe", strings.NewReader("MZ")); err != nil {
		t.Fatal(err)
	}
	if err := m.SaveFile("stable/latest.yml", strings.NewReader("version: 1.0")); err != nil {
		t.Fatal(err)
	}
	if got := fileNames(t, s, site); got != "index.html,stable/latest.yml" {
		t.Fatalf("the site changed before Commit: %s", got)
	}
	if b, _ := s.ReadContentFile(site, "stable/latest.yml"); string(b) != "version: 0.9" {
		t.Fatalf("the feed changed before Commit: %q", b)
	}
	if err := m.Commit(); err != nil {
		t.Fatal(err)
	}
	if got := fileNames(t, s, site); got != "index.html,stable/app-1.0.exe,stable/latest.yml" {
		t.Fatalf("after Commit: %s", got)
	}
	if b, _ := s.ReadContentFile(site, "stable/latest.yml"); string(b) != "version: 1.0" {
		t.Fatalf("the feed after Commit: %q", b)
	}
	if left := stagingDirs(t, s, site); len(left) != 0 {
		t.Fatalf("staging left behind: %v", left)
	}
}

func TestMergeCommitsInTheOrderTheFilesCame(t *testing.T) {
	s := newTestStore(t)
	site, _, _ := s.Create()
	var moved []string
	mergeMoved = func(rel string) { moved = append(moved, rel) }
	defer func() { mergeMoved = nil }()
	m, _ := s.BeginMerge(site)
	defer m.Abort()
	for _, name := range []string{"stable/z-installer.exe", "stable/a-installer.dmg", "stable/latest.yml"} {
		if err := m.SaveFile(name, strings.NewReader("x")); err != nil {
			t.Fatal(err)
		}
	}
	m.SaveFile("stable/z-installer.exe", strings.NewReader("y")) // again: keeps its place
	if err := m.Commit(); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(moved, ","); got != "stable/z-installer.exe,stable/a-installer.dmg,stable/latest.yml" {
		t.Fatalf("committed in the order %s", got)
	}
}

func TestMergeAbortAndAFailedWriteLeaveTheSiteAsItWas(t *testing.T) {
	s := newTestStore(t)
	site, _, _ := s.Create()
	s.SaveFile(site, "index.html", strings.NewReader("home"))

	m, _ := s.BeginMerge(site)
	m.SaveFile("a.exe", strings.NewReader("MZ"))
	m.Abort()
	if got := fileNames(t, s, site); got != "index.html" {
		t.Fatalf("after Abort: %s", got)
	}

	m, err := s.BeginMerge(site)
	if err != nil {
		t.Fatalf("a merge after an abort: %v", err)
	}
	defer m.Abort()
	m.SaveFile("a.exe", strings.NewReader("MZ"))
	if err := m.SaveFile("../escape", strings.NewReader("x")); err == nil {
		t.Fatal("a bad path was staged")
	}
	if err := m.Commit(); err == nil {
		t.Fatal("a merge with a failed write was committed")
	}
	if got := fileNames(t, s, site); got != "index.html" {
		t.Fatalf("after a refused Commit: %s", got)
	}
}

func TestMergeCommitChecksTheCapsAgainstTheSiteAsItIs(t *testing.T) {
	s := newTestStore(t)
	site, _, _ := s.Create()
	if err := s.Update(site, func(m *Meta) error { m.QuotaBytes = 100; m.QuotaFiles = 2; return nil }); err != nil {
		t.Fatal(err)
	}
	s.SaveFile(site, "big.bin", strings.NewReader(strings.Repeat("a", 60)))

	// 60 live + 50 new is past 100.
	m, _ := s.BeginMerge(site)
	m.SaveFile("more.bin", strings.NewReader(strings.Repeat("b", 50)))
	if err := m.Commit(); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("over the byte cap: %v", err)
	}
	// Overwriting: the 60 bytes leave, 70 arrive.
	m, _ = s.BeginMerge(site)
	m.SaveFile("big.bin", strings.NewReader(strings.Repeat("c", 70)))
	if err := m.Commit(); err != nil {
		t.Fatalf("an overwrite within the cap: %v", err)
	}
	// One file live, two new is past 2 files; one new plus an overwrite is not.
	m, _ = s.BeginMerge(site)
	m.SaveFile("x", strings.NewReader("x"))
	m.SaveFile("y", strings.NewReader("y"))
	if err := m.Commit(); !errors.Is(err, ErrTooManyFiles) {
		t.Fatalf("over the file cap: %v", err)
	}
	m, _ = s.BeginMerge(site)
	m.SaveFile("x", strings.NewReader("x"))
	m.SaveFile("big.bin", strings.NewReader("small"))
	if err := m.Commit(); err != nil {
		t.Fatalf("one new file and an overwrite: %v", err)
	}
	if got := fileNames(t, s, site); got != "big.bin,x" {
		t.Fatalf("files: %s", got)
	}
}

func TestMergeCommitRefusesAFolderWhereAFileGoesBeforeMovingAnything(t *testing.T) {
	s := newTestStore(t)
	site, _, _ := s.Create()
	s.SaveFile(site, "docs/index.html", strings.NewReader("docs"))
	m, _ := s.BeginMerge(site)
	m.SaveFile("first.txt", strings.NewReader("1"))
	m.SaveFile("docs", strings.NewReader("a file where the folder is"))
	if err := m.Commit(); !errors.Is(err, ErrBadPath) {
		t.Fatalf("a file over a folder: %v", err)
	}
	if got := fileNames(t, s, site); got != "docs/index.html" {
		t.Fatalf("a refused merge moved files: %s", got)
	}
}

func TestMergeRefusesALockedOrDeletedSite(t *testing.T) {
	s := newTestStore(t)
	site, _, _ := s.Create()
	m, _ := s.BeginMerge(site)
	m.SaveFile("a.exe", strings.NewReader("MZ"))
	if _, err := s.SetLock(site, &SiteLock{By: "operator", Reason: "test"}); err != nil {
		t.Fatal(err)
	}
	if err := m.Commit(); !errors.Is(err, ErrLocked) {
		t.Fatalf("locked: %v", err)
	}

	site2, _, _ := s.Create()
	m, _ = s.BeginMerge(site2)
	m.SaveFile("a.exe", strings.NewReader("MZ"))
	if err := s.Delete(site2); err != nil {
		t.Fatal(err)
	}
	if err := m.Commit(); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted: %v", err)
	}
}

func TestOneStagedUploadPerSiteAtATime(t *testing.T) {
	s := newTestStore(t)
	site, _, _ := s.Create()
	m, _ := s.BeginMerge(site)
	defer m.Abort()
	if _, err := s.BeginReplace(site); !errors.Is(err, ErrReplaceBusy) {
		t.Fatalf("a replace during a merge: %v", err)
	}
	if _, err := s.BeginMerge(site); !errors.Is(err, ErrReplaceBusy) {
		t.Fatalf("a merge during a merge: %v", err)
	}
}

func TestMergeIntoAViewerSiteGoesUnderRaw(t *testing.T) {
	s := newTestStore(t)
	site, _, _ := s.Create()
	if err := s.Update(site, func(m *Meta) error { m.Mode = ModeViewer; return nil }); err != nil {
		t.Fatal(err)
	}
	m, _ := s.BeginMerge(site)
	m.SaveFile("report.pdf", strings.NewReader("%PDF"))
	if err := m.Commit(); err != nil {
		t.Fatal(err)
	}
	if b, err := s.ReadContentFile(site, "report.pdf"); err != nil || string(b) != "%PDF" {
		t.Fatalf("viewer merge: %q %v", b, err)
	}
}

// A merge counts the repository with the files, at Commit, against the site
// as it is then: the live files stay beside the staged ones.
func TestMergeCountsRepository(t *testing.T) {
	s := newTestStore(t)
	site, _, _ := s.Create()
	capSite(t, s, site, 120)
	s.SaveFile(site, "old.txt", strings.NewReader(strings.Repeat("o", 15)))
	writeRepoFile(t, site, "objects/pack/pack-1.pack", 100)

	m, _ := s.BeginMerge(site)
	m.SaveFile("a.txt", strings.NewReader(strings.Repeat("x", 6))) // 15 + 100 + 6 > 120
	if err := m.Commit(); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("a merge past the cap with the repository: %v, want ErrTooLarge", err)
	}
	m, _ = s.BeginMerge(site)
	m.SaveFile("a.txt", strings.NewReader(strings.Repeat("x", 5))) // exactly 120
	if err := m.Commit(); err != nil {
		t.Fatalf("a merge that fills the cap exactly: %v", err)
	}
}

// A merge is checked against the owner's ACCOUNT cap at Commit, with the
// owner's other sites as they are then, and notes what the site holds after.
func TestMergeCommitCountsTheAccount(t *testing.T) {
	s := newTestStore(t)
	a1 := acctSite(t, s, "acct-a", 200, 0)
	a2 := acctSite(t, s, "acct-a", 200, 100)

	m, _ := s.BeginMerge(a1)
	m.SaveFile("a.bin", strings.NewReader(strings.Repeat("a", 60))) // fits the 100 left now
	// The other site grows while the upload streams: 40 left.
	if err := s.SaveFile(a2, "more.bin", strings.NewReader(strings.Repeat("m", 60))); err != nil {
		t.Fatal(err)
	}
	if err := m.Commit(); !errors.Is(err, ErrAccountFull) {
		t.Fatalf("a merge past what the account has left at Commit: %v, want ErrAccountFull", err)
	}

	m, _ = s.BeginMerge(a1)
	m.SaveFile("a.bin", strings.NewReader(strings.Repeat("a", 30)))
	if err := m.Commit(); err != nil {
		t.Fatalf("a merge within the account's room: %v", err)
	}
	if _, others := s.AccountUsage(a2); others != 30 {
		t.Fatalf("the merge was not noted: others %d, want 30", others)
	}
}
