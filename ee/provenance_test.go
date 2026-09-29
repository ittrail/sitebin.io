//go:build ee

package ee

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ittrail/sitebin.io/ee/account"
	"github.com/ittrail/sitebin.io/internal/ext"
	"github.com/ittrail/sitebin.io/internal/provenance"
)

// provSites is fakeSites with the core's optional provenance seam.
type provSites struct {
	*fakeSites
	mu   sync.Mutex
	logs map[string][]provenance.Entry
}

func (s *provSites) SiteProvenance(id string) ([]provenance.Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.infos[id]; !ok {
		return nil, ext.ErrSiteGone
	}
	return append([]provenance.Entry(nil), s.logs[id]...), nil
}

func (s *provSites) RecordSiteProvenance(id string, e provenance.Entry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.logs == nil {
		s.logs = map[string][]provenance.Entry{}
	}
	s.logs[id] = append(s.logs[id], e)
}

func (s *provSites) SitesSeenFrom(m provenance.Match) (map[string][]provenance.Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string][]provenance.Entry{}
	for id, es := range s.logs {
		if hit := m.Filter(es); len(hit) > 0 {
			out[id] = hit
		}
	}
	return out, nil
}

type provHost struct {
	*fakeHost
	sites *provSites
}

func (h *provHost) Sites() ext.SiteService { return h.sites }

// setupProvAccounts is setupAccounts with a SiteService that keeps site logs.
func setupProvAccounts(t *testing.T) (*provider, *provHost, http.Handler) {
	t.Helper()
	t.Setenv("SITEBIN_ACCOUNT_MODE", "accounts")
	p := newProvider()
	fs := &fakeSites{infos: map[string]ext.SiteInfo{}}
	host := &provHost{fakeHost: &fakeHost{dir: t.TempDir(), sites: fs}, sites: &provSites{fakeSites: fs}}
	if err := p.Init(host); err != nil {
		t.Fatalf("Init: %v", err)
	}
	mux := http.NewServeMux()
	for pat, h := range p.PublicRoutes() {
		mux.Handle(pat, h)
	}
	return p, host, mux
}

func accountLog(t *testing.T, p *provider, id string) []provenance.Entry {
	t.Helper()
	es, err := p.accounts.Provenance(id)
	if err != nil {
		t.Fatal(err)
	}
	return es
}

func from(r *http.Request, ip, ua string) *http.Request {
	r.Header.Set("X-Forwarded-For", ip)
	r.Header.Set("User-Agent", ua)
	return r
}

// Sign-up, sign-in and every token minted are recorded with the address the
// request came from and its client.
func TestAccountLogRecordsSignupSigninAndTokens(t *testing.T) {
	p, _, mux := setupProvAccounts(t)

	req := from(form(url.Values{"email": {"billy@example.com"}, "password": {"password123"}}), "10.0.0.1, 203.0.113.7", "Mozilla/5.0 Signup")
	req.URL.Path = "/account/signup"
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("signup = %d %s", w.Code, w.Body)
	}
	cookie := sessionCookie(t, w)
	acc, _ := p.accounts.ByEmail("billy@example.com")

	req = from(form(url.Values{"csrf": {p.csrf(acc)}, "name": {"testing"}}), "203.0.113.7", "Mozilla/5.0 Signup")
	req.URL.Path = "/account/tokens"
	req.AddCookie(cookie)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("token = %d %s", w.Code, w.Body)
	}

	req = from(form(url.Values{"email": {"billy@example.com"}, "password": {"password123"}}), "198.51.100.9", "curl/8")
	req.URL.Path = "/account/login"
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("login = %d %s", w.Code, w.Body)
	}

	es := accountLog(t, p, acc.ID)
	if len(es) != 3 {
		t.Fatalf("want signup, token-mint, signin; got %+v", es)
	}
	if e := es[0]; e.Action != provenance.ActionSignup || e.Surface != provenance.SurfaceLocal || e.IP != "203.0.113.7" || e.UA != "Mozilla/5.0 Signup" || e.Account != acc.ID {
		t.Errorf("signup: %+v", e)
	}
	if e := es[1]; e.Action != provenance.ActionTokenMint || e.IP != "203.0.113.7" || !strings.Contains(e.Detail, `"testing"`) || !strings.Contains(e.Detail, "sbp_") {
		t.Errorf("token-mint: %+v", e)
	}
	if strings.Count(es[1].Detail, "sbp_") != 1 || len(es[1].Detail) > 40 {
		t.Errorf("the token's secret may not be recorded, only its prefix: %q", es[1].Detail)
	}
	if e := es[2]; e.Action != provenance.ActionSignin || e.IP != "198.51.100.9" || e.UA != "curl/8" {
		t.Errorf("signin: %+v", e)
	}
}

// The dashboard's rename and password rotation go to the site's log; its
// delete goes to the account's, since the site's own log goes with it.
func TestDashboardWritesAreRecorded(t *testing.T) {
	p, host, mux := setupProvAccounts(t)
	cookie := signedUpUser(t, mux)
	acc, _ := p.accounts.ByEmail("user@example.com")
	host.sites.site("siteaaaaaaaaaaaaaaaaaaaaaa")
	p.accounts.LinkSite(acc, "siteaaaaaaaaaaaaaaaaaaaaaa")

	post := func(path string, v url.Values) {
		t.Helper()
		v.Set("csrf", p.csrf(acc))
		req := from(form(v), "203.0.113.20", "Mozilla/5.0 Dash")
		req.URL.Path = path
		req.AddCookie(cookie)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code >= 400 {
			t.Fatalf("%s = %d %s", path, w.Code, w.Body)
		}
	}
	post("/account/sites/siteaaaaaaaaaaaaaaaaaaaaaa/name", url.Values{"name": {"Launch"}})
	post("/account/sites/siteaaaaaaaaaaaaaaaaaaaaaa/rotate", url.Values{})
	logs, _ := host.sites.SiteProvenance("siteaaaaaaaaaaaaaaaaaaaaaa")
	if len(logs) != 2 || logs[0].Action != provenance.ActionRename || logs[1].Action != provenance.ActionRotatePassword ||
		logs[0].Surface != provenance.SurfaceDashboard || logs[0].Account != acc.ID || logs[0].IP != "203.0.113.20" {
		t.Fatalf("site log: %+v", logs)
	}

	post("/account/sites/siteaaaaaaaaaaaaaaaaaaaaaa/delete", url.Values{})
	es := accountLog(t, p, acc.ID)
	last := es[len(es)-1]
	if last.Action != provenance.ActionSiteDelete || last.Site != "siteaaaaaaaaaaaaaaaaaaaaaa" || last.IP != "203.0.113.20" {
		t.Fatalf("account log after delete: %+v", es)
	}
}

// The purge's evidence holds, precisely: a suspended account's log and that
// of an account owning a locked site outlive the 90 days only up to the lock
// retention (heldBefore); an evidence hold on one of the account's locked
// sites keeps the whole log; with no retention (heldBefore zero) a held log
// is kept whole.
func TestAccountPurgeKeepsEvidenceHolds(t *testing.T) {
	p, host, _ := setupProvAccounts(t)
	now := time.Now().UTC()
	day := 24 * time.Hour
	before, heldBefore := now.Add(-provenance.Retention), now.Add(-180*day)
	mk := func(email string) *account.Account {
		a, err := p.accounts.CreateLocal(email, "h", "")
		if err != nil {
			t.Fatal(err)
		}
		// older than the lock retention, and between it and the 90 days
		p.accounts.RecordProvenance(a.ID, provenance.Entry{Time: now.Add(-181 * day), Action: provenance.ActionSignup, IP: "203.0.113.7"})
		p.accounts.RecordProvenance(a.ID, provenance.Entry{Time: now.Add(-91 * day), Action: provenance.ActionSignin, IP: "203.0.113.8"})
		return a
	}
	lockSite := func(acc *account.Account, id string, hold bool) {
		l := &ext.SiteLock{At: now.Add(-10 * day), By: ext.LockByAdmin}
		if hold {
			l.Hold = &ext.LockHold{At: now, By: "acct-admin"}
		}
		host.sites.infos[id] = ext.SiteInfo{ViewID: id, Locked: l}
		p.accounts.LinkSite(acc, id)
	}
	suspend := func(acc *account.Account) {
		p.accounts.Update(acc, func(a *account.Account) error { a.SuspendedAt = &now; return nil })
	}
	plain, suspended, holder, caseOpen := mk("a@example.com"), mk("b@example.com"), mk("c@example.com"), mk("d@example.com")
	suspend(suspended)
	lockSite(holder, "lockedaaaaaaaaaaaaaaaaaaaa", false)
	suspend(caseOpen)
	lockSite(caseOpen, "lockedbbbbbbbbbbbbbbbbbbbb", false)
	lockSite(caseOpen, "heldcccccccccccccccccccccc", true)

	p.PurgeProvenance(before, heldBefore)
	if es := accountLog(t, p, plain.ID); len(es) != 0 {
		t.Errorf("plain account not purged: %+v", es)
	}
	for name, acc := range map[string]*account.Account{"suspended account": suspended, "account owning a locked site": holder} {
		es := accountLog(t, p, acc.ID)
		if len(es) != 1 || es[0].Action != provenance.ActionSignin {
			t.Errorf("%s: want only the entry younger than the lock retention, got %+v", name, es)
		}
	}
	if es := accountLog(t, p, caseOpen.ID); len(es) != 2 {
		t.Errorf("an account with a held site lost entries: %+v", es)
	}

	// Locked sites kept forever (retention 0): a held log is kept whole.
	forever := mk("e@example.com")
	suspend(forever)
	p.PurgeProvenance(before, time.Time{})
	if es := accountLog(t, p, forever.ID); len(es) != 2 {
		t.Errorf("retention 0: the suspended account's log was purged: %+v", es)
	}
}

// An account created from an MCP access token records its sign-up with the
// address the first request came from.
func TestTokenProvisioningRecordsTheSignup(t *testing.T) {
	ti := newTestIssuer(t)
	consent := newConsentStack(t)
	p, _ := setupMCPOAuthInstance(t, ti, stackStub(consent))
	tok := ti.sign(t, nil, nil)

	req := from(bearerRequest(tok, true), "192.0.2.44", "Claude-User")
	cred, ok := p.BearerCredential(req)
	if !ok {
		t.Fatal("a newcomer with complete consent was refused")
	}
	es := accountLog(t, p, cred.AccountID)
	if len(es) != 1 || es[0].Action != provenance.ActionSignup || es[0].Surface != provenance.SurfaceMCP || es[0].IP != "192.0.2.44" || es[0].UA != "Claude-User" {
		t.Fatalf("token sign-up: %+v", es)
	}
	// A later request is no second sign-up.
	p.BearerCredential(from(bearerRequest(tok, true), "192.0.2.45", "Claude-User"))
	if es := accountLog(t, p, cred.AccountID); len(es) != 1 {
		t.Fatalf("a second request recorded a sign-up again: %+v", es)
	}
}

// The browser sign-in through the identity provider: the first is the
// sign-up, every later one a sign-in.
func TestOIDCCallbackRecordsSignupThenSignin(t *testing.T) {
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
	if err := p.Init(host); err != nil {
		t.Fatal(err)
	}
	mux := serveMux(p)

	signIn := func(ip string) {
		t.Helper()
		state := "state1"
		nonce = "nonce-" + ip
		cookie := p.oauthSigner().Sign("oidc|"+state+"|"+nonce, time.Now(), 10*time.Minute)
		req := from(httptest.NewRequest("GET", "/account/auth/oidc/callback?code=c&state="+state, nil), ip, "Mozilla/5.0 SSO")
		req.AddCookie(&http.Cookie{Name: oauthCookie, Value: cookie})
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code != http.StatusSeeOther {
			t.Fatalf("callback = %d %s", w.Code, w.Body)
		}
	}
	signIn("203.0.113.61")
	signIn("203.0.113.62")
	acc, err := p.accounts.ByOAuth(account.OIDCProv, testSubject)
	if err != nil {
		t.Fatal(err)
	}
	es := accountLog(t, p, acc.ID)
	if len(es) != 2 || es[0].Action != provenance.ActionSignup || es[0].Surface != provenance.SurfaceOIDC || es[0].IP != "203.0.113.61" ||
		es[1].Action != provenance.ActionSignin || es[1].IP != "203.0.113.62" {
		t.Fatalf("sso log: %+v", es)
	}
}

// The GDPR export carries the account's own log and, from its sites, the
// entries that were this account's — not what the edit password did there,
// which nothing says was this person.
func TestGDPRExportCarriesTheAccountsProvenance(t *testing.T) {
	t.Setenv("SITEBIN_ACCOUNT_MODE", "tiers")
	t.Setenv("SITEBIN_TIERS", stackTiersJSON)
	t.Setenv("SITEBIN_DEFAULT_TIER", "free")
	t.Setenv("SITEBIN_OAUTH_OIDC_ISSUER", "https://auth.example.com/realms/saas-stack")
	t.Setenv("SITEBIN_OAUTH_OIDC_CLIENT_ID", "sitebin-app")
	t.Setenv("SITEBIN_STACK_GDPR_SECRET", testGDPRSecret)
	p := newProvider()
	fs := &fakeSites{infos: map[string]ext.SiteInfo{}}
	host := &provHost{fakeHost: &fakeHost{dir: t.TempDir(), sites: fs}, sites: &provSites{fakeSites: fs}}
	if err := p.Init(host); err != nil {
		t.Fatal(err)
	}
	mux := serveMux(p)
	acc, _ := stackUser(t, p, host.fakeHost, "11111111-1111-4111-8111-111111111111", "subject@example.com")

	p.RecordAccountProvenance(acc.ID, provenance.Entry{Action: provenance.ActionSignup, Surface: provenance.SurfaceOIDC, Account: acc.ID, IP: "203.0.113.7"})
	host.sites.RecordSiteProvenance("aaaaaaaaaaaaaaaaaaaaaaaaaa", provenance.Entry{Action: provenance.ActionCreate, Account: acc.ID, IP: "203.0.113.7"})
	host.sites.RecordSiteProvenance("aaaaaaaaaaaaaaaaaaaaaaaaaa", provenance.Entry{Action: provenance.ActionUpload, Auth: provenance.AuthPassword, IP: "198.51.100.66"})

	w := serve(mux, stackOrder(gdprExportPath, testGDPRSecret, time.Now(), `{"userId":"`+acc.OAuthSubject+`"}`))
	if w.Code != 200 {
		t.Fatalf("export = %d %s", w.Code, w.Body)
	}
	var out struct {
		Provenance *struct {
			Account []provenance.Entry `json:"account"`
			Sites   []struct {
				Site    string             `json:"site"`
				Entries []provenance.Entry `json:"entries"`
			} `json:"sites"`
			Retention string `json:"retention"`
		} `json:"provenance"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	pv := out.Provenance
	if pv == nil || len(pv.Account) != 1 || pv.Account[0].IP != "203.0.113.7" || pv.Retention == "" {
		t.Fatalf("provenance: %s", w.Body)
	}
	if len(pv.Sites) != 1 || pv.Sites[0].Site != "aaaaaaaaaaaaaaaaaaaaaaaaaa" || len(pv.Sites[0].Entries) != 1 || pv.Sites[0].Entries[0].Action != provenance.ActionCreate {
		t.Fatalf("site provenance: %+v", pv.Sites)
	}
	if strings.Contains(w.Body.String(), "198.51.100.66") {
		t.Fatal("an edit-password entry was exported as the subject's")
	}
}
