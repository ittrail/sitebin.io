package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ittrail/sitebin.io/internal/ext"
	"github.com/ittrail/sitebin.io/internal/store"
)

// sessionProvider is a provider whose dashboard signs people in with a browser
// session: the cookie "sb_test" names the account. fakeProvider on its own has
// no sessions, which is the other half every test here needs.
type sessionProvider struct {
	*fakeProvider
	sessions map[string]string // cookie value -> account id
}

func (s *sessionProvider) SessionAccount(r *http.Request) (string, bool) {
	c, err := r.Cookie("sb_test")
	if err != nil {
		return "", false
	}
	id, ok := s.sessions[c.Value]
	return id, ok
}

// ownedSite creates a site owned by acct-1 and returns its edit id.
func ownedSite(t *testing.T, e *env) string {
	t.Helper()
	c := e.createSite(t, nil, map[string]string{"index.html": "x"})
	edit := editIDFrom(t, c.EditURL)
	site, err := e.st.ByEditID(edit)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.st.Update(site, func(m *store.Meta) error { m.OwnerAccountID = "acct-1"; return nil }); err != nil {
		t.Fatal(err)
	}
	return edit
}

// sessionReq builds a request the edit page would send: the session cookie,
// the marker header and same-origin fetch metadata. Tests then take away one
// piece at a time.
func sessionReq(method, path, body, cookie string) *http.Request {
	var req *http.Request
	if body != "" {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
	if cookie != "" {
		req.AddCookie(&http.Cookie{Name: "sb_test", Value: cookie})
	}
	req.Header.Set("X-Sitebin-Session", "1")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	return req
}

// registerProvider installs p for the rest of the test.
func registerProvider(t *testing.T, p ext.Provider) {
	t.Helper()
	ext.Register(p)
	t.Cleanup(ext.Reset)
}

func registerSessions(t *testing.T) *sessionProvider {
	t.Helper()
	p := &sessionProvider{
		fakeProvider: &fakeProvider{enabled: true, owner: "acct-1"},
		sessions:     map[string]string{"owner-cookie": "acct-1", "other-cookie": "acct-2"},
	}
	registerProvider(t, p)
	return p
}

func TestOwnerSessionManagesTheSiteWithoutPassword(t *testing.T) {
	e := newEnv(t, nil)
	edit := ownedSite(t, e)
	registerSessions(t)

	if w := e.public(t, sessionReq("GET", "/api/sites/"+edit, "", "owner-cookie")); w.Code != 200 {
		t.Fatalf("GET with the owner's session = %d %s", w.Code, w.Body)
	}
	if w := e.public(t, sessionReq("PUT", "/api/sites/"+edit, `{"name":"via session"}`, "owner-cookie")); w.Code != 200 {
		t.Fatalf("PUT with the owner's session = %d %s", w.Code, w.Body)
	}
	site, _ := e.st.ByEditID(edit)
	if site.Meta.Name != "via session" {
		t.Errorf("the session's PUT did not apply: %q", site.Meta.Name)
	}
	// Browsers without fetch metadata (Safari before 16.4) still get in: the
	// marker header is the boundary, and they cannot send it cross-origin.
	req := sessionReq("GET", "/api/sites/"+edit, "", "owner-cookie")
	req.Header.Del("Sec-Fetch-Site")
	if w := e.public(t, req); w.Code != 200 {
		t.Errorf("owner session without Sec-Fetch-Site = %d, want 200", w.Code)
	}
	if w := e.public(t, sessionReq("DELETE", "/api/sites/"+edit, "", "owner-cookie")); w.Code != 200 {
		t.Fatalf("DELETE with the owner's session = %d %s", w.Code, w.Body)
	}
}

// Every way a session must NOT open a site. Each is a 401 — the session is
// simply not a credential there, so the caller falls back to the password.
func TestSessionIsRefusedOutsideItsRule(t *testing.T) {
	e := newEnv(t, nil)
	edit := ownedSite(t, e)
	anon := editIDFrom(t, e.createSite(t, nil, map[string]string{"index.html": "x"}).EditURL)
	registerSessions(t)

	cases := []struct {
		name string
		req  func() *http.Request
	}{
		{"no marker header (a cross-site form or img cannot add one)", func() *http.Request {
			r := sessionReq("GET", "/api/sites/"+edit, "", "owner-cookie")
			r.Header.Del("X-Sitebin-Session")
			return r
		}},
		{"same-site page, e.g. the marketing site on the apex", func() *http.Request {
			r := sessionReq("GET", "/api/sites/"+edit, "", "owner-cookie")
			r.Header.Set("Sec-Fetch-Site", "same-site")
			return r
		}},
		{"cross-site page", func() *http.Request {
			r := sessionReq("PUT", "/api/sites/"+edit, `{"name":"pwned"}`, "owner-cookie")
			r.Header.Set("Sec-Fetch-Site", "cross-site")
			return r
		}},
		{"another account's session", func() *http.Request {
			return sessionReq("GET", "/api/sites/"+edit, "", "other-cookie")
		}},
		{"an unknown session", func() *http.Request {
			return sessionReq("GET", "/api/sites/"+edit, "", "forged")
		}},
		{"an anonymous site, which no account owns", func() *http.Request {
			return sessionReq("GET", "/api/sites/"+anon, "", "owner-cookie")
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if w := e.public(t, c.req()); w.Code != 401 {
				t.Errorf("= %d %s, want 401", w.Code, w.Body)
			}
		})
	}
	site, _ := e.st.ByEditID(edit)
	if site.Meta.Name == "pwned" {
		t.Error("a cross-site request renamed the site")
	}
}

// A provider without sessions, and an instance with accounts off, never
// honour one — the community build has no sessions at all.
func TestSessionNeedsASessionProviderWithAccounts(t *testing.T) {
	t.Run("provider without sessions", func(t *testing.T) {
		e := newEnv(t, nil)
		edit := ownedSite(t, e)
		registerProvider(t, &fakeProvider{enabled: true})
		if w := e.public(t, sessionReq("GET", "/api/sites/"+edit, "", "owner-cookie")); w.Code != 401 {
			t.Errorf("= %d, want 401", w.Code)
		}
	})
	t.Run("accounts disabled", func(t *testing.T) {
		e := newEnv(t, nil)
		edit := ownedSite(t, e)
		p := registerSessions(t)
		p.enabled = false
		if w := e.public(t, sessionReq("GET", "/api/sites/"+edit, "", "owner-cookie")); w.Code != 401 {
			t.Errorf("= %d, want 401", w.Code)
		}
	})
}

// The marker header is only a boundary while no preflight for it is ever
// approved on a per-site route.
func TestPerSiteRoutesApproveNoPreflight(t *testing.T) {
	e := newEnv(t, map[string]string{"SITEBIN_EMBED_ORIGINS": "*"})
	edit := ownedSite(t, e)
	registerSessions(t).embedOK = true
	req := httptest.NewRequest("OPTIONS", "/api/sites/"+edit, nil)
	req.Header.Set("Origin", "https://evil.example")
	req.Header.Set("Access-Control-Request-Method", "PUT")
	req.Header.Set("Access-Control-Request-Headers", "x-sitebin-session")
	w := e.public(t, req)
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("preflight on a per-site route answered Allow-Origin %q", got)
	}
	if got := w.Header().Get("Access-Control-Allow-Headers"); strings.Contains(strings.ToLower(got), "x-sitebin-session") {
		t.Errorf("preflight approved the session header: %q", got)
	}
}

// The edit page learns from a 401 whether offering "sign in" makes sense.
func TestMissingPasswordPointsAtTheAccountWhenSessionsExist(t *testing.T) {
	e := newEnv(t, nil)
	edit := ownedSite(t, e)

	body := func() map[string]string {
		w := e.public(t, httptest.NewRequest("GET", "/api/sites/"+edit, nil))
		if w.Code != 401 {
			t.Fatalf("= %d, want 401", w.Code)
		}
		var m map[string]string
		json.Unmarshal(w.Body.Bytes(), &m)
		return m
	}
	if m := body(); m["account_url"] != "" {
		t.Errorf("community build offered an account: %v", m)
	}
	registerSessions(t)
	if m := body(); m["account_url"] != e.api.apiAccountHint() || m["error"] == "" {
		t.Errorf("with sessions: %v, want account_url %q and the error", m, e.api.apiAccountHint())
	}
}

// MCP stays token-only: a browser session, header and all, is not a
// credential there.
func TestMCPIgnoresTheBrowserSession(t *testing.T) {
	e := newEnv(t, nil)
	edit := ownedSite(t, e)
	registerSessions(t)
	cs := mcpClient(t, e, http.Header{
		"Cookie":            {"sb_test=owner-cookie"},
		"X-Sitebin-Session": {"1"},
	})
	res := mcpCall(t, cs, "get_site", map[string]any{"edit_id": edit})
	if !res.IsError {
		t.Fatal("MCP accepted a browser session in place of a token or password")
	}
}
