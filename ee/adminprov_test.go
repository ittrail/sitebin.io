//go:build ee

package ee

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ittrail/sitebin.io/ee/account"
	"github.com/ittrail/sitebin.io/internal/ext"
	"github.com/ittrail/sitebin.io/internal/provenance"
)

// setupProvAdmin is setupAdmin with a SiteService that keeps site logs.
func setupProvAdmin(t *testing.T) (*provider, *provHost, http.Handler, *http.Cookie) {
	t.Helper()
	t.Setenv("SITEBIN_ACCOUNT_MODE", "tiers")
	t.Setenv("SITEBIN_TIERS", adminTiersJSON)
	t.Setenv("SITEBIN_DEFAULT_TIER", "free")
	t.Setenv("SITEBIN_ADMIN_ACCOUNTS", "boss@example.com")
	p := newProvider()
	fs := &fakeSites{infos: map[string]ext.SiteInfo{}}
	host := &provHost{fakeHost: &fakeHost{dir: t.TempDir(), sites: fs}, sites: &provSites{fakeSites: fs}}
	if err := p.Init(host); err != nil {
		t.Fatal(err)
	}
	mux := serveMux(p)
	cookie, _ := adminUser(t, p, mux, "boss@example.com", "admin")
	return p, host, mux, cookie
}

// abuseScene is the incident's shape: an account signs up, mints a token 12
// seconds later and publishes a site from another address; an anonymous drop
// comes from that same address; an unrelated site from elsewhere.
func abuseScene(t *testing.T, p *provider, host *provHost) (abuser *account.Account) {
	t.Helper()
	abuser, err := p.accounts.CreateOAuth(account.OIDCProv, "sub-abuser", "mflea@example.com", true, "free")
	if err != nil {
		t.Fatal(err)
	}
	t0 := time.Now().Add(-time.Hour).UTC()
	p.RecordAccountProvenance(abuser.ID, provenance.Entry{Time: t0, Action: provenance.ActionSignup, Surface: provenance.SurfaceOIDC, Account: abuser.ID, IP: "203.0.113.7", UA: "Mozilla/5.0 <script>alert(1)</script>"})
	p.RecordAccountProvenance(abuser.ID, provenance.Entry{Time: t0.Add(12 * time.Second), Action: provenance.ActionTokenMint, Surface: provenance.SurfaceDashboard, Account: abuser.ID, IP: "203.0.113.7", Detail: `"testing" sbp_abc123…`})

	for id, owner := range map[string]string{"phishaaaaaaaaaaaaaaaaaaaaa": abuser.ID, "dropaaaaaaaaaaaaaaaaaaaaaa": "", "fineaaaaaaaaaaaaaaaaaaaaaa": ""} {
		host.sites.infos[id] = ext.SiteInfo{ViewID: id, Owner: owner, Mode: "webserver", CreatedAt: t0}
	}
	p.accounts.LinkSite(abuser, "phishaaaaaaaaaaaaaaaaaaaaa")
	host.sites.RecordSiteProvenance("phishaaaaaaaaaaaaaaaaaaaaa", provenance.Entry{Time: t0.Add(time.Minute), Action: provenance.ActionCreate, Surface: provenance.SurfaceAPI, Auth: provenance.AuthToken, Account: abuser.ID, IP: "198.51.100.23", UA: "python-requests/2.32", Files: 1, Detail: "bug.html"})
	host.sites.RecordSiteProvenance("dropaaaaaaaaaaaaaaaaaaaaaa", provenance.Entry{Time: t0.Add(2 * time.Minute), Action: provenance.ActionCreate, Surface: provenance.SurfaceUI, Auth: provenance.AuthNone, IP: "198.51.100.23", UA: "Mozilla/5.0"})
	host.sites.RecordSiteProvenance("fineaaaaaaaaaaaaaaaaaaaaaa", provenance.Entry{Time: t0.Add(3 * time.Minute), Action: provenance.ActionCreate, Surface: provenance.SurfaceUI, IP: "192.0.2.1"})
	return abuser
}

func TestRegisterShowsWhereEachSiteCameFrom(t *testing.T) {
	p, host, mux, cookie := setupProvAdmin(t)
	abuseScene(t, p, host)
	w := getAs(mux, "/account/admin", cookie)
	if w.Code != 200 {
		t.Fatalf("register = %d", w.Code)
	}
	body := w.Body.String()
	for _, want := range []string{
		`from <a href="/account/admin?q=198.51.100.23"`,
		`api · token`,
		`/account/admin/sites/phishaaaaaaaaaaaaaaaaaaaaa/trail`,
		`from <a href="/account/admin?q=192.0.2.1"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("register lacks %q", want)
		}
	}
}

// One address, several identities: the address search lists every site whose
// log names it and every account seen there.
func TestRegisterAddressSearch(t *testing.T) {
	p, host, mux, cookie := setupProvAdmin(t)
	abuser := abuseScene(t, p, host)

	body := getAs(mux, "/account/admin?q=198.51.100.23", cookie).Body.String()
	if !strings.Contains(body, "phishaaaaaaaaaaaaaaaaaaaaa") || !strings.Contains(body, "dropaaaaaaaaaaaaaaaaaaaaaa") {
		t.Fatal("the address search misses a site created from the address")
	}
	if strings.Contains(body, "fineaaaaaaaaaaaaaaaaaaaaaa\"") || strings.Contains(body, ">fineaaaaaaaaaaaaaaaaaaaaaa<") {
		t.Fatal("the address search lists a site from another address")
	}
	if !strings.Contains(body, "Seen from 198.51.100.23: 2 site(s), 1 account(s)") || !strings.Contains(body, "/account/admin/accounts/"+abuser.ID) {
		t.Fatalf("accounts panel wrong:\n%s", body)
	}

	// The sign-up address finds the account through its own log, and a range
	// finds it too.
	for _, q := range []string{"203.0.113.7", "203.0.113.0%2F24"} {
		body := getAs(mux, "/account/admin?q="+q, cookie).Body.String()
		if !strings.Contains(body, "mflea@example.com") || !strings.Contains(body, "1 account(s)") {
			t.Errorf("q=%s: account not found through its sign-up", q)
		}
		if strings.Contains(body, "&lt;script&gt;alert(1)&lt;/script&gt;") == false && strings.Contains(body, "Mozilla/5.0") {
			t.Errorf("q=%s: the user agent was not escaped", q)
		}
		if strings.Contains(body, "<script>alert(1)</script>") {
			t.Fatalf("q=%s: a client-supplied user agent reached the page unescaped", q)
		}
	}

	// An ordinary text query still searches names, not addresses.
	if body := getAs(mux, "/account/admin?q=mflea", cookie).Body.String(); strings.Contains(body, "Seen from") || !strings.Contains(body, "phishaaaaaaaaaaaaaaaaaaaaa") {
		t.Error("a text query was taken for an address")
	}
}

// The trail shows the site's log and its owner's: the sign-up, and a token
// minted 12 seconds after it, flagged.
func TestTrailAndAccountPages(t *testing.T) {
	p, host, mux, cookie := setupProvAdmin(t)
	abuser := abuseScene(t, p, host)

	w := getAs(mux, "/account/admin/sites/phishaaaaaaaaaaaaaaaaaaaaa/trail", cookie)
	if w.Code != 200 {
		t.Fatalf("trail = %d", w.Code)
	}
	body := w.Body.String()
	for _, want := range []string{"bug.html", "python-requests/2.32", "198.51.100.23", "mflea@example.com", "minted 12 s after sign-up", "Sign-up"} {
		if !strings.Contains(body, want) {
			t.Errorf("trail lacks %q", want)
		}
	}
	if strings.Contains(body, "<script>alert(1)</script>") {
		t.Fatal("a client-supplied user agent reached the trail unescaped")
	}
	// The console's own policy: no script but the dashboard's hashed copy button.
	if got, want := w.Header().Get("Content-Security-Policy"), getAs(mux, "/account/admin", cookie).Header().Get("Content-Security-Policy"); got == "" || got != want {
		t.Errorf("trail CSP = %q, register's = %q", got, want)
	}

	w = getAs(mux, "/account/admin/accounts/"+abuser.ID, cookie)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "minted 12 s after sign-up") {
		t.Fatalf("account page = %d", w.Code)
	}
	if w := getAs(mux, "/account/admin/sites/zzzzzzzzzzzzzzzzzzzzzzzzzz/trail", cookie); w.Code != 404 {
		t.Errorf("trail of an unknown site = %d", w.Code)
	}
}

// Like the register, the new pages do not exist for anyone but the operator.
func TestProvenancePagesAre404ForOthers(t *testing.T) {
	p, host, mux, _ := setupProvAdmin(t)
	abuser := abuseScene(t, p, host)
	other, _ := adminUser(t, p, mux, "nobody@example.com", "free")
	for _, path := range []string{"/account/admin/sites/phishaaaaaaaaaaaaaaaaaaaaa/trail", "/account/admin/accounts/" + abuser.ID} {
		if w := getAs(mux, path, other); w.Code != 404 {
			t.Errorf("%s as a non-admin = %d", path, w.Code)
		}
		if w := getAs(mux, path, nil); w.Code != 404 {
			t.Errorf("%s anonymous = %d", path, w.Code)
		}
	}
}

// A creation is in the site's log and mirrored into the owner's: the address
// search shows it once.
func TestAddressSearchShowsAMirroredCreationOnce(t *testing.T) {
	p, host, mux, cookie := setupProvAdmin(t)
	acc, _ := p.accounts.CreateOAuth(account.OIDCProv, "sub-x", "x@example.com", true, "free")
	at := time.Now().UTC()
	host.sites.infos["siteaaaaaaaaaaaaaaaaaaaaaa"] = ext.SiteInfo{ViewID: "siteaaaaaaaaaaaaaaaaaaaaaa", Owner: acc.ID, CreatedAt: at}
	e := provenance.Entry{Time: at, Action: provenance.ActionCreate, Surface: provenance.SurfaceAPI, Account: acc.ID, IP: "203.0.113.9"}
	host.sites.RecordSiteProvenance("siteaaaaaaaaaaaaaaaaaaaaaa", e)
	m := e
	m.Action, m.Site = provenance.ActionSiteCreate, "siteaaaaaaaaaaaaaaaaaaaaaa"
	p.RecordAccountProvenance(acc.ID, m)

	body := getAs(mux, "/account/admin?q=203.0.113.9", cookie).Body.String()
	i := strings.Index(body, `class="ipseen"`)
	j := strings.Index(body, `class="reg"`)
	if i < 0 || j < i {
		t.Fatal("no accounts panel")
	}
	panel := body[i:j]
	if n := strings.Count(panel, `class="ev"`); n != 1 {
		t.Fatalf("the creation is listed %d times:\n%s", n, panel)
	}
}
