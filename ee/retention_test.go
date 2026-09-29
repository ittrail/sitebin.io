//go:build ee

package ee

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/ittrail/sitebin.io/internal/ext"
	"github.com/ittrail/sitebin.io/internal/store"
)

// The lock retention in the register: every locked row says when the sweep
// purges the site or that an evidence hold keeps it, and the operator places
// and releases the hold in two server-rendered steps. See the lock-retention
// addendum of docs/superpowers/specs/2026-09-28-site-lock-and-account-suspension.md.

const retentionDays = 180

func TestAdminPlacesAndReleasesAnEvidenceHold(t *testing.T) {
	p, host, mux := setupAdmin(t, "boss@example.com")
	host.sites.retention = retentionDays * 24 * time.Hour
	cookie, accID := adminUser(t, p, mux, "boss@example.com", "admin")
	instance(t, host, accID)
	acc, _ := p.accounts.ByEmail("boss@example.com")
	at := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	host.sites.SetLock(siteC, &ext.SiteLock{At: at, By: ext.LockByScanner, Reason: "held for review"})
	due := at.Add(retentionDays * 24 * time.Hour).Local().Format("2006-01-02")

	body := getAs(mux, "/account/admin", cookie).Body.String()
	if !strings.Contains(body, "purge due "+due) {
		t.Fatalf("the locked row does not show the purge date %s: %s", due, body)
	}
	if !strings.Contains(body, "?hold="+siteC) || strings.Contains(body, "?unhold="+siteC) {
		t.Error("an unheld lock does not offer Hold (or offers Release)")
	}

	// Keep turns the scanner's lock into the operator's; the clock stays.
	postAs(mux, "/account/admin/sites/"+siteC+"/lock", cookie, url.Values{"csrf": {p.csrf(acc)}, "reason": {"phishing"}})
	if l := host.sites.infos[siteC].Locked; !l.At.Equal(at) || l.By != ext.LockByAdmin {
		t.Fatalf("Keep moved the lock date: %+v", l)
	}

	// Step one renders the hold confirmation and changes nothing.
	body = getAs(mux, "/account/admin?hold="+siteC+"&filter=locked", cookie).Body.String()
	if !strings.Contains(body, `action="/account/admin/sites/`+siteC+`/hold?filter=locked"`) || !strings.Contains(body, "Yes, hold "+siteC) {
		t.Fatalf("hold step not rendered: %s", body)
	}
	if !strings.Contains(body, "180-day lock retention") || !strings.Contains(body, "purge due "+due) {
		t.Error("the hold step does not say what it holds off")
	}
	if host.sites.infos[siteC].Locked.Hold != nil {
		t.Fatal("step one placed the hold")
	}

	r := form(url.Values{"csrf": {p.csrf(acc)}})
	r.URL.Path, r.URL.RawQuery = "/account/admin/sites/"+siteC+"/hold", "filter=locked"
	r.AddCookie(cookie)
	w := serve(mux, r)
	if w.Code != http.StatusSeeOther || !strings.Contains(w.Header().Get("Location"), "flash=held") || !strings.Contains(w.Header().Get("Location"), "filter=locked") {
		t.Fatalf("hold = %d %q", w.Code, w.Header().Get("Location"))
	}
	l := host.sites.infos[siteC].Locked
	if l.Hold == nil || l.Hold.By != accID || l.Hold.At.IsZero() {
		t.Fatalf("hold = %+v", l.Hold)
	}
	body = getAs(mux, "/account/admin", cookie).Body.String()
	if !strings.Contains(body, "held (case open) since") || !strings.Contains(body, "by boss@example.com") {
		t.Errorf("the held row does not say so: %s", body)
	}
	if strings.Contains(body, "purge due") || !strings.Contains(body, "retention ends "+due) {
		t.Error("a held row shows a purge date, or not when its retention ends")
	}
	if !strings.Contains(body, "?unhold="+siteC) || strings.Contains(body, "?hold="+siteC) {
		t.Error("a held lock does not offer Release hold (or still offers Hold)")
	}

	// Release is a step too, and says what follows.
	body = getAs(mux, "/account/admin?unhold="+siteC, cookie).Body.String()
	if !strings.Contains(body, `action="/account/admin/sites/`+siteC+`/unhold"`) || !strings.Contains(body, "purged on "+due) {
		t.Fatalf("release step not rendered: %s", body)
	}
	w = postAs(mux, "/account/admin/sites/"+siteC+"/unhold", cookie, url.Values{"csrf": {p.csrf(acc)}})
	if w.Code != http.StatusSeeOther || !strings.Contains(w.Header().Get("Location"), "flash=unheld") {
		t.Fatalf("release = %d %q", w.Code, w.Header().Get("Location"))
	}
	if l := host.sites.infos[siteC].Locked; l.Hold != nil || l.PurgeAt == nil {
		t.Fatalf("released lock = %+v", l)
	}
}

// A lock whose date is already past the retention: releasing the hold
// means the next sweep purges the site, and the step says that.
func TestAdminReleaseStepWarnsOfAnImmediatePurge(t *testing.T) {
	p, host, mux := setupAdmin(t, "boss@example.com")
	host.sites.retention = retentionDays * 24 * time.Hour
	cookie, accID := adminUser(t, p, mux, "boss@example.com", "admin")
	instance(t, host, accID)
	host.sites.SetLock(siteC, &ext.SiteLock{At: time.Now().Add(-400 * 24 * time.Hour), By: ext.LockByAdmin})
	host.sites.SetHold(siteC, &ext.LockHold{At: time.Now(), By: store.HoldByCLI})

	body := getAs(mux, "/account/admin", cookie).Body.String()
	if !strings.Contains(body, "by the CLI") {
		t.Error("a CLI hold is not named as such")
	}
	body = getAs(mux, "/account/admin?unhold="+siteC, cookie).Body.String()
	if !strings.Contains(body, "the next sweep purges the site") {
		t.Errorf("the release step does not warn of the purge: %s", body)
	}
}

// With locks kept forever there is nothing for a hold to hold off: no
// purge date, no Hold button.
func TestAdminRetentionOff(t *testing.T) {
	p, host, mux := setupAdmin(t, "boss@example.com")
	cookie, accID := adminUser(t, p, mux, "boss@example.com", "admin")
	instance(t, host, accID)
	host.sites.SetLock(siteC, &ext.SiteLock{By: ext.LockByAdmin})
	body := getAs(mux, "/account/admin", cookie).Body.String()
	if !strings.Contains(body, "kept until unlocked") || strings.Contains(body, "?hold="+siteC) || strings.Contains(body, "purge due") {
		t.Errorf("retention 0: %s", body)
	}
	if body := getAs(mux, "/account/admin?lock="+siteA, cookie).Body.String(); !strings.Contains(body, "nothing is deleted until you unlock or delete it") {
		t.Error("the lock step promises a retention the instance does not have")
	}
}

func TestAdminHoldActionsRequireCSRFAnAdminAndALock(t *testing.T) {
	p, host, mux := setupAdmin(t, "boss@example.com")
	host.sites.retention = retentionDays * 24 * time.Hour
	adminCookie, accID := adminUser(t, p, mux, "boss@example.com", "admin")
	instance(t, host, accID)
	acc, _ := p.accounts.ByEmail("boss@example.com")
	host.sites.SetLock(siteC, &ext.SiteLock{By: ext.LockByAdmin})
	for _, path := range []string{"/account/admin/sites/" + siteC + "/hold", "/account/admin/sites/" + siteC + "/unhold"} {
		if w := postAs(mux, path, adminCookie, url.Values{}); w.Code != http.StatusForbidden {
			t.Errorf("%s without csrf = %d, want 403", path, w.Code)
		}
	}
	cookie, _ := adminUser(t, p, mux, "nobody@example.com", "free")
	nobody, _ := p.accounts.ByEmail("nobody@example.com")
	if w := postAs(mux, "/account/admin/sites/"+siteC+"/hold", cookie, url.Values{"csrf": {p.csrf(nobody)}}); w.Code != 404 {
		t.Errorf("non-admin hold = %d, want 404", w.Code)
	}
	if host.sites.infos[siteC].Locked.Hold != nil {
		t.Fatal("a refused request placed a hold")
	}
	// An unlocked site has nothing to hold.
	if w := postAs(mux, "/account/admin/sites/"+siteA+"/hold", adminCookie, url.Values{"csrf": {p.csrf(acc)}}); w.Code != http.StatusConflict {
		t.Errorf("hold on an unlocked site = %d, want 409", w.Code)
	}
}

// The account-deletion refusal stays while a site is locked, held or not;
// once the sweep has purged the site, the deletion goes through. Against
// the real store, through the shipping SiteService.
func TestAccountDeletionWorksOnceTheLockedSiteIsPurged(t *testing.T) {
	p, st := setupRealSites(t, "")
	st.SetLockRetention(retentionDays * 24 * time.Hour)
	acc, err := p.accounts.CreateLocal("owner@example.com", "$argon2$notreal", "free")
	if err != nil {
		t.Fatal(err)
	}
	site := paidSite(t, st, acc.ID)
	if err := p.accounts.LinkSite(acc, site.ViewID); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	st.SetLock(site, &store.SiteLock{At: now.Add(-(retentionDays + 1) * 24 * time.Hour), By: store.LockByAdmin})
	st.SetHold(site, &store.LockHold{By: store.HoldByCLI})

	if !p.refuseLockedDeletion(httptest.NewRecorder(), acc) {
		t.Fatal("the deletion of an account owning a held site was not refused")
	}
	if purged, _ := st.PurgeLocked(site, now); purged {
		t.Fatal("a held site was purged")
	}
	st.SetHold(site, nil)
	if !p.refuseLockedDeletion(httptest.NewRecorder(), acc) {
		t.Fatal("the deletion of an account owning a locked site was not refused")
	}
	if purged, err := st.PurgeLocked(site, now); err != nil || !purged {
		t.Fatalf("purge = %v, %v", purged, err)
	}
	if p.refuseLockedDeletion(httptest.NewRecorder(), acc) {
		t.Fatal("the account's deletion is still refused after its locked site was purged")
	}
}

// An unsuspension never ends a case: a suspension's lock the operator put
// an evidence hold on stays locked, as the operator's own, and the account
// still cannot be deleted around it.
func TestUnsuspensionKeepsAHeldSiteLocked(t *testing.T) {
	p, host, mux := setupStackInstance(t, testGDPRSecret)
	acc, _ := stackUser(t, p, host, suspendSubject, "subject@example.com")
	if w := orderSuspend(mux, suspendOrderBody(suspendSubject, true, "phishing")); w.Code != 200 {
		t.Fatalf("suspend = %d %s", w.Code, w.Body)
	}
	lockAt := host.sites.infos[siteA].Locked.At
	if err := host.sites.SetHold(siteA, &ext.LockHold{At: time.Now(), By: "acct-admin"}); err != nil {
		t.Fatal(err)
	}

	w := orderSuspend(mux, suspendOrderBody(suspendSubject, false, ""))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"sitesUnlocked":1`) {
		t.Fatalf("unsuspend = %d %s", w.Code, w.Body)
	}
	l := host.sites.infos[siteA].Locked
	if l == nil || l.By != ext.LockByAdmin || l.Hold == nil || !l.At.Equal(lockAt) {
		t.Fatalf("the held site after the unsuspension: %+v", l)
	}
	if host.sites.infos[siteB].Locked != nil {
		t.Error("the unheld suspension lock survived the unsuspension")
	}
	if !p.refuseLockedDeletion(httptest.NewRecorder(), acc) {
		t.Error("the account can be deleted around a held site")
	}
}
