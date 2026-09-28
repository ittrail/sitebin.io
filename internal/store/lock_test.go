package store

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

// A meta.json from before locks existed has no "locked" key, and reads as
// unlocked: rule 1, no migration.
func TestOldMetaReadsUnlocked(t *testing.T) {
	s := newTestStore(t)
	site, _, _ := s.Create()
	b, err := os.ReadFile(metaPath(site.Dir()))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), `"locked"`) {
		t.Fatalf("an unlocked site writes a locked key: %s", b)
	}
	got, err := s.ByViewID(site.ViewID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Meta.IsLocked() {
		t.Fatal("an old site reads as locked")
	}
}

func TestSetLockAndUnlock(t *testing.T) {
	s := newTestStore(t)
	site, _, _ := s.Create()
	at := time.Date(2026, 9, 28, 10, 0, 0, 0, time.UTC)
	changed, err := s.SetLock(site, &SiteLock{At: at, Reason: "  phishing\n(bank)  ", By: LockByAdmin})
	if err != nil || !changed {
		t.Fatalf("SetLock = %v, %v", changed, err)
	}
	got, _ := s.ByViewID(site.ViewID)
	l := got.Meta.Locked
	if l == nil || !l.At.Equal(at) || l.By != LockByAdmin || l.Reason != "phishing (bank)" {
		t.Fatalf("lock = %+v", l)
	}
	changed, err = s.SetLock(site, nil)
	if err != nil || !changed {
		t.Fatalf("unlock = %v, %v", changed, err)
	}
	got, _ = s.ByViewID(site.ViewID)
	if got.Meta.IsLocked() {
		t.Fatal("still locked after unlock")
	}
	if changed, _ := s.SetLock(site, nil); changed {
		t.Error("unlocking an unlocked site reported a change")
	}
}

func TestSetLockDefaultsTheDateAndTheAuthor(t *testing.T) {
	s := newTestStore(t)
	site, _, _ := s.Create()
	before := time.Now().Add(-time.Second)
	if _, err := s.SetLock(site, &SiteLock{}); err != nil {
		t.Fatal(err)
	}
	l := site.Meta.Locked
	if l == nil || l.By != LockByAdmin || l.At.Before(before) {
		t.Fatalf("lock = %+v", l)
	}
}

// A suspension locks every site of the account, but it must never replace a
// lock the operator placed site by site — or the unsuspension that lifts
// account locks would lift the operator's too. A repeated suspension keeps
// the first date.
func TestAccountLockNeverReplacesALock(t *testing.T) {
	s := newTestStore(t)
	site, _, _ := s.Create()
	first := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	s.SetLock(site, &SiteLock{At: first, Reason: "evidence", By: LockByAdmin})
	changed, err := s.SetLock(site, &SiteLock{Reason: "suspended", By: LockByAccount})
	if err != nil || changed {
		t.Fatalf("account lock over an admin lock = %v, %v", changed, err)
	}
	if l := site.Meta.Locked; l.By != LockByAdmin || l.Reason != "evidence" {
		t.Fatalf("admin lock replaced: %+v", l)
	}

	other, _, _ := s.Create()
	s.SetLock(other, &SiteLock{At: first, Reason: "one", By: LockByAccount})
	if changed, _ := s.SetLock(other, &SiteLock{Reason: "two", By: LockByAccount}); changed {
		t.Error("a repeated account lock changed the lock")
	}
	if l := other.Meta.Locked; !l.At.Equal(first) || l.Reason != "one" {
		t.Errorf("repeat moved the lock: %+v", l)
	}
	// The operator's lock does replace an account lock: that is "keep locked".
	if changed, _ := s.SetLock(other, &SiteLock{Reason: "keep", By: LockByAdmin}); !changed {
		t.Fatal("an admin lock did not replace an account lock")
	}
	if other.Meta.Locked.By != LockByAdmin {
		t.Errorf("lock = %+v", other.Meta.Locked)
	}
}

func TestReleaseLockLiftsOnlyItsOwnKind(t *testing.T) {
	s := newTestStore(t)
	admin, _, _ := s.Create()
	acct, _, _ := s.Create()
	s.SetLock(admin, &SiteLock{By: LockByAdmin})
	s.SetLock(acct, &SiteLock{By: LockByAccount})

	if released, err := s.ReleaseLock(admin, LockByAccount); err != nil || released {
		t.Fatalf("released an admin lock as an account lock: %v, %v", released, err)
	}
	if !admin.Meta.IsLocked() {
		t.Fatal("admin lock lifted")
	}
	if released, err := s.ReleaseLock(acct, LockByAccount); err != nil || !released {
		t.Fatalf("account lock not released: %v, %v", released, err)
	}
	if acct.Meta.IsLocked() {
		t.Fatal("account lock still there")
	}
	if released, _ := s.ReleaseLock(acct, LockByAccount); released {
		t.Error("releasing an unlocked site reported a release")
	}
}

// The runtime restarts a project only when its restart sequence moves, so an
// unlock of an enabled container project bumps it — and leaves a project its
// owner had stopped alone.
func TestUnlockRestartsAnEnabledContainerProject(t *testing.T) {
	s := newTestStore(t)
	on, _, _ := s.Create()
	off, _, _ := s.Create()
	for _, c := range []struct {
		site    *Site
		enabled bool
	}{{on, true}, {off, false}} {
		enabled := c.enabled
		s.Update(c.site, func(m *Meta) error {
			m.Mode = ModeContainer
			m.Container = &ContainerMeta{Enabled: enabled, RestartSeq: 3, Status: ContainerStopped}
			return nil
		})
		s.SetLock(c.site, &SiteLock{By: LockByAdmin})
		s.SetLock(c.site, nil)
	}
	if c := on.Meta.Container; c.RestartSeq != 4 || c.Status != ContainerStarting {
		t.Errorf("enabled project after unlock = %+v, want seq 4, starting", c)
	}
	if c := off.Meta.Container; c.RestartSeq != 3 || c.Status != ContainerStopped {
		t.Errorf("stopped project after unlock = %+v, want it untouched", c)
	}
}

func TestDeleteRefusesALockedSite(t *testing.T) {
	s := newTestStore(t)
	site, _, _ := s.Create()
	s.SaveFile(site, "index.html", strings.NewReader("evidence"))
	s.SetLock(site, &SiteLock{By: LockByAdmin})
	if err := s.Delete(site); !errors.Is(err, ErrLocked) {
		t.Fatalf("Delete of a locked site = %v, want ErrLocked", err)
	}
	if _, err := s.ByViewID(site.ViewID); err != nil {
		t.Fatalf("locked site gone after a refused delete: %v", err)
	}
	if _, err := s.ByEditID(site.EditID); err != nil {
		t.Fatalf("locked site's edit index gone: %v", err)
	}
	// The operator's takedown goes past the hold.
	if err := s.ForceDelete(site); err != nil {
		t.Fatalf("ForceDelete: %v", err)
	}
	if _, err := s.ByViewID(site.ViewID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("site survived ForceDelete: %v", err)
	}
}

// The handle a caller holds can be older than the lock; Delete reads the lock
// from disk, not from the snapshot.
func TestDeleteReadsTheLockFromDisk(t *testing.T) {
	s := newTestStore(t)
	site, _, _ := s.Create()
	stale, _ := s.ByViewID(site.ViewID)
	s.SetLock(site, &SiteLock{By: LockByAdmin})
	if err := s.Delete(stale); !errors.Is(err, ErrLocked) {
		t.Fatalf("Delete through a stale handle = %v, want ErrLocked", err)
	}
}

// A replace that began before the lock must not swap out the evidence.
func TestReplaceCommitRefusesALockedSite(t *testing.T) {
	s := newTestStore(t)
	site, _, _ := s.Create()
	s.SaveFile(site, "index.html", strings.NewReader("evidence"))
	rep, err := s.BeginReplace(site)
	if err != nil {
		t.Fatal(err)
	}
	defer rep.Abort()
	if err := rep.SaveFile("index.html", strings.NewReader("clean")); err != nil {
		t.Fatal(err)
	}
	s.SetLock(site, &SiteLock{By: LockByAdmin})
	if err := rep.Commit(); !errors.Is(err, ErrLocked) {
		t.Fatalf("Commit on a locked site = %v, want ErrLocked", err)
	}
	b, err := s.ReadContentFile(site, "index.html")
	if err != nil || string(b) != "evidence" {
		t.Fatalf("content = %q, %v; the lock's evidence was replaced", b, err)
	}
	if left := stagingDirs(t, s, site); len(left) != 0 {
		t.Errorf("staging left behind: %v", left)
	}
}

func TestCleanLockReason(t *testing.T) {
	cases := map[string]string{
		"":                       "",
		"  phishing  ":           "phishing",
		"a\tb\nc\x00d":           "a b c d",
		strings.Repeat("x", 300): strings.Repeat("x", maxLockReason),
		strings.Repeat("ä", 250): strings.Repeat("ä", maxLockReason),
	}
	for in, want := range cases {
		if got := CleanLockReason(in); got != want {
			t.Errorf("CleanLockReason(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLockedMessage(t *testing.T) {
	if got := LockedMessage(nil); got != "This site is locked by the operator" {
		t.Errorf("no lock = %q", got)
	}
	if got := LockedMessage(&SiteLock{}); got != "This site is locked by the operator" {
		t.Errorf("no reason = %q", got)
	}
	if got := LockedMessage(&SiteLock{Reason: "phishing"}); got != "This site is locked by the operator: phishing" {
		t.Errorf("reason = %q", got)
	}
}
