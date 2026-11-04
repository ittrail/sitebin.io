//go:build ee

package ee

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ittrail/sitebin.io/ee/account"
	"github.com/ittrail/sitebin.io/ee/eeconfig"
	"github.com/ittrail/sitebin.io/internal/ext"
	"github.com/ittrail/sitebin.io/internal/ids"
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
	if err := initT(t, p, host); err != nil {
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

	defer func() { assertSiteIntact(t, sites, "locked site of another owner") }()
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

func TestOneUnreadableOwnerDoesNotStopTheSuspensionOfTheRest(t *testing.T) {
	p, host, mux := setupStackInstance(t, testGDPRSecret)
	acc, _ := stackUser(t, p, host, suspendSubject, "subject@example.com")
	const bad, good = "aaaaaaaaaaaaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbbbbbbbbbbbb"
	host.sites.ownerErrs = map[string]error{bad: errors.New("meta.json is damaged")}

	w := orderSuspend(mux, suspendOrderBody(suspendSubject, true, "phishing"))
	if w.Code == 200 {
		t.Errorf("the order reported success although a site could not be resolved: %s", w.Body)
	}
	if host.sites.infos[good].Locked == nil {
		t.Error("the readable site was not locked because another site's owner could not be read")
	}
	if host.sites.infos[bad].Locked != nil {
		t.Error("a site whose owner is unknown was locked")
	}

	gi := host.sites.infos[good]
	gi.Locked = nil // a locked site is frozen against restamps
	host.sites.infos[good] = gi
	// A restamp does the same: the readable site is restamped, the failure is reported.
	if err := p.restampSites(acc, eeconfig.Tier{ID: "free"}); err == nil {
		t.Error("restampSites reported success with an unresolved site")
	}
	if _, ok := host.sites.quotas[good]; !ok {
		t.Error("the readable site was not restamped")
	}
	if _, ok := host.sites.quotas[bad]; ok {
		t.Error("a site whose owner is unknown was restamped")
	}
}

func TestUnknownOwnerExcludesTheSiteAndNothingIsDeleted(t *testing.T) {
	p, host, mux := setupStackInstance(t, testGDPRSecret)
	acc, _ := stackUser(t, p, host, suspendSubject, "subject@example.com")
	const bad = "aaaaaaaaaaaaaaaaaaaaaaaaaa"
	host.sites.ownerErrs = map[string]error{bad: errors.New("meta.json is damaged")}

	if ids, _ := p.ownedSiteIDs(acc); slices.Contains(ids, bad) {
		t.Error("a site with an unreadable owner is listed")
	}
	// The core refuses the delete of an unknown owner; the fake mirrors that
	// by failing the same read.
	host.sites.deleteErrs = map[string]error{bad: errors.New("meta.json is damaged")}
	body := `{"userId":"` + acc.OAuthSubject + `","email":"subject@example.com"}`
	w := serve(mux, stackOrder(gdprDeletePath, testGDPRSecret, time.Now(), body))
	if w.Code == 200 {
		t.Errorf("the account was deleted while a site's owner is unknown: %s", w.Body)
	}
	if _, err := p.accounts.ByID(acc.ID); err != nil {
		t.Errorf("the account is gone: %v", err)
	}
	if slices.Contains(host.sites.deleted, bad) {
		t.Error("the site was deleted")
	}
}

// racySites lets a transfer commit between the dashboard's owns() check and
// the delete: the owner changes just before DeleteSiteIfOwner runs.
type racySites struct {
	*fakeSites
	newOwner string
}

func (r *racySites) DeleteSiteIfOwner(id, owner string) error {
	i := r.infos[id]
	i.Owner = r.newOwner
	r.infos[id] = i
	return r.fakeSites.DeleteSiteIfOwner(id, owner)
}

type racyHost struct {
	*fakeHost
	s *racySites
}

func (h *racyHost) Sites() ext.SiteService { return h.s }

func TestDashboardDeleteRefusesWhenTheOwnerChangedAfterTheCheck(t *testing.T) {
	t.Setenv("SITEBIN_ACCOUNT_MODE", "accounts")
	p := newProvider()
	fs := &fakeSites{infos: map[string]ext.SiteInfo{}}
	host := &racyHost{fakeHost: &fakeHost{dir: t.TempDir(), sites: fs}, s: &racySites{fakeSites: fs, newOwner: "someone-else"}}
	if err := initT(t, p, host); err != nil {
		t.Fatal(err)
	}
	mux := serveMux(p)
	cookie, id := adminUser(t, p, mux, "old@example.com", "free")
	acc, _ := p.accounts.ByID(id)
	fs.site(staleSite)
	if err := linkOwned(p, acc, staleSite); err != nil {
		t.Fatal(err)
	}
	w := postAs(mux, "/account/sites/"+staleSite+"/delete", cookie, url.Values{"csrf": {p.csrf(acc)}})
	if w.Code != http.StatusForbidden {
		t.Errorf("delete after a transfer = %d, want 403", w.Code)
	}
	assertSiteIntact(t, fs, "dashboard delete")
}

// sharedSite makes a site in the fake service that lists admin (with until,
// when set), and links admin's share marker for it.
func sharedSite(t *testing.T, p *provider, sites *fakeSites, admin string, until *time.Time) string {
	t.Helper()
	id := ids.New()
	sites.infos[id] = ext.SiteInfo{ViewID: id, Mode: "webserver", Owner: "someone-else",
		Tags: []string{"owner-tag"}, Admins: []ext.SiteAdmin{{Account: admin, Until: until}}}
	if err := p.owner.LinkShare(admin, id); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestSharedSiteIDsKeepsOnlySitesThatStillListTheAccount(t *testing.T) {
	p, _ := sharingProvider(t, "accounts", "")
	sites := p.host.Sites().(*fakeSites)
	me, other := ids.New(), ids.New()

	live := sharedSite(t, p, sites, me, nil)
	end := time.Now().Add(-time.Hour)
	ended := sharedSite(t, p, sites, me, &end) // the core gate decides a date; the index keeps it
	removed := sharedSite(t, p, sites, me, nil)
	sites.infos[removed] = ext.SiteInfo{ViewID: removed, Admins: []ext.SiteAdmin{{Account: other}}}
	gone := ids.New()
	if err := p.owner.LinkShare(me, gone); err != nil { // its site is deleted
		t.Fatal(err)
	}
	broken := sharedSite(t, p, sites, me, nil)
	sites.adminErrs = map[string]error{broken: errors.New("meta.json is damaged")}

	got := p.SharedSiteIDs(me)
	slices.Sort(got)
	want := []string{live, ended}
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("SharedSiteIDs = %v, want %v", got, want)
	}
	if got := p.SharedSiteIDs(other); len(got) != 0 {
		t.Errorf("an account with no marker = %v", got)
	}
}

func TestAdminTagsAreTheAdminsOwn(t *testing.T) {
	p, _ := sharingProvider(t, "accounts", "")
	sites := p.host.Sites().(*fakeSites)
	mux := serveMux(p)
	_, me := adminUser(t, p, mux, "admin@example.com", "free")
	_, other := adminUser(t, p, mux, "other@example.com", "free")
	site := sharedSite(t, p, sites, me, nil)

	if got := p.AdminTags(me, site); got != nil {
		t.Fatalf("AdminTags before = %v", got)
	}
	if err := p.SetAdminTags(me, site, []string{"Kunde", "Urgent"}); err != nil {
		t.Fatal(err)
	}
	if got := p.AdminTags(me, site); !slices.Equal(got, []string{"Kunde", "Urgent"}) {
		t.Errorf("AdminTags = %v", got)
	}
	if got := p.AdminTags(other, site); got != nil {
		t.Errorf("another account sees %v", got)
	}
	if got := sites.infos[site].Tags; !slices.Equal(got, []string{"owner-tag"}) {
		t.Errorf("the owner's Meta.Tags changed: %v", got)
	}
	// An empty list clears the key.
	if err := p.SetAdminTags(me, site, nil); err != nil {
		t.Fatal(err)
	}
	if got := p.AdminTags(me, site); got != nil {
		t.Errorf("AdminTags after clearing = %v", got)
	}
	reg, _ := p.accounts.Tags(me)
	if _, there := reg.Sites[site]; there {
		t.Errorf("the key stays: %v", reg.Sites)
	}

	// A site that does not list the account is refused, and nothing is stored.
	notMine := ids.New()
	sites.infos[notMine] = ext.SiteInfo{ViewID: notMine, Owner: me} // the owner is not an admin of its own site
	for name, id := range map[string]string{"another's site": notMine, "no such site": ids.New()} {
		if err := p.SetAdminTags(me, id, []string{"x"}); err == nil {
			t.Errorf("%s: SetAdminTags was accepted", name)
		}
	}
	// A refused tag list is an error and stores nothing.
	if err := p.SetAdminTags(me, site, []string{"bad\ttag"}); err == nil {
		t.Error("an invalid tag was accepted")
	}
	reg, _ = p.accounts.Tags(me)
	if len(reg.Sites) != 0 {
		t.Errorf("a refused write stored %v", reg.Sites)
	}
}

func TestOwnershipHelpersWithoutInit(t *testing.T) {
	p := newProvider() // Init never ran, as for `sitebin cleanup`
	if got := p.SharedSiteIDs("x"); got != nil {
		t.Errorf("SharedSiteIDs = %v", got)
	}
	if got := p.AdminTags("x", "y"); got != nil {
		t.Errorf("AdminTags = %v", got)
	}
	if err := p.SetAdminTags("x", "y", []string{"a"}); err == nil {
		t.Error("SetAdminTags without Init succeeded")
	}
}
