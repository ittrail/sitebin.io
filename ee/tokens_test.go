//go:build ee

package ee

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/ittrail/sitebin.io/internal/ext"
)

// The secret is shown once and never stored. If it could be read back, the
// whole scheme would be a password file.
func TestTokenSecretIsShownOnceAndNotStored(t *testing.T) {
	p, _, mux := setupAccounts(t)
	cookie := signedUpUser(t, mux)
	acc, _ := p.accounts.ByEmail("user@example.com")

	req := form(url.Values{"csrf": {p.csrf(acc)}, "name": {"build agent"}})
	req.URL.Path = "/account/tokens"
	req.AddCookie(cookie)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("create token = %d (%s)", w.Code, w.Body)
	}
	body := w.Body.String()
	i := strings.Index(body, "sbp_")
	if i < 0 {
		t.Fatal("the new token was not shown to its owner")
	}
	secret := body[i : i+44]

	// it works
	if _, ok := p.accounts.ByToken(secret); !ok {
		t.Fatal("the token just issued does not authenticate")
	}
	// and the dashboard never shows it again
	dash := httptest.NewRequest("GET", "/account", nil)
	dash.AddCookie(cookie)
	w2 := httptest.NewRecorder()
	mux.ServeHTTP(w2, dash)
	if strings.Contains(w2.Body.String(), secret) {
		t.Error("the dashboard re-displayed the secret; only its prefix may appear")
	}
	// Assert on something only a rendered ROW can produce: its revoke form.
	// An earlier version looked for the token's name, which also appears in the
	// input's placeholder — so it passed while the list was never filled at all.
	dashBody := w2.Body.String()
	if !strings.Contains(dashBody, "/account/tokens/") || !strings.Contains(dashBody, "/delete") {
		t.Fatalf("no token row rendered: the list is empty even though a token exists")
	}
	if !strings.Contains(dashBody, "build agent") {
		t.Error("the token's name is missing from the list")
	}
	if !strings.Contains(dashBody, secret[:10]) {
		t.Error("the row should show the token's prefix so tokens can be told apart")
	}
}

func TestTokenRevocationTakesEffect(t *testing.T) {
	p, _, mux := setupAccounts(t)
	cookie := signedUpUser(t, mux)
	acc, _ := p.accounts.ByEmail("user@example.com")

	tok, secret, err := p.accounts.CreateToken(acc, "throwaway")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := p.accounts.ByToken(secret); !ok {
		t.Fatal("precondition: token should work")
	}

	req := form(url.Values{"csrf": {p.csrf(acc)}})
	req.URL.Path = "/account/tokens/" + tok.ID + "/delete"
	req.AddCookie(cookie)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("revoke = %d, want 303", w.Code)
	}
	if _, ok := p.accounts.ByToken(secret); ok {
		t.Fatal("a revoked token still authenticates")
	}
}

// A token is a script credential for sites, not a session. It must not open the
// dashboard, or it could read a CSRF token and go on to delete the account.
func TestTokenIsNotASessionSubstitute(t *testing.T) {
	p, _, mux := setupAccounts(t)
	signedUpUser(t, mux)
	acc, _ := p.accounts.ByEmail("user@example.com")
	_, secret, err := p.accounts.CreateToken(acc, "")
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("GET", "/account", nil)
	req.Header.Set("Authorization", "Bearer "+secret)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code == 200 {
		t.Fatal("an API token opened the dashboard; it must only reach the site API")
	}
}

// AuthorizeCreate is the other half: a script with a token creates sites owned
// by that account, without a browser session.
func TestTokenCreatesOwnedSites(t *testing.T) {
	p, _, mux := setupAccounts(t)
	signedUpUser(t, mux)
	acc, _ := p.accounts.ByEmail("user@example.com")
	_, secret, err := p.accounts.CreateToken(acc, "ci")
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("POST", "/api/sites", nil)
	req.Header.Set("Authorization", "Bearer "+secret)
	grant, err := p.AuthorizeCreate(req)
	if err != nil {
		t.Fatalf("AuthorizeCreate with a token: %v", err)
	}
	if grant.OwnerAccountID != acc.ID {
		t.Fatalf("grant owner = %q, want %q", grant.OwnerAccountID, acc.ID)
	}
	var _ ext.CreateGrant = grant
}

func TestTokenCapIsEnforced(t *testing.T) {
	p, _, mux := setupAccounts(t)
	signedUpUser(t, mux)
	acc, _ := p.accounts.ByEmail("user@example.com")
	for i := 0; i < 25; i++ {
		if _, _, err := p.accounts.CreateToken(acc, ""); err != nil {
			t.Fatalf("token %d: %v", i, err)
		}
	}
	if _, _, err := p.accounts.CreateToken(acc, ""); err == nil {
		t.Fatal("the per-account cap is not enforced")
	}
}

func signedUpUser(t *testing.T, mux http.Handler) *http.Cookie {
	t.Helper()
	req := form(url.Values{"email": {"user@example.com"}, "password": {"password123"}})
	req.URL.Path = "/account/signup"
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("signup = %d (%s)", w.Code, w.Body)
	}
	return sessionCookie(t, w)
}

// The copy button is the one script the dashboard carries, and the CSP admits
// it by hash. If the template's copy drifts from the constant the hash covers,
// the browser silently refuses to run it — so assert they are the same text.
func TestCopyScriptIsAdmittedByItsOwnHash(t *testing.T) {
	p, _, mux := setupAccounts(t)
	cookie := signedUpUser(t, mux)
	acc, _ := p.accounts.ByEmail("user@example.com")

	req := form(url.Values{"csrf": {p.csrf(acc)}})
	req.URL.Path = "/account/tokens"
	req.AddCookie(cookie)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if !strings.Contains(w.Body.String(), copyScript) {
		t.Fatal("the page does not carry the exact script the CSP hash covers")
	}
	csp := w.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, copyScriptCSP) {
		t.Fatalf("CSP %q does not admit the copy script hash %s", csp, copyScriptCSP)
	}
	// Only script-src matters here; style-src legitimately carries
	// 'unsafe-inline' for the pages' own inline CSS.
	var scriptSrc string
	for _, d := range strings.Split(csp, ";") {
		if d = strings.TrimSpace(d); strings.HasPrefix(d, "script-src") {
			scriptSrc = d
		}
	}
	if strings.Contains(scriptSrc, "unsafe-inline") {
		t.Errorf("the copy button must not cost us 'unsafe-inline': %q", scriptSrc)
	}
	if !strings.Contains(w.Body.String(), `id="copybtn"`) {
		t.Error("no copy button rendered")
	}
}
