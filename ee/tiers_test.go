//go:build ee

package ee

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/ittrail/sitebin/internal/ext"
)

const tiersJSON = `[
  {"id":"free","label":"Free","max_site_bytes":1000,"max_files":10,"max_sites":1,"webdav":false,"custom_domains":0,"max_expiry_days":7},
  {"id":"pro","label":"Pro","max_site_bytes":5000000,"max_files":500,"max_sites":50,"webdav":true,"custom_domains":5,"max_expiry_days":0,"price":{"stripe":"price_x"}}
]`

func setupTiers(t *testing.T) *provider {
	t.Helper()
	t.Setenv("SITEBIN_ACCOUNT_MODE", "tiers")
	t.Setenv("SITEBIN_TIERS", tiersJSON)
	t.Setenv("SITEBIN_DEFAULT_TIER", "free")
	p := newProvider()
	host := &fakeHost{dir: t.TempDir(), sites: &fakeSites{infos: map[string]ext.SiteInfo{}}}
	if err := p.Init(host); err != nil {
		t.Fatalf("Init: %v", err)
	}
	return p
}

func TestTierGrantCarriesCaps(t *testing.T) {
	p := setupTiers(t)
	acc, err := p.local.Signup("free@example.com", "password123", p.tierForNewAccount())
	if err != nil {
		t.Fatal(err)
	}
	if acc.Tier != "free" {
		t.Fatalf("new account tier = %q, want free", acc.Tier)
	}
	grant, err := p.grantForAccount(acc)
	if err != nil {
		t.Fatalf("grant: %v", err)
	}
	if grant.OwnerAccountID != acc.ID || grant.MaxSiteBytes != 1000 || grant.MaxFiles != 10 ||
		grant.MaxCustomDomain != 0 || grant.MaxExpiryDays != 7 || grant.WebDAV == nil || *grant.WebDAV {
		t.Errorf("free tier caps wrong: %+v", grant)
	}
}

func TestTierMaxSitesEnforced(t *testing.T) {
	p := setupTiers(t)
	acc, _ := p.local.Signup("cap@example.com", "password123", p.tierForNewAccount())

	// first site allowed
	if _, err := p.grantForAccount(acc); err != nil {
		t.Fatalf("first grant: %v", err)
	}
	// simulate owning one site (free tier max_sites=1)
	p.accounts.LinkSite(acc, "abcdefghijklmnopqrstuvwxyz")

	_, err := p.grantForAccount(acc)
	var ce *ext.CreateError
	if !errors.As(err, &ce) || ce.Status != 403 {
		t.Fatalf("expected 403 max-sites CreateError, got %v", err)
	}
}

func TestTierAnonymousUsesAnonTier(t *testing.T) {
	t.Setenv("SITEBIN_ACCOUNT_MODE", "tiers")
	t.Setenv("SITEBIN_TIERS", tiersJSON)
	t.Setenv("SITEBIN_DEFAULT_TIER", "free")
	t.Setenv("SITEBIN_ANON_TIER", "free")
	p := newProvider()
	if err := p.Init(&fakeHost{dir: t.TempDir(), sites: &fakeSites{infos: map[string]ext.SiteInfo{}}}); err != nil {
		t.Fatal(err)
	}
	grant, err := p.AuthorizeCreate(httptest.NewRequest("POST", "/api/sites", nil))
	if err != nil {
		t.Fatalf("anonymous with anon tier should be allowed: %v", err)
	}
	if grant.OwnerAccountID != "" || grant.MaxSiteBytes != 1000 {
		t.Errorf("anon grant wrong: %+v", grant)
	}
}

func TestTierAnonymousBlockedWithoutAnonTier(t *testing.T) {
	p := setupTiers(t) // no SITEBIN_ANON_TIER
	_, err := p.AuthorizeCreate(httptest.NewRequest("POST", "/api/sites", nil))
	var ce *ext.CreateError
	if !errors.As(err, &ce) || ce.Status != 401 {
		t.Fatalf("expected 401, got %v", err)
	}
}

func TestSelfSelectTier(t *testing.T) {
	t.Setenv("SITEBIN_ACCOUNT_MODE", "tiers")
	t.Setenv("SITEBIN_TIERS", tiersJSON)
	t.Setenv("SITEBIN_DEFAULT_TIER", "free")
	t.Setenv("SITEBIN_TIER_SELF_SELECT", "true")
	p := newProvider()
	if err := p.Init(&fakeHost{dir: t.TempDir(), sites: &fakeSites{infos: map[string]ext.SiteInfo{}}}); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	for pat, h := range p.PublicRoutes() {
		mux.Handle(pat, h)
	}
	// signup on free
	req := form(url.Values{"email": {"s@example.com"}, "password": {"password123"}})
	req.URL.Path = "/account/signup"
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	cookie := sessionCookie(t, w)
	acc, _ := p.accounts.ByEmail("s@example.com")

	// switching to the paid tier via self-select is refused (needs checkout)
	pay := form(url.Values{"csrf": {p.csrf(acc)}, "tier": {"pro"}})
	pay.URL.Path = "/account/tier"
	pay.AddCookie(cookie)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, pay)
	if w.Code != http.StatusForbidden {
		t.Fatalf("self-select to paid tier = %d, want 403", w.Code)
	}
}
