//go:build ee

package ee

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/ittrail/sitebin.io/ee/account"
	"github.com/ittrail/sitebin.io/internal/ext"
)

const zoneTiersJSON = `[
  {"id":"free","label":"Free","max_site_bytes":1000,"max_files":10,"max_sites":1,"custom_domains":0,"max_expiry_days":7},
  {"id":"pro","label":"Pro","max_site_bytes":1000,"max_files":10,"max_sites":5,"custom_domains":5,"max_expiry_days":0},
  {"id":"studio","label":"Studio","max_site_bytes":1000,"max_files":10,"max_sites":5,"custom_domains":25,"max_expiry_days":0,"max_zones":3}
]`

func setupZones(t *testing.T) (*provider, *fakeSites, http.Handler) {
	t.Helper()
	t.Setenv("SITEBIN_ACCOUNT_MODE", "tiers")
	t.Setenv("SITEBIN_TIERS", zoneTiersJSON)
	t.Setenv("SITEBIN_DEFAULT_TIER", "free")
	p := newProvider()
	sites := &fakeSites{infos: map[string]ext.SiteInfo{}}
	host := &fakeHost{dir: t.TempDir(), sites: sites}
	if err := p.Init(host); err != nil {
		t.Fatalf("Init: %v", err)
	}
	mux := http.NewServeMux()
	for pat, h := range p.PublicRoutes() {
		mux.Handle(pat, h)
	}
	return p, sites, mux
}

func TestZonesAllowedFollowsTheTier(t *testing.T) {
	p, _, mux := setupZones(t)
	_, studio := adminUser(t, p, mux, "studio@example.com", "studio")
	_, pro := adminUser(t, p, mux, "pro@example.com", "pro")
	for acct, want := range map[string]int{studio: 3, pro: 0, "nosuchaccount": 0} {
		if got, err := p.ZonesAllowed(acct); err != nil || got != want {
			t.Errorf("ZonesAllowed(%s) = %d, %v; want %d", acct, got, err, want)
		}
	}
}

func TestZoneSectionOnlyForPlansWithZones(t *testing.T) {
	p, sites, mux := setupZones(t)
	studioCookie, studio := adminUser(t, p, mux, "studio@example.com", "studio")
	proCookie, _ := adminUser(t, p, mux, "pro@example.com", "pro")

	if body := getAs(mux, "/account", studioCookie).Body.String(); !strings.Contains(body, `action="/account/zones"`) {
		t.Error("a Studio account is not offered zones")
	}
	if body := getAs(mux, "/account", proCookie).Body.String(); strings.Contains(body, `id="zones"`) {
		t.Error("a Pro account without zones sees the zones section")
	}
	// A downgraded account that still holds a zone sees it (and can remove
	// it) but is not offered a new one.
	_, _, _ = sites.ClaimZone(studio, "kunde.example")
	acc, _ := p.accounts.ByID(studio)
	p.accounts.Update(acc, func(a *account.Account) error { a.Tier = "pro"; return nil })
	body := getAs(mux, "/account", studioCookie).Body.String()
	if !strings.Contains(body, "kunde.example") || !strings.Contains(body, "/account/zones/kunde.example/delete") {
		t.Error("a held zone is hidden after a downgrade")
	}
	if strings.Contains(body, `placeholder="example.com"`) {
		t.Error("a plan without zones is offered a new one")
	}
}

func TestClaimZoneNeedsSessionAndCSRF(t *testing.T) {
	p, sites, mux := setupZones(t)
	cookie, studio := adminUser(t, p, mux, "studio@example.com", "studio")
	acc, _ := p.accounts.ByID(studio)

	if w := postAs(mux, "/account/zones", cookie, url.Values{"zone": {"kunde.example"}}); w.Code != http.StatusForbidden {
		t.Errorf("without CSRF = %d, want 403", w.Code)
	}
	// An API token is not a session: account routes never take one.
	_, secret, err := p.accounts.CreateToken(acc, "agent")
	if err != nil {
		t.Fatal(err)
	}
	r := form(url.Values{"zone": {"kunde.example"}, "csrf": {p.csrf(acc)}})
	r.URL.Path = "/account/zones"
	r.Header.Set("Authorization", "Bearer "+secret)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != http.StatusSeeOther || !strings.Contains(w.Header().Get("Location"), "/account/login") {
		t.Errorf("with an API token = %d %s, want a redirect to sign in", w.Code, w.Header().Get("Location"))
	}
	if len(sites.zones[studio]) != 0 {
		t.Fatal("a zone was claimed without a session")
	}

	w = postAs(mux, "/account/zones", cookie, url.Values{"zone": {"kunde.example"}, "csrf": {p.csrf(acc)}})
	if w.Code != http.StatusSeeOther {
		t.Fatalf("claim = %d (%s)", w.Code, w.Body)
	}
	if _, ok := sites.zones[studio]["kunde.example"]; !ok {
		t.Fatal("the zone was not claimed")
	}
	body := getAs(mux, "/account", cookie).Body.String()
	if !strings.Contains(body, "_sitebin-zone.kunde.example") || !strings.Contains(body, "sitebin-zone=tok") {
		t.Error("the TXT record to create is not shown")
	}

	w = postAs(mux, "/account/zones/kunde.example/delete", cookie, url.Values{"csrf": {p.csrf(acc)}})
	if w.Code != http.StatusSeeOther {
		t.Fatalf("remove = %d", w.Code)
	}
	if _, ok := sites.zones[studio]["kunde.example"]; ok {
		t.Error("the zone was not removed")
	}
}

func TestAccountDeletionReleasesZones(t *testing.T) {
	p, sites, mux := setupZones(t)
	cookie, studio := adminUser(t, p, mux, "studio@example.com", "studio")
	acc, _ := p.accounts.ByID(studio)
	_, _, _ = sites.ClaimZone(studio, "kunde.example")

	w := postAs(mux, "/account/delete/confirm", cookie, url.Values{"csrf": {p.csrf(acc)}})
	if w.Code != http.StatusOK {
		t.Fatalf("delete = %d (%s)", w.Code, w.Body)
	}
	if len(sites.zonesReleased) != 1 || sites.zonesReleased[0] != studio {
		t.Errorf("zones released for %v, want [%s]", sites.zonesReleased, studio)
	}
}
