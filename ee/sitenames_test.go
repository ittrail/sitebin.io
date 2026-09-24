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

// namedAccount signs an account up with one owned site, and returns what the
// site-list tests need.
func namedAccount(t *testing.T) (*provider, *fakeHost, http.Handler, *account.Account, *http.Cookie) {
	t.Helper()
	p, host, mux := setupAccounts(t)
	req := form(url.Values{"email": {"names@example.com"}, "password": {"password123"}})
	req.URL.Path = "/account/signup"
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("signup = %d (%s)", w.Code, w.Body)
	}
	acc, err := p.accounts.ByEmail("names@example.com")
	if err != nil {
		t.Fatal(err)
	}
	return p, host, mux, acc, sessionCookie(t, w)
}

const ownedSite = "abcdefghijklmnopqrstuvwxyz"

// The dashboard lists each site by the handles a person remembers: its name,
// and its custom domains — the verified ones as links, the ones still waiting
// for their DNS proof marked as such.
func TestDashboardListsNamesAndCustomDomains(t *testing.T) {
	p, host, mux, acc, cookie := namedAccount(t)
	const unnamed = "bbcdefghijklmnopqrstuvwxyz"
	for _, id := range []string{ownedSite, unnamed} {
		if err := p.accounts.LinkSite(acc, id); err != nil {
			t.Fatal(err)
		}
	}
	host.sites.infos[ownedSite] = ext.SiteInfo{
		ViewID: ownedSite, Mode: "webserver", Name: "Client <docs>",
		ViewURL: "http://" + ownedSite + ".sitebin.example", EditURL: "http://sitebin.example/e/one",
		Domains: []string{"docs.example.com"},
		DomainLinks: []ext.DomainLink{
			{Domain: "docs.example.com", URL: "https://docs.example.com"},
			{Domain: "wait.example.com", Pending: true},
		},
	}
	host.sites.infos[unnamed] = ext.SiteInfo{ViewID: unnamed, Mode: "webserver", ViewURL: "http://" + unnamed + ".sitebin.example", EditURL: "http://sitebin.example/e/two"}

	w := getAs(mux, "/account", cookie)
	if w.Code != 200 {
		t.Fatalf("dashboard = %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "Client &lt;docs&gt;") {
		t.Error("dashboard does not show the site's name (escaped)")
	}
	if strings.Contains(body, "Client <docs>") {
		t.Error("the site name reached the page unescaped")
	}
	if !strings.Contains(body, `href="https://docs.example.com"`) {
		t.Error("dashboard does not link the verified custom domain")
	}
	if !strings.Contains(body, "wait.example.com") || !strings.Contains(body, "pending DNS") {
		t.Error("dashboard does not show the pending claim as pending")
	}
	if strings.Contains(body, `href="https://wait.example.com"`) || strings.Contains(body, `href=""`) {
		t.Error("a pending claim serves nothing and must not be a link")
	}
	if !strings.Contains(body, `action="/account/sites/`+unnamed+`/name"`) {
		t.Error("an unnamed site has no rename form")
	}
}

func TestDashboardRenamesAnOwnedSite(t *testing.T) {
	p, host, mux, acc, cookie := namedAccount(t)
	if err := p.accounts.LinkSite(acc, ownedSite); err != nil {
		t.Fatal(err)
	}
	host.sites.site(ownedSite)

	post := func(vals url.Values, id string) *httptest.ResponseRecorder {
		r := form(vals)
		r.URL.Path = "/account/sites/" + id + "/name"
		r.AddCookie(cookie)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	csrf := p.csrf(acc)

	if w := post(url.Values{"csrf": {csrf}, "name": {"  Portfolio  "}}, ownedSite); w.Code != http.StatusSeeOther {
		t.Fatalf("rename = %d (%s)", w.Code, w.Body)
	}
	if got := host.sites.infos[ownedSite].Name; got != "Portfolio" {
		t.Errorf("name = %q, want the trimmed name", got)
	}

	// No CSRF token: refused, unchanged.
	if w := post(url.Values{"name": {"Hijacked"}}, ownedSite); w.Code != http.StatusForbidden {
		t.Errorf("rename without csrf = %d, want 403", w.Code)
	}
	// A site the account does not own: refused, and the fake never reached.
	const foreign = "zzzzzzzzzzzzzzzzzzzzzzzzzz"
	host.sites.site(foreign)
	if w := post(url.Values{"csrf": {csrf}, "name": {"Mine now"}}, foreign); w.Code != http.StatusForbidden {
		t.Errorf("rename of a foreign site = %d, want 403", w.Code)
	}
	if host.sites.infos[foreign].Name != "" {
		t.Error("a foreign site was renamed")
	}
	if host.sites.infos[ownedSite].Name != "Portfolio" {
		t.Error("a refused rename changed the site")
	}

	// A bad name is refused with the rule, not a bare error.
	w := post(url.Values{"csrf": {csrf}, "name": {strings.Repeat("a", 61)}}, ownedSite)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "60 characters") {
		t.Errorf("over-long name = %d (%s)", w.Code, w.Body)
	}

	// An empty name clears it.
	if w := post(url.Values{"csrf": {csrf}, "name": {""}}, ownedSite); w.Code != http.StatusSeeOther {
		t.Fatalf("clear = %d (%s)", w.Code, w.Body)
	}
	if got := host.sites.infos[ownedSite].Name; got != "" {
		t.Errorf("name after clearing = %q", got)
	}
}

// Operators get the same handle owners do: the register shows the name and
// finds a site by it.
func TestAdminRegisterShowsAndSearchesNames(t *testing.T) {
	p, host, mux := setupAdmin(t, "boss@example.com")
	cookie, owner := adminUser(t, p, mux, "boss@example.com", "admin")
	instance(t, host, owner)
	named := host.sites.infos["aaaaaaaaaaaaaaaaaaaaaaaaaa"]
	named.Name = "Physio landing page"
	host.sites.infos[named.ViewID] = named

	body := getAs(mux, "/account/admin", cookie).Body.String()
	if !strings.Contains(body, "Physio landing page") {
		t.Error("the register does not show the site's name")
	}
	body = getAs(mux, "/account/admin?q=physio", cookie).Body.String()
	if !strings.Contains(body, named.ViewID) || strings.Contains(body, "bbbbbbbbbbbbbbbbbbbbbbbbbb") {
		t.Error("searching the register by name does not find exactly the named site")
	}
}
