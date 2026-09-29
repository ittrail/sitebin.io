//go:build ee

package ee

import (
	"encoding/json"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/ittrail/sitebin.io/ee/account"
	"github.com/ittrail/sitebin.io/ee/session"
	"github.com/ittrail/sitebin.io/internal/ext"
)

// Sign-out used to revoke Sitebin's sessions and send the browser to
// /account/login, which on an SSO-only instance goes straight back into the
// identity provider -- whose own session nobody had ended, so it signed the
// same person in again before a page was drawn. The button looked dead and no
// other account could ever be chosen. See
// docs/superpowers/specs/2026-09-29-sign-out-ends-the-sso-session.md.

const signedOutURL = "http://sitebin.example/account/signed-out" // fakeHost.BaseURL + the page

// setupSSO is the hosted instance in miniature: SSO only, one generic
// provider, the test issuer.
func setupSSO(t *testing.T, ti *testIssuer, localAuth bool) (*provider, http.Handler) {
	t.Helper()
	t.Setenv("SITEBIN_ACCOUNT_MODE", "accounts")
	if !localAuth {
		t.Setenv("SITEBIN_LOCAL_AUTH", "false")
	}
	t.Setenv("SITEBIN_OAUTH_OIDC_ISSUER", ti.URL())
	t.Setenv("SITEBIN_OAUTH_OIDC_CLIENT_ID", "sitebin-app")
	t.Setenv("SITEBIN_OAUTH_OIDC_CLIENT_SECRET", "s3cret")
	t.Setenv("SITEBIN_OAUTH_OIDC_LABEL", "IT-Trail Login")
	p := newProvider()
	if err := p.Init(&fakeHost{dir: t.TempDir(), sites: &fakeSites{infos: map[string]ext.SiteInfo{}}}); err != nil {
		t.Fatal(err)
	}
	return p, serveMux(p)
}

// idToken is an ID token the test issuer signed for sitebin-app.
func idToken(t *testing.T, ti *testIssuer, edit func(map[string]any)) string {
	t.Helper()
	return ti.sign(t, func(c map[string]any) {
		c["aud"] = "sitebin-app"
		c["azp"] = "sitebin-app"
		c["typ"] = "ID"
		delete(c, "scope")
		if edit != nil {
			edit(c)
		}
	}, nil)
}

// logout posts the dashboard's Sign out form with the given cookies.
func logout(mux http.Handler, csrf string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	r := form(url.Values{"csrf": {csrf}})
	r.URL.Path = "/account/logout"
	for _, c := range cookies {
		if c != nil {
			r.AddCookie(c)
		}
	}
	return serve(mux, r)
}

var refreshRe = regexp.MustCompile(`http-equiv="refresh" content="0;url=([^"]+)"`)

// handoffTo is where a handoff page sends the browser. The dashboard's CSP
// says form-action 'self', which Chrome applies to the redirect a form POST
// is answered with, so leaving for the identity provider must be a page that
// navigates on its own -- a 303 there is dropped without a word.
func handoffTo(t *testing.T, w *httptest.ResponseRecorder) *url.URL {
	t.Helper()
	if w.Code != http.StatusOK {
		t.Fatalf("sign-out = %d %q, want 200 with a handoff page (%s)", w.Code, w.Header().Get("Location"), w.Body)
	}
	if loc := w.Header().Get("Location"); loc != "" {
		t.Fatalf("sign-out carries Location %q; a form-submission redirect off this origin is what the CSP blocks", loc)
	}
	m := refreshRe.FindStringSubmatch(w.Body.String())
	if m == nil {
		t.Fatalf("no refresh on the handoff page: %s", w.Body)
	}
	raw := html.UnescapeString(m[1])
	if !strings.Contains(html.UnescapeString(w.Body.String()), `href="`+raw+`"`) {
		t.Errorf("handoff page has no link to %s for a browser that does not follow the refresh", raw)
	}
	if csp := w.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "form-action 'self'") {
		t.Errorf("the handoff page relaxed the CSP (%q)", csp)
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

// cleared reports whether w deletes the cookie called name at path.
func cleared(w *httptest.ResponseRecorder, name, path string) bool {
	for _, c := range w.Result().Cookies() {
		if c.Name == name && c.Path == path && c.MaxAge < 0 && c.Value == "" {
			return true
		}
	}
	return false
}

// assertSignedOutHere: both cookies are cleared, and every session of the
// account is revoked -- the "sign out everywhere" that sign-out always was.
func assertSignedOutHere(t *testing.T, p *provider, mux http.Handler, w *httptest.ResponseRecorder, old *http.Cookie) {
	t.Helper()
	if !cleared(w, session.CookieName, "/") {
		t.Error("the session cookie was not cleared")
	}
	if !cleared(w, session.HintCookieName, "/account/logout") {
		t.Error("the logout-hint cookie was not cleared")
	}
	if old != nil {
		if g := getAs(mux, "/account", old); g.Code != http.StatusSeeOther || g.Header().Get("Location") != "/account/login" {
			t.Errorf("the old session still opens the dashboard: %d %q", g.Code, g.Header().Get("Location"))
		}
	}
}

func TestSignOutEndsTheProvidersSessionWithTheHint(t *testing.T) {
	for name, edit := range map[string]func(map[string]any){
		"fresh token": nil,
		// Keycloak 26.7 checks a hint's signature, not its expiry, and its
		// ID tokens live for minutes: an expired hint is still sent.
		"expired token": func(c map[string]any) { c["exp"] = time.Now().Add(-6 * time.Hour).Unix() },
	} {
		t.Run(name, func(t *testing.T) {
			ti := newTestIssuer(t)
			ti.endSession = "/protocol/openid-connect/logout"
			p, mux := setupSSO(t, ti, false)
			acc, cookie := oidcUser(t, p, testSubject, "person@example.com")
			tok := idToken(t, ti, edit)

			w := logout(mux, p.csrf(acc), cookie, p.sessions.HintCookie(tok))
			u := handoffTo(t, w)
			if got := u.Scheme + "://" + u.Host + u.Path; got != ti.URL()+"/protocol/openid-connect/logout" {
				t.Errorf("sign-out goes to %s, want the advertised end_session_endpoint", got)
			}
			q := u.Query()
			if q.Get("id_token_hint") != tok {
				t.Errorf("id_token_hint = %q, want the ID token of the sign-in", q.Get("id_token_hint"))
			}
			if q.Get("client_id") != "sitebin-app" {
				t.Errorf("client_id = %q", q.Get("client_id"))
			}
			if q.Get("post_logout_redirect_uri") != signedOutURL {
				t.Errorf("post_logout_redirect_uri = %q, want %s", q.Get("post_logout_redirect_uri"), signedOutURL)
			}
			assertSignedOutHere(t, p, mux, w, cookie)
		})
	}
}

// Without the hint -- a session from before this change, a hint that
// lapsed after a week, a token too large for a cookie -- the provider is
// asked by client_id alone. Keycloak may then show "Do you want to log
// out?", but the browser still comes back to the signed-out page.
func TestSignOutWithoutAHintAsksTheProviderByClientAlone(t *testing.T) {
	ti := newTestIssuer(t)
	ti.endSession = "/logout"
	p, mux := setupSSO(t, ti, false)
	acc, cookie := oidcUser(t, p, testSubject, "person@example.com")

	w := logout(mux, p.csrf(acc), cookie)
	q := handoffTo(t, w).Query()
	if q.Has("id_token_hint") {
		t.Errorf("a hint was sent that the browser never held: %q", q.Get("id_token_hint"))
	}
	if q.Get("client_id") != "sitebin-app" || q.Get("post_logout_redirect_uri") != signedOutURL {
		t.Errorf("logout query = %v", q)
	}
	assertSignedOutHere(t, p, mux, w, cookie)
}

// A hint left by somebody else's sign-in is not sent: Keycloak would ask
// about a session that is not this one, or refuse a foreign client outright.
func TestSignOutDropsAHintThatIsNotThisAccounts(t *testing.T) {
	ti := newTestIssuer(t)
	ti.endSession = "/logout"
	p, mux := setupSSO(t, ti, false)
	acc, cookie := oidcUser(t, p, testSubject, "person@example.com")
	other := idToken(t, ti, func(c map[string]any) { c["sub"] = "11111111-1111-4111-8111-111111111111" })

	q := handoffTo(t, logout(mux, p.csrf(acc), cookie, p.sessions.HintCookie(other))).Query()
	if q.Has("id_token_hint") || q.Get("client_id") != "sitebin-app" {
		t.Errorf("logout query = %v, want client_id alone", q)
	}
}

// A provider with no end_session_endpoint has no session to end from here,
// and a discovery that fails at sign-out must not keep anybody signed in:
// both go straight to the signed-out page, same-origin, so a 303 is fine.
func TestSignOutWithoutAProviderLogoutGoesStraightToTheSignedOutPage(t *testing.T) {
	t.Run("no end_session_endpoint", func(t *testing.T) {
		ti := newTestIssuer(t)
		p, mux := setupSSO(t, ti, false)
		acc, cookie := oidcUser(t, p, testSubject, "person@example.com")
		w := logout(mux, p.csrf(acc), cookie, p.sessions.HintCookie(idToken(t, ti, nil)))
		if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/account/signed-out" {
			t.Fatalf("sign-out = %d %q, want 303 /account/signed-out", w.Code, w.Header().Get("Location"))
		}
		assertSignedOutHere(t, p, mux, w, cookie)
	})
	t.Run("discovery down", func(t *testing.T) {
		ti := newTestIssuer(t)
		ti.endSession = "/logout"
		ti.down.Store(true)
		p, mux := setupSSO(t, ti, false)
		acc, cookie := oidcUser(t, p, testSubject, "person@example.com")
		w := logout(mux, p.csrf(acc), cookie)
		if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/account/signed-out" {
			t.Fatalf("sign-out = %d %q, want 303 /account/signed-out", w.Code, w.Header().Get("Location"))
		}
		assertSignedOutHere(t, p, mux, w, cookie)
	})
}

// Google and Microsoft signed in directly are never signed out at the
// provider -- that would end the person's whole Google session. No network.
func TestSignOutOfADirectGoogleAccountStaysHere(t *testing.T) {
	p := setupOAuth(t)
	mux := serveMux(p)
	acc, err := p.accounts.CreateOAuth(account.Google, "g-sub-1", "g@example.com", true, "")
	if err != nil {
		t.Fatal(err)
	}
	cookie := p.sessions.Cookie(acc.ID, acc.TokenVersion)
	w := logout(mux, p.csrf(acc), cookie)
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/account/signed-out" {
		t.Fatalf("sign-out = %d %q, want 303 /account/signed-out", w.Code, w.Header().Get("Location"))
	}
	assertSignedOutHere(t, p, mux, w, cookie)
	// The signed-out page offers Google again, asking which account.
	body := getAs(mux, "/account/signed-out", nil).Body.String()
	if !strings.Contains(body, `href="/account/auth/google?fresh=1"`) {
		t.Errorf("the signed-out page has no fresh Google sign-in: %s", body)
	}
}

// Local auth is unchanged but for the destination: no provider, sessions
// revoked, and the signed-out page leads to the password form, which renders
// there rather than redirecting anywhere.
func TestSignOutOfALocalAccount(t *testing.T) {
	p, _, mux := setupAccounts(t)
	acc, cookie := localUser(t, p, "local@example.com")
	w := logout(mux, p.csrf(acc), cookie)
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/account/signed-out" {
		t.Fatalf("sign-out = %d %q, want 303 /account/signed-out", w.Code, w.Header().Get("Location"))
	}
	assertSignedOutHere(t, p, mux, w, cookie)

	body := getAs(mux, "/account/signed-out", nil).Body.String()
	if !strings.Contains(body, `href="/account/login"`) {
		t.Errorf("the signed-out page does not lead to the password form: %s", body)
	}
	login := getAs(mux, "/account/login", nil)
	if login.Code != http.StatusOK || !strings.Contains(login.Body.String(), `type="password"`) {
		t.Errorf("/account/login = %d, want the form", login.Code)
	}
}

// A sign-out without a session -- a stale dashboard tab -- clears what the
// browser holds and ends at the signed-out page. There is no account to
// check a CSRF token against, so no provider hop is made on its behalf.
func TestSignOutWithoutASession(t *testing.T) {
	ti := newTestIssuer(t)
	ti.endSession = "/logout"
	p, mux := setupSSO(t, ti, false)
	w := logout(mux, "", p.sessions.HintCookie(idToken(t, ti, nil)))
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/account/signed-out" {
		t.Fatalf("sign-out = %d %q, want 303 /account/signed-out", w.Code, w.Header().Get("Location"))
	}
	assertSignedOutHere(t, p, mux, w, nil)
	if n := ti.discoveries.Load(); n != 0 {
		t.Errorf("a sign-out without a session reached the provider (%d discoveries)", n)
	}
}

// The signed-out page is where the old flow went wrong: it must never send
// the browser anywhere by itself. Its Sign in starts a FRESH request, which
// the provider answers with its login page even if an SSO session survived.
func TestSignedOutPageNeverRedirects(t *testing.T) {
	ti := newTestIssuer(t)
	p, mux := setupSSO(t, ti, false)
	w := getAs(mux, "/account/signed-out", nil)
	if w.Code != http.StatusOK || w.Header().Get("Location") != "" {
		t.Fatalf("signed-out page = %d %q, want 200 and no redirect", w.Code, w.Header().Get("Location"))
	}
	body := w.Body.String()
	if !strings.Contains(body, "You are signed out") {
		t.Error("the page does not say so")
	}
	if strings.Contains(body, "http-equiv") || strings.Contains(body, "<script") {
		t.Error("the signed-out page navigates by itself")
	}
	if !strings.Contains(body, `href="/account/auth/oidc?fresh=1"`) {
		t.Errorf("no fresh sign-in on the page: %s", body)
	}
	// /account/login redirects straight into the provider on this instance,
	// which is exactly the silent round trip this page exists to stop.
	if strings.Contains(body, `href="/account/login"`) {
		t.Error("the signed-out page links /account/login, which auto-redirects on an SSO-only instance")
	}
	if csp := w.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'self'") {
		t.Errorf("the signed-out page has no dashboard CSP: %q", csp)
	}
	// Still rendered, never redirected, for a visitor who is signed in.
	_, cookie := oidcUser(t, p, testSubject, "person@example.com")
	if w := getAs(mux, "/account/signed-out", cookie); w.Code != http.StatusOK || w.Header().Get("Location") != "" {
		t.Errorf("signed-out page with a session = %d %q", w.Code, w.Header().Get("Location"))
	}
}

// fresh=1 asks the provider who is signing in; without it single sign-on
// stays one click, and /account/login still goes straight in.
func TestFreshSignInAsksForTheLoginPage(t *testing.T) {
	ti := newTestIssuer(t)
	_, mux := setupSSO(t, ti, false)

	start := func(path string) url.Values {
		t.Helper()
		w := getAs(mux, path, nil)
		if w.Code != http.StatusSeeOther {
			t.Fatalf("%s = %d %s", path, w.Code, w.Body)
		}
		u, err := url.Parse(w.Header().Get("Location"))
		if err != nil || !strings.HasPrefix(u.String(), ti.URL()+"/auth") {
			t.Fatalf("%s -> %q, want the authorization endpoint", path, w.Header().Get("Location"))
		}
		return u.Query()
	}
	if q := start("/account/auth/oidc?fresh=1"); q.Get("prompt") != "login" {
		t.Errorf("fresh sign-in prompt = %q, want login", q.Get("prompt"))
	}
	if q := start("/account/auth/oidc"); q.Has("prompt") {
		t.Errorf("an ordinary sign-in carries prompt=%q; SSO from the hub must stay one click", q.Get("prompt"))
	}
	if w := getAs(mux, "/account/login", nil); w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/account/auth/oidc" {
		t.Errorf("/account/login = %d %q, want the unchanged one-click redirect", w.Code, w.Header().Get("Location"))
	}
}

// Where the login page renders -- an error page's "Back", an instance with a
// choice -- it keeps its one-click buttons and offers a different account.
func TestLoginPageOffersADifferentAccount(t *testing.T) {
	ti := newTestIssuer(t)
	_, mux := setupSSO(t, ti, false)
	body := getAs(mux, "/account/login?stay=1", nil).Body.String()
	if !strings.Contains(body, `href="/account/auth/oidc"`) {
		t.Error("the one-click button is gone")
	}
	if !strings.Contains(body, `href="/account/auth/oidc?fresh=1"`) || !strings.Contains(body, "different account") {
		t.Errorf("no way to a different account on the login page: %s", body)
	}
}

// The callback keeps the ID token for the sign-out, in the path-limited
// cookie; a provider with no end-session endpoint keeps nothing, and a local
// sign-in clears what an earlier one left.
func TestSignInKeepsTheHintForTheSignOut(t *testing.T) {
	signIn := func(t *testing.T, ti *testIssuer, mux http.Handler, p *provider) (*httptest.ResponseRecorder, string) {
		t.Helper()
		var tok string
		nonce := "nonce-1"
		ti.srv.Config.Handler.(*http.ServeMux).HandleFunc("POST /token", func(w http.ResponseWriter, r *http.Request) {
			tok = idToken(t, ti, func(c map[string]any) { c["nonce"] = nonce })
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"access_token": "at", "token_type": "Bearer", "expires_in": 60, "id_token": tok})
		})
		state := "state1"
		req := httptest.NewRequest("GET", "/account/auth/oidc/callback?code=c&state="+state, nil)
		req.AddCookie(&http.Cookie{Name: oauthCookie, Value: p.oauthSigner().Sign("oidc|"+state+"|"+nonce, time.Now(), 10*time.Minute)})
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/account" {
			t.Fatalf("callback = %d %q %s", w.Code, w.Header().Get("Location"), w.Body)
		}
		return w, tok
	}

	t.Run("kept", func(t *testing.T) {
		ti := newTestIssuer(t)
		ti.endSession = "/logout"
		p, mux := setupSSO(t, ti, false)
		w, tok := signIn(t, ti, mux, p)
		var hint *http.Cookie
		for _, c := range w.Result().Cookies() {
			if c.Name == session.HintCookieName {
				hint = c
			}
		}
		if hint == nil || hint.Value != tok {
			t.Fatalf("hint cookie = %+v, want the ID token", hint)
		}
		if hint.Path != "/account/logout" || !hint.HttpOnly || hint.SameSite != http.SameSiteLaxMode ||
			hint.MaxAge != int(session.DefaultTTL.Seconds()) || hint.Domain != "" {
			t.Errorf("hint cookie attributes = %+v", hint)
		}
		// And it is what the sign-out then sends.
		acc, err := p.accounts.ByOAuth(account.OIDCProv, testSubject)
		if err != nil {
			t.Fatal(err)
		}
		var sess *http.Cookie
		for _, c := range w.Result().Cookies() {
			if c.Name == session.CookieName {
				sess = c
			}
		}
		if q := handoffTo(t, logout(mux, p.csrf(acc), sess, hint)).Query(); q.Get("id_token_hint") != tok {
			t.Errorf("the sign-out did not send the kept hint: %v", q)
		}
	})
	t.Run("no end-session endpoint: nothing kept", func(t *testing.T) {
		ti := newTestIssuer(t)
		p, mux := setupSSO(t, ti, false)
		w, _ := signIn(t, ti, mux, p)
		if !cleared(w, session.HintCookieName, "/account/logout") {
			t.Error("a sign-in with no provider logout kept (or left) a hint")
		}
	})
	t.Run("local sign-in clears it", func(t *testing.T) {
		p, _, mux := setupAccounts(t)
		if _, err := p.local.Signup("pw@example.com", "password123", ""); err != nil {
			t.Fatal(err)
		}
		r := form(url.Values{"email": {"pw@example.com"}, "password": {"password123"}})
		r.URL.Path = "/account/login"
		w := serve(mux, r)
		if w.Code != http.StatusSeeOther {
			t.Fatalf("login = %d", w.Code)
		}
		if !cleared(w, session.HintCookieName, "/account/logout") {
			t.Error("a local sign-in left an earlier sign-in's hint in place")
		}
	})
}

// MCP never reads the browser session: a connector's OAuth token keeps
// working after its owner signs out of the dashboard, as it always has.
func TestSignOutLeavesMCPAlone(t *testing.T) {
	ti := newTestIssuer(t)
	ti.endSession = "/logout"
	p, _ := setupMCPOAuthInstance(t, ti, nil)
	mux := serveMux(p)
	acc, err := p.accounts.CreateOAuth(account.OIDCProv, testSubject, "agent-owner@example.com", true, "")
	if err != nil {
		t.Fatal(err)
	}
	cookie := p.sessions.Cookie(acc.ID, acc.TokenVersion)
	handoffTo(t, logout(mux, p.csrf(acc), cookie))
	if cred, ok := p.BearerCredential(bearerRequest(ti.sign(t, nil, nil), true)); !ok || cred.AccountID != acc.ID {
		t.Errorf("the MCP token stopped working after a browser sign-out: %+v %v", cred, ok)
	}
}
