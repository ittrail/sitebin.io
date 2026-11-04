package store

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakePush is a registered git write shaped like a real push: its cancel
// kills the "process", which needs a moment to exit and, on its way out,
// still writes into the repository (git creating objects/ab) before the
// handler calls end.
type fakePush struct {
	cancelled chan struct{}
	finished  chan struct{}
	ended     atomic.Bool
}

func startFakePush(t *testing.T, s *Store, site *Site) *fakePush {
	t.Helper()
	p := &fakePush{cancelled: make(chan struct{}), finished: make(chan struct{})}
	var once sync.Once
	end, err := s.BeginGitWrite(site, func() { once.Do(func() { close(p.cancelled) }) })
	if err != nil {
		t.Fatalf("BeginGitWrite: %v", err)
	}
	go func() {
		defer close(p.finished)
		<-p.cancelled
		time.Sleep(50 * time.Millisecond)
		os.MkdirAll(filepath.Join(site.GitDir(), "objects", "ab"), 0o755)
		p.ended.Store(true)
		end()
	}()
	return p
}

// wait waits for the fake push's goroutine to be completely over.
func (p *fakePush) wait(t *testing.T) {
	t.Helper()
	select {
	case <-p.finished:
	case <-time.After(5 * time.Second):
		t.Fatal("the push was never cancelled")
	}
}

func (p *fakePush) wasCancelled() bool {
	select {
	case <-p.cancelled:
		return true
	default:
		return false
	}
}

func TestBeginGitWriteOnePerSite(t *testing.T) {
	s := newTestStore(t)
	site, _, _ := s.Create()
	other, _, _ := s.Create()
	if s.GitWriteRunning(site) {
		t.Fatal("running before any registration")
	}
	end, err := s.BeginGitWrite(site, func() {})
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	if !s.GitWriteRunning(site) || s.GitWriteRunning(other) {
		t.Fatalf("GitWriteRunning = %v (site), %v (other), want true, false", s.GitWriteRunning(site), s.GitWriteRunning(other))
	}
	if _, err := s.BeginGitWrite(site, func() {}); !errors.Is(err, ErrGitBusy) {
		t.Fatalf("second while the first runs: %v, want ErrGitBusy", err)
	}
	// Asking registers nothing: the running push is still the one.
	if !s.GitWriteRunning(site) {
		t.Fatal("asking twice changed the answer")
	}
	endOther, err := s.BeginGitWrite(other, func() {})
	if err != nil {
		t.Fatalf("another site is not busy: %v", err)
	}
	endOther()
	end()
	end() // harmless twice
	if s.GitWriteRunning(site) {
		t.Fatal("running after end")
	}
	end2, err := s.BeginGitWrite(site, func() {})
	if err != nil {
		t.Fatalf("after end: %v", err)
	}
	// The first registration's end, called late, must not unregister the
	// second.
	end()
	if _, err := s.BeginGitWrite(site, func() {}); !errors.Is(err, ErrGitBusy) {
		t.Fatalf("a stale end freed the site: %v", err)
	}
	end2()
}

func TestBeginGitWriteRefusesLockedAndDeletedSites(t *testing.T) {
	s := newTestStore(t)
	site, _, _ := s.Create()
	if _, err := s.SetLock(site, &SiteLock{Reason: "evidence"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.BeginGitWrite(site, func() {}); !errors.Is(err, ErrLocked) {
		t.Fatalf("locked: %v, want ErrLocked", err)
	}
	gone, _, _ := s.Create()
	if err := s.Delete(gone); err != nil {
		t.Fatal(err)
	}
	if _, err := s.BeginGitWrite(gone, func() {}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted: %v, want ErrNotFound", err)
	}
	// A refusal registers nothing.
	if _, err := s.SetLock(site, nil); err != nil {
		t.Fatal(err)
	}
	end, err := s.BeginGitWrite(site, func() {})
	if err != nil {
		t.Fatalf("after the unlock: %v", err)
	}
	end()
}

// Review Focus 4: a delete during a push must not leave git recreating the
// site folder after it went.
func TestDeleteCancelsRunningPush(t *testing.T) {
	s := newTestStore(t)
	site, _, _ := s.Create()
	writeRepoFile(t, site, "HEAD", 21)
	p := startFakePush(t, s, site)
	if err := s.Delete(site); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if !p.ended.Load() {
		t.Error("Delete returned before the push had ended")
	}
	p.wait(t)
	if _, err := os.Stat(site.Dir()); !os.IsNotExist(err) {
		t.Fatalf("the site folder remains after the delete: %v", err)
	}
}

func TestForceDeleteCancelsRunningPush(t *testing.T) {
	s := newTestStore(t)
	site, _, _ := s.Create()
	p := startFakePush(t, s, site)
	if err := s.ForceDelete(site); err != nil {
		t.Fatalf("ForceDelete: %v", err)
	}
	p.wait(t)
	if _, err := os.Stat(site.Dir()); !os.IsNotExist(err) {
		t.Fatalf("the site folder remains after the delete: %v", err)
	}
}

// A refused delete (a locked site) leaves a push alone — there is none to
// leave, since the lock stopped it, but the refusal must not act either.
func TestRefusedDeleteKeepsTheRepository(t *testing.T) {
	s := newTestStore(t)
	site, _, _ := s.Create()
	writeRepoFile(t, site, "HEAD", 21)
	if _, err := s.SetLock(site, &SiteLock{Reason: "evidence"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(site); !errors.Is(err, ErrLocked) {
		t.Fatalf("Delete of a locked site: %v", err)
	}
	if _, err := os.Stat(filepath.Join(site.GitDir(), "HEAD")); err != nil {
		t.Fatalf("the repository of a locked site went: %v", err)
	}
}

// No push may begin while a delete is removing the site: one registered
// after the delete stopped the running push would write into the folder as
// it goes.
func TestNoPushBeginsWhileTheSiteIsDeleted(t *testing.T) {
	s := newTestStore(t)
	site, _, _ := s.Create()
	var once sync.Once
	cancelled := make(chan struct{})
	end, err := s.BeginGitWrite(site, func() { once.Do(func() { close(cancelled) }) })
	if err != nil {
		t.Fatal(err)
	}
	second := make(chan error, 1)
	go func() {
		<-cancelled
		end()
		// The delete is now free to remove the folder; a push arriving
		// in that moment must be refused, not registered.
		e, err := s.BeginGitWrite(site, func() {})
		if err == nil {
			e()
		}
		second <- err
	}()
	if err := s.Delete(site); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-second:
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("a push beginning during the delete: %v, want ErrNotFound", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the delete never cancelled the running push")
	}
}

func TestLockCancelsRunningPush(t *testing.T) {
	s := newTestStore(t)
	site, _, _ := s.Create()
	p := startFakePush(t, s, site)
	if _, err := s.SetLock(site, &SiteLock{Reason: "evidence"}); err != nil {
		t.Fatal(err)
	}
	if !p.wasCancelled() {
		t.Fatal("SetLock returned and the push was not cancelled")
	}
	if !p.ended.Load() {
		t.Error("SetLock returned before the push had ended")
	}
	p.wait(t)
	// The repository is the lock's evidence: it stays.
	if _, err := os.Stat(site.GitDir()); err != nil {
		t.Fatalf("the repository of a locked site: %v", err)
	}
}

// An account lock (a suspension) stops a push like the operator's.
func TestAccountLockCancelsRunningPush(t *testing.T) {
	s := newTestStore(t)
	site, _, _ := s.Create()
	p := startFakePush(t, s, site)
	if _, err := s.SetLock(site, &SiteLock{By: LockByAccount}); err != nil {
		t.Fatal(err)
	}
	if !p.wasCancelled() {
		t.Fatal("an account lock did not cancel the push")
	}
	p.wait(t)
}

// An unlock stops nothing.
func TestUnlockLeavesAPushAlone(t *testing.T) {
	s := newTestStore(t)
	site, _, _ := s.Create()
	end, err := s.BeginGitWrite(site, func() { t.Error("an unlock cancelled the push") })
	if err != nil {
		t.Fatal(err)
	}
	defer end()
	if _, err := s.SetLock(site, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReleaseLock(site, LockByAccount); err != nil {
		t.Fatal(err)
	}
}

// The abuse guard's hold is a lock like any other: a push running beside the
// upload that was held is stopped.
func TestScannerLockCancelsRunningPush(t *testing.T) {
	s := newTestStore(t)
	site, _, _ := s.Create()
	p := startFakePush(t, s, site)
	err := s.SaveFile(site, "index.html", strings.NewReader(kit))
	if !errors.Is(err, ErrHeld) {
		t.Fatalf("SaveFile = %v, want a hold", err)
	}
	if !p.wasCancelled() {
		t.Fatal("the scanner's hold did not cancel the push")
	}
	p.wait(t)
}

// A push that never ends does not hold a delete forever.
func TestStopGitWriteWaitIsBounded(t *testing.T) {
	old := gitStopWait
	gitStopWait = 50 * time.Millisecond
	defer func() { gitStopWait = old }()
	s := newTestStore(t)
	site, _, _ := s.Create()
	end, err := s.BeginGitWrite(site, func() {})
	if err != nil {
		t.Fatal(err)
	}
	defer end()
	done := make(chan error, 1)
	go func() { done <- s.Delete(site) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Delete waited past the bound for a push that never ended")
	}
}

func TestDeleteGitRepoRefusesLocked(t *testing.T) {
	s := newTestStore(t)
	site, _, _ := s.Create()
	writeRepoFile(t, site, "objects/pack/pack-1.pack", 50)
	if _, err := s.SetLock(site, &SiteLock{Reason: "evidence"}); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteGitRepo(site); !errors.Is(err, ErrLocked) {
		t.Fatalf("DeleteGitRepo on a locked site: %v, want ErrLocked", err)
	}
	if u, _ := s.SiteUsage(site); u.GitBytes != 50 {
		t.Fatalf("the evidence went: %+v", u)
	}
}

func TestDeleteGitRepoFreesBytes(t *testing.T) {
	s := newTestStore(t)
	site, _, _ := s.Create()
	if err := s.SaveFile(site, "index.html", strings.NewReader("hello")); err != nil {
		t.Fatal(err)
	}
	writeRepoFile(t, site, "objects/pack/pack-1.pack", 50)
	if u, _ := s.SiteUsage(site); u.Bytes() != 55 {
		t.Fatalf("before: %+v", u)
	}
	if err := s.DeleteGitRepo(site); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(site.GitDir()); !os.IsNotExist(err) {
		t.Fatalf("git/ is still there: %v", err)
	}
	u, err := s.SiteUsage(site)
	if err != nil || u != (Usage{FileBytes: 5, Files: 1}) {
		t.Fatalf("after: %+v %v", u, err)
	}
	// Twice, or on a site that never had one, is not an error.
	if err := s.DeleteGitRepo(site); err != nil {
		t.Fatal(err)
	}
	gone, _, _ := s.Create()
	s.Delete(gone)
	if err := s.DeleteGitRepo(gone); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a deleted site: %v, want ErrNotFound", err)
	}
}

func TestDeleteGitRepoStopsARunningPush(t *testing.T) {
	s := newTestStore(t)
	site, _, _ := s.Create()
	writeRepoFile(t, site, "HEAD", 21)
	p := startFakePush(t, s, site)
	if err := s.DeleteGitRepo(site); err != nil {
		t.Fatal(err)
	}
	if !p.ended.Load() {
		t.Error("DeleteGitRepo returned before the push had ended")
	}
	p.wait(t)
	if _, err := os.Stat(site.GitDir()); !os.IsNotExist(err) {
		t.Fatalf("git/ is still there: %v", err)
	}
	if _, err := os.Stat(metaPath(site.Dir())); err != nil {
		t.Fatalf("the site went with its repository: %v", err)
	}
}
