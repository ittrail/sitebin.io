//go:build ee

package ee

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/ittrail/sitebin.io/ee/account"
	"github.com/ittrail/sitebin.io/internal/ext"
)

// The provider is the core's source for "whose browser session is this", so a
// signed-in owner can manage their sites on the edit page without the edit
// password. It answers exactly what the dashboard itself trusts.
func TestSessionAccountIsTheDashboardsOwnAnswer(t *testing.T) {
	p, _, mux := setupAccounts(t)
	var sa ext.SessionAccounts = p // the core asserts this interface

	req := form(url.Values{"email": {"owner@example.com"}, "password": {"password123"}})
	req.URL.Path = "/account/signup"
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("signup = %d (%s)", w.Code, w.Body)
	}
	cookie := sessionCookie(t, w)
	acc, err := p.accounts.ByEmail("owner@example.com")
	if err != nil {
		t.Fatal(err)
	}

	withCookie := func(c *http.Cookie) *http.Request {
		r := httptest.NewRequest("GET", "/api/sites/x", nil)
		if c != nil {
			r.AddCookie(c)
		}
		return r
	}
	if id, ok := sa.SessionAccount(withCookie(cookie)); !ok || id != acc.ID {
		t.Fatalf("SessionAccount = %q, %v; want %q", id, ok, acc.ID)
	}
	if _, ok := sa.SessionAccount(withCookie(nil)); ok {
		t.Error("no cookie resolved to an account")
	}
	forged := *cookie
	forged.Value = "not-a-signed-session"
	if _, ok := sa.SessionAccount(withCookie(&forged)); ok {
		t.Error("an unsigned cookie resolved to an account")
	}

	// Signing out bumps the token version: the old cookie must stop opening
	// sites at once, as it stops opening the dashboard.
	if err := p.accounts.Update(acc, func(a *account.Account) error { a.TokenVersion++; return nil }); err != nil {
		t.Fatal(err)
	}
	if _, ok := sa.SessionAccount(withCookie(cookie)); ok {
		t.Error("a signed-out session still resolves to the account")
	}
}

// A bearer token is not a session: the dashboard refuses tokens, and so does
// this lookup, or a script could present one where only a browser belongs.
func TestSessionAccountIgnoresBearerTokens(t *testing.T) {
	p, _, _ := setupAccounts(t)
	r := httptest.NewRequest("GET", "/api/sites/x", nil)
	r.Header.Set("Authorization", "Bearer sbp_whatever")
	if _, ok := p.SessionAccount(r); ok {
		t.Error("a bearer header resolved as a session")
	}
}
