//go:build ee

package ee

import (
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/ittrail/sitebin.io/ee/session"
)

// Sessions are stateless HMAC cookies, so "sign out" used to be nothing but a
// Set-Cookie the browser was asked to honour: a stolen cookie stayed valid for
// its full lifetime whatever the owner did. Logout now bumps the account's
// token version, which every cookie is checked against, so every session of
// the account ends at once -- and the lifetime is a week, renewed on use.

func TestLogoutRevokesEverySessionOfTheAccount(t *testing.T) {
	p, _, mux := setupAccounts(t)
	acc, cookie := localUser(t, p, "two@example.com")
	other := p.sessions.Cookie(acc.ID, acc.TokenVersion) // a second device

	if w := getAs(mux, "/account", other); w.Code != http.StatusOK {
		t.Fatalf("second session before logout = %d", w.Code)
	}
	w := postAs(mux, "/account/logout", cookie, url.Values{"csrf": {p.csrf(acc)}})
	if w.Code != http.StatusSeeOther {
		t.Fatalf("logout = %d", w.Code)
	}
	for name, c := range map[string]*http.Cookie{"the signed-out session": cookie, "the other device's session": other} {
		if w := getAs(mux, "/account", c); w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/account/login" {
			t.Errorf("%s still opens the dashboard after logout: %d %q", name, w.Code, w.Header().Get("Location"))
		}
	}
}

func TestLogoutNeedsTheCSRFToken(t *testing.T) {
	p, _, mux := setupAccounts(t)
	_, cookie := localUser(t, p, "csrf@example.com")
	if w := postAs(mux, "/account/logout", cookie, url.Values{}); w.Code != http.StatusForbidden {
		t.Fatalf("logout without csrf = %d, want 403: a cross-site form must not be able to sign people out", w.Code)
	}
	if w := getAs(mux, "/account", cookie); w.Code != http.StatusOK {
		t.Error("the session was ended by a request without the token")
	}
}

// A visit renews the cookie, so an active user is never signed out mid-week
// and an abandoned session lapses after one.
func TestDashboardRenewsTheSession(t *testing.T) {
	p, _, mux := setupAccounts(t)
	_, cookie := localUser(t, p, "slide@example.com")
	w := getAs(mux, "/account", cookie)
	renewed := false
	for _, c := range w.Result().Cookies() {
		if c.Name == session.CookieName && c.Value != "" && c.MaxAge > 0 {
			renewed = true
		}
	}
	if !renewed {
		t.Error("the dashboard did not re-issue the session cookie")
	}
	if session.DefaultTTL != 7*24*time.Hour {
		t.Errorf("session lifetime = %v, want one week", session.DefaultTTL)
	}
}
