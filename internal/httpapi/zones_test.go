package httpapi

import (
	"testing"

	"github.com/ittrail/sitebin.io/internal/store"
)

// The dashboard's zone operations go through the seam, and a name held
// through the owner's zone is marked for the edit page.
func TestZonesThroughTheSiteService(t *testing.T) {
	e := newEnv(t, nil)
	e.st.SetZoneCheck(func(string) (int, error) { return 1, nil })
	ss := e.api.SiteService()

	z, ok, err := ss.ClaimZone("acct", "*.Kunde.Example") // the trusting verifier proves it
	if err != nil || !ok || z.Zone != "kunde.example" || z.TXTName != "_sitebin-zone.kunde.example" {
		t.Fatalf("ClaimZone = %+v %v %v", z, ok, err)
	}
	if _, _, err := ss.ClaimZone("acct", "second.example"); err == nil {
		t.Error("a second zone went past the plan's one")
	}

	site, _, err := e.st.Create()
	if err != nil {
		t.Fatal(err)
	}
	e.st.Update(site, func(m *store.Meta) error { m.OwnerAccountID = "acct"; return nil })
	if err := e.st.AddDomain(site, "shop.kunde.example"); err != nil {
		t.Fatal(err)
	}
	if got := e.api.zoneDomains(site); got["shop.kunde.example"] != "kunde.example" {
		t.Errorf("zone_domains = %v", got)
	}
	zs, err := ss.Zones("acct")
	if err != nil || len(zs) != 1 || len(zs[0].Names) != 1 || zs[0].Names[0].ViewID != site.ViewID {
		t.Fatalf("Zones = %+v %v", zs, err)
	}
	if err := ss.ReleaseZone("other", "kunde.example"); err == nil {
		t.Error("another account released the zone")
	}
	if err := ss.ReleaseZones("acct"); err != nil {
		t.Fatal(err)
	}
	if zs, _ := ss.Zones("acct"); len(zs) != 0 {
		t.Errorf("after ReleaseZones: %+v", zs)
	}
}
