package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

// Operator zones: the operator's own zone, pointed here by a wildcard, needs
// no DNS proof for the operator and is closed to everyone else.

func operatorStore(t *testing.T, operators ...string) (*Store, *scriptedVerifier) {
	t.Helper()
	s, v := verifiedStore(t)
	s.SetOperatorZones([]string{"*.App.Operator.example"})
	s.SetOperatorCheck(func(owner string) bool {
		for _, o := range operators {
			if o == owner {
				return true
			}
		}
		return false
	})
	return s, v
}

func ownedSite(t *testing.T, s *Store, owner string) *Site {
	t.Helper()
	site, _, err := s.Create()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Update(site, func(m *Meta) error { m.OwnerAccountID = owner; return nil }); err != nil {
		t.Fatal(err)
	}
	return site
}

func TestOperatorZoneAttachesAtOnceForTheOperator(t *testing.T) {
	s, v := operatorStore(t, "op")
	site := ownedSite(t, s, "op")

	if err := s.AddDomain(site, "fabiphysio.app.operator.example"); err != nil {
		t.Fatalf("AddDomain = %v, want attached", err)
	}
	if len(site.Meta.CustomDomains) != 1 {
		t.Fatalf("not attached: %v", site.Meta.CustomDomains)
	}
	if got, err := s.ByDomain("fabiphysio.app.operator.example"); err != nil || got.ViewID != site.ViewID {
		t.Errorf("not indexed: %v %v", got, err)
	}
	if len(v.calls) != 0 {
		t.Errorf("DNS was asked about an operator-zone domain: %v", v.calls)
	}
	// The zone apex is the operator's too.
	if err := s.AddDomain(site, "app.operator.example"); err != nil {
		t.Errorf("apex: %v", err)
	}
}

func TestOperatorZoneIsClosedToEveryoneElse(t *testing.T) {
	s, v := operatorStore(t, "op")
	for name, owner := range map[string]string{"customer": "someone", "anonymous": ""} {
		site := ownedSite(t, s, owner)
		err := s.AddDomain(site, "bank-login.app.operator.example")
		if !errors.Is(err, ErrBadDomain) {
			t.Errorf("%s: AddDomain = %v, want ErrBadDomain", name, err)
		}
		if len(site.Meta.DomainClaims) != 0 {
			t.Errorf("%s: a claim was recorded: %+v", name, site.Meta.DomainClaims)
		}
	}
	if len(v.calls) != 0 {
		t.Errorf("DNS was asked: %v", v.calls)
	}
	// A name that merely ends the same way is not in the zone.
	other := ownedSite(t, s, "someone")
	v.answers["notapp.operator.example"] = true
	if err := s.AddDomain(other, "notapp.operator.example"); err != nil {
		t.Errorf("a name outside the zone went through the operator gate: %v", err)
	}
}

func TestOperatorZoneWithoutACheckRefusesAndChangesNothing(t *testing.T) {
	s, _ := verifiedStore(t)
	s.SetOperatorZones([]string{"app.operator.example"})
	site := ownedSite(t, s, "op")
	if err := s.AddDomain(site, "x.app.operator.example"); !errors.Is(err, ErrBadDomain) {
		t.Errorf("AddDomain with no operator check = %v, want ErrBadDomain", err)
	}

	// An attached operator domain survives a sweep that cannot ask (the
	// one-shot cleanup command): an unanswerable check is a lookup error.
	s.SetOperatorCheck(func(string) bool { return true })
	if err := s.AddDomain(site, "x.app.operator.example"); err != nil {
		t.Fatal(err)
	}
	s.SetOperatorCheck(nil)
	later := time.Now().Add(30 * 24 * time.Hour)
	for i := 0; i < 3; i++ {
		if err := s.ReconcileDomains(context.Background(), site, later.Add(time.Duration(i)*revokeAfter)); err != nil {
			t.Fatal(err)
		}
	}
	site, _ = s.ByViewID(site.ViewID)
	if len(site.Meta.CustomDomains) != 1 {
		t.Errorf("an unanswerable check detached the domain: %+v", site.Meta)
	}
}

func TestOperatorZonePendingClaimIsAttachedBySweep(t *testing.T) {
	// A claim recorded before the zone was configured — fabiphysio's case.
	s, _ := verifiedStore(t)
	site := ownedSite(t, s, "op")
	if err := s.AddDomain(site, "fabiphysio.app.operator.example"); !errors.Is(err, ErrDomainPending) {
		t.Fatalf("before the zone: %v", err)
	}
	s.SetOperatorZones([]string{"app.operator.example"})
	s.SetOperatorCheck(func(o string) bool { return o == "op" })
	if err := s.ReconcileDomains(context.Background(), site, time.Now()); err != nil {
		t.Fatal(err)
	}
	site, _ = s.ByViewID(site.ViewID)
	if len(site.Meta.CustomDomains) != 1 {
		t.Errorf("the sweep did not attach the operator's pending claim: %+v", site.Meta.DomainClaims)
	}
}

func TestOperatorZoneDomainGoesWhenTheOwnerStopsBeingOperator(t *testing.T) {
	isOp := true
	s, _ := verifiedStore(t)
	s.SetOperatorZones([]string{"app.operator.example"})
	s.SetOperatorCheck(func(string) bool { return isOp })
	site := ownedSite(t, s, "op")
	if err := s.AddDomain(site, "x.app.operator.example"); err != nil {
		t.Fatal(err)
	}
	isOp = false
	t0 := time.Now().Add(reverifyEvery + time.Hour)
	s.ReconcileDomains(context.Background(), site, t0)
	site, _ = s.ByViewID(site.ViewID)
	if len(site.Meta.CustomDomains) != 1 {
		t.Fatal("detached at once; the revocation window must run first")
	}
	s.ReconcileDomains(context.Background(), site, t0.Add(revokeAfter+reverifyEvery))
	site, _ = s.ByViewID(site.ViewID)
	if len(site.Meta.CustomDomains) != 0 {
		t.Errorf("still attached after the window: %v", site.Meta.CustomDomains)
	}
}
