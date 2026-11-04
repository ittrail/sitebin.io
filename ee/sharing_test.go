//go:build ee

package ee

import (
	"net/http"
	"net/http/httptest"
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
	if _, ok := sites.infos[staleSite]; !ok {
		t.Errorf("%s: the site is gone", what)
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

// A marker on a site meta.json gives to nobody (an anonymous drop) grants
// nothing either: ownerless is not "mine".
func TestOwnerlessSiteGrantsNothingToAMarker(t *testing.T) {
	p, sites, mux := setupZones(t)
	cookie, id := adminUser(t, p, mux, "marker@example.com", "pro")
	acc, _ := p.accounts.ByID(id)
	staleMarker(t, p, sites, acc, "")

	if body := getAs(mux, "/account", cookie).Body.String(); strings.Contains(body, staleSite) {
		t.Error("the dashboard lists an anonymous drop")
	}
	for _, action := range []string{"rotate", "delete"} {
		if w := postAs(mux, "/account/sites/"+staleSite+"/"+action, cookie, url.Values{"csrf": {p.csrf(acc)}}); w.Code != http.StatusForbidden {
			t.Errorf("%s on an anonymous drop = %d, want 403", action, w.Code)
		}
	}
	if err := p.restampSites(acc, eeconfig.Tier{ID: "pro", Trusted: true}); err != nil {
		t.Fatalf("restamp: %v", err)
	}
	if _, stamped := sites.quotas[staleSite]; stamped {
		t.Error("a restamp stamped (and could trust) an anonymous drop")
	}
	if ids, ok := p.AccountSiteIDs(id); !ok || len(ids) != 0 {
		t.Errorf("AccountSiteIDs = %v, %v; want none", ids, ok)
	}
	if w := postAs(mux, "/account/delete/confirm", cookie, url.Values{"csrf": {p.csrf(acc)}}); w.Code != http.StatusOK {
		t.Fatalf("account deletion = %d", w.Code)
	}
	assertSiteIntact(t, sites, "account deletion")
}

func TestSuspendingTheOldOwnerLeavesTheNewOwnersSiteUnlocked(t *testing.T) {
	p, host, mux := setupStackInstance(t, testGDPRSecret)
	old, _ := stackUser(t, p, host, suspendSubject, "subject@example.com")
	cur, err := p.accounts.CreateOAuth(account.OIDCProv, "22222222-2222-4222-8222-222222222222", "new@example.com", true, "free")
	if err != nil {
		t.Fatal(err)
	}
	staleMarker(t, p, host.sites, old, cur.ID)

	if w := orderSuspend(mux, suspendOrderBody(suspendSubject, true, "phishing")); w.Code != 200 {
		t.Fatalf("suspend = %d (%s)", w.Code, w.Body)
	}
	if host.sites.infos[staleSite].Locked != nil {
		t.Error("suspending the old owner locked the new owner's site")
	}
	if host.sites.infos["aaaaaaaaaaaaaaaaaaaaaaaaaa"].Locked == nil {
		t.Error("the old owner's own site was not locked (the test proves nothing)")
	}
}

func TestAccountSiteIDsLeavesOutAStaleMarkerSite(t *testing.T) {
	p, sites, mux := setupZones(t)
	_, oldID := adminUser(t, p, mux, "old@example.com", "pro")
	_, newID := adminUser(t, p, mux, "new@example.com", "pro")
	old, _ := p.accounts.ByID(oldID)
	staleMarker(t, p, sites, old, newID)
	if ids, ok := p.AccountSiteIDs(oldID); !ok || len(ids) != 0 {
		t.Errorf("AccountSiteIDs(old) = %v, %v; want none", ids, ok)
	}
}

func TestLockedSiteOfAnotherOwnerDoesNotBlockDeletion(t *testing.T) {
	p, sites, mux := setupZones(t)
	cookie, oldID := adminUser(t, p, mux, "old@example.com", "pro")
	_, newID := adminUser(t, p, mux, "new@example.com", "pro")
	old, _ := p.accounts.ByID(oldID)
	staleMarker(t, p, sites, old, newID)
	info := sites.infos[staleSite]
	info.Locked = &ext.SiteLock{At: time.Now(), By: ext.LockByAdmin, Hold: &ext.LockHold{}}
	sites.infos[staleSite] = info

	if locked, err := p.lockedSites(old); err != nil || len(locked) != 0 {
		t.Errorf("lockedSites = %v, %v; want none", locked, err)
	}
	if p.refuseLockedDeletion(httptest.NewRecorder(), old) {
		t.Error("a lock on another owner's site blocked the old owner's deletion")
	}
	if w := postAs(mux, "/account/delete/confirm", cookie, url.Values{"csrf": {p.csrf(old)}}); w.Code != http.StatusOK {
		t.Errorf("deletion = %d", w.Code)
	}
}

func TestSharingAllowedFalseInOpenMode(t *testing.T) {
	t.Setenv("SITEBIN_ACCOUNT_MODE", "open")
	p := newProvider()
	if ok, err := p.SharingAllowed("anyone"); ok || err != nil {
		t.Errorf("SharingAllowed in open mode = %v, %v; want false, nil", ok, err)
	}
}
