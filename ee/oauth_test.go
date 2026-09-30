//go:build ee

package ee

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ittrail/sitebin.io/ee/account"
	"github.com/ittrail/sitebin.io/ee/authn"
	"github.com/ittrail/sitebin.io/ee/eeconfig"
	"github.com/ittrail/sitebin.io/internal/ext"
)

func setupOAuth(t *testing.T) *provider {
	t.Helper()
	t.Setenv("SITEBIN_ACCOUNT_MODE", "accounts")
	t.Setenv("SITEBIN_OAUTH_GOOGLE_CLIENT_ID", "gid")
	t.Setenv("SITEBIN_OAUTH_GOOGLE_CLIENT_SECRET", "gsecret")
	p := newProvider()
	if err := p.Init(&fakeHost{dir: t.TempDir(), sites: &fakeSites{infos: map[string]ext.SiteInfo{}}}); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestOIDCProviderRegistry(t *testing.T) {
	cfg, _ := eeconfig.Load(func(k string) string {
		return map[string]string{
			"SITEBIN_ACCOUNT_MODE":              "accounts",
			"SITEBIN_OAUTH_GOOGLE_CLIENT_ID":    "gid",
			"SITEBIN_OAUTH_MICROSOFT_CLIENT_ID": "mid",
		}[k]
	}, func(string) ([]byte, error) { return nil, nil })
	m := authn.NewOIDC(cfg, "https://sitebin.example")
	if !m.Configured(account.Google) || !m.Configured(account.Microsoft) {
		t.Fatal("providers not configured")
	}
	if len(m.Providers()) != 2 {
		t.Errorf("providers = %v", m.Providers())
	}
}

func TestOAuthButtonsShownOnLogin(t *testing.T) {
	p := setupOAuth(t)
	mux := serveMux(p)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/account/login", nil))
	if !strings.Contains(w.Body.String(), "/account/auth/google") {
		t.Error("google button missing from login page")
	}
}

func TestLinkOrCreateOAuth(t *testing.T) {
	p := setupOAuth(t)
	id := authn.Identity{Provider: account.Google, Subject: "sub-1", Email: "u@example.com", EmailVerified: true}

	// first time → new account
	acc, err := p.linkOrCreateOAuth(id)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if acc.Provider != account.Google {
		t.Errorf("provider = %q", acc.Provider)
	}
	// second time → same account (login)
	acc2, err := p.linkOrCreateOAuth(id)
	if err != nil || acc2.ID != acc.ID {
		t.Fatalf("relogin: %v, %q vs %q", err, acc2.ID, acc.ID)
	}
	// a local account with the same email collides
	if _, err := p.local.Signup("taken@example.com", "password123", ""); err != nil {
		t.Fatal(err)
	}
	collide := authn.Identity{Provider: account.Google, Subject: "sub-2", Email: "taken@example.com", EmailVerified: true}
	if _, err := p.linkOrCreateOAuth(collide); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("email collision should error, got %v", err)
	}
}

func TestOAuthCallbackRejectsBadState(t *testing.T) {
	p := setupOAuth(t)
	mux := serveMux(p)

	// no oauth cookie → error page (before any network call)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/account/auth/google/callback?code=x&state=y", nil))
	if !strings.Contains(w.Body.String(), "Sign-in failed") {
		t.Fatalf("missing-cookie callback = %d, body missing error", w.Code)
	}
}

// The sign-in state has to outlive everything that runs while a person sits
// on the identity provider's login form: the stack's Keycloak keeps that form
// alive for 30 minutes and the stack's consent gate parks the flow for an
// hour. At 10 minutes, a person who took a quarter of an hour to find their
// password came back to "The sign-in session expired" although every other
// part of the sign-in was still valid.
func TestOAuthStateOutlivesTheStacksSignInFlow(t *testing.T) {
	p := setupOAuth(t)
	mux := serveMux(p)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/account/auth/google", nil))
	if w.Code != http.StatusSeeOther {
		t.Fatalf("start = %d, want 303", w.Code)
	}
	var state *http.Cookie
	for _, c := range w.Result().Cookies() {
		if c.Name == oauthCookie {
			state = c
		}
	}
	if state == nil {
		t.Fatal("no state cookie set")
	}
	const stackFlow = time.Hour
	if time.Duration(state.MaxAge)*time.Second <= stackFlow {
		t.Errorf("state cookie Max-Age = %ds, want longer than the stack's %v flow", state.MaxAge, stackFlow)
	}
	if _, ok := p.oauthSigner().Parse(state.Value, time.Now().Add(stackFlow+10*time.Minute)); !ok {
		t.Error("the signed state is refused after the stack's flow lifetime; it must outlive it")
	}
}

func serveMux(p *provider) *http.ServeMux {
	m := http.NewServeMux()
	for pat, h := range p.PublicRoutes() {
		m.Handle(pat, h)
	}
	return m
}

func TestGenericOIDCProvider(t *testing.T) {
	cfg, err := eeconfig.Load(func(k string) string {
		return map[string]string{
			"SITEBIN_ACCOUNT_MODE":             "accounts",
			"SITEBIN_OAUTH_OIDC_ISSUER":        "https://auth.stack.example/api/v1/sitebin",
			"SITEBIN_OAUTH_OIDC_CLIENT_ID":     "sitebin",
			"SITEBIN_OAUTH_OIDC_CLIENT_SECRET": "s3cret",
			"SITEBIN_OAUTH_OIDC_LABEL":         "IT-Trail SSO",
		}[k]
	}, func(string) ([]byte, error) { return nil, nil })
	if err != nil {
		t.Fatal(err)
	}
	m := authn.NewOIDC(cfg, "https://sitebin.example")
	if !m.Configured(account.OIDCProv) {
		t.Fatal("generic oidc provider not configured")
	}
}

func TestGenericOIDCLoginButtonLabel(t *testing.T) {
	t.Setenv("SITEBIN_ACCOUNT_MODE", "accounts")
	t.Setenv("SITEBIN_OAUTH_OIDC_ISSUER", "https://auth.stack.example/api/v1/sitebin")
	t.Setenv("SITEBIN_OAUTH_OIDC_CLIENT_ID", "sitebin")
	t.Setenv("SITEBIN_OAUTH_OIDC_LABEL", "IT-Trail SSO")
	p := newProvider()
	if err := p.Init(&fakeHost{dir: t.TempDir(), sites: &fakeSites{infos: map[string]ext.SiteInfo{}}}); err != nil {
		t.Fatal(err)
	}
	mux := serveMux(p)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/account/login", nil))
	body := w.Body.String()
	if !strings.Contains(body, "/account/auth/oidc") {
		t.Error("oidc button missing from login page")
	}
	if !strings.Contains(body, "IT-Trail SSO") {
		t.Error("configured label missing from login page")
	}
}

func setupSSOOnly(t *testing.T) *provider {
	t.Helper()
	t.Setenv("SITEBIN_ACCOUNT_MODE", "accounts")
	t.Setenv("SITEBIN_LOCAL_AUTH", "false")
	t.Setenv("SITEBIN_OAUTH_OIDC_ISSUER", "https://auth.stack.example/api/v1/sitebin")
	t.Setenv("SITEBIN_OAUTH_OIDC_CLIENT_ID", "sitebin")
	t.Setenv("SITEBIN_OAUTH_OIDC_LABEL", "IT-Trail Login")
	p := newProvider()
	if err := p.Init(&fakeHost{dir: t.TempDir(), sites: &fakeSites{infos: map[string]ext.SiteInfo{}}}); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestSSOOnlyLoginRedirectsToProvider(t *testing.T) {
	p := setupSSOOnly(t)
	mux := serveMux(p)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/account/login", nil))
	if w.Code != 303 || w.Header().Get("Location") != "/account/auth/oidc" {
		t.Fatalf("login = %d -> %q, want 303 -> /account/auth/oidc", w.Code, w.Header().Get("Location"))
	}
	// ?stay=1 renders the page (no redirect loop from OAuth error pages),
	// without the email/password form
	w2 := httptest.NewRecorder()
	mux.ServeHTTP(w2, httptest.NewRequest("GET", "/account/login?stay=1", nil))
	if w2.Code != 200 {
		t.Fatalf("stay page = %d", w2.Code)
	}
	body := w2.Body.String()
	if strings.Contains(body, "type=\"password\"") || strings.Contains(body, "Create an account") {
		t.Error("local login form should be hidden in SSO-only mode")
	}
	if !strings.Contains(body, "IT-Trail Login") {
		t.Error("SSO button missing")
	}
}

func TestSSOOnlyDisablesLocalPosts(t *testing.T) {
	p := setupSSOOnly(t)
	mux := serveMux(p)
	for _, path := range []string{"/account/login", "/account/signup"} {
		w := httptest.NewRecorder()
		req := httptest.NewRequest("POST", path, strings.NewReader("email=a@b.c&password=longenough1"))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		mux.ServeHTTP(w, req)
		if w.Code != 404 {
			t.Errorf("POST %s = %d, want 404", path, w.Code)
		}
	}
	// signup page redirects into the provider too
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/account/signup", nil))
	if w.Code != 303 || w.Header().Get("Location") != "/account/auth/oidc" {
		t.Errorf("signup = %d -> %q", w.Code, w.Header().Get("Location"))
	}
}
