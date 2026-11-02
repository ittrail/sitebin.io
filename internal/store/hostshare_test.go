package store

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"
)

// Sharing a host between projects of one account: an address attaches through
// the host while another address of the same account holds it by its own
// proof, never through one that is itself held through the host, and every
// address held through a host that lost its last proof shares one clock.

// tokenVerifier proves a host's TXT per token, so two sites' claims on one
// host are told apart, and counts its lookups.
type tokenVerifier struct {
	ok    map[string]bool // host|token
	errs  map[string]error
	calls int
}

func (v *tokenVerifier) Verify(_ context.Context, domain, token, _ string) (bool, error) {
	v.calls++
	if err := v.errs[domain]; err != nil {
		return false, err
	}
	return v.ok[domain+"|"+token], nil
}

func shareStore(t *testing.T) (*Store, *tokenVerifier) {
	t.Helper()
	s := newTestStore(t)
	v := &tokenVerifier{ok: map[string]bool{}, errs: map[string]error{}}
	s.SetDomainVerifier(v, "sitebin.example")
	return s, v
}

func claimOn(t *testing.T, site *Site, addr string) DomainClaim {
	t.Helper()
	if i := claimIndex(&site.Meta, addr); i >= 0 {
		return site.Meta.DomainClaims[i]
	}
	t.Fatalf("no claim on %s in %+v", addr, site.Meta.DomainClaims)
	return DomainClaim{}
}

// prove attaches addr to site by its own TXT record — also where the host
// would have held it already: asking again with the record in place makes it
// independent.
func prove(t *testing.T, s *Store, v *tokenVerifier, site *Site, addr string) {
	t.Helper()
	if err := s.AddDomain(site, addr); err != nil && !errors.Is(err, ErrDomainPending) {
		t.Fatalf("AddDomain(%s) = %v", addr, err)
	}
	v.ok[hostOf(addr)+"|"+claimOn(t, site, addr).Token] = true
	if err := s.AddDomain(site, addr); err != nil {
		t.Fatalf("AddDomain(%s) with its TXT = %v", addr, err)
	}
	if c := claimOn(t, site, addr); !c.Verified() || c.ViaHost {
		t.Fatalf("prove(%s) left %+v, want held by its own proof", addr, c)
	}
}

func revoke(v *tokenVerifier, site *Site, addr string) {
	delete(v.ok, hostOf(addr)+"|"+claimFor(site, addr).Token)
}

func claimFor(site *Site, addr string) DomainClaim {
	if i := claimIndex(&site.Meta, addr); i >= 0 {
		return site.Meta.DomainClaims[i]
	}
	return DomainClaim{}
}

func reconcile(t *testing.T, s *Store, now time.Time, sites ...*Site) {
	t.Helper()
	for _, site := range sites {
		if err := s.ReconcileDomains(context.Background(), reload(t, s, site), now); err != nil {
			t.Fatal(err)
		}
	}
}

func attached(t *testing.T, s *Store, site *Site, addr string) bool {
	t.Helper()
	return slices.Contains(reload(t, s, site).Meta.CustomDomains, addr)
}

func TestSameOwnerAttachesViaHost(t *testing.T) {
	s, v := shareStore(t)
	a, b := ownedSite(t, s, "u1"), ownedSite(t, s, "u1")
	prove(t, s, v, a, "m.at")
	calls := v.calls
	if err := s.AddDomain(b, "m.at/demos"); err != nil {
		t.Fatalf("AddDomain via the host = %v, want attached at once", err)
	}
	if v.calls != calls {
		t.Errorf("attaching through the host asked DNS %d times", v.calls-calls)
	}
	if c := claimOn(t, b, "m.at/demos"); !c.Verified() || !c.ViaHost {
		t.Errorf("claim = %+v, want verified and ViaHost", c)
	}
	if c := claimOn(t, reload(t, s, a), "m.at"); c.ViaHost {
		t.Error("the voucher itself must stay held by its own proof")
	}
}

func TestPlainHostViaPath(t *testing.T) {
	s, v := shareStore(t)
	a, b := ownedSite(t, s, "u1"), ownedSite(t, s, "u1")
	prove(t, s, v, a, "m.at/x")
	if err := s.AddDomain(b, "m.at"); err != nil {
		t.Fatalf("the plain host via a path address = %v", err)
	}
	if !claimOn(t, b, "m.at").ViaHost {
		t.Error("want ViaHost")
	}
}

func TestForeignOwnerRefusedAndPendingDropped(t *testing.T) {
	s, v := shareStore(t)
	a, c := ownedSite(t, s, "u1"), ownedSite(t, s, "u2")
	prove(t, s, v, a, "m.at")
	v.ok["m.at|"] = true // whatever DNS says
	if err := s.AddDomain(c, "m.at/x"); !errors.Is(err, ErrDomainTaken) {
		t.Fatalf("another account's path on a held host = %v, want ErrDomainTaken", err)
	}
	if len(c.Meta.DomainClaims) != 0 {
		t.Errorf("a refused claim was recorded: %+v", c.Meta.DomainClaims)
	}

	s2, v2 := shareStore(t)
	a2, c2 := ownedSite(t, s2, "u1"), ownedSite(t, s2, "u2")
	if err := s2.AddDomain(c2, "m.at/y"); !errors.Is(err, ErrDomainPending) {
		t.Fatalf("a claim on a free host = %v, want pending", err)
	}
	prove(t, s2, v2, a2, "m.at")
	reconcile(t, s2, time.Now(), c2)
	if got := reload(t, s2, c2).Meta.DomainClaims; len(got) != 0 {
		t.Fatalf("the other account's pending claim survived the sweep: %+v", got)
	}
}

func TestExactAddressTaken(t *testing.T) {
	s, v := shareStore(t)
	a, b := ownedSite(t, s, "u1"), ownedSite(t, s, "u1")
	prove(t, s, v, a, "m.at/x")
	if err := s.AddDomain(b, "m.at/x"); !errors.Is(err, ErrDomainTaken) {
		t.Fatalf("the same address twice = %v, want ErrDomainTaken", err)
	}
}

func TestViaHostNeverVouches(t *testing.T) {
	s, v := shareStore(t)
	a, b, c := ownedSite(t, s, "u1"), ownedSite(t, s, "u1"), ownedSite(t, s, "u1")
	prove(t, s, v, a, "m.at/a")
	mustAdd(t, s, b, "m.at/b")
	mustAdd(t, s, c, "m.at/c")
	revoke(v, a, "m.at/a")

	t0 := time.Now().Add(25 * time.Hour)
	for _, at := range []time.Time{t0, t0.Add(25 * time.Hour), t0.Add(49 * time.Hour)} {
		reconcile(t, s, at, a, b, c)
		for _, x := range []struct {
			site *Site
			addr string
		}{{a, "m.at/a"}, {b, "m.at/b"}, {c, "m.at/c"}} {
			if !attached(t, s, x.site, x.addr) {
				t.Fatalf("%s detached at %v, before the window ran out", x.addr, at.Sub(t0))
			}
		}
	}
	for _, x := range []*Site{b, c} {
		if fs := claimFor(reload(t, s, x), x.Meta.DomainClaims[0].Domain).FailingSince; fs == nil || !fs.Equal(t0.UTC()) {
			t.Fatalf("FailingSince = %v, want the voucher's start %v (one shared clock)", fs, t0.UTC())
		}
	}
	reconcile(t, s, t0.Add(73*time.Hour), a, b, c)
	if attached(t, s, a, "m.at/a") || attached(t, s, b, "m.at/b") || attached(t, s, c, "m.at/c") {
		t.Fatal("addresses held through the host kept each other alive after the proof was gone")
	}
}

func TestReconcileViaHostSharedClock(t *testing.T) {
	for _, rescue := range []bool{false, true} {
		s, v := shareStore(t)
		a, b, c := ownedSite(t, s, "u1"), ownedSite(t, s, "u1"), ownedSite(t, s, "u1")
		prove(t, s, v, a, "m.at")
		mustAdd(t, s, b, "m.at/x")
		mustAdd(t, s, c, "m.at/y")
		if err := s.Delete(reload(t, s, a)); err != nil {
			t.Fatal(err)
		}
		t1 := time.Now().Add(25 * time.Hour)
		reconcile(t, s, t1, b, c)
		for _, x := range []*Site{b, c} {
			got := reload(t, s, x).Meta.DomainClaims[0]
			if got.FailingSince == nil || !got.FailingSince.Equal(t1.UTC()) || !got.Verified() {
				t.Fatalf("claim after the voucher went = %+v, want still attached, clock at %v", got, t1)
			}
		}
		if rescue {
			v.ok["m.at|"+claimFor(reload(t, s, b), "m.at/x").Token] = true
			reconcile(t, s, t1.Add(30*time.Hour), b, c)
			if got := claimFor(reload(t, s, b), "m.at/x"); got.ViaHost || got.FailingSince != nil {
				t.Fatalf("b proved itself: %+v, want own proof and no clock", got)
			}
			if got := claimFor(reload(t, s, c), "m.at/y"); !got.ViaHost || got.FailingSince != nil {
				t.Fatalf("c is vouched for by b again: %+v, want ViaHost and no clock", got)
			}
		}
		reconcile(t, s, t1.Add(73*time.Hour), b, c)
		if got := attached(t, s, b, "m.at/x") && attached(t, s, c, "m.at/y"); got != rescue {
			t.Fatalf("rescue=%v: still attached = %v", rescue, got)
		}
	}
}

func TestOwnProofPromotesOnRecheck(t *testing.T) {
	s, v := shareStore(t)
	a, b := ownedSite(t, s, "u1"), ownedSite(t, s, "u1")
	prove(t, s, v, a, "m.at")
	mustAdd(t, s, b, "m.at/x")
	v.ok["m.at|"+claimOn(t, b, "m.at/x").Token] = true
	if err := s.AddDomain(b, "m.at/x"); err != nil {
		t.Fatal(err)
	}
	if claimOn(t, b, "m.at/x").ViaHost {
		t.Error("checking again with its own TXT in place must make it independent")
	}
}

func TestOwnProofFailsButVouched(t *testing.T) {
	s, v := shareStore(t)
	a, b := ownedSite(t, s, "u1"), ownedSite(t, s, "u1")
	prove(t, s, v, a, "m.at")
	prove(t, s, v, b, "m.at/x")
	revoke(v, b, "m.at/x")
	reconcile(t, s, time.Now().Add(25*time.Hour), b)
	got := claimFor(reload(t, s, b), "m.at/x")
	if !got.ViaHost || got.FailingSince != nil || got.CheckResult == "" {
		t.Fatalf("claim = %+v, want held through the host, no clock, and what is missing", got)
	}
}

func TestLookupErrorChangesNothing(t *testing.T) {
	s, v := shareStore(t)
	a, b := ownedSite(t, s, "u1"), ownedSite(t, s, "u1")
	prove(t, s, v, a, "m.at")
	mustAdd(t, s, b, "m.at/x")
	before := claimFor(reload(t, s, b), "m.at/x")
	v.errs["m.at"] = errors.New("SERVFAIL")
	if err := s.Delete(reload(t, s, a)); err != nil {
		t.Fatal(err)
	}
	reconcile(t, s, time.Now().Add(25*time.Hour), b)
	after := claimFor(reload(t, s, b), "m.at/x")
	if after.FailingSince != nil || !after.ViaHost || !after.CheckedAt.Equal(*before.CheckedAt) {
		t.Fatalf("a failed lookup changed the claim: %+v -> %+v", before, after)
	}
}

func TestOpenModeNoVouching(t *testing.T) {
	s, v := shareStore(t)
	a, b := ownedSite(t, s, ""), ownedSite(t, s, "")
	prove(t, s, v, a, "m.at")
	calls := v.calls
	if err := s.AddDomain(b, "m.at/x"); !errors.Is(err, ErrDomainPending) {
		t.Fatalf("without accounts = %v, want pending: every address proves itself", err)
	}
	if v.calls != calls+1 {
		t.Errorf("lookups = %d, want 1", v.calls-calls)
	}
	prove(t, s, v, b, "m.at/x")
	if claimOn(t, b, "m.at/x").ViaHost {
		t.Error("open mode must never hold through the host")
	}
}

func TestPendingAttachesViaHostInSweep(t *testing.T) {
	s, v := shareStore(t)
	a, b := ownedSite(t, s, "u1"), ownedSite(t, s, "u1")
	if err := s.AddDomain(b, "m.at/x"); !errors.Is(err, ErrDomainPending) {
		t.Fatalf("= %v, want pending", err)
	}
	prove(t, s, v, a, "m.at")
	calls := v.calls
	reconcile(t, s, time.Now(), b)
	if !attached(t, s, b, "m.at/x") || !claimFor(reload(t, s, b), "m.at/x").ViaHost {
		t.Fatal("the sweep did not attach the pending claim through the host")
	}
	if v.calls != calls {
		t.Errorf("the sweep asked DNS %d times for it", v.calls-calls)
	}
}

func TestZoneNameWithPathExempt(t *testing.T) {
	s, v := zoneStore(t)
	mustZone(t, s, v, "u1", "m.at")
	site := ownedSite(t, s, "u1")
	zero := 0
	if err := s.Update(site, func(m *Meta) error { m.QuotaDomains = &zero; return nil }); err != nil {
		t.Fatal(err)
	}
	for _, addr := range []string{"m.at/a", "shop.m.at/b"} {
		if err := s.AddDomain(site, addr); err != nil {
			t.Fatalf("AddDomain(%s) in the owner's zone = %v", addr, err)
		}
	}
	if n := s.countedClaims(site); n != 0 {
		t.Fatalf("countedClaims = %d, want 0: names in the owner's zone are unlimited", n)
	}
	other := ownedSite(t, s, "u2")
	if err := s.AddDomain(other, "m.at/c"); !errors.Is(err, ErrBadDomain) && !errors.Is(err, ErrDomainTaken) {
		t.Fatalf("another account inside the zone = %v, want refused", err)
	}
}

func TestDeleteRootKeepsPathAddresses(t *testing.T) {
	s, v := shareStore(t)
	a, b := ownedSite(t, s, "u1"), ownedSite(t, s, "u1")
	prove(t, s, v, a, "m.at")
	mustAdd(t, s, b, "m.at/x")
	if err := s.Delete(reload(t, s, a)); err != nil {
		t.Fatal(err)
	}
	if got, _, err := s.ResolveAddress("m.at", "/x/y"); err != nil || got.ViewID != b.ViewID {
		t.Fatalf("after the root was deleted: %v, %v; want b", got, err)
	}
	if _, _, err := s.ResolveAddress("m.at", "/"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the empty root = %v, want ErrNotFound", err)
	}
}

func TestDependents(t *testing.T) {
	s, v := shareStore(t)
	a, b, c := ownedSite(t, s, "u1"), ownedSite(t, s, "u1"), ownedSite(t, s, "u1")
	prove(t, s, v, a, "m.at")
	mustAdd(t, s, b, "m.at/x")
	mustAdd(t, s, c, "m.at/y")
	if got := s.Dependents(reload(t, s, a), "m.at"); !slices.Equal(got, []string{"m.at/x", "m.at/y"}) {
		t.Fatalf("Dependents = %v", got)
	}
	if got := s.Dependents(reload(t, s, b), "m.at/x"); len(got) != 0 {
		t.Fatalf("an address held through the host has no dependents, got %v", got)
	}
	d := ownedSite(t, s, "u1")
	prove(t, s, v, d, "m.at/z")
	if got := s.Dependents(reload(t, s, a), "m.at"); len(got) != 0 {
		t.Fatalf("with a second proof on the host, Dependents = %v, want none", got)
	}
}

// A path address of another account inside a zone is as much a conflict as a
// plain host: the zone stays pending, and the names in use list it.
func TestZoneConflictSeesPathAddress(t *testing.T) {
	s, v := zoneStore(t)
	agency := ownedSite(t, s, "agency")
	v.answers["shop.kunde.example"] = true
	if err := s.AddDomain(agency, "shop.kunde.example/demo"); err != nil {
		t.Fatal(err)
	}
	v.zoneAnswers["kunde.example"] = true
	z, err := s.ClaimZone(context.Background(), "a", "kunde.example")
	if !errors.Is(err, ErrZonePending) {
		t.Fatalf("with a foreign path address inside: %v, want pending", err)
	}
	if len(z.Conflicts) != 1 || z.Conflicts[0] != "shop.kunde.example/demo" {
		t.Errorf("conflicts = %v", z.Conflicts)
	}
	if got := s.ZoneDomains("kunde.example"); len(got) != 1 || got[0].Domain != "shop.kunde.example/demo" {
		t.Errorf("ZoneDomains = %+v", got)
	}
}

// Removing a pending claim never takes down the link another site holds on
// the same address (review, 2026-11-02).
func TestRemovePendingClaimKeepsSiblingLink(t *testing.T) {
	s, v := shareStore(t)
	a, b, c := ownedSite(t, s, "u1"), ownedSite(t, s, "u1"), ownedSite(t, s, "u1")
	if err := s.AddDomain(b, "m.at/x"); !errors.Is(err, ErrDomainPending) {
		t.Fatalf("= %v, want pending", err)
	}
	prove(t, s, v, a, "m.at")
	if err := s.AddDomain(c, "m.at/x"); err != nil {
		t.Fatalf("the sibling attaches through the host: %v", err)
	}
	if err := s.RemoveDomain(reload(t, s, b), "m.at/x"); err != nil {
		t.Fatal(err)
	}
	if got, err := s.ByDomain("m.at/x"); err != nil || got.ViewID != c.ViewID {
		t.Fatalf("after b dropped its pending claim, m.at/x = %v, %v; want still c", got, err)
	}
}
