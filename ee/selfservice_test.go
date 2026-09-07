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

// The hosted instance: PayGate sells, the stack's realm signs people in, and
// the stack can order erasures back. Local auth stays on so the same instance
// can show what a LOCAL account gets, which is the control.
func setupSelfService(t *testing.T, gdprSecret string) (*provider, *fakeHost, http.Handler) {
	t.Helper()
	t.Setenv("SITEBIN_ACCOUNT_MODE", "tiers")
	t.Setenv("SITEBIN_TIERS", stackTiersJSON)
	t.Setenv("SITEBIN_DEFAULT_TIER", "free")
	t.Setenv("SITEBIN_OAUTH_OIDC_ISSUER", "https://auth.example.com/realms/saas-stack")
	t.Setenv("SITEBIN_OAUTH_OIDC_CLIENT_ID", "sitebin-app")
	t.Setenv("SITEBIN_BILLING", "paygate")
	t.Setenv("SITEBIN_PAYGATE_URL", "https://paygate.saas-stack.example.com")
	t.Setenv("SITEBIN_PAYGATE_APP_ID", "sitebin")
	t.Setenv("SITEBIN_PAYGATE_API_KEY", "ssk_test_x")
	if gdprSecret != "" {
		t.Setenv("SITEBIN_STACK_GDPR_SECRET", gdprSecret)
	}
	p := newProvider()
	host := &fakeHost{dir: t.TempDir(), sites: &fakeSites{infos: map[string]ext.SiteInfo{}}}
	if err := p.Init(host); err != nil {
		t.Fatalf("Init: %v", err)
	}
	mux := http.NewServeMux()
	for pat, h := range p.PublicRoutes() {
		mux.Handle(pat, h)
	}
	return p, host, mux
}

func oidcUser(t *testing.T, p *provider, subject, email string) (*account.Account, *http.Cookie) {
	t.Helper()
	acc, err := p.accounts.CreateOAuth(account.OIDCProv, subject, email, true, "free")
	if err != nil {
		t.Fatal(err)
	}
	return acc, p.sessions.Cookie(acc.ID, acc.TokenVersion)
}

func localUser(t *testing.T, p *provider, email string) (*account.Account, *http.Cookie) {
	t.Helper()
	acc, err := p.local.Signup(email, "password123", "free")
	if err != nil {
		t.Fatal(err)
	}
	return acc, p.sessions.Cookie(acc.ID, acc.TokenVersion)
}

func postAs(mux http.Handler, path string, cookie *http.Cookie, v url.Values) *httptest.ResponseRecorder {
	r := form(v)
	r.URL.Path = path
	r.AddCookie(cookie)
	return serve(mux, r)
}

const (
	wantAccountURL = "https://auth.example.com/realms/saas-stack/account/?referrer=sitebin-app"
	wantPlanURL    = "https://auth.example.com/apps/sitebin/plan"
)

// Once a user is signed in, "manage my account" and "manage my plan" are
// LINKS to pages the stack hosts, not screens Sitebin builds.
func TestDashboardLinksTheStackConsoleAndPlanPage(t *testing.T) {
	p, _, mux := setupSelfService(t, testGDPRSecret)
	acc, cookie := oidcUser(t, p, "11111111-1111-4111-8111-111111111111", "stack@example.com")

	body := getAs(mux, "/account", cookie).Body.String()
	if !strings.Contains(body, `href="`+wantAccountURL+`"`) || !strings.Contains(body, "Manage account") {
		t.Errorf("the dashboard does not link the account console at %s", wantAccountURL)
	}
	if !strings.Contains(body, `action="/account/billing/portal"`) {
		t.Error("the dashboard has no way to the plan page")
	}

	w := postAs(mux, "/account/billing/portal", cookie, url.Values{"csrf": {p.csrf(acc)}})
	if w.Code != http.StatusSeeOther {
		t.Fatalf("portal = %d, want 303 (%s)", w.Code, w.Body)
	}
	if loc := w.Header().Get("Location"); loc != wantPlanURL {
		t.Errorf("portal redirects to %q, want the stack's hosted plan page %q", loc, wantPlanURL)
	}
	// And still nothing without the CSRF token.
	if w := postAs(mux, "/account/billing/portal", cookie, url.Values{}); w.Code != http.StatusForbidden {
		t.Errorf("portal without csrf = %d, want 403", w.Code)
	}
}

// A local account has no stack identity: no console to manage it in, and a
// portal that has nobody to show.
func TestLocalAccountGetsNoStackLinks(t *testing.T) {
	p, _, mux := setupSelfService(t, testGDPRSecret)
	acc, cookie := localUser(t, p, "local@example.com")

	body := getAs(mux, "/account", cookie).Body.String()
	if strings.Contains(body, "Manage account") || strings.Contains(body, wantAccountURL) {
		t.Error("a local account was offered the stack's account console")
	}
	w := postAs(mux, "/account/billing/portal", cookie, url.Values{"csrf": {p.csrf(acc)}})
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/account" {
		t.Errorf("portal for a local account = %d %q, want a redirect back to /account", w.Code, w.Header().Get("Location"))
	}
}

// A stack identity is deleted where it lives. The dashboard says so, the
// button is a link to the console, and a POST to the local route — the old
// form, a stale tab — goes there too instead of deleting half an account.
func TestStackAccountIsDeletedAtTheConsole(t *testing.T) {
	p, host, mux := setupSelfService(t, testGDPRSecret)
	acc, cookie := oidcUser(t, p, "11111111-1111-4111-8111-111111111111", "stack@example.com")
	host.sites.site("aaaaaaaaaaaaaaaaaaaaaaaaaa")
	p.accounts.LinkSite(acc, "aaaaaaaaaaaaaaaaaaaaaaaaaa")

	body := getAs(mux, "/account", cookie).Body.String()
	if !strings.Contains(body, "Delete account in the account console") {
		t.Error("the danger zone does not send a stack user to the console")
	}
	if strings.Contains(body, `action="/account/delete"`) {
		t.Error("the danger zone still offers the local delete form to a stack user")
	}

	for _, path := range []string{"/account/delete", "/account/delete/confirm"} {
		w := postAs(mux, path, cookie, url.Values{"csrf": {p.csrf(acc)}})
		if w.Code != http.StatusSeeOther || w.Header().Get("Location") != wantAccountURL {
			t.Fatalf("%s for a stack user = %d %q, want a 303 to the console", path, w.Code, w.Header().Get("Location"))
		}
	}
	if _, err := p.accounts.ByID(acc.ID); err != nil {
		t.Error("the account was deleted locally, leaving the stack identity behind")
	}
	if len(host.sites.deleted) != 0 {
		t.Error("sites were deleted locally")
	}
}

// Without a GDPR secret nothing will ever order the local erasure, so the
// console cannot be where deletion goes — even for a stack user.
func TestStackAccountDeletesLocallyWhenTheStackCannotOrderIt(t *testing.T) {
	p, host, mux := setupSelfService(t, "")
	acc, cookie := oidcUser(t, p, "11111111-1111-4111-8111-111111111111", "stack@example.com")
	host.sites.site("aaaaaaaaaaaaaaaaaaaaaaaaaa")
	p.accounts.LinkSite(acc, "aaaaaaaaaaaaaaaaaaaaaaaaaa")

	body := getAs(mux, "/account", cookie).Body.String()
	if !strings.Contains(body, `action="/account/delete"`) {
		t.Error("the local delete form is missing although nothing else can delete this account")
	}
	// The console link itself is still right: password and sessions live there.
	if !strings.Contains(body, wantAccountURL) {
		t.Error("the account console link is missing")
	}
	w := postAs(mux, "/account/delete/confirm", cookie, url.Values{"csrf": {p.csrf(acc)}})
	if w.Code != 200 || !strings.Contains(w.Body.String(), "Account deleted") {
		t.Fatalf("local delete = %d (%s)", w.Code, w.Body)
	}
	if _, err := p.accounts.ByID(acc.ID); err == nil {
		t.Error("the account survived")
	}
	if len(host.sites.deleted) != 1 {
		t.Errorf("sites deleted = %v", host.sites.deleted)
	}
}

// A local account on the same instance keeps local deletion: the stack has
// never heard of it.
func TestLocalAccountDeletesLocallyOnAStackInstance(t *testing.T) {
	p, host, mux := setupSelfService(t, testGDPRSecret)
	acc, cookie := localUser(t, p, "local@example.com")
	host.sites.site("aaaaaaaaaaaaaaaaaaaaaaaaaa")
	p.accounts.LinkSite(acc, "aaaaaaaaaaaaaaaaaaaaaaaaaa")
	// And a marker for a site that is already gone must not block it.
	p.accounts.LinkSite(acc, "bbbbbbbbbbbbbbbbbbbbbbbbbb")

	w := postAs(mux, "/account/delete/confirm", cookie, url.Values{"csrf": {p.csrf(acc)}})
	if w.Code != 200 || !strings.Contains(w.Body.String(), "Account deleted") {
		t.Fatalf("local delete = %d (%s)", w.Code, w.Body)
	}
	if _, err := p.accounts.ByID(acc.ID); err == nil {
		t.Error("the account survived")
	}
	if len(host.sites.deleted) != 1 || host.sites.deleted[0] != "aaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Errorf("sites deleted = %v", host.sites.deleted)
	}
}
