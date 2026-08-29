//go:build ee

package ee

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/ittrail/sitebin.io/ee/account"
	"github.com/ittrail/sitebin.io/internal/cleanup"
	"github.com/ittrail/sitebin.io/internal/config"
	"github.com/ittrail/sitebin.io/internal/ext"
	"github.com/ittrail/sitebin.io/internal/httpapi"
	"github.com/ittrail/sitebin.io/internal/store"
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
		grant.MaxCustomDomain == nil || *grant.MaxCustomDomain != 0 || grant.MaxExpiryDays != 7 ||
		grant.WebDAV == nil || *grant.WebDAV {
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
	p.host.Sites().(*fakeSites).site("abcdefghijklmnopqrstuvwxyz")

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

	// a self-select that goes through restamps the account's sites directly
	// (handleSelectTier just wrote acc.Tier, so syncTier would be a no-op)
	if err := p.accounts.LinkSite(acc, "abcdefghijklmnopqrstuvwxyz"); err != nil {
		t.Fatal(err)
	}
	p.host.Sites().(*fakeSites).site("abcdefghijklmnopqrstuvwxyz")
	free := form(url.Values{"csrf": {p.csrf(acc)}, "tier": {"free"}})
	free.URL.Path = "/account/tier"
	free.AddCookie(cookie)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, free)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("self-select to free tier = %d, want redirect", w.Code)
	}
	sites := p.host.Sites().(*fakeSites)
	g, ok := sites.quotas["abcdefghijklmnopqrstuvwxyz"]
	if !ok || g.MaxSiteBytes != 1000 || g.MaxExpiryDays != 7 {
		t.Fatalf("site not restamped by self-select: %+v", g)
	}
}

// ---- PayGate as tier source ----

func pgStub(t *testing.T, tier, status string, code int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if code != 200 {
			w.WriteHeader(code)
			return
		}
		w.Write([]byte(`{"data":{"tier":"` + tier + `","status":"` + status + `"}}`))
	}))
}

func setupPayGate(t *testing.T, srvURL string) *provider {
	t.Helper()
	t.Setenv("SITEBIN_ACCOUNT_MODE", "tiers")
	t.Setenv("SITEBIN_TIERS", tiersJSON)
	t.Setenv("SITEBIN_DEFAULT_TIER", "free")
	t.Setenv("SITEBIN_PAYGATE_URL", srvURL)
	t.Setenv("SITEBIN_PAYGATE_APP_ID", "sitebin")
	t.Setenv("SITEBIN_PAYGATE_API_KEY", "ssk_test_k")
	t.Setenv("SITEBIN_OAUTH_OIDC_ISSUER", "https://auth.stack.example/api/v1/sitebin")
	t.Setenv("SITEBIN_OAUTH_OIDC_CLIENT_ID", "sitebin")
	p := newProvider()
	host := &fakeHost{dir: t.TempDir(), sites: &fakeSites{infos: map[string]ext.SiteInfo{}}}
	if err := p.Init(host); err != nil {
		t.Fatalf("Init: %v", err)
	}
	return p
}

func TestPayGateTierGovernsOIDCAccount(t *testing.T) {
	srv := pgStub(t, "pro", "active", 200)
	defer srv.Close()
	p := setupPayGate(t, srv.URL)
	acc, err := p.accounts.CreateOAuth(account.OIDCProv, "stack-user-1", "u@example.com", "free")
	if err != nil {
		t.Fatal(err)
	}
	grant, err := p.grantForAccount(acc)
	if err != nil {
		t.Fatal(err)
	}
	if grant.MaxSiteBytes != 5000000 || grant.MaxCustomDomain == nil || *grant.MaxCustomDomain != 5 {
		t.Errorf("grant should carry pro caps from PayGate, got %+v", grant)
	}
}

func TestPayGateFallsBackToStoredTier(t *testing.T) {
	srv := pgStub(t, "", "", 500)
	defer srv.Close()
	p := setupPayGate(t, srv.URL)
	acc, err := p.accounts.CreateOAuth(account.OIDCProv, "stack-user-2", "u2@example.com", "free")
	if err != nil {
		t.Fatal(err)
	}
	grant, err := p.grantForAccount(acc)
	if err != nil {
		t.Fatal(err)
	}
	if grant.MaxSiteBytes != 1000 {
		t.Errorf("PayGate outage should fall back to stored free tier, got %+v", grant)
	}
}

func TestPayGateIgnoresNonOIDCAccounts(t *testing.T) {
	srv := pgStub(t, "pro", "active", 200)
	defer srv.Close()
	p := setupPayGate(t, srv.URL)
	acc, err := p.local.Signup("local@example.com", "password123", p.tierForNewAccount())
	if err != nil {
		t.Fatal(err)
	}
	grant, err := p.grantForAccount(acc)
	if err != nil {
		t.Fatal(err)
	}
	if grant.MaxSiteBytes != 1000 {
		t.Errorf("local account must keep its stored tier, got %+v", grant)
	}
}

func TestPayGateUnknownTierFallsBack(t *testing.T) {
	srv := pgStub(t, "mega", "active", 200)
	defer srv.Close()
	p := setupPayGate(t, srv.URL)
	acc, err := p.accounts.CreateOAuth(account.OIDCProv, "stack-user-3", "u3@example.com", "free")
	if err != nil {
		t.Fatal(err)
	}
	grant, err := p.grantForAccount(acc)
	if err != nil {
		t.Fatal(err)
	}
	if grant.MaxSiteBytes != 1000 {
		t.Errorf("unknown stack tier id should fall back to stored tier, got %+v", grant)
	}
}

func TestPayGateDashboardShowsManageLink(t *testing.T) {
	srv := pgStub(t, "pro", "active", 200)
	defer srv.Close()
	t.Setenv("SITEBIN_PAYGATE_MANAGE_URL", "https://stack.example/account")
	p := setupPayGate(t, srv.URL)
	acc, err := p.accounts.CreateOAuth(account.OIDCProv, "stack-user-9", "u9@example.com", "free")
	if err != nil {
		t.Fatal(err)
	}
	mux := serveMux(p)
	req := httptest.NewRequest("GET", "/account", nil)
	req.AddCookie(p.sessions.Cookie(acc.ID, acc.TokenVersion))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	body := w.Body.String()
	if !strings.Contains(body, "https://stack.example/account") || !strings.Contains(body, "Manage subscription") {
		t.Error("manage-subscription link missing from dashboard")
	}
	if strings.Contains(body, "/account/billing/") {
		t.Error("built-in checkout should be hidden for PayGate-resolved accounts")
	}
	if !strings.Contains(body, "Pro tier") {
		t.Errorf("dashboard should show the PayGate-resolved tier; body header: %.200s", body)
	}
}

// TestDashboardShowsSiteExpiry pins the promise the pricing FAQ makes about
// cancelling: "your existing sites get 30 days — the date shows on each one in
// your dashboard". Without it a cancelling customer has 30 days to lose their
// data and no in-product way to learn the deadline.
func TestDashboardShowsSiteExpiry(t *testing.T) {
	p := setupTiers(t)
	acc, err := p.local.Signup("dates@example.com", "password123", p.tierForNewAccount())
	if err != nil {
		t.Fatal(err)
	}
	const expiring, permanent = "abcdefghijklmnopqrstuvwxyz", "bbcdefghijklmnopqrstuvwxyz"
	for _, id := range []string{expiring, permanent} {
		if err := p.accounts.LinkSite(acc, id); err != nil {
			t.Fatal(err)
		}
	}
	when := time.Now().Add(30 * 24 * time.Hour)
	sites := p.host.Sites().(*fakeSites)
	sites.infos[expiring] = ext.SiteInfo{ViewID: expiring, Mode: "webserver", ExpiresAt: &when}
	sites.infos[permanent] = ext.SiteInfo{ViewID: permanent, Mode: "webserver"}

	mux := serveMux(p)
	req := httptest.NewRequest("GET", "/account", nil)
	req.AddCookie(p.sessions.Cookie(acc.ID, acc.TokenVersion))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	body := w.Body.String()
	if want := "expires " + when.Local().Format("2006-01-02"); !strings.Contains(body, want) {
		t.Errorf("dashboard does not show the deletion date %q", want)
	}
	if !strings.Contains(body, "no expiry") {
		t.Error("dashboard does not label a site that has no expiry")
	}
}

// TestDashboardRendersSitesAfterTheTierSync guards the order of two statements
// in renderDashboard. This render is the first moment a PayGate tier change is
// noticed, and the sync it triggers rewrites every site's expiry — so building
// the rows first shows the owner the pre-change dates on precisely the render
// where the change is news.
func TestDashboardRendersSitesAfterTheTierSync(t *testing.T) {
	srv := pgStub(t, "pro", "active", 200) // upgraded: Pro sites never expire
	defer srv.Close()
	p := setupPayGate(t, srv.URL)
	acc, err := p.accounts.CreateOAuth(account.OIDCProv, "stack-user-fresh", "fresh@example.com", "free")
	if err != nil {
		t.Fatal(err)
	}
	const viewID = "abcdefghijklmnopqrstuvwxyz"
	if err := p.accounts.LinkSite(acc, viewID); err != nil {
		t.Fatal(err)
	}
	stale := time.Now().Add(3 * 24 * time.Hour) // the date the old Free cap left behind
	sites := p.host.Sites().(*fakeSites)
	sites.infos[viewID] = ext.SiteInfo{ViewID: viewID, Mode: "webserver", ExpiresAt: &stale}

	mux := serveMux(p)
	req := httptest.NewRequest("GET", "/account", nil)
	req.AddCookie(p.sessions.Cookie(acc.ID, acc.TokenVersion))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	body := w.Body.String()
	if old := "expires " + stale.Local().Format("2006-01-02"); strings.Contains(body, old) {
		t.Errorf("dashboard shows %q — the rows were built before the tier sync lifted it", old)
	}
	if !strings.Contains(body, "no expiry") {
		t.Error("dashboard does not show the upgraded site as permanent")
	}
}

func TestSyncTierRestampsOwnedSitesFromPayGate(t *testing.T) {
	srv := pgStub(t, "pro", "active", 200)
	defer srv.Close()
	p := setupPayGate(t, srv.URL)
	acc, err := p.accounts.CreateOAuth(account.OIDCProv, "stack-user-sync", "sync@example.com", "free")
	if err != nil {
		t.Fatal(err)
	}
	if err := p.accounts.LinkSite(acc, "abcdefghijklmnopqrstuvwxyz"); err != nil {
		t.Fatal(err)
	}
	p.host.Sites().(*fakeSites).site("abcdefghijklmnopqrstuvwxyz")

	p.syncTier(acc)

	if acc.Tier != "pro" {
		t.Fatalf("stored tier = %q, want pro", acc.Tier)
	}
	sites := p.host.Sites().(*fakeSites)
	g, ok := sites.quotas["abcdefghijklmnopqrstuvwxyz"]
	if !ok {
		t.Fatal("owned site was not restamped")
	}
	if g.MaxSiteBytes != 5000000 || g.MaxExpiryDays != 0 {
		t.Fatalf("restamped with the wrong tier: %+v", g)
	}
}

// TestCreateSyncsStaleTierBeforeGranting covers the population the cleanup
// sweep cannot reach: a customer who downgrades in the billing portal and never
// opens the dashboard again. Their old sites have no expiry, so the sweep skips
// them before it ever asks about the tier. Creating a new site is the one
// moment they always come back for.
func TestCreateSyncsStaleTierBeforeGranting(t *testing.T) {
	srv := pgStub(t, "free", "active", 200) // PayGate says they cancelled
	defer srv.Close()
	p := setupPayGate(t, srv.URL)
	acc, err := p.accounts.CreateOAuth(account.OIDCProv, "stack-user-lapsed", "lapsed@example.com", "pro")
	if err != nil {
		t.Fatal(err)
	}
	if err := p.accounts.LinkSite(acc, "abcdefghijklmnopqrstuvwxyz"); err != nil {
		t.Fatal(err)
	}
	p.host.Sites().(*fakeSites).site("abcdefghijklmnopqrstuvwxyz")

	// The sync runs before the site-count cap, so the downgrade lands even when
	// the create it arrived on is refused: Free allows one site and they have one.
	_, err = p.grantForAccount(acc)
	var ce *ext.CreateError
	if !errors.As(err, &ce) || ce.Status != 403 {
		t.Fatalf("grant = %v, want a 403 max-sites CreateError on the cancelled plan", err)
	}
	if acc.Tier != "free" {
		t.Errorf("stored tier = %q, want free", acc.Tier)
	}
	sites := p.host.Sites().(*fakeSites)
	g, ok := sites.quotas["abcdefghijklmnopqrstuvwxyz"]
	if !ok {
		t.Fatal("the stale back catalogue was not restamped: one month of Pro still buys permanent hosting")
	}
	if g.MaxSiteBytes != 1000 || g.MaxExpiryDays != 7 {
		t.Fatalf("back catalogue restamped with the wrong tier: %+v", g)
	}
}

func TestSyncTierIsANoOpWhenNothingChanged(t *testing.T) {
	srv := pgStub(t, "free", "active", 200)
	defer srv.Close()
	p := setupPayGate(t, srv.URL)
	acc, err := p.accounts.CreateOAuth(account.OIDCProv, "stack-user-same", "same@example.com", "free")
	if err != nil {
		t.Fatal(err)
	}
	if err := p.accounts.LinkSite(acc, "abcdefghijklmnopqrstuvwxyz"); err != nil {
		t.Fatal(err)
	}
	p.host.Sites().(*fakeSites).site("abcdefghijklmnopqrstuvwxyz")

	p.syncTier(acc)

	sites := p.host.Sites().(*fakeSites)
	if len(sites.quotas) != 0 {
		t.Fatalf("restamped despite no tier change: %v", sites.quotas)
	}
}

// ---- an account on a tier id the config does not have ----
//
// A renamed tier id, a truncated tiers.json or a half-applied env rollout puts
// accounts on tier ids this instance cannot resolve. The only record of what
// they were on is acc.Tier itself, so nothing may overwrite it, and no site may
// be judged against the guessed default cap.

// unknownTierAccount returns an account whose stored tier id is not configured.
func unknownTierAccount(t *testing.T, p *provider, email string) *account.Account {
	t.Helper()
	acc, err := p.local.Signup(email, "password123", p.tierForNewAccount())
	if err != nil {
		t.Fatal(err)
	}
	// the operator renamed "free" to "starter" without migrating accounts
	if err := p.accounts.Update(acc, func(cur *account.Account) error { cur.Tier = "starter"; return nil }); err != nil {
		t.Fatal(err)
	}
	return acc
}

func TestSyncTierRefusesToActOnAnUnconfiguredTier(t *testing.T) {
	p := setupTiers(t)
	acc := unknownTierAccount(t, p, "renamed@example.com")
	if err := p.accounts.LinkSite(acc, "abcdefghijklmnopqrstuvwxyz"); err != nil {
		t.Fatal(err)
	}
	p.host.Sites().(*fakeSites).site("abcdefghijklmnopqrstuvwxyz")

	p.syncTier(acc)

	if acc.Tier != "starter" {
		t.Fatalf("stored tier rewritten to a guess: %q — fixing the config can no longer restore it", acc.Tier)
	}
	reload, _ := p.accounts.ByID(acc.ID)
	if reload.Tier != "starter" {
		t.Fatalf("persisted tier rewritten to a guess: %q", reload.Tier)
	}
	sites := p.host.Sites().(*fakeSites)
	if len(sites.quotas) != 0 {
		t.Fatalf("sites restamped from a guessed tier: %v", sites.quotas)
	}
}

func TestQuotaForRefusesToGuessAnUnconfiguredTier(t *testing.T) {
	p := setupTiers(t)
	acc := unknownTierAccount(t, p, "renamed2@example.com")

	_, _, err := p.QuotaFor(acc.ID)
	if err == nil {
		t.Fatal("QuotaFor answered with the default tier's caps for an unconfigured tier; the sweep would delete against a guess")
	}
	if !errors.Is(err, errUnknownTier) {
		t.Fatalf("QuotaFor error = %v, want errUnknownTier", err)
	}
}

// TestSweepKeepsSiteOfAccountOnUnconfiguredTier runs the real cleanup sweep
// against the real enterprise provider: the seam contract ("a non-nil error
// means keep") is only worth anything if this provider actually produces one.
func TestSweepKeepsSiteOfAccountOnUnconfiguredTier(t *testing.T) {
	p := setupTiers(t)
	acc := unknownTierAccount(t, p, "renamed3@example.com")

	// the package init() registered a bare provider; swap in this configured one
	ext.Reset()
	ext.Register(p)
	defer func() { ext.Reset(); ext.Register(newProvider()) }()

	st, err := store.New(t.TempDir(), "sitebin.example", 1<<20, 100)
	if err != nil {
		t.Fatal(err)
	}
	site, _, err := st.Create()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	when := now.Add(-25 * time.Hour) // expired well past the deletion grace
	if err := st.Update(site, func(m *store.Meta) error {
		m.OwnerAccountID = acc.ID
		m.QuotaExpiryDays = 7
		m.ExpiresAt = &when
		m.ExpiryFromTier = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	removed, err := cleanup.Sweep(st, now)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if removed != 0 {
		t.Fatalf("removed = %d, want 0 — a config mistake must not delete a customer's sites", removed)
	}
	if _, err := st.ByViewID(site.ViewID); err != nil {
		t.Fatalf("site deleted because its owner's tier id is missing from the config: %v", err)
	}
}

// TestSyncTierRetriesAfterAPartialRestamp pins the order of the two writes.
// acc.Tier is the only retry marker there is: once it matches the resolved
// tier, every later syncTier early-returns. Persisting it before the sites are
// restamped would therefore freeze a half-finished downgrade in place forever.
func TestSyncTierRetriesAfterAPartialRestamp(t *testing.T) {
	srv := pgStub(t, "pro", "active", 200)
	defer srv.Close()
	p := setupPayGate(t, srv.URL)
	acc, err := p.accounts.CreateOAuth(account.OIDCProv, "stack-user-partial", "partial@example.com", "free")
	if err != nil {
		t.Fatal(err)
	}
	const good, bad = "abcdefghijklmnopqrstuvwxyz", "bbcdefghijklmnopqrstuvwxyz"
	sites := p.host.Sites().(*fakeSites)
	for _, id := range []string{good, bad} {
		if err := p.accounts.LinkSite(acc, id); err != nil {
			t.Fatal(err)
		}
		sites.site(id) // both sites exist; only the write to one of them fails
	}
	sites.applyErrs = map[string]error{bad: errors.New("disk full")}

	p.syncTier(acc)

	if acc.Tier != "free" {
		t.Fatalf("stored tier advanced to %q while a site was left on its old caps", acc.Tier)
	}
	if _, ok := sites.quotas[bad]; ok {
		t.Fatal("the failing site was restamped after all; the fixture is wrong")
	}

	// the failure clears (the disk has room again) and the next pass finishes
	sites.applyErrs = nil
	p.syncTier(acc)

	if acc.Tier != "pro" {
		t.Fatalf("stored tier = %q after a clean pass, want pro", acc.Tier)
	}
	for _, id := range []string{good, bad} {
		if g, ok := sites.quotas[id]; !ok || g.MaxSiteBytes != 5000000 {
			t.Fatalf("site %s not restamped on the retry: %+v", id, g)
		}
	}
}

// ---- against the real store, through the shipping SiteService ----
//
// The two questions below cannot be asked of fakeSites: one is about the
// store's expiry transition table, the other about a site that really is gone.
// These wire the provider to httpapi's SiteService over a real store, so the
// whole path (restampSites → siteService.ApplyQuota → store.ApplyQuota) runs.

type storeHost struct {
	dir   string
	sites ext.SiteService
}

func (h *storeHost) DataDir() string        { return h.dir }
func (h *storeHost) BaseDomain() string     { return "sitebin.example" }
func (h *storeHost) HTTPOnly() bool         { return true }
func (h *storeHost) Secret() []byte         { return []byte("0123456789abcdef0123456789abcdef") }
func (h *storeHost) PathViews() bool        { return false }
func (h *storeHost) Sites() ext.SiteService { return h.sites }
func (h *storeHost) BaseURL() string        { return "http://sitebin.example" }
func (h *storeHost) MCPOAuthIssuer() string { return "" }
func (h *storeHost) MCPResource() string    { return "https://sitebin.example/mcp" }

// setupRealSites returns a tiers provider whose SiteService is the shipping one.
// paygateURL may be empty for a provider with no PayGate.
func setupRealSites(t *testing.T, paygateURL string) (*provider, *store.Store) {
	t.Helper()
	t.Setenv("SITEBIN_ACCOUNT_MODE", "tiers")
	t.Setenv("SITEBIN_TIERS", tiersJSON)
	t.Setenv("SITEBIN_DEFAULT_TIER", "free")
	if paygateURL != "" {
		t.Setenv("SITEBIN_PAYGATE_URL", paygateURL)
		t.Setenv("SITEBIN_PAYGATE_APP_ID", "sitebin")
		t.Setenv("SITEBIN_PAYGATE_API_KEY", "ssk_test_k")
		t.Setenv("SITEBIN_OAUTH_OIDC_ISSUER", "https://auth.stack.example/api/v1/sitebin")
		t.Setenv("SITEBIN_OAUTH_OIDC_CLIENT_ID", "sitebin")
	}
	st, err := store.New(t.TempDir(), "sitebin.example", 1<<20, 100)
	if err != nil {
		t.Fatal(err)
	}
	api, err := httpapi.New(config.Config{BaseDomain: "sitebin.example", HTTPOnly: true}, st,
		[]byte("0123456789abcdef0123456789abcdef"), nil)
	if err != nil {
		t.Fatal(err)
	}
	p := newProvider()
	if err := p.Init(&storeHost{dir: t.TempDir(), sites: api.SiteService()}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	return p, st
}

// paidSite creates a site stamped like one made on the Pro tier: owned,
// uncapped lifetime, no expiry at all.
func paidSite(t *testing.T, st *store.Store, owner string) *store.Site {
	t.Helper()
	site, _, err := st.Create()
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Update(site, func(m *store.Meta) error {
		m.OwnerAccountID = owner
		m.QuotaBytes = 5000000
		m.QuotaFiles = 500
		m.QuotaExpiryDays = 0
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return site
}

// TestSyncTierHealsADanglingMarkerAndKeepsTheGrace is the Critical, end to end.
//
// Ownership markers outlive their sites: the edit page's delete and the cleanup
// sweep both go straight to the store and tell the extension nothing. A marker
// left behind used to make every restamp fail, which meant the stored tier was
// never persisted — so the account re-ran the whole restamp on every dashboard
// render and every create, forever. And because the second pass saw the 30-day
// grace the first one had just stamped sitting beyond the new 7-day cap, it
// clamped it: the customer lost their sites 23 days before the date the pricing
// page promised and their dashboard showed them.
func TestSyncTierHealsADanglingMarkerAndKeepsTheGrace(t *testing.T) {
	srv := pgStub(t, "free", "active", 200) // they cancelled Pro
	defer srv.Close()
	p, st := setupRealSites(t, srv.URL)
	acc, err := p.accounts.CreateOAuth(account.OIDCProv, "stack-user-dangling", "dangling@example.com", "pro")
	if err != nil {
		t.Fatal(err)
	}
	live := paidSite(t, st, acc.ID)
	const gone = "zzzzzzzzzzzzzzzzzzzzzzzzzz" // deleted from the edit page; the marker stayed
	for _, id := range []string{live.ViewID, gone} {
		if err := p.accounts.LinkSite(acc, id); err != nil {
			t.Fatal(err)
		}
	}

	p.syncTier(acc)

	reload, err := p.accounts.ByID(acc.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reload.Tier != "free" {
		t.Fatalf("persisted tier = %q, want free — one deleted site blocked the downgrade for good", reload.Tier)
	}
	if p.owns(acc, gone) {
		t.Error("the stale ownership marker survived; the account cannot heal itself")
	}
	ids, _ := p.accounts.ListSiteIDs(acc)
	if len(ids) != 1 || ids[0] != live.ViewID {
		t.Fatalf("owned sites after the sync = %v, want just the live one", ids)
	}

	got, err := st.ByViewID(live.ViewID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Meta.QuotaBytes != 1000 || got.Meta.QuotaExpiryDays != 7 {
		t.Fatalf("live site not restamped with the free caps: %+v", got.Meta)
	}
	if got.Meta.ExpiresAt == nil {
		t.Fatal("the downgrade did not stamp the 30-day grace")
	}
	grace := *got.Meta.ExpiresAt
	if want := time.Now().Add(store.DowngradeGrace); grace.Sub(want) > time.Minute || grace.Sub(want) < -time.Minute {
		t.Fatalf("grace = %v, want ~%v", grace, want)
	}

	// A later pass — another dashboard render, another create — must be a no-op.
	p.syncTier(acc)

	after, err := st.ByViewID(live.ViewID)
	if err != nil {
		t.Fatal(err)
	}
	if !after.Meta.ExpiresAt.Equal(grace) {
		t.Fatalf("a second pass moved the grace: %v -> %v — the customer loses the site early", grace, after.Meta.ExpiresAt)
	}
}

// TestRestampSitesTwiceKeepsTheDowngradeGrace covers the same destruction by its
// other routes: re-selecting the tier already active, or a repeated
// subscription.updated webhook, both call restampSites directly with a tier that
// has not changed.
func TestRestampSitesTwiceKeepsTheDowngradeGrace(t *testing.T) {
	p, st := setupRealSites(t, "")
	acc, err := p.local.Signup("repeat@example.com", "password123", "pro")
	if err != nil {
		t.Fatal(err)
	}
	site := paidSite(t, st, acc.ID)
	if err := p.accounts.LinkSite(acc, site.ViewID); err != nil {
		t.Fatal(err)
	}
	free, ok := p.cfg.Tier("free")
	if !ok {
		t.Fatal("free tier missing from the test config")
	}

	if err := p.restampSites(acc, free); err != nil {
		t.Fatalf("first restamp: %v", err)
	}
	first, err := st.ByViewID(site.ViewID)
	if err != nil {
		t.Fatal(err)
	}
	if first.Meta.ExpiresAt == nil {
		t.Fatal("the downgrade did not stamp the grace")
	}
	grace := *first.Meta.ExpiresAt

	if err := p.restampSites(acc, free); err != nil {
		t.Fatalf("second restamp: %v", err)
	}
	got, err := st.ByViewID(site.ViewID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Meta.ExpiresAt.Equal(grace) {
		t.Fatalf("the repeat restamp moved the grace: %v -> %v", grace, got.Meta.ExpiresAt)
	}
	if want := time.Now().Add(store.DowngradeGrace); got.Meta.ExpiresAt.Sub(want) > time.Minute || got.Meta.ExpiresAt.Sub(want) < -time.Minute {
		t.Fatalf("grace = %v, want ~%v (the 30 days the pricing page promises)", got.Meta.ExpiresAt, want)
	}
}

// TestRestampSitesUnlinksOnlyTheVanishedSite keeps the self-heal narrow: a real
// failure (a disk error) must still block the tier persist and must NOT drop the
// marker, or a transient error would silently disown a live site.
func TestRestampSitesUnlinksOnlyTheVanishedSite(t *testing.T) {
	p := setupTiers(t)
	acc, err := p.local.Signup("narrow@example.com", "password123", "pro")
	if err != nil {
		t.Fatal(err)
	}
	const broken, gone = "abcdefghijklmnopqrstuvwxyz", "bbcdefghijklmnopqrstuvwxyz"
	sites := p.host.Sites().(*fakeSites)
	for _, id := range []string{broken, gone} {
		if err := p.accounts.LinkSite(acc, id); err != nil {
			t.Fatal(err)
		}
	}
	sites.site(broken) // exists, but the write fails
	sites.applyErrs = map[string]error{broken: errors.New("disk full")}

	free, _ := p.cfg.Tier("free")
	if err := p.restampSites(acc, free); err == nil {
		t.Fatal("restamp reported success while a live site kept its old caps")
	}
	if !p.owns(acc, broken) {
		t.Error("a site that merely failed to be written was disowned")
	}
	if p.owns(acc, gone) {
		t.Error("the marker of a site that no longer exists was kept")
	}
}

func TestSyncTierLeavesEverythingAloneOnLookupFailure(t *testing.T) {
	srv := pgStub(t, "", "", 500)
	defer srv.Close()
	p := setupPayGate(t, srv.URL)
	acc, err := p.accounts.CreateOAuth(account.OIDCProv, "stack-user-down", "down@example.com", "free")
	if err != nil {
		t.Fatal(err)
	}
	if err := p.accounts.LinkSite(acc, "abcdefghijklmnopqrstuvwxyz"); err != nil {
		t.Fatal(err)
	}
	p.host.Sites().(*fakeSites).site("abcdefghijklmnopqrstuvwxyz")

	p.syncTier(acc)

	if acc.Tier != "free" {
		t.Fatalf("stored tier changed during an outage: %q", acc.Tier)
	}
	sites := p.host.Sites().(*fakeSites)
	if len(sites.quotas) != 0 {
		t.Fatalf("restamped despite a failed lookup: %v", sites.quotas)
	}
}
