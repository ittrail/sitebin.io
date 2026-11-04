//go:build ee

package ee

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/ittrail/sitebin.io/ee/account"
	"github.com/ittrail/sitebin.io/ee/eeconfig"
	"github.com/ittrail/sitebin.io/internal/ext"
)

const sharingTiersJSON = `[
  {"id":"free","label":"Free","max_site_bytes":1000,"max_files":10,"max_sites":1,"custom_domains":0,"max_expiry_days":7},
  {"id":"team","label":"Team","max_site_bytes":1000,"max_files":10,"max_sites":5,"custom_domains":5,"max_expiry_days":0,"sharing":true}
]`

func sharingProvider(t *testing.T, mode, tiers string) (*provider, http.Handler) {
	t.Helper()
	t.Setenv("SITEBIN_ACCOUNT_MODE", mode)
	if tiers != "" {
		t.Setenv("SITEBIN_TIERS", tiers)
		t.Setenv("SITEBIN_DEFAULT_TIER", "free")
	}
	p := newProvider()
	host := &fakeHost{dir: t.TempDir(), sites: &fakeSites{infos: map[string]ext.SiteInfo{}}}
	if err := p.Init(host); err != nil {
		t.Fatalf("Init: %v", err)
	}
	return p, serveMux(p)
}

func TestSharingAllowedByMode(t *testing.T) {
	check := func(p *provider, id string, want bool, wantErr bool) {
		t.Helper()
		got, err := p.SharingAllowed(id)
		if (err != nil) != wantErr || got != want {
			t.Errorf("SharingAllowed(%s) = %v, %v; want %v, error=%v", id, got, err, want, wantErr)
		}
	}

	p, mux := sharingProvider(t, "accounts", "")
	_, live := adminUser(t, p, mux, "live@example.com", "free")
	check(p, live, true, false)
	check(p, "nosuchaccount", false, false)

	p, mux = sharingProvider(t, "tiers", sharingTiersJSON)
	_, team := adminUser(t, p, mux, "team@example.com", "team")
	_, free := adminUser(t, p, mux, "free@example.com", "free")
	_, gone := adminUser(t, p, mux, "gone@example.com", "legacy")
	_, susp := adminUser(t, p, mux, "susp@example.com", "team")
	check(p, team, true, false)
	check(p, free, false, false)
	check(p, "nosuchaccount", false, false)
	check(p, gone, false, true) // a plan this instance does not configure is unknown, not "no"

	acc, _ := p.accounts.ByID(susp)
	now := time.Now()
	p.accounts.Update(acc, func(a *account.Account) error { a.SuspendedAt = &now; return nil })
	check(p, susp, false, false)

}

const staleSite = "ssssssssssssssssssssssssss"

// staleMarker leaves old with a marker for a site that meta.json gives to
// owner, as a crash in the middle of a transfer would.
func staleMarker(t *testing.T, p *provider, sites *fakeSites, old *account.Account, owner string) {
	t.Helper()
	sites.infos[staleSite] = ext.SiteInfo{ViewID: staleSite, Mode: "webserver", Owner: owner}
	if err := p.accounts.LinkSite(old, staleSite); err != nil {
		t.Fatal(err)
	}
}

func assertSiteIntact(t *testing.T, sites *fakeSites, what string) {
	t.Helper()
	if len(sites.deleted) != 0 {
		t.Errorf("%s: deleted %v", what, sites.deleted)
	}
	if info, ok := sites.infos[staleSite]; !ok || info.Owner == "" {
		t.Errorf("%s: the site or its meta owner is gone", what)
	}
}

func TestStaleMarkerGrantsNothing(t *testing.T) {
	t.Run("dashboard, restamp and local deletion", func(t *testing.T) {
		p, sites, mux := setupZones(t)
		oldCookie, oldID := adminUser(t, p, mux, "old@example.com", "pro")
		_, newID := adminUser(t, p, mux, "new@example.com", "pro")
		old, _ := p.accounts.ByID(oldID)
		staleMarker(t, p, sites, old, newID)

		if body := getAs(mux, "/account", oldCookie).Body.String(); strings.Contains(body, staleSite) {
			t.Error("the old owner's dashboard lists a site meta.json gave away")
		}
		for _, action := range []string{"rotate", "delete"} {
			w := postAs(mux, "/account/sites/"+staleSite+"/"+action, oldCookie, url.Values{"csrf": {p.csrf(old)}})
			if w.Code != http.StatusForbidden {
				t.Errorf("%s by the old owner = %d, want 403", action, w.Code)
			}
		}
		if len(sites.rotated) != 0 {
			t.Errorf("a password was rotated: %v", sites.rotated)
		}

		if err := p.restampSites(old, eeconfig.Tier{ID: "pro"}); err != nil {
			t.Fatalf("restamp: %v", err)
		}
		if _, stamped := sites.quotas[staleSite]; stamped {
			t.Error("a restamp for the old owner touched the new owner's site")
		}

		w := postAs(mux, "/account/delete/confirm", oldCookie, url.Values{"csrf": {p.csrf(old)}})
		if w.Code != http.StatusOK {
			t.Fatalf("deleting the old account = %d (%s)", w.Code, w.Body)
		}
		if _, err := p.accounts.ByID(oldID); err == nil {
			t.Error("the old account still exists")
		}
		assertSiteIntact(t, sites, "local account deletion")
	})

	t.Run("GDPR deletion", func(t *testing.T) {
		p, host, mux := setupStackInstance(t, testGDPRSecret)
		old, err := p.accounts.CreateOAuth(account.OIDCProv, "11111111-1111-4111-8111-111111111111", "old@example.com", true, "free")
		if err != nil {
			t.Fatal(err)
		}
		cur, err := p.accounts.CreateOAuth(account.OIDCProv, "22222222-2222-4222-8222-222222222222", "new@example.com", true, "free")
		if err != nil {
			t.Fatal(err)
		}
		staleMarker(t, p, host.sites, old, cur.ID)

		body := `{"userId":"` + old.OAuthSubject + `","email":"old@example.com"}`
		exp := serve(mux, stackOrder(gdprExportPath, testGDPRSecret, time.Now(), body))
		if exp.Code != 200 || strings.Contains(exp.Body.String(), staleSite) {
			t.Errorf("the export of the old owner = %d, lists the site: %v", exp.Code, strings.Contains(exp.Body.String(), staleSite))
		}
		w := serve(mux, stackOrder(gdprDeletePath, testGDPRSecret, time.Now(), body))
		if w.Code != 200 {
			t.Fatalf("delete = %d (%s)", w.Code, w.Body)
		}
		if _, err := p.accounts.ByID(old.ID); err == nil {
			t.Error("the old account still exists")
		}
		assertSiteIntact(t, host.sites, "GDPR deletion")
	})
}

