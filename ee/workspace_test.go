//go:build ee

package ee

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/ittrail/sitebin.io/ee/account"
	"github.com/ittrail/sitebin.io/internal/ext"
	"github.com/ittrail/sitebin.io/internal/store"
	"github.com/ittrail/sitebin.io/web"
)

func chooseUI(t *testing.T, p *provider, mux http.Handler, acc *account.Account, cookie *http.Cookie, ui string) *httptest.ResponseRecorder {
	t.Helper()
	req := form(url.Values{"csrf": {p.csrf(acc)}, "ui": {ui}})
	req.URL.Path = "/account/ui"
	req.AddCookie(cookie)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	return w
}

// The choice is stored on the account — so it holds on every device and at
// every sign-in — and it switches back as easily as it switched.
func TestWorkspaceChoiceIsStoredOnTheAccountAndSwitchesBack(t *testing.T) {
	p, _, mux, acc, cookie := namedAccount(t)

	// classic is the default: nobody's interface changes under them
	w := getAs(mux, "/account", cookie)
	if !strings.Contains(w.Body.String(), "Your account") {
		t.Fatalf("a new account should see the classic dashboard:\n%s", w.Body)
	}
	if !strings.Contains(w.Body.String(), `action="/account/ui"`) {
		t.Error("the classic dashboard offers no way into the workspace")
	}

	if w := chooseUI(t, p, mux, acc, cookie, "workspace"); w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/account" {
		t.Fatalf("choose workspace = %d %q", w.Code, w.Header().Get("Location"))
	}
	stored, _ := p.accounts.ByID(acc.ID)
	if stored.UI != uiWorkspace {
		t.Fatalf("stored UI = %q, want workspace", stored.UI)
	}
	w = getAs(mux, "/account", cookie)
	if strings.Contains(w.Body.String(), "Your account") {
		t.Error("a workspace account still got the classic dashboard")
	}
	if csp := w.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "script-src 'self'") {
		t.Errorf("the workspace page cannot run its scripts: CSP %q", csp)
	}

	chooseUI(t, p, mux, acc, cookie, "classic")
	stored, _ = p.accounts.ByID(acc.ID)
	if stored.UI != uiClassic {
		t.Fatalf("stored UI = %q after switching back", stored.UI)
	}
	if w := getAs(mux, "/account", cookie); !strings.Contains(w.Body.String(), "Your account") {
		t.Error("switching back did not bring the classic dashboard back")
	}
}

func TestWorkspaceChoiceRefusesForgeryAndUnknownValues(t *testing.T) {
	p, _, mux, acc, cookie := namedAccount(t)
	req := form(url.Values{"ui": {"workspace"}}) // no csrf
	req.URL.Path = "/account/ui"
	req.AddCookie(cookie)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Errorf("no csrf = %d, want 403", w.Code)
	}
	if w := chooseUI(t, p, mux, acc, cookie, "neon"); w.Code != http.StatusBadRequest {
		t.Errorf("unknown ui = %d, want 400", w.Code)
	}
	stored, _ := p.accounts.ByID(acc.ID)
	if stored.UI != "" {
		t.Errorf("a refused choice was stored: %q", stored.UI)
	}
}

// The workspace is the default: an account that never chose lands in it, at
// sign-up and at every later visit; "Classic interface" is stored as the
// account's own choice and outlasts the default; an instance may keep the
// classic pages as its default (SITEBIN_DEFAULT_UI=classic).
func TestWorkspaceIsTheDefaultUntilAnAccountChoosesClassic(t *testing.T) {
	p, _, mux, acc, cookie := namedAccount(t)
	p.cfg.DefaultUI = uiWorkspace
	isWorkspace := func() bool {
		return strings.Contains(getAs(mux, "/account", cookie).Body.String(), `class="ws ws-app"`)
	}
	if !isWorkspace() {
		t.Fatal("an account without a choice should land in the workspace")
	}
	if w := chooseUI(t, p, mux, acc, cookie, "classic"); w.Code != http.StatusSeeOther {
		t.Fatalf("choose classic = %d %s", w.Code, w.Body)
	}
	stored, _ := p.accounts.ByID(acc.ID)
	if stored.UI != uiClassic {
		t.Fatalf("the classic choice is stored as %q, want %q", stored.UI, uiClassic)
	}
	if isWorkspace() {
		t.Fatal("an account that chose classic must keep the classic pages under the workspace default")
	}
	// back to no choice: the instance's default decides, either way
	if err := p.accounts.Update(stored, func(a *account.Account) error { a.UI = ""; return nil }); err != nil {
		t.Fatal(err)
	}
	if !isWorkspace() {
		t.Fatal("no choice under the workspace default should be the workspace")
	}
	p.cfg.DefaultUI = uiClassic
	if isWorkspace() {
		t.Fatal("no choice under SITEBIN_DEFAULT_UI=classic should be the classic pages")
	}
}

func postJSONMode(t *testing.T, mux http.Handler, path string, v url.Values, cookie *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	req := form(v)
	req.URL.Path = path
	req.Header.Set(uiHeader, "json")
	req.AddCookie(cookie)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	return w
}

// The workspace posts to the same routes as the classic forms; asked for
// JSON, a route answers its redirect and its message as JSON, and a refusal
// keeps its status.
func TestJSONModeAnswersTheSameRoutes(t *testing.T) {
	p, _, mux, acc, cookie := namedAccount(t)

	w := postJSONMode(t, mux, "/account/tokens", url.Values{"csrf": {p.csrf(acc)}, "name": {"ci"}}, cookie)
	var a uiAnswer
	if err := json.Unmarshal(w.Body.Bytes(), &a); err != nil || w.Code != 200 {
		t.Fatalf("create token in json mode = %d %s", w.Code, w.Body)
	}
	if a.Message == nil || !strings.HasPrefix(a.Message.Detail, "sbp_") {
		t.Fatalf("the secret shown once is missing from the answer: %+v", a)
	}
	toks, _ := p.accounts.ListTokens(acc)
	if len(toks) != 1 {
		t.Fatalf("tokens = %d", len(toks))
	}

	w = postJSONMode(t, mux, "/account/tokens/"+toks[0].ID+"/delete", url.Values{"csrf": {p.csrf(acc)}}, cookie)
	a = uiAnswer{}
	if err := json.Unmarshal(w.Body.Bytes(), &a); err != nil || !a.OK || a.Redirect != "/account" {
		t.Fatalf("revoke in json mode = %d %s", w.Code, w.Body)
	}

	// A refusal keeps its status and is not dressed up as success.
	w = postJSONMode(t, mux, "/account/tokens", url.Values{"csrf": {"forged"}}, cookie)
	if w.Code != http.StatusForbidden {
		t.Errorf("forged csrf in json mode = %d, want 403", w.Code)
	}

	// Without the header the same route still redirects.
	req := form(url.Values{"csrf": {p.csrf(acc)}, "ui": {"classic"}})
	req.URL.Path = "/account/ui"
	req.AddCookie(cookie)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusSeeOther {
		t.Errorf("classic form post = %d, want 303", w.Code)
	}
}

// A server-rendered page opens inside the workspace for a workspace account,
// and as before for everyone else.
func TestServerPagesOpenInsideTheWorkspaceFrame(t *testing.T) {
	p, _, mux, acc, cookie := namedAccount(t)
	if w := getAs(mux, "/account/tags", cookie); strings.Contains(w.Body.String(), "ws-frame") {
		t.Fatal("a classic account got the workspace frame")
	}
	chooseUI(t, p, mux, acc, cookie, "workspace")
	w := getAs(mux, "/account/tags", cookie)
	if !strings.Contains(w.Body.String(), "ws-frame") || !strings.Contains(w.Body.String(), uiURL("shell.js")) {
		t.Fatalf("the tags page is not framed for a workspace account:\n%s", w.Body)
	}
	if csp := w.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "script-src 'self'") {
		t.Errorf("the framed page cannot run the shell: %q", csp)
	}
}

func TestWorkspaceAssetsAreServedVersioned(t *testing.T) {
	_, _, mux := setupAccounts(t)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", uiURL("app.js"), nil))
	if w.Code != 200 || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/javascript") {
		t.Fatalf("app.js = %d %q", w.Code, w.Header().Get("Content-Type"))
	}
	if !strings.Contains(w.Header().Get("Cache-Control"), "immutable") {
		t.Error("a versioned asset should be cached for good")
	}
	for _, name := range []string{"app.html", "nope.js", "..%2fworkspace.go"} {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("GET", "/account/ui/"+name, nil))
		if w.Code != http.StatusNotFound {
			t.Errorf("%s = %d, want 404", name, w.Code)
		}
	}
}

// The state is the owner's view of their rows: never who placed a lock, the
// abuse findings or the CSP report data — only the register shows those.
func TestWorkspaceStateIsTheOwnersView(t *testing.T) {
	p, host, mux, acc, cookie := namedAccount(t)
	if err := linkOwned(p, acc, ownedSite); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	host.sites.infos[ownedSite] = ext.SiteInfo{
		ViewID: ownedSite, Owner: acc.ID, Mode: "webserver", Name: "Docs",
		ViewURL: "http://" + ownedSite + ".sitebin.example", EditURL: "http://sitebin.example/e/one",
		UpdatedAt: now, Tags: []string{"Client"},
		Locked:     &ext.SiteLock{At: now, Reason: "evidence", By: "operator-secret-id"},
		Findings:   []ext.ScanFinding{{Rule: "phish-kit", Path: "index.html"}},
		Violations: 3, Blocked: []string{"evil.example"},
	}
	w := getAs(mux, "/account/api/state", cookie)
	if w.Code != 200 {
		t.Fatalf("state = %d %s", w.Code, w.Body)
	}
	body := w.Body.String()
	for _, leak := range []string{"operator-secret-id", "phish-kit", "evil.example", "Violations", "Findings"} {
		if strings.Contains(body, leak) {
			t.Errorf("the owner's state carries %q", leak)
		}
	}
	var st wsState
	if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	if len(st.Sites) != 1 || st.Sites[0].Name != "Docs" || st.Sites[0].Locked == nil || st.Sites[0].Locked.Reason != "evidence" {
		t.Fatalf("sites = %+v", st.Sites)
	}
	if st.Account.CSRF != p.csrf(acc) || st.Account.Email != acc.Email {
		t.Errorf("account = %+v", st.Account)
	}
	if len(st.Sites[0].Tags) != 1 || st.Sites[0].Tags[0].Name != "Client" {
		t.Errorf("tags = %+v", st.Sites[0].Tags)
	}

	// signed out: no state
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/account/api/state", nil))
	if w.Code != http.StatusUnauthorized {
		t.Errorf("signed-out state = %d", w.Code)
	}
}

// The site page of the workspace is served only to a signed-in account that
// chose it; a password visitor and a classic account get edit.html.
func TestEditPageSeamServesTheWorkspaceOnlyToItsAccounts(t *testing.T) {
	p, _, mux, acc, cookie := namedAccount(t)
	req := httptest.NewRequest("GET", "/e/whatever", nil)
	if p.ServeEditPage(httptest.NewRecorder(), req) {
		t.Fatal("an anonymous visitor got the workspace site page")
	}
	req.AddCookie(cookie)
	if p.ServeEditPage(httptest.NewRecorder(), req) {
		t.Fatal("a classic account got the workspace site page")
	}
	chooseUI(t, p, mux, acc, cookie, "workspace")
	w := httptest.NewRecorder()
	if !p.ServeEditPage(w, req) {
		t.Fatal("a workspace account did not get the workspace site page")
	}
	if csp := w.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "script-src 'self'") || !strings.Contains(csp, "frame-ancestors 'none'") {
		t.Errorf("site page CSP = %q", csp)
	}
}

func TestActivityIsForTheSitesOwnerAndAdminsOnly(t *testing.T) {
	p, host, mux, acc, cookie := namedAccount(t)
	if err := linkOwned(p, acc, ownedSite); err != nil {
		t.Fatal(err)
	}
	host.sites.infos[ownedSite] = ext.SiteInfo{ViewID: ownedSite, Owner: acc.ID, Mode: "webserver"}
	if w := getAs(mux, "/account/api/sites/"+ownedSite+"/activity", cookie); w.Code != 200 {
		t.Fatalf("owner activity = %d %s", w.Code, w.Body)
	}
	// a stranger learns nothing
	const foreign = "zzzdefghijklmnopqrstuvwxyz"
	host.sites.infos[foreign] = ext.SiteInfo{ViewID: foreign, Owner: "someone-else", Mode: "webserver"}
	if w := getAs(mux, "/account/api/sites/"+foreign+"/activity", cookie); w.Code != http.StatusNotFound {
		t.Errorf("foreign activity = %d, want 404", w.Code)
	}
}

func TestConsoleJSONIsForTheOperatorOnly(t *testing.T) {
	_, _, mux, _, cookie := namedAccount(t)
	for _, path := range []string{"/account/api/admin/register", "/account/api/admin/reports", "/account/api/admin/health"} {
		if w := getAs(mux, path, cookie); w.Code != http.StatusNotFound {
			t.Errorf("%s for a customer = %d, want 404", path, w.Code)
		}
	}
}

// The workspace's site page runs the core's edit.js, which finds every
// control by its id. A control added to edit.html and not to site.html would
// silently do nothing in the workspace, so every id must be in both.
func TestWorkspaceSitePageCarriesEveryEditPageID(t *testing.T) {
	edit, err := fs.ReadFile(web.Assets, "static/edit.html")
	if err != nil {
		t.Fatal(err)
	}
	ids := regexp.MustCompile(`\bid="([^"]+)"`)
	have := map[string]bool{}
	for _, m := range ids.FindAllStringSubmatch(string(uiAssets["site.html"].body), -1) {
		have[m[1]] = true
	}
	for _, m := range ids.FindAllStringSubmatch(string(edit), -1) {
		if !have[m[1]] {
			t.Errorf("edit.html has id %q, ee/ui/site.html does not: the workspace's site page would miss that control", m[1])
		}
	}
}

// The workspace's site page loads the same three scripts as edit.html.
func TestWorkspaceSitePageRunsTheEditPageScripts(t *testing.T) {
	page := string(uiAssets["site.html"].body)
	for _, s := range []string{"/_sitebin/assets/static/edit.js", "/_sitebin/assets/static/edit-git.js", "/_sitebin/assets/static/edit-analytics.js"} {
		if !strings.Contains(page, s) {
			t.Errorf("site.html does not load %s", s)
		}
	}
}

// Signing in through the identity provider lands a workspace account in the
// workspace: the choice is the account's, so it holds at every SSO sign-in,
// and the callback itself is unchanged by the workspace's middleware.
func TestSSOSignInOfAWorkspaceAccountLandsInTheWorkspace(t *testing.T) {
	ti := newTestIssuer(t)
	var nonce string
	ti.srv.Config.Handler.(*http.ServeMux).HandleFunc("POST /token", func(w http.ResponseWriter, r *http.Request) {
		id := ti.sign(t, func(c map[string]any) {
			c["aud"] = "sitebin-app"
			c["nonce"] = nonce
			c["typ"] = "ID"
		}, nil)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"access_token": "at", "token_type": "Bearer", "expires_in": 60, "id_token": id})
	})
	t.Setenv("SITEBIN_ACCOUNT_MODE", "accounts")
	t.Setenv("SITEBIN_OAUTH_OIDC_ISSUER", ti.URL())
	t.Setenv("SITEBIN_OAUTH_OIDC_CLIENT_ID", "sitebin-app")
	t.Setenv("SITEBIN_OAUTH_OIDC_CLIENT_SECRET", "s3cret")
	p := newProvider()
	host := &fakeHost{dir: t.TempDir(), sites: &fakeSites{infos: map[string]ext.SiteInfo{}}}
	if err := initT(t, p, host); err != nil {
		t.Fatal(err)
	}
	mux := serveMux(p)

	signIn := func(n string) *http.Cookie {
		t.Helper()
		state := "state-" + n
		nonce = "nonce-" + n
		cookie := p.oauthSigner().Sign("oidc|"+state+"|"+nonce, time.Now(), 10*time.Minute)
		req := httptest.NewRequest("GET", "/account/auth/oidc/callback?code=c&state="+state, nil)
		req.AddCookie(&http.Cookie{Name: oauthCookie, Value: cookie})
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/account" {
			t.Fatalf("callback = %d %q %s", w.Code, w.Header().Get("Location"), w.Body)
		}
		return sessionCookie(t, w)
	}
	first := signIn("1")
	if w := getAs(mux, "/account", first); !strings.Contains(w.Body.String(), "Your account") {
		t.Fatal("a new SSO account should start on the classic dashboard")
	}
	acc, err := p.accounts.ByOAuth(account.OIDCProv, testSubject)
	if err != nil {
		t.Fatal(err)
	}
	if w := chooseUI(t, p, mux, acc, first, "workspace"); w.Code != http.StatusSeeOther {
		t.Fatalf("choose workspace = %d", w.Code)
	}
	// A fresh sign-in, as on another device: the choice comes with the account.
	second := signIn("2")
	w := getAs(mux, "/account", second)
	if strings.Contains(w.Body.String(), "Your account") || !strings.Contains(w.Header().Get("Content-Security-Policy"), "script-src 'self'") {
		t.Fatalf("the SSO sign-in of a workspace account did not land in the workspace:\n%s", w.Body)
	}
}

// The operator's Health panel states the last complete backup from the note
// `sitebin backup` leaves, and the disk's size beside its free space.
func TestHealthReportsTheLastBackupAndTheDisk(t *testing.T) {
	p, _, mux := setupAdmin(t, "boss@example.com")
	cookie, _ := adminUser(t, p, mux, "boss@example.com", "admin")
	read := func() map[string]any {
		w := getAs(mux, "/account/api/admin/health", cookie)
		if w.Code != http.StatusOK {
			t.Fatalf("health = %d %s", w.Code, w.Body)
		}
		var h map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &h); err != nil {
			t.Fatal(err)
		}
		return h
	}
	if h := read(); h["backup"] != nil {
		t.Errorf("a backup before any ran: %v", h["backup"])
	}
	at := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)
	if err := store.WriteBackupMark(p.host.DataDir(), store.BackupMark{At: at, Entries: 12, Bytes: 3456}); err != nil {
		t.Fatal(err)
	}
	h := read()
	b, _ := h["backup"].(map[string]any)
	if b == nil || b["bytes"] != float64(3456) || b["entries"] != float64(12) {
		t.Fatalf("backup = %v", h["backup"])
	}
	if got, _ := time.Parse(time.RFC3339, b["at"].(string)); !got.Equal(at) {
		t.Errorf("backup at = %v, want %v", b["at"], at)
	}
	if total, _ := h["data_total"].(float64); total <= 0 {
		t.Errorf("data_total = %v, want the volume's size", h["data_total"])
	}
}
