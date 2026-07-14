//go:build ee

package ee

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ittrail/sitebin/ee/account"
	"github.com/ittrail/sitebin/ee/authn"
	"github.com/ittrail/sitebin/ee/eeconfig"
	"github.com/ittrail/sitebin/internal/ext"
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
	collide := authn.Identity{Provider: account.Google, Subject: "sub-2", Email: "taken@example.com"}
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

func serveMux(p *provider) *http.ServeMux {
	m := http.NewServeMux()
	for pat, h := range p.PublicRoutes() {
		m.Handle(pat, h)
	}
	return m
}
