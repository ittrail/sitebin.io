package store

import (
	"errors"
	"testing"
	"time"
)

// A check that only records what DNS said is not a change to the site. The
// workspace shows updated_at as "Last change", and the daily re-check made
// every site with a custom domain look edited within the last day: an owner
// asked who had changed a site nobody had touched for four days
// (2026-11-10). Detaching a domain IS a change to what the site serves, and
// still moves it.
func TestDomainChecksLeaveUpdatedAt(t *testing.T) {
	s, v := shareStore(t)
	a, b := ownedSite(t, s, "u1"), ownedSite(t, s, "u1")
	prove(t, s, v, a, "m.at")
	if err := s.AddDomain(b, "m.at/demos"); err != nil {
		t.Fatal(err)
	}
	if err := s.AddDomain(b, "pending.example"); !errors.Is(err, ErrDomainPending) {
		t.Fatal(err)
	}
	// Every write so far was the owner's; from here on only the sweep writes.
	stamp := func(site *Site) time.Time { t.Helper(); return reload(t, s, site).Meta.UpdatedAt }
	ua, ub := stamp(a), stamp(b)
	same := func(step string) {
		t.Helper()
		if got := stamp(a); !got.Equal(ua) {
			t.Errorf("%s moved the voucher's updated_at: %v, was %v", step, got, ua)
		}
		if got := stamp(b); !got.Equal(ub) {
			t.Errorf("%s moved the vouched site's updated_at: %v, was %v", step, got, ub)
		}
	}

	// Proof in place (stampOK), held through the host (stampViaHost), still
	// pending (stampCheck).
	t1 := time.Now().Add(reverifyEvery + time.Hour)
	reconcile(t, s, t1, a, b)
	if c := claimOn(t, reload(t, s, a), "m.at"); c.CheckedAt == nil || !c.CheckedAt.Equal(t1.UTC()) {
		t.Fatalf("the re-check was not recorded: %+v", c)
	}
	if c := claimOn(t, reload(t, s, b), "m.at/demos"); c.CheckedAt == nil || !c.CheckedAt.Equal(t1.UTC()) || !c.ViaHost {
		t.Fatalf("the re-check through the host was not recorded: %+v", c)
	}
	if c := claimOn(t, reload(t, s, b), "pending.example"); c.CheckedAt == nil || c.CheckResult == "" {
		t.Fatalf("the pending check was not recorded: %+v", c)
	}
	same("a re-check that changed nothing")

	// The proof is gone: the revocation window starts (markFailing), on both.
	revoke(v, a, "m.at")
	t2 := t1.Add(reverifyEvery + time.Hour)
	reconcile(t, s, t2, a, b)
	if c := claimOn(t, reload(t, s, a), "m.at"); c.FailingSince == nil {
		t.Fatalf("the failing proof was not recorded: %+v", c)
	}
	same("a re-check that started the revocation window")

	// Gone past the window: the domain is detached, which is a change.
	old := time.Now().Add(-time.Hour).UTC()
	m, err := readMeta(a.dir)
	if err != nil {
		t.Fatal(err)
	}
	m.UpdatedAt = old
	if err := writeMeta(a.dir, m); err != nil {
		t.Fatal(err)
	}
	reconcile(t, s, t2.Add(revokeAfter+reverifyEvery), a)
	if attached(t, s, a, "m.at") {
		t.Fatal("not detached")
	}
	if got := stamp(a); !got.After(old) {
		t.Errorf("detaching a domain left updated_at at %v", got)
	}
}
