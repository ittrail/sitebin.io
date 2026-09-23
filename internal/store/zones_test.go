package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// zoneVerifier is scriptedVerifier plus the zone TXT route: zoneAnswers maps
// zone -> proven, zoneErrs zone -> lookup failure.
type zoneVerifier struct {
	*scriptedVerifier
	zoneAnswers map[string]bool
	zoneErrs    map[string]error
	zoneCalls   []string
}

func (v *zoneVerifier) VerifyZone(_ context.Context, zone, token string) (bool, error) {
	v.zoneCalls = append(v.zoneCalls, zone)
	if err := v.zoneErrs[zone]; err != nil {
		return false, err
	}
	return v.zoneAnswers[zone], nil
}

// zoneStore is a verifying store where every account may hold three zones.
func zoneStore(t *testing.T) (*Store, *zoneVerifier) {
	t.Helper()
	s := newTestStore(t)
	v := &zoneVerifier{scriptedVerifier: newScripted(), zoneAnswers: map[string]bool{}, zoneErrs: map[string]error{}}
	s.SetDomainVerifier(v, "sitebin.example")
	s.SetZoneCheck(func(string) (int, error) { return 3, nil })
	return s, v
}

func mustZone(t *testing.T, s *Store, v *zoneVerifier, acct, zone string) {
	t.Helper()
	v.zoneAnswers[zone] = true
	// The first claim only mints the token (see TestFreshZoneClaimAsksNoDNS);
	// asking again is the owner's "check now".
	s.ClaimZone(context.Background(), acct, zone)
	if _, err := s.ClaimZone(context.Background(), acct, zone); err != nil {
		t.Fatalf("ClaimZone(%s, %s) = %v", acct, zone, err)
	}
}

func TestZoneOwnerAttachesNamesWithoutDNS(t *testing.T) {
	s, v := zoneStore(t)
	z, err := s.ClaimZone(context.Background(), "a", "*.Kunde.Example")
	if !errors.Is(err, ErrZonePending) {
		t.Fatalf("before the TXT record: %v", err)
	}
	if z.TXTName() != "_sitebin-zone.kunde.example" || !strings.HasPrefix(z.TXTValue(), "sitebin-zone=") {
		t.Errorf("record = %s %s", z.TXTName(), z.TXTValue())
	}
	v.zoneAnswers["kunde.example"] = true
	if _, err := s.ClaimZone(context.Background(), "a", "kunde.example"); err != nil {
		t.Fatalf("with the TXT record: %v", err)
	}

	site := ownedSite(t, s, "a")
	for _, d := range []string{"shop.kunde.example", "deep.app.kunde.example", "kunde.example"} {
		if err := s.AddDomain(site, d); err != nil {
			t.Errorf("AddDomain(%s) = %v, want attached", d, err)
		}
	}
	if len(v.calls) != 0 {
		t.Errorf("DNS was asked about zone names: %v", v.calls)
	}
	if got := s.ZoneOf("shop.kunde.example", "a"); got != "kunde.example" {
		t.Errorf("ZoneOf = %q", got)
	}
}

// A fresh claim's record cannot exist yet, and looking it up would plant an
// NXDOMAIN in the resolver's cache for the zone's negative TTL. So the claim
// asks no DNS; the next check does.
func TestFreshZoneClaimAsksNoDNS(t *testing.T) {
	s, v := zoneStore(t)
	v.zoneAnswers["kunde.example"] = true
	if _, err := s.ClaimZone(context.Background(), "a", "kunde.example"); !errors.Is(err, ErrZonePending) {
		t.Fatalf("fresh claim = %v, want pending", err)
	}
	if len(v.zoneCalls) != 0 {
		t.Fatalf("a fresh claim looked up its record: %v", v.zoneCalls)
	}
	if _, err := s.ClaimZone(context.Background(), "a", "kunde.example"); err != nil {
		t.Fatalf("check now = %v", err)
	}
	if len(v.zoneCalls) != 1 {
		t.Errorf("zone lookups = %v, want exactly the check", v.zoneCalls)
	}
}

func TestZoneIsClosedToOtherAccounts(t *testing.T) {
	s, v := zoneStore(t)
	mustZone(t, s, v, "a", "kunde.example")
	for name, owner := range map[string]string{"other account": "b", "anonymous": ""} {
		site := ownedSite(t, s, owner)
		v.answers["login.kunde.example"] = true // even with a per-name proof
		if err := s.AddDomain(site, "login.kunde.example"); !errors.Is(err, ErrBadDomain) {
			t.Errorf("%s: AddDomain = %v, want ErrBadDomain", name, err)
		}
		if len(site.Meta.DomainClaims) != 0 {
			t.Errorf("%s: a claim was recorded", name)
		}
	}
	// A look-alike outside the zone is ordinary.
	other := ownedSite(t, s, "b")
	v.answers["notkunde.example"] = true
	if err := s.AddDomain(other, "notkunde.example"); err != nil {
		t.Errorf("look-alike: %v", err)
	}
}

func TestZoneClaimRefusesOverlaps(t *testing.T) {
	s, v := zoneStore(t)
	s.ReserveDomains("sitebin.example")
	s.SetOperatorZones([]string{"app.operator.example"})
	mustZone(t, s, v, "a", "kunde.example")
	mustZone(t, s, v, "a", "other.example")

	cases := map[string]struct{ acct, zone string }{
		"base domain itself":        {"b", "sitebin.example"},
		"view domain":               {"b", "x.sitebin.example"},
		"parent of the view domain": {"b", "example"},
		"operator zone":             {"b", "operator.example"},
		"inside an operator zone":   {"b", "x.app.operator.example"},
		"another account's zone":    {"b", "kunde.example"},
		"under another's zone":      {"b", "app.kunde.example"},
		"own zone nested":           {"a", "app.kunde.example"},
		"parent of another's zone":  {"b", "sub.other.example"},
	}
	for name, c := range cases {
		if _, err := s.ClaimZone(context.Background(), c.acct, c.zone); !errors.Is(err, ErrBadDomain) {
			t.Errorf("%s: ClaimZone(%s) = %v, want ErrBadDomain", name, c.zone, err)
		}
	}
	if _, err := s.ClaimZone(context.Background(), "", "anon.example"); !errors.Is(err, ErrBadDomain) {
		t.Errorf("anonymous claim = %v", err)
	}
}

func TestZoneClaimFollowsThePlan(t *testing.T) {
	s, v := zoneStore(t)
	s.SetZoneCheck(func(acct string) (int, error) {
		if acct == "pro" {
			return 0, nil
		}
		return 1, nil
	})
	if _, err := s.ClaimZone(context.Background(), "pro", "pro.example"); !errors.Is(err, ErrTooManyZones) {
		t.Errorf("plan without zones: %v", err)
	}
	mustZone(t, s, v, "studio", "one.example")
	if _, err := s.ClaimZone(context.Background(), "studio", "two.example"); !errors.Is(err, ErrTooManyZones) {
		t.Errorf("over the plan: %v", err)
	}
	// Asking again about a zone already held is a re-check, not a new zone.
	if _, err := s.ClaimZone(context.Background(), "studio", "one.example"); err != nil {
		t.Errorf("re-check: %v", err)
	}
	s.SetZoneCheck(func(string) (int, error) { return 0, errors.New("paygate down") })
	if _, err := s.ClaimZone(context.Background(), "x", "x.example"); err == nil {
		t.Error("an unknown plan granted a zone")
	}
}

func TestTwoPendingClaimantsFirstProverWins(t *testing.T) {
	s, v := zoneStore(t)
	if _, err := s.ClaimZone(context.Background(), "a", "kunde.example"); !errors.Is(err, ErrZonePending) {
		t.Fatal(err)
	}
	// b may claim the same zone: a pending claim reserves nothing.
	zb, err := s.ClaimZone(context.Background(), "b", "kunde.example")
	if !errors.Is(err, ErrZonePending) {
		t.Fatalf("second claimant: %v", err)
	}
	za, _ := s.Zones("a")
	if za[0].Token == zb.Token {
		t.Fatal("claimants share a token")
	}
	// b proves it (the verifier answers per zone; a's claim is checked too
	// but the sweep drops it once b holds the zone).
	v.zoneAnswers["kunde.example"] = true
	if _, err := s.ClaimZone(context.Background(), "b", "kunde.example"); err != nil {
		t.Fatal(err)
	}
	s.ReconcileZones(context.Background(), time.Now())
	if zs, _ := s.Zones("a"); len(zs) != 0 {
		t.Errorf("the loser's claim survived: %+v", zs)
	}
	if zs, _ := s.Zones("b"); len(zs) != 1 || !zs[0].Verified() {
		t.Errorf("winner: %+v", zs)
	}
}

func TestForeignVerifiedDomainBlocksTheZone(t *testing.T) {
	s, v := zoneStore(t)
	agency := ownedSite(t, s, "agency")
	v.answers["shop.kunde.example"] = true
	if err := s.AddDomain(agency, "shop.kunde.example"); err != nil {
		t.Fatal(err)
	}
	// The owner's own verified domain inside the zone is no conflict.
	own := ownedSite(t, s, "a")
	v.answers["www.kunde.example"] = true
	if err := s.AddDomain(own, "www.kunde.example"); err != nil {
		t.Fatal(err)
	}

	v.zoneAnswers["kunde.example"] = true
	z, err := s.ClaimZone(context.Background(), "a", "kunde.example")
	if !errors.Is(err, ErrZonePending) {
		t.Fatalf("with a foreign domain inside: %v", err)
	}
	if len(z.Conflicts) != 1 || z.Conflicts[0] != "shop.kunde.example" {
		t.Errorf("conflicts = %v", z.Conflicts)
	}
	// Nothing was taken away.
	if got, err := s.ByDomain("shop.kunde.example"); err != nil || got.ViewID != agency.ViewID {
		t.Errorf("the agency's domain moved: %v %v", got, err)
	}
	// Once the agency lets go, the sweep verifies the zone.
	if err := s.RemoveDomain(agency, "shop.kunde.example"); err != nil {
		t.Fatal(err)
	}
	s.ReconcileZones(context.Background(), time.Now())
	if zs, _ := s.Zones("a"); len(zs) != 1 || !zs[0].Verified() || len(zs[0].Conflicts) != 0 {
		t.Errorf("after the conflict cleared: %+v", zs)
	}
}

func TestZoneDropsOtherAccountsPendingClaims(t *testing.T) {
	s, v := zoneStore(t)
	squatter := ownedSite(t, s, "b")
	if err := s.AddDomain(squatter, "shop.kunde.example"); !errors.Is(err, ErrDomainPending) {
		t.Fatal(err)
	}
	mustZone(t, s, v, "a", "kunde.example")
	v.answers["shop.kunde.example"] = true // even a proof no longer helps
	if err := s.ReconcileDomains(context.Background(), squatter, time.Now()); err != nil {
		t.Fatal(err)
	}
	squatter, _ = s.ByViewID(squatter.ViewID)
	if len(squatter.Meta.DomainClaims) != 0 || len(squatter.Meta.CustomDomains) != 0 {
		t.Errorf("a claim inside another account's zone survived: %+v", squatter.Meta)
	}
}

func TestPendingNameAttachesInTheSweepThatVerifiesTheZone(t *testing.T) {
	s, v := zoneStore(t)
	site := ownedSite(t, s, "a")
	if err := s.AddDomain(site, "shop.kunde.example"); !errors.Is(err, ErrDomainPending) {
		t.Fatal(err)
	}
	if _, err := s.ClaimZone(context.Background(), "a", "kunde.example"); !errors.Is(err, ErrZonePending) {
		t.Fatal(err)
	}
	v.zoneAnswers["kunde.example"] = true
	now := time.Now()
	s.ReconcileZones(context.Background(), now)
	if err := s.ReconcileDomains(context.Background(), site, now); err != nil {
		t.Fatal(err)
	}
	site, _ = s.ByViewID(site.ViewID)
	if len(site.Meta.CustomDomains) != 1 {
		t.Errorf("not attached: %+v", site.Meta.DomainClaims)
	}
}

func TestZoneRevocationAndFallbackToPerNameProof(t *testing.T) {
	s, v := zoneStore(t)
	mustZone(t, s, v, "a", "kunde.example")
	site := ownedSite(t, s, "a")
	for _, d := range []string{"cname.kunde.example", "bare.kunde.example"} {
		if err := s.AddDomain(site, d); err != nil {
			t.Fatal(err)
		}
	}

	// A lookup error changes nothing.
	v.zoneErrs["kunde.example"] = errors.New("SERVFAIL")
	t0 := time.Now().Add(reverifyEvery + time.Hour)
	s.ReconcileZones(context.Background(), t0)
	if zs, _ := s.Zones("a"); len(zs) != 1 || zs[0].FailingSince != nil {
		t.Fatalf("a lookup error marked the zone: %+v", zs)
	}

	// Definitively absent: the window starts, and the zone holds until it ends.
	delete(v.zoneErrs, "kunde.example")
	v.zoneAnswers["kunde.example"] = false
	s.ReconcileZones(context.Background(), t0)
	if zs, _ := s.Zones("a"); len(zs) != 1 || zs[0].FailingSince == nil {
		t.Fatalf("window not started: %+v", zs)
	}
	t1 := t0.Add(revokeAfter + reverifyEvery)
	s.ReconcileZones(context.Background(), t1)
	if zs, _ := s.Zones("a"); len(zs) != 0 {
		t.Fatalf("zone not released after the window: %+v", zs)
	}

	// The names fall back to per-name proof: one has its own CNAME and stays;
	// the other enters its own revocation window and goes after it.
	v.answers["cname.kunde.example"] = true
	v.answers["bare.kunde.example"] = false
	s.ReconcileDomains(context.Background(), site, t1.Add(reverifyEvery))
	site, _ = s.ByViewID(site.ViewID)
	if len(site.Meta.CustomDomains) != 2 {
		t.Fatalf("a name was detached on the spot: %v", site.Meta.CustomDomains)
	}
	s.ReconcileDomains(context.Background(), site, t1.Add(2*reverifyEvery+revokeAfter))
	site, _ = s.ByViewID(site.ViewID)
	if len(site.Meta.CustomDomains) != 1 || site.Meta.CustomDomains[0] != "cname.kunde.example" {
		t.Errorf("after the per-name window: %v", site.Meta.CustomDomains)
	}
}

func TestZoneNamesAreExemptFromTheSiteCap(t *testing.T) {
	s, v := zoneStore(t)
	mustZone(t, s, v, "a", "kunde.example")
	site := ownedSite(t, s, "a")
	one := 1
	if err := s.Update(site, func(m *Meta) error { m.QuotaDomains = &one; return nil }); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if err := s.AddDomain(site, string(rune('a'+i))+".kunde.example"); err != nil {
			t.Fatalf("zone name %d: %v", i, err)
		}
	}
	// The cap still binds ordinary domains: one slot, then refused.
	v.answers["first.example"] = true
	if err := s.AddDomain(site, "first.example"); err != nil {
		t.Fatalf("first ordinary domain: %v", err)
	}
	if err := s.AddDomain(site, "second.example"); !errors.Is(err, ErrTooManyDomain) {
		t.Errorf("second ordinary domain = %v, want ErrTooManyDomain", err)
	}
}

func TestZoneNamesThrottle(t *testing.T) {
	s, v := zoneStore(t)
	s.SetZoneNamesPerHour(2)
	mustZone(t, s, v, "a", "kunde.example")
	site := ownedSite(t, s, "a")
	for _, d := range []string{"a.kunde.example", "b.kunde.example"} {
		if err := s.AddDomain(site, d); err != nil {
			t.Fatal(err)
		}
	}
	err := s.AddDomain(site, "c.kunde.example")
	if !errors.Is(err, ErrTooManyDomain) || !strings.Contains(err.Error(), "per hour") {
		t.Errorf("third name in the hour = %v", err)
	}
	// Re-adding a name the site already holds is a re-check, not a new name.
	if err := s.AddDomain(site, "a.kunde.example"); err != nil {
		t.Errorf("re-check throttled: %v", err)
	}
	s.SetZoneNamesPerHour(0)
	if err := s.AddDomain(site, "c.kunde.example"); err != nil {
		t.Errorf("throttle off: %v", err)
	}
}

func TestDowngradedOwnerKeepsNamesButCannotAdd(t *testing.T) {
	s, v := zoneStore(t)
	mustZone(t, s, v, "a", "kunde.example")
	site := ownedSite(t, s, "a")
	if err := s.AddDomain(site, "kept.kunde.example"); err != nil {
		t.Fatal(err)
	}
	s.SetZoneCheck(func(string) (int, error) { return 0, nil })
	if err := s.AddDomain(site, "new.kunde.example"); !errors.Is(err, ErrTooManyDomain) {
		t.Errorf("new name after downgrade = %v", err)
	}
	s.ReconcileZones(context.Background(), time.Now().Add(reverifyEvery+time.Hour))
	s.ReconcileDomains(context.Background(), site, time.Now().Add(30*24*time.Hour))
	site, _ = s.ByViewID(site.ViewID)
	if len(site.Meta.CustomDomains) != 1 {
		t.Errorf("a downgrade took a name away: %v", site.Meta.CustomDomains)
	}
}

func TestReleaseZoneAndPendingTTL(t *testing.T) {
	s, v := zoneStore(t)
	mustZone(t, s, v, "a", "kunde.example")
	if _, err := s.ClaimZone(context.Background(), "a", "later.example"); !errors.Is(err, ErrZonePending) {
		t.Fatal(err)
	}
	if err := s.ReleaseZone("b", "kunde.example"); !errors.Is(err, ErrNotFound) {
		t.Errorf("another account released it: %v", err)
	}
	s.ReconcileZones(context.Background(), time.Now().Add(pendingClaimTTL+time.Hour))
	zs, _ := s.Zones("a")
	if len(zs) != 1 || zs[0].Zone != "kunde.example" {
		t.Errorf("after the pending TTL: %+v", zs)
	}
	if err := s.ReleaseZones("a"); err != nil {
		t.Fatal(err)
	}
	if zs, _ := s.Zones("a"); len(zs) != 0 {
		t.Errorf("after ReleaseZones: %+v", zs)
	}
}

func TestNoZoneVerifierProvesNoZone(t *testing.T) {
	s, _ := verifiedStore(t) // scriptedVerifier has no VerifyZone
	s.SetZoneCheck(func(string) (int, error) { return 3, nil })
	if _, err := s.ClaimZone(context.Background(), "a", "kunde.example"); !errors.Is(err, ErrZonePending) {
		t.Errorf("= %v, want pending", err)
	}
	// And a store with no zone directory at all has no zones.
	os.RemoveAll(filepath.Join(s.root, zonesDirName))
	if zs, err := s.Zones("a"); err != nil || len(zs) != 0 {
		t.Errorf("Zones on a fresh store = %v %v", zs, err)
	}
}

func TestTrustingVerifierProvesZones(t *testing.T) {
	s := newTestStore(t)
	s.SetDomainVerifier(TrustingVerifier{}, "sitebin.example")
	s.SetZoneCheck(func(string) (int, error) { return 1, nil })
	if _, err := s.ClaimZone(context.Background(), "a", "kunde.example"); err != nil {
		t.Errorf("= %v", err)
	}
}
