//go:build ee

package ee

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/ittrail/sitebin.io/ee/account"
	"github.com/ittrail/sitebin.io/internal/ext"
)

// adminTiersJSON adds two uncapped tiers to the fixture set: one that carries
// the admin flag and one that does not, so a test can tell the flag apart from
// "the tier happens to be generous".
const adminTiersJSON = `[
  {"id":"free","label":"Free","max_site_bytes":1000,"max_files":10,"max_sites":1,"webdav":false,"custom_domains":0,"max_expiry_days":7},
  {"id":"unlimited","label":"Unlimited","max_site_bytes":1099511627776,"max_files":1000000,"max_sites":0,"webdav":true,"custom_domains":10000,"max_expiry_days":0},
  {"id":"admin","label":"Admin","max_site_bytes":1099511627776,"max_files":1000000,"max_sites":0,"webdav":true,"custom_domains":10000,"max_expiry_days":0,"admin":true}
]`

// setupAdmin builds a tiers-mode provider with the admin fixture set and the
// given allowlist, and returns the provider plus a mux carrying its routes.
func setupAdmin(t *testing.T, allowlist string) (*provider, *fakeHost, http.Handler) {
	t.Helper()
	t.Setenv("SITEBIN_ACCOUNT_MODE", "tiers")
	t.Setenv("SITEBIN_TIERS", adminTiersJSON)
	t.Setenv("SITEBIN_DEFAULT_TIER", "free")
	t.Setenv("SITEBIN_ADMIN_ACCOUNTS", allowlist)
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

// adminUser signs an account up on the named tier and returns it with a session
// cookie, so each gating test differs only in the two things being tested.
func adminUser(t *testing.T, p *provider, mux http.Handler, email, tier string) (*http.Cookie, string) {
	t.Helper()
	req := form(url.Values{"email": {email}, "password": {"password123"}})
	req.URL.Path = "/account/signup"
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("signup = %d (%s)", w.Code, w.Body)
	}
	acc, err := p.accounts.ByEmail(email)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.accounts.Update(acc, func(a *account.Account) error { a.Tier = tier; return nil }); err != nil {
		t.Fatal(err)
	}
	return sessionCookie(t, w), acc.ID
}

func getAs(mux http.Handler, path string, c *http.Cookie) *httptest.ResponseRecorder {
	r := httptest.NewRequest("GET", path, nil)
	if c != nil {
		r.AddCookie(c)
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	return w
}

// --- §1 gating ---

func TestAdminConsoleNeedsBothTierAndAllowlist(t *testing.T) {
	cases := []struct {
		name, tier, allowlist string
		want                  int
	}{
		{"admin tier and allowlisted", "admin", "boss@example.com", 200},
		{"admin tier but not allowlisted", "admin", "", 404},
		{"allowlisted but tier carries no flag", "unlimited", "boss@example.com", 404},
		{"neither", "free", "", 404},
		{"allowlist names someone else", "admin", "other@example.com", 404},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p, _, mux := setupAdmin(t, c.allowlist)
			cookie, _ := adminUser(t, p, mux, "boss@example.com", c.tier)
			if w := getAs(mux, "/account/admin", cookie); w.Code != c.want {
				t.Fatalf("GET /account/admin = %d, want %d", w.Code, c.want)
			}
		})
	}
}

func TestAdminAllowlistIsCaseInsensitive(t *testing.T) {
	p, _, mux := setupAdmin(t, "  BOSS@Example.COM , ")
	cookie, _ := adminUser(t, p, mux, "boss@example.com", "admin")
	if w := getAs(mux, "/account/admin", cookie); w.Code != 200 {
		t.Fatalf("GET /account/admin = %d, want 200 — allowlist matching must normalize", w.Code)
	}
}

func TestAdminConsoleRefusesAnonymous(t *testing.T) {
	_, _, mux := setupAdmin(t, "boss@example.com")
	if w := getAs(mux, "/account/admin", nil); w.Code != 404 {
		t.Fatalf("anonymous GET /account/admin = %d, want 404", w.Code)
	}
}

// The console must not announce itself: a signed-in non-admin has to be unable
// to tell the route apart from one that does not exist.
func TestAdminConsoleIs404NotForbidden(t *testing.T) {
	p, _, mux := setupAdmin(t, "boss@example.com")
	cookie, _ := adminUser(t, p, mux, "nobody@example.com", "free")
	w := getAs(mux, "/account/admin", cookie)
	if w.Code != 404 {
		t.Fatalf("non-admin GET /account/admin = %d, want 404", w.Code)
	}
	if strings.Contains(strings.ToLower(w.Body.String()), "admin") {
		t.Errorf("404 body mentions admin, which advertises the console: %q", w.Body)
	}
}

// --- §2 what the console shows ---

// instance registers a mixed set of sites: two owned, two anonymous, one of
// them expiring inside the week and one well outside it.
func instance(t *testing.T, host *fakeHost, owner string) {
	t.Helper()
	soon := time.Now().Add(48 * time.Hour)
	later := time.Now().Add(60 * 24 * time.Hour)
	for _, s := range []ext.SiteInfo{
		{ViewID: "aaaaaaaaaaaaaaaaaaaaaaaaaa", Owner: owner, Mode: "webserver", Bytes: 1000, Files: 3},
		{ViewID: "bbbbbbbbbbbbbbbbbbbbbbbbbb", Owner: owner, Mode: "viewer", Bytes: 2000, Files: 5, ExpiresAt: &soon},
		{ViewID: "cccccccccccccccccccccccccc", Mode: "webserver", Bytes: 4000, Files: 7, Domains: []string{"example.test"}},
		{ViewID: "dddddddddddddddddddddddddd", Mode: "webserver", Bytes: 8000, Files: 11, ExpiresAt: &later},
	} {
		host.sites.infos[s.ViewID] = s
	}
}

func TestAdminConsoleListsEverySiteIncludingAnonymous(t *testing.T) {
	p, host, mux := setupAdmin(t, "boss@example.com")
	cookie, accID := adminUser(t, p, mux, "boss@example.com", "admin")
	instance(t, host, accID)

	body := getAs(mux, "/account/admin", cookie).Body.String()
	for _, id := range []string{"aaaaaaaaaaaaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbbbbbbbbbbbb", "cccccccccccccccccccccccccc", "dddddddddddddddddddddddddd"} {
		if !strings.Contains(body, id) {
			t.Errorf("site %s missing from the console", id)
		}
	}
	if !strings.Contains(body, "example.test") {
		t.Error("custom domain missing from the console")
	}
	if !strings.Contains(body, "boss@example.com") {
		t.Error("owner email missing — the console resolves account ids to emails")
	}
}

func TestAdminConsoleCountsTheInstance(t *testing.T) {
	p, host, mux := setupAdmin(t, "boss@example.com")
	_, accID := adminUser(t, p, mux, "boss@example.com", "admin")
	instance(t, host, accID)

	stats := p.instanceStats(mustAll(t, host))
	if stats.Sites != 4 {
		t.Errorf("Sites = %d, want 4", stats.Sites)
	}
	if stats.Owned != 2 || stats.Anonymous != 2 {
		t.Errorf("Owned/Anonymous = %d/%d, want 2/2", stats.Owned, stats.Anonymous)
	}
	if stats.Bytes != 15000 || stats.Files != 26 {
		t.Errorf("Bytes/Files = %d/%d, want 15000/26", stats.Bytes, stats.Files)
	}
	if stats.Expiring != 2 {
		t.Errorf("Expiring = %d, want 2 (sites carrying any expiry)", stats.Expiring)
	}
	if stats.ExpiringSoon != 1 {
		t.Errorf("ExpiringSoon = %d, want 1 (within seven days)", stats.ExpiringSoon)
	}
}

// An enumeration failure must be visible. Rendering an empty list would tell an
// operator their instance is empty, which is the one wrong answer.
func TestAdminConsoleReportsAnEnumerationFailure(t *testing.T) {
	p, host, mux := setupAdmin(t, "boss@example.com")
	cookie, _ := adminUser(t, p, mux, "boss@example.com", "admin")
	host.sites.allErr = fmt.Errorf("disk on fire")

	w := getAs(mux, "/account/admin", cookie)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("GET /account/admin with a failing All() = %d, want 500", w.Code)
	}
}

// --- §3 actions ---

func TestAdminDeletesAnySite(t *testing.T) {
	p, host, mux := setupAdmin(t, "boss@example.com")
	cookie, accID := adminUser(t, p, mux, "boss@example.com", "admin")
	instance(t, host, accID)
	acc, _ := p.accounts.ByEmail("boss@example.com")

	// an anonymous site the admin does not own
	req := form(url.Values{"csrf": {p.csrf(acc)}})
	req.URL.Path = "/account/admin/sites/cccccccccccccccccccccccccc/delete"
	req.AddCookie(cookie)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("delete = %d, want 303 (%s)", w.Code, w.Body)
	}
	if len(host.sites.deleted) != 1 || host.sites.deleted[0] != "cccccccccccccccccccccccccc" {
		t.Fatalf("deleted = %v, want exactly the named site", host.sites.deleted)
	}
	if _, ok := host.sites.infos["aaaaaaaaaaaaaaaaaaaaaaaaaa"]; !ok {
		t.Error("delete took a site it was not asked to take")
	}
}

func TestAdminSetsAndClearsExpiry(t *testing.T) {
	p, host, mux := setupAdmin(t, "boss@example.com")
	cookie, accID := adminUser(t, p, mux, "boss@example.com", "admin")
	instance(t, host, accID)
	acc, _ := p.accounts.ByEmail("boss@example.com")
	target := "aaaaaaaaaaaaaaaaaaaaaaaaaa"

	post := func(v url.Values) int {
		r := form(v)
		r.URL.Path = "/account/admin/sites/" + target + "/expiry"
		r.AddCookie(cookie)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w.Code
	}

	if code := post(url.Values{"csrf": {p.csrf(acc)}, "expires": {"2027-01-31"}}); code != http.StatusSeeOther {
		t.Fatalf("set expiry = %d, want 303", code)
	}
	got := host.sites.expirySet[target]
	if got == nil || got.Format("2006-01-02") != "2027-01-31" {
		t.Fatalf("expiry = %v, want 2027-01-31", got)
	}

	if code := post(url.Values{"csrf": {p.csrf(acc)}, "expires": {""}}); code != http.StatusSeeOther {
		t.Fatalf("clear expiry = %d, want 303", code)
	}
	if v, ok := host.sites.expirySet[target]; !ok || v != nil {
		t.Fatalf("cleared expiry = %v, want nil written", v)
	}
}

func TestAdminRejectsABadDate(t *testing.T) {
	p, host, mux := setupAdmin(t, "boss@example.com")
	cookie, accID := adminUser(t, p, mux, "boss@example.com", "admin")
	instance(t, host, accID)
	acc, _ := p.accounts.ByEmail("boss@example.com")

	r := form(url.Values{"csrf": {p.csrf(acc)}, "expires": {"the day after tomorrow"}})
	r.URL.Path = "/account/admin/sites/aaaaaaaaaaaaaaaaaaaaaaaaaa/expiry"
	r.AddCookie(cookie)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("bad date = %d, want 400", w.Code)
	}
	if len(host.sites.expirySet) != 0 {
		t.Errorf("a rejected date still wrote: %v", host.sites.expirySet)
	}
}

func TestAdminActionsRequireCSRF(t *testing.T) {
	p, host, mux := setupAdmin(t, "boss@example.com")
	cookie, accID := adminUser(t, p, mux, "boss@example.com", "admin")
	instance(t, host, accID)

	for _, path := range []string{
		"/account/admin/sites/aaaaaaaaaaaaaaaaaaaaaaaaaa/delete",
		"/account/admin/sites/aaaaaaaaaaaaaaaaaaaaaaaaaa/expiry",
	} {
		r := form(url.Values{})
		r.URL.Path = path
		r.AddCookie(cookie)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != http.StatusForbidden {
			t.Errorf("%s without csrf = %d, want 403", path, w.Code)
		}
	}
	if len(host.sites.deleted) != 0 || len(host.sites.expirySet) != 0 {
		t.Error("a request without CSRF still mutated")
	}
}

// A non-admin must not reach the actions either — gating the page alone would
// leave the POSTs open to anyone who learned their shape.
func TestAdminActionsRefuseANonAdmin(t *testing.T) {
	p, host, mux := setupAdmin(t, "boss@example.com")
	cookie, _ := adminUser(t, p, mux, "nobody@example.com", "free")
	instance(t, host, "someone-else")
	acc, _ := p.accounts.ByEmail("nobody@example.com")

	r := form(url.Values{"csrf": {p.csrf(acc)}})
	r.URL.Path = "/account/admin/sites/cccccccccccccccccccccccccc/delete"
	r.AddCookie(cookie)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != 404 {
		t.Fatalf("non-admin delete = %d, want 404", w.Code)
	}
	if len(host.sites.deleted) != 0 {
		t.Fatal("a non-admin deleted a site")
	}
}

func mustAll(t *testing.T, host *fakeHost) []ext.SiteInfo {
	t.Helper()
	all, err := host.sites.All()
	if err != nil {
		t.Fatal(err)
	}
	return all
}

// The console is reachable only by typing its path unless the dashboard links
// it, and a path is a poor secret and a worse feature.
func TestDashboardLinksTheConsoleForAdminsOnly(t *testing.T) {
	for _, c := range []struct {
		name, tier, allowlist string
		want                  bool
	}{
		{"admin", "admin", "boss@example.com", true},
		{"non-admin", "free", "boss@example.com", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			p, _, mux := setupAdmin(t, c.allowlist)
			cookie, _ := adminUser(t, p, mux, "boss@example.com", c.tier)
			body := getAs(mux, "/account", cookie).Body.String()
			if got := strings.Contains(body, `href="/account/admin"`); got != c.want {
				t.Fatalf("dashboard links the console = %v, want %v", got, c.want)
			}
		})
	}
}
