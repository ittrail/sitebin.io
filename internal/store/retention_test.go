package store

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ittrail/sitebin.io/internal/provenance"
)

const day = 24 * time.Hour

// The retention clock is the start of the continuous lock. Re-locking the
// same site — the scanner's lock, the operator's Keep over it, a
// suspension arriving after, the operator re-locking with a new reason —
// never restarts it; otherwise retention would never end.
func TestReLockKeepsTheLockDate(t *testing.T) {
	s := newTestStore(t)
	site, _, _ := s.Create()
	first := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	if _, err := s.SetLock(site, &SiteLock{At: first, By: LockByScanner, Reason: "held for review: kit"}); err != nil {
		t.Fatal(err)
	}
	// Keep: the operator's lock replaces the scanner's.
	if changed, err := s.SetLock(site, &SiteLock{At: first.Add(40 * day), By: LockByAdmin, Reason: "phishing"}); err != nil || !changed {
		t.Fatalf("keep = %v, %v", changed, err)
	}
	// A suspension never replaces a lock.
	s.SetLock(site, &SiteLock{At: first.Add(50 * day), By: LockByAccount, Reason: "suspended"})
	// The operator re-locks with another reason.
	s.SetLock(site, &SiteLock{By: LockByAdmin, Reason: "phishing, reported to the host"})

	got, _ := s.ByViewID(site.ViewID)
	l := got.Meta.Locked
	if !l.At.Equal(first) {
		t.Fatalf("lock date moved to %v, want %v", l.At, first)
	}
	if l.By != LockByAdmin || l.Reason != "phishing, reported to the host" {
		t.Errorf("the replacement's author and reason were not taken: %+v", l)
	}

	// An unlock ends the clock; the next lock starts a new one.
	s.SetLock(site, nil)
	later := first.Add(100 * day)
	s.SetLock(site, &SiteLock{At: later, By: LockByAdmin})
	if l := site.Meta.Locked; !l.At.Equal(later) {
		t.Errorf("a lock after an unlock kept the old date: %v", l.At)
	}
}

// An unsuspension that turns into a scanner lock is the same continuous
// lock: the date stays. (A held one becomes the operator's instead, below.)
func TestAccountLockTurnedScannerKeepsTheDate(t *testing.T) {
	s := newTestStore(t)
	site, _, _ := s.Create()
	first := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	s.SetLock(site, &SiteLock{At: first, By: LockByAccount})
	s.Update(site, func(m *Meta) error {
		m.Abuse = &AbuseState{Findings: []Finding{{Rule: "telegram-bot-api", Severity: "block", Path: "index.html"}}}
		return nil
	})
	if released, err := s.ReleaseLock(site, LockByAccount); err != nil || released {
		t.Fatalf("release = %v, %v; want the lock turned into a scanner lock", released, err)
	}
	l := site.Meta.Locked
	if l == nil || l.By != LockByScanner || !l.At.Equal(first) {
		t.Fatalf("scanner lock = %+v", l)
	}
}

// An unsuspension never ends a case: a suspension's lock carrying an
// evidence hold becomes the operator's own lock instead of being lifted.
func TestReleaseLockKeepsAHeldLock(t *testing.T) {
	s := newTestStore(t)
	site, _, _ := s.Create()
	first := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	s.SetLock(site, &SiteLock{At: first, By: LockByAccount, Reason: "account suspended"})
	s.SetHold(site, &LockHold{By: "acct-admin"})
	if released, err := s.ReleaseLock(site, LockByAccount); err != nil || released {
		t.Fatalf("release of a held account lock = %v, %v", released, err)
	}
	got, _ := s.ByViewID(site.ViewID)
	l := got.Meta.Locked
	if l == nil || l.By != LockByAdmin || !l.At.Equal(first) || l.Hold == nil || l.Reason != "account suspended" {
		t.Fatalf("lock after the unsuspension = %+v", l)
	}
	// A second unsuspension finds the operator's lock and leaves it.
	if released, _ := s.ReleaseLock(site, LockByAccount); released || !site.Meta.IsLocked() {
		t.Fatal("the operator's held lock was released")
	}
}

func TestEvidenceHold(t *testing.T) {
	s := newTestStore(t)
	site, _, _ := s.Create()
	if _, err := s.SetHold(site, &LockHold{By: HoldByCLI}); !errors.Is(err, ErrNotLocked) {
		t.Fatalf("hold on an unlocked site = %v, want ErrNotLocked", err)
	}
	s.SetLock(site, &SiteLock{By: LockByAdmin})
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	if changed, err := s.SetHold(site, &LockHold{At: at, By: "acct-admin"}); err != nil || !changed {
		t.Fatalf("hold = %v, %v", changed, err)
	}
	// A second hold keeps the first.
	if changed, _ := s.SetHold(site, &LockHold{By: HoldByCLI}); changed {
		t.Error("a second hold replaced the first")
	}
	got, _ := s.ByViewID(site.ViewID)
	if h := got.Meta.Locked.Hold; h == nil || !h.At.Equal(at) || h.By != "acct-admin" {
		t.Fatalf("hold = %+v", h)
	}
	// A replaced lock keeps the hold; a caller cannot smuggle one in.
	s.SetLock(site, &SiteLock{By: LockByAdmin, Reason: "new reason"})
	if site.Meta.Locked.Hold == nil {
		t.Fatal("re-locking dropped the evidence hold")
	}
	other, _, _ := s.Create()
	s.SetLock(other, &SiteLock{By: LockByAdmin, Hold: &LockHold{By: "x"}})
	if other.Meta.Locked.Hold != nil {
		t.Error("SetLock placed a hold")
	}
	// Release.
	if changed, err := s.SetHold(site, nil); err != nil || !changed {
		t.Fatalf("release = %v, %v", changed, err)
	}
	if changed, _ := s.SetHold(site, nil); changed {
		t.Error("releasing no hold reported a change")
	}
	// An unlock ends the hold with the lock.
	s.SetHold(site, &LockHold{By: HoldByCLI})
	s.SetLock(site, nil)
	s.SetLock(site, &SiteLock{By: LockByAdmin})
	if site.Meta.Locked.Hold != nil {
		t.Error("a hold survived an unlock")
	}
}

// A lock written before holds existed reads as unheld; an unheld lock writes
// no hold key.
func TestOldLockReadsUnheld(t *testing.T) {
	s := newTestStore(t)
	site, _, _ := s.Create()
	s.SetLock(site, &SiteLock{By: LockByAdmin})
	b, _ := os.ReadFile(metaPath(site.Dir()))
	if strings.Contains(string(b), `"hold"`) {
		t.Fatalf("an unheld lock writes a hold key: %s", b)
	}
}

func TestLockPurgeAt(t *testing.T) {
	at := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	r := 180 * day
	if _, ok := LockPurgeAt(nil, r); ok {
		t.Error("an unlocked site has a purge date")
	}
	if _, ok := LockPurgeAt(&SiteLock{At: at}, 0); ok {
		t.Error("retention 0 has a purge date")
	}
	if _, ok := LockPurgeAt(&SiteLock{At: at, Hold: &LockHold{}}, r); ok {
		t.Error("a held lock has a purge date")
	}
	// A hold stops the purge; it does not move when the retention ends.
	if end, ok := LockRetentionEnds(&SiteLock{At: at, Hold: &LockHold{}}, r); !ok || !end.Equal(at.Add(r)) {
		t.Errorf("retention end of a held lock = %v, %v", end, ok)
	}
	if _, ok := LockRetentionEnds(&SiteLock{At: at}, 0); ok {
		t.Error("retention 0 has an end")
	}
	if got, ok := LockPurgeAt(&SiteLock{At: at}, r); !ok || !got.Equal(at.Add(r)) {
		t.Errorf("purge at = %v, %v", got, ok)
	}
}

// The purge deletes the site completely — files, meta, indexes and its
// provenance — only once the lock is older than the retention, and tells
// the hook what it removed.
func TestPurgeLocked(t *testing.T) {
	s := newTestStore(t)
	s.SetLockRetention(180 * day)
	var heard []LockPurge
	s.SetPurgeHook(func(p LockPurge) { heard = append(heard, p) })

	site, _, _ := s.Create()
	s.SaveFile(site, "index.html", strings.NewReader("kit"))
	if err := s.AddDomain(site, "bank-login.example.org"); err != nil {
		t.Fatal(err)
	}
	s.RecordProvenance(site, provenance.Entry{Time: time.Now(), Action: provenance.ActionCreate, IP: "203.0.113.7"})
	at := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	s.SetLock(site, &SiteLock{At: at, By: LockByScanner, Reason: "held for review"})

	// Exactly at the retention: not yet.
	if purged, err := s.PurgeLocked(site, at.Add(180*day)); err != nil || purged {
		t.Fatalf("purge at the retention = %v, %v", purged, err)
	}
	if _, err := s.ByViewID(site.ViewID); err != nil {
		t.Fatal("purged before the retention was over")
	}
	now := at.Add(180*day + time.Minute)
	if purged, err := s.PurgeLocked(site, now); err != nil || !purged {
		t.Fatalf("purge past the retention = %v, %v", purged, err)
	}
	if _, err := s.ByViewID(site.ViewID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("site survived its purge: %v", err)
	}
	if _, err := s.ByEditID(site.EditID); !errors.Is(err, ErrNotFound) {
		t.Error("the edit index survived")
	}
	if _, err := s.ByDomain("bank-login.example.org"); !errors.Is(err, ErrNotFound) {
		t.Error("the domain index survived")
	}
	if _, err := os.Stat(filepath.Join(site.Dir(), provenance.FileName)); !os.IsNotExist(err) {
		t.Error("the site's provenance survived")
	}
	if len(heard) != 1 || heard[0].ViewID != site.ViewID || heard[0].Lock.By != LockByScanner ||
		!heard[0].Lock.At.Equal(at) || heard[0].Retention != 180*day || len(heard[0].Domains) != 1 {
		t.Fatalf("hook heard %+v", heard)
	}
}

// The decision is taken again from disk under the site lock: a hold, an
// unlock or a zero retention that arrived after the sweep read the site wins.
func TestPurgeLockedRereadsTheLock(t *testing.T) {
	s := newTestStore(t)
	s.SetLockRetention(180 * day)
	at := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	now := at.Add(200 * day)

	held, _, _ := s.Create()
	s.SetLock(held, &SiteLock{At: at, By: LockByAdmin})
	stale, _ := s.ByViewID(held.ViewID)
	s.SetHold(held, &LockHold{By: HoldByCLI})
	if purged, err := s.PurgeLocked(stale, now); err != nil || purged {
		t.Fatalf("a held site was purged through a stale handle: %v, %v", purged, err)
	}

	unlocked, _, _ := s.Create()
	s.SetLock(unlocked, &SiteLock{At: at, By: LockByAdmin})
	stale, _ = s.ByViewID(unlocked.ViewID)
	s.SetLock(unlocked, nil)
	if purged, _ := s.PurgeLocked(stale, now); purged {
		t.Fatal("an unlocked site was purged")
	}

	forever, _, _ := s.Create()
	s.SetLock(forever, &SiteLock{At: at, By: LockByAdmin})
	s.SetLockRetention(0)
	if purged, _ := s.PurgeLocked(forever, now.Add(10000*day)); purged {
		t.Fatal("retention 0 purged a site")
	}
	for _, site := range []*Site{held, unlocked, forever} {
		if _, err := s.ByViewID(site.ViewID); err != nil {
			t.Errorf("site %s gone: %v", site.ViewID, err)
		}
	}
}
