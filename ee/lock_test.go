//go:build ee

package ee

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/ittrail/sitebin.io/internal/ext"
)

// The operator's lock in the extension: the register places and lifts it,
// the owner's dashboard shows it and refuses what it freezes, and no account
// deletion ends it. See
// docs/superpowers/specs/2026-09-28-site-lock-and-account-suspension.md.

const (
	siteA = "aaaaaaaaaaaaaaaaaaaaaaaaaa"
	siteC = "cccccccccccccccccccccccccc"
)

// --- the register ---

func TestAdminLocksWithAReasonAndUnlocks(t *testing.T) {
	p, host, mux := setupAdmin(t, "boss@example.com")
	cookie, accID := adminUser(t, p, mux, "boss@example.com", "admin")
	instance(t, host, accID)
	acc, _ := p.accounts.ByEmail("boss@example.com")

	// Step one renders the lock form and changes nothing.
	body := getAs(mux, "/account/admin?lock="+siteC+"&filter=anon", cookie).Body.String()
	if !strings.Contains(body, `action="/account/admin/sites/`+siteC+`/lock?filter=anon"`) || !strings.Contains(body, `name="reason"`) {
		t.Fatalf("lock step not rendered: %s", body)
	}
	if host.sites.infos[siteC].Locked != nil {
		t.Fatal("step one locked the site")
	}

	w := postAs(mux, "/account/admin/sites/"+siteC+"/lock", cookie, url.Values{"csrf": {p.csrf(acc)}, "reason": {"phishing\n(bank)"}})
	if w.Code != http.StatusSeeOther || !strings.Contains(w.Header().Get("Location"), "flash=locked") {
		t.Fatalf("lock = %d %q", w.Code, w.Header().Get("Location"))
	}
	l := host.sites.infos[siteC].Locked
	if l == nil || l.By != ext.LockByAdmin || l.Reason != "phishing (bank)" || l.At.IsZero() {
		t.Fatalf("lock = %+v", l)
	}

	body = getAs(mux, "/account/admin", cookie).Body.String()
	if !strings.Contains(body, "LOCKED") || !strings.Contains(body, "phishing (bank)") || !strings.Contains(body, "by an admin") {
		t.Error("the register does not show the lock with its reason and author")
	}
	if !strings.Contains(body, "?unlock="+siteC) {
		t.Error("a locked row offers no unlock")
	}

	// Unlock is a step too.
	body = getAs(mux, "/account/admin?unlock="+siteC, cookie).Body.String()
	if !strings.Contains(body, `action="/account/admin/sites/`+siteC+`/unlock"`) {
		t.Fatalf("unlock step not rendered")
	}
	w = postAs(mux, "/account/admin/sites/"+siteC+"/unlock", cookie, url.Values{"csrf": {p.csrf(acc)}})
	if w.Code != http.StatusSeeOther || host.sites.infos[siteC].Locked != nil {
		t.Fatalf("unlock = %d, lock %+v", w.Code, host.sites.infos[siteC].Locked)
	}
}

func TestAdminFiltersAndCountsLockedSites(t *testing.T) {
	p, host, mux := setupAdmin(t, "boss@example.com")
	cookie, accID := adminUser(t, p, mux, "boss@example.com", "admin")
	instance(t, host, accID)
	host.sites.SetLock(siteC, &ext.SiteLock{By: ext.LockByAdmin})

	if n := p.instanceStats(mustAll(t, host)).Locked; n != 1 {
		t.Errorf("Locked = %d, want 1", n)
	}
	only := getAs(mux, "/account/admin?filter=locked", cookie).Body.String()
	if !strings.Contains(only, siteC) || strings.Contains(only, siteA) {
		t.Error("the locked filter does not show exactly the locked site")
	}
	// The figure describes the instance, never the filtered view.
	if !strings.Contains(only, `<span class="k">Locked</span><span class="v">1</span>`) {
		t.Error("the Locked figure is missing")
	}
}

// The operator's delete is the one a lock does not stop, and its
// confirmation says the site is held.
func TestAdminDeletesALockedSiteExplicitly(t *testing.T) {
	p, host, mux := setupAdmin(t, "boss@example.com")
	cookie, accID := adminUser(t, p, mux, "boss@example.com", "admin")
	instance(t, host, accID)
	acc, _ := p.accounts.ByEmail("boss@example.com")
	host.sites.SetLock(siteC, &ext.SiteLock{By: ext.LockByAdmin})

	if body := getAs(mux, "/account/admin?confirm="+siteC, cookie).Body.String(); !strings.Contains(body, "LOCKED") {
		t.Error("the delete confirmation does not say the site is locked")
	}
	w := postAs(mux, "/account/admin/sites/"+siteC+"/delete", cookie, url.Values{"csrf": {p.csrf(acc)}})
	if w.Code != http.StatusSeeOther {
		t.Fatalf("delete = %d", w.Code)
	}
	if len(host.sites.forceDeleted) != 1 || host.sites.forceDeleted[0] != siteC {
		t.Fatalf("forceDeleted = %v", host.sites.forceDeleted)
	}
}

// A suspension's lock can be made the operator's own, so that the
// unsuspension leaves it in place.
func TestAdminKeepsAnAccountLock(t *testing.T) {
	p, host, mux := setupAdmin(t, "boss@example.com")
	cookie, accID := adminUser(t, p, mux, "boss@example.com", "admin")
	instance(t, host, accID)
	acc, _ := p.accounts.ByEmail("boss@example.com")
	host.sites.SetLock(siteC, &ext.SiteLock{Reason: "suspended", By: ext.LockByAccount})

	body := getAs(mux, "/account/admin", cookie).Body.String()
	if !strings.Contains(body, "with the owner&#39;s suspension") || !strings.Contains(body, "?lock="+siteC) {
		t.Fatal("an account lock is not shown as one, or offers no keep")
	}
	postAs(mux, "/account/admin/sites/"+siteC+"/lock", cookie, url.Values{"csrf": {p.csrf(acc)}, "reason": {"evidence"}})
	if l := host.sites.infos[siteC].Locked; l == nil || l.By != ext.LockByAdmin || l.Reason != "evidence" {
		t.Fatalf("kept lock = %+v", l)
	}
	if released, _ := host.sites.ReleaseLock(siteC, ext.LockByAccount); released {
		t.Error("an unsuspension would lift the lock the operator kept")
	}
}

func TestAdminLockActionsRequireCSRFAndAnAdmin(t *testing.T) {
	p, host, mux := setupAdmin(t, "boss@example.com")
	adminCookie, accID := adminUser(t, p, mux, "boss@example.com", "admin")
	instance(t, host, accID)
	for _, path := range []string{"/account/admin/sites/" + siteC + "/lock", "/account/admin/sites/" + siteC + "/unlock"} {
		if w := postAs(mux, path, adminCookie, url.Values{}); w.Code != http.StatusForbidden {
			t.Errorf("%s without csrf = %d, want 403", path, w.Code)
		}
	}
	cookie, _ := adminUser(t, p, mux, "nobody@example.com", "free")
	nobody, _ := p.accounts.ByEmail("nobody@example.com")
	if w := postAs(mux, "/account/admin/sites/"+siteC+"/lock", cookie, url.Values{"csrf": {p.csrf(nobody)}}); w.Code != 404 {
		t.Errorf("non-admin lock = %d, want 404", w.Code)
	}
	if host.sites.infos[siteC].Locked != nil {
		t.Fatal("a refused request locked the site")
	}
}

// --- the owner's dashboard ---

func TestDashboardShowsALockAndOffersNothingItRefuses(t *testing.T) {
	p, host, mux := setupAccounts(t)
	acc, cookie := localUser(t, p, "owner@example.com")
	for _, id := range []string{siteA, siteC} {
		host.sites.site(id)
		p.accounts.LinkSite(acc, id)
	}
	host.sites.SetLock(siteC, &ext.SiteLock{At: time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC), Reason: "phishing", By: ext.LockByAdmin})

	body := getAs(mux, "/account", cookie).Body.String()
	if !strings.Contains(body, "Locked by the operator on 2026-09-28: phishing") {
		t.Fatalf("the dashboard does not show the lock: %s", body)
	}
	for _, action := range []string{"/rotate", "/name", "/delete"} {
		if strings.Contains(body, "/account/sites/"+siteC+action) {
			t.Errorf("a locked site still offers %s", action)
		}
		if !strings.Contains(body, "/account/sites/"+siteA+action) {
			t.Errorf("an unlocked site lost %s", action)
		}
	}
	if strings.Contains(body, "admin") && strings.Contains(body, "by an admin") {
		t.Error("the owner is told who locked the site")
	}

	// A form rendered before the lock is answered with the lock.
	for _, action := range []string{"/rotate", "/name", "/delete"} {
		w := postAs(mux, "/account/sites/"+siteC+action, cookie, url.Values{"csrf": {p.csrf(acc)}, "name": {"clean"}})
		if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "locked by the operator") {
			t.Errorf("%s on a locked site = %d %s", action, w.Code, w.Body)
		}
	}
	if len(host.sites.rotated) != 0 || len(host.sites.deleted) != 0 || host.sites.infos[siteC].Name != "" {
		t.Error("the owner changed a locked site")
	}
	if ids, _ := p.accounts.ListSiteIDs(acc); len(ids) != 2 {
		t.Error("a refused delete dropped the ownership marker")
	}
}

// --- no account deletion ends a hold ---

func TestLocalDeletionRefusedWhileASiteIsLocked(t *testing.T) {
	p, host, mux := setupAccounts(t)
	fake := &fakeBilling{name: "fake"}
	p.billing = fake
	acc, cookie := localUser(t, p, "owner@example.com")
	subscribed(t, p, acc, "fake")
	for _, id := range []string{siteA, siteC} {
		host.sites.site(id)
		p.accounts.LinkSite(acc, id)
	}
	host.sites.SetLock(siteC, &ext.SiteLock{By: ext.LockByAdmin})

	for _, path := range []string{"/account/delete", "/account/delete/confirm"} {
		w := postAs(mux, path, cookie, url.Values{"csrf": {p.csrf(acc)}})
		if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "Account not deleted") {
			t.Fatalf("%s with a locked site = %d %s", path, w.Code, w.Body)
		}
	}
	if _, err := p.accounts.ByID(acc.ID); err != nil {
		t.Fatal("the account was deleted")
	}
	if len(host.sites.deleted) != 0 {
		t.Errorf("sites deleted: %v", host.sites.deleted)
	}
	if len(fake.cancelled) != 0 {
		t.Error("the subscription was cancelled for an account that stays")
	}
}

func TestGDPRDeleteRefusedWhileASiteIsLocked(t *testing.T) {
	p, host, mux := setupStackInstance(t, testGDPRSecret)
	acc, _ := stackUser(t, p, host, "11111111-1111-4111-8111-111111111111", "subject@example.com")
	host.sites.SetLock("bbbbbbbbbbbbbbbbbbbbbbbbbb", &ext.SiteLock{By: ext.LockByAccount})

	order := `{"userId":"` + acc.OAuthSubject + `","email":"subject@example.com"}`
	w := serve(mux, stackOrder(gdprDeletePath, testGDPRSecret, time.Now(), order))
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "bbbbbbbbbbbbbbbbbbbbbbbbbb") {
		t.Fatalf("delete with a locked site = %d %s, want 409 naming it", w.Code, w.Body)
	}
	if _, err := p.accounts.ByID(acc.ID); err != nil {
		t.Fatal("the account was erased around a locked site")
	}
	if len(host.sites.deleted) != 0 {
		t.Fatalf("sites deleted before the refusal: %v", host.sites.deleted)
	}

	// The operator deletes the held site in the register; the retry goes through.
	host.sites.ForceDelete("bbbbbbbbbbbbbbbbbbbbbbbbbb")
	w = serve(mux, stackOrder(gdprDeletePath, testGDPRSecret, time.Now(), order))
	if w.Code != 200 {
		t.Fatalf("retry = %d %s", w.Code, w.Body)
	}
	if _, err := p.accounts.ByID(acc.ID); err == nil {
		t.Error("the account survived the retry")
	}
}

// A locked site's restamp is a no-op, and the tier sync must not treat it as
// a failure that holds the account's tier back.
func TestRestampSkipsALockedSite(t *testing.T) {
	p, host, _ := setupAdmin(t, "")
	acc, err := p.accounts.CreateLocal("owner@example.com", "$argon2$notreal", "free")
	if err != nil {
		t.Fatal(err)
	}
	host.sites.site(siteC)
	p.accounts.LinkSite(acc, siteC)
	host.sites.SetLock(siteC, &ext.SiteLock{By: ext.LockByAdmin})
	tier, _ := p.cfg.Tier("unlimited")
	if err := p.restampSites(acc, tier); err != nil {
		t.Fatalf("restamp with a locked site: %v", err)
	}
	if _, stamped := host.sites.quotas[siteC]; stamped {
		t.Error("a locked site was restamped")
	}
}
