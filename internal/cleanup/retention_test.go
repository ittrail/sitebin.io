package cleanup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ittrail/sitebin.io/internal/ext"
	"github.com/ittrail/sitebin.io/internal/provenance"
	"github.com/ittrail/sitebin.io/internal/store"
)

// The lock retention (SITEBIN_LOCK_RETENTION_DAYS): the sweep purges a
// locked site once its lock is older than the retention, unless it carries
// an evidence hold. See the lock-retention addendum of
// docs/superpowers/specs/2026-09-28-site-lock-and-account-suspension.md.

const day = 24 * time.Hour

func retentionStore(t *testing.T, retention time.Duration) *store.Store {
	t.Helper()
	st, err := store.New(t.TempDir(), "sitebin.example", 1<<20, 100)
	if err != nil {
		t.Fatal(err)
	}
	st.SetDomainVerifier(store.TrustingVerifier{}, "sitebin.example")
	st.SetLockRetention(retention)
	return st
}

func lockedAt(t *testing.T, st *store.Store, at time.Time, by string) *store.Site {
	t.Helper()
	site, _, err := st.Create()
	if err != nil {
		t.Fatal(err)
	}
	st.SaveFile(site, "index.html", strings.NewReader("kit"))
	st.RecordProvenance(site, provenance.Entry{Time: at.Add(-30 * day), Action: provenance.ActionCreate, IP: "203.0.113.7"})
	if _, err := st.SetLock(site, &store.SiteLock{At: at, By: by, Reason: "phishing"}); err != nil {
		t.Fatal(err)
	}
	return site
}

func gone(st *store.Store, site *store.Site) bool {
	_, err := st.ByViewID(site.ViewID)
	return errors.Is(err, store.ErrNotFound)
}

// Past the retention the site goes whole — files, meta, indexes, its
// provenance — and the purge is reported; before it, nothing happens.
func TestSweepPurgesALockPastTheRetention(t *testing.T) {
	ext.Reset()
	st := retentionStore(t, 180*day)
	var heard []store.LockPurge
	st.SetPurgeHook(func(p store.LockPurge) { heard = append(heard, p) })
	now := time.Now().UTC()

	young := lockedAt(t, st, now.Add(-180*day+time.Hour), store.LockByAdmin)
	old := lockedAt(t, st, now.Add(-180*day-time.Hour), store.LockByScanner)
	if err := st.AddDomain(old, "bank-login.example.org"); err != nil {
		t.Fatal(err)
	}

	removed, err := Sweep(st, now)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 {
		t.Fatalf("removed = %d, want 1", removed)
	}
	if gone(st, young) {
		t.Fatal("a lock younger than the retention was purged")
	}
	if !gone(st, old) {
		t.Fatal("a lock older than the retention survived")
	}
	if _, err := os.Stat(old.Dir()); !os.IsNotExist(err) {
		t.Error("the purged site's folder (files, provenance) survived")
	}
	if _, err := st.ByEditID(old.EditID); err == nil {
		t.Error("the purged site's edit index survived")
	}
	if _, err := st.ByDomain("bank-login.example.org"); err == nil {
		t.Error("the purged site's domain index survived")
	}
	if len(heard) != 1 || heard[0].ViewID != old.ViewID || heard[0].Lock.By != store.LockByScanner {
		t.Fatalf("purge hook heard %+v", heard)
	}
	// The young lock keeps its whole trail meanwhile: a locked site's log
	// is not purged at 90 days.
	if es, _ := st.Provenance(young); len(es) != 1 {
		t.Errorf("the locked site's log was purged: %+v", es)
	}
}

// An evidence hold keeps the site, however old its lock.
func TestSweepKeepsAHeldLock(t *testing.T) {
	ext.Reset()
	st := retentionStore(t, 180*day)
	now := time.Now().UTC()
	held := lockedAt(t, st, now.Add(-400*day), store.LockByAdmin)
	if _, err := st.SetHold(held, &store.LockHold{By: "acct-admin"}); err != nil {
		t.Fatal(err)
	}
	if removed, _ := Sweep(st, now); removed != 0 || gone(st, held) {
		t.Fatalf("a held site was purged (removed %d)", removed)
	}
	// Released, the next sweep purges it.
	if _, err := st.SetHold(held, nil); err != nil {
		t.Fatal(err)
	}
	if removed, _ := Sweep(st, now); removed != 1 || !gone(st, held) {
		t.Fatalf("a released site past its retention was kept (removed %d)", removed)
	}
}

// Re-locking does not reset the clock: the scanner's lock, the operator's
// Keep over it and a suspension arriving later are one continuous lock,
// whose retention runs from the scanner's date.
func TestSweepReLockDoesNotResetTheClock(t *testing.T) {
	ext.Reset()
	st := retentionStore(t, 180*day)
	now := time.Now().UTC()
	site := lockedAt(t, st, now.Add(-181*day), store.LockByScanner)
	st.SetLock(site, &store.SiteLock{At: now.Add(-10 * day), By: store.LockByAdmin, Reason: "kept"})
	st.SetLock(site, &store.SiteLock{At: now.Add(-time.Hour), By: store.LockByAccount, Reason: "suspended"})
	if removed, _ := Sweep(st, now); removed != 1 || !gone(st, site) {
		t.Fatalf("re-locking restarted the retention (removed %d)", removed)
	}
}

// Retention 0 keeps locked sites forever, as before the retention existed.
func TestSweepRetentionZeroNeverPurges(t *testing.T) {
	ext.Reset()
	st := retentionStore(t, 0)
	now := time.Now().UTC()
	site := lockedAt(t, st, now.Add(-3650*day), store.LockByAdmin)
	if removed, _ := Sweep(st, now); removed != 0 || gone(st, site) {
		t.Fatalf("retention 0 purged a site (removed %d)", removed)
	}
}

// containerProvider is stubProvider with a container runtime.
type containerProvider struct {
	*stubProvider
	rt *stopRuntime
}

func (p *containerProvider) Containers() ext.ContainerRuntime { return p.rt }

type stopRuntime struct {
	stopped []string
	err     error
}

func (r *stopRuntime) Allowed(string) error { return nil }
func (r *stopRuntime) Kick(string)          {}
func (r *stopRuntime) Stop(id string) error {
	r.stopped = append(r.stopped, id)
	return r.err
}
func (r *stopRuntime) Logs(context.Context, string, string, int) (string, error) { return "", nil }

// A container site's project is removed before its files, as for an expired
// site; a runtime that cannot stop it keeps the site for the next sweep.
func TestSweepStopsALockedContainerSiteBeforeThePurge(t *testing.T) {
	rt := &stopRuntime{err: errors.New("docker is down")}
	ext.Register(&containerProvider{stubProvider: &stubProvider{}, rt: rt})
	defer ext.Reset()
	st := retentionStore(t, 180*day)
	now := time.Now().UTC()
	site := lockedAt(t, st, now.Add(-200*day), store.LockByAdmin)
	st.Update(site, func(m *store.Meta) error { m.Mode = store.ModeContainer; return nil })

	if removed, _ := Sweep(st, now); removed != 0 || gone(st, site) {
		t.Fatal("the site was purged although its containers could not be stopped")
	}
	rt.err = nil
	if removed, _ := Sweep(st, now); removed != 1 || !gone(st, site) {
		t.Fatal("the site was not purged once its containers stopped")
	}
	if len(rt.stopped) != 2 || rt.stopped[1] != site.ViewID {
		t.Errorf("stopped = %v", rt.stopped)
	}
}

// The account logs' cutoffs: 90 days for all, and for an account held as
// evidence the lock retention — never less than 90 days, and none at all
// (kept whole) when locked sites are kept forever. The account purge runs
// after the sites', so an account whose last locked site was just purged
// is judged without it.
func TestSweepHandsTheAccountPurgeTheLockRetention(t *testing.T) {
	now := time.Now().UTC()
	for _, c := range []struct {
		retention time.Duration
		held      time.Time
	}{
		{180 * day, now.Add(-180 * day)},
		{30 * day, now.Add(-provenance.Retention)},
		{0, time.Time{}},
	} {
		p := &purgingProvider{stubProvider: &stubProvider{}}
		ext.Reset()
		ext.Register(p)
		st := retentionStore(t, c.retention)
		var site *store.Site
		if c.retention > 0 {
			site = lockedAt(t, st, now.Add(-c.retention-day), store.LockByAccount)
			p.onPurge = func() {
				if !gone(st, site) {
					t.Errorf("retention %v: the account purge ran before the locked site was purged", c.retention)
				}
			}
		}
		if _, err := Sweep(st, now); err != nil {
			t.Fatal(err)
		}
		if len(p.cutoffs) != 1 || !p.cutoffs[0].Equal(now.Add(-provenance.Retention)) || !p.held[0].Equal(c.held) {
			t.Errorf("retention %v: cutoffs %v held %v, want held %v", c.retention, p.cutoffs, p.held, c.held)
		}
	}
	ext.Reset()
}

// A purged site leaves no dangling index behind for the link pruning.
func TestSweepPurgeLeavesNoDanglingLinks(t *testing.T) {
	ext.Reset()
	dir := t.TempDir()
	st, _ := store.New(dir, "sitebin.example", 1<<20, 100)
	st.SetLockRetention(day)
	site := lockedAt(t, st, time.Now().Add(-2*day), store.LockByAdmin)
	if removed, _ := Sweep(st, time.Now()); removed != 1 {
		t.Fatal("not purged")
	}
	if _, err := os.Lstat(filepath.Join(dir, "edit-index", site.EditID)); !os.IsNotExist(err) {
		t.Error("edit link left behind")
	}
}

// The sweep's snapshot can be minutes old when a site's turn comes: a
// container site unlocked meanwhile — already restarted by the runtime —
// must not be stopped on the strength of it.
func TestSweepPurgeReadsTheSiteAgainBeforeStoppingIt(t *testing.T) {
	rt := &stopRuntime{}
	ext.Register(&containerProvider{stubProvider: &stubProvider{}, rt: rt})
	defer ext.Reset()
	st := retentionStore(t, 180*day)
	now := time.Now().UTC()
	site := lockedAt(t, st, now.Add(-200*day), store.LockByAdmin)
	st.Update(site, func(m *store.Meta) error { m.Mode = store.ModeContainer; return nil })
	stale, _ := st.ByViewID(site.ViewID)
	st.SetLock(site, nil) // the operator unlocks it while the sweep works

	if purgeLocked(st, stale, now) {
		t.Fatal("an unlocked site was purged")
	}
	if len(rt.stopped) != 0 {
		t.Fatalf("the containers of a site unlocked mid-sweep were stopped: %v", rt.stopped)
	}
	if gone(st, site) {
		t.Fatal("the site is gone")
	}
}
