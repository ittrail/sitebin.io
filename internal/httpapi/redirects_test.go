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

// Domain redirects and the generated address, through the API and authz. See
// docs/superpowers/specs/2026-10-16-domain-redirects-design.md.

// redirectEnv is an enterprise env with a site that has the given domains
// attached.
func redirectEnv(t *testing.T, over map[string]string, domains ...string) (*env, createResp, string) {
	t.Helper()
	ext.Register(&fakeProvider{domainsOK: true})
	t.Cleanup(ext.Reset)
	e := newEnv(t, over)
	c := e.createSite(t, nil, map[string]string{"index.html": "x"})
	edit := editIDFrom(t, c.EditURL)
	for _, d := range domains {
		req := authed(httptest.NewRequest("POST", "/api/sites/"+edit+"/domains", strings.NewReader(`{"domain":"`+d+`"}`)), c.EditPassword)
		req.Header.Set("Content-Type", "application/json")
		if w := e.public(t, req); w.Code != 200 {
			t.Fatalf("attach %s: %d %s", d, w.Code, w.Body)
		}
	}
	return e, c, edit
}

func jsonReq(method, target, body, pw string) *http.Request {
	req := authed(httptest.NewRequest(method, target, strings.NewReader(body)), pw)
	req.Header.Set("Content-Type", "application/json")
	return req
}

type addressPayload struct {
	PrimaryURL      string `json:"primary_url"`
	ViewURL         string `json:"view_url"`
	DomainRedirects map[string]struct {
		To       string `json:"to"`
		Code     int    `json:"code"`
		InEffect bool   `json:"in_effect"`
	} `json:"domain_redirects"`
	ViewAddress struct {
		Mode     string `json:"mode"`
		To       string `json:"to"`
		Code     int    `json:"code"`
		InEffect bool   `json:"in_effect"`
	} `json:"view_address"`
}

func decodeAddresses(t *testing.T, w *httptest.ResponseRecorder) addressPayload {
	t.Helper()
	var p addressPayload
	if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil {
		t.Fatalf("payload: %v\n%s", err, w.Body)
	}
	return p
}

func TestDomainRedirectAPIAndAuthz(t *testing.T) {
	e, c, edit := redirectEnv(t, nil, "morgenfertig.at", "morgenfertig.de")

	// Before any rule both domains serve, and the payload says so.
	w := e.public(t, authed(httptest.NewRequest("GET", "/api/sites/"+edit, nil), c.EditPassword))
	p := decodeAddresses(t, w)
	if len(p.DomainRedirects) != 0 || p.ViewAddress.Mode != "serve" || !p.ViewAddress.InEffect || p.PrimaryURL != p.ViewURL {
		t.Fatalf("payload before any rule: %+v", p)
	}

	w = e.public(t, jsonReq("PUT", "/api/sites/"+edit+"/domains/morgenfertig.de/redirect", `{"to":"morgenfertig.at"}`, c.EditPassword))
	if w.Code != 200 {
		t.Fatalf("set redirect: %d %s", w.Code, w.Body)
	}
	p = decodeAddresses(t, w)
	if r := p.DomainRedirects["morgenfertig.de"]; r.To != "morgenfertig.at" || r.Code != 301 || !r.InEffect {
		t.Errorf("payload redirect = %+v, want → morgenfertig.at 301 in effect", r)
	}

	// authz answers the redirect itself, keeping path and query.
	aw := e.internal(t, authzReq("morgenfertig.de", "/preise?x=1", ""))
	if aw.Code != 301 {
		t.Fatalf("authz on the redirecting domain: %d, want 301", aw.Code)
	}
	if loc := aw.Header().Get("Location"); loc != "http://morgenfertig.at/preise?x=1" {
		t.Errorf("Location = %q", loc)
	}
	if cc := aw.Header().Get("Cache-Control"); cc != "public, max-age=3600" {
		t.Errorf("a permanent redirect's Cache-Control = %q", cc)
	}
	if aw := e.internal(t, authzReq("morgenfertig.at", "/", "")); aw.Code != 200 {
		t.Errorf("the target must serve: %d", aw.Code)
	}
	// Any port the client named, any letter case.
	if aw := e.internal(t, authzReq("MorgenFertig.DE:443", "/", "")); aw.Code != 301 {
		t.Errorf("redirect with port and capitals: %d", aw.Code)
	}

	// A temporary code is not cached.
	if w := e.public(t, jsonReq("PUT", "/api/sites/"+edit+"/domains/morgenfertig.de/redirect", `{"to":"morgenfertig.at","code":307}`, c.EditPassword)); w.Code != 200 {
		t.Fatalf("set 307: %d %s", w.Code, w.Body)
	}
	aw = e.internal(t, authzReq("morgenfertig.de", "/", ""))
	if aw.Code != 307 || aw.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("307: %d %q", aw.Code, aw.Header().Get("Cache-Control"))
	}

	// A path the client crafted cannot move the redirect off the target.
	for _, uri := range []string{"@evil.example/", "//evil.example/", "http://evil.example/"} {
		aw = e.internal(t, authzReq("morgenfertig.de", uri, ""))
		if loc := aw.Header().Get("Location"); loc != "http://morgenfertig.at/" {
			t.Errorf("uri %q → Location %q, want the target's root", uri, loc)
		}
	}

	// Refusals carry the reason.
	for _, c2 := range []struct {
		target, body string
		code         int
	}{
		{"/domains/morgenfertig.de/redirect", `{"to":"morgenfertig.de"}`, 400},
		{"/domains/morgenfertig.de/redirect", `{"to":"elsewhere.example"}`, 400},
		{"/domains/morgenfertig.de/redirect", `{"to":"morgenfertig.at","code":303}`, 400},
		{"/domains/nope.example/redirect", `{"to":"morgenfertig.at"}`, 404},
		{"/domains/morgenfertig.de/redirect", `not json`, 400},
	} {
		w := e.public(t, jsonReq("PUT", "/api/sites/"+edit+c2.target, c2.body, c.EditPassword))
		if w.Code != c2.code {
			t.Errorf("PUT %s %s = %d %s, want %d", c2.target, c2.body, w.Code, w.Body, c2.code)
		}
	}
	w = e.public(t, jsonReq("PUT", "/api/sites/"+edit+"/domains/morgenfertig.de/redirect", `{"to":"elsewhere.example"}`, c.EditPassword))
	if !strings.Contains(w.Body.String(), "elsewhere.example is not an attached custom domain of this site") {
		t.Errorf("refusal does not say why: %s", w.Body)
	}

	// DELETE makes the domain serve again.
	w = e.public(t, authed(httptest.NewRequest("DELETE", "/api/sites/"+edit+"/domains/morgenfertig.de/redirect", nil), c.EditPassword))
	if w.Code != 200 {
		t.Fatalf("clear redirect: %d %s", w.Code, w.Body)
	}
	if aw := e.internal(t, authzReq("morgenfertig.de", "/", "")); aw.Code != 200 {
		t.Errorf("after DELETE the domain must serve: %d", aw.Code)
	}

	// Without the edit password nothing changes.
	req := httptest.NewRequest("PUT", "/api/sites/"+edit+"/domains/morgenfertig.de/redirect", strings.NewReader(`{"to":"morgenfertig.at"}`))
	req.Header.Set("Content-Type", "application/json")
	if w := e.public(t, req); w.Code != 401 {
		t.Errorf("unauthenticated PUT: %d", w.Code)
	}
}

func TestViewAddressAPIAndAuthz(t *testing.T) {
	e, c, edit := redirectEnv(t, nil, "morgenfertig.at")
	viewHost := c.ID + ".sitebin.example"

	w := e.public(t, jsonReq("PUT", "/api/sites/"+edit+"/view-address", `{"mode":"off"}`, c.EditPassword))
	if w.Code != 200 {
		t.Fatalf("off: %d %s", w.Code, w.Body)
	}
	p := decodeAddresses(t, w)
	if p.ViewAddress.Mode != "off" || !p.ViewAddress.InEffect || p.PrimaryURL != "http://morgenfertig.at" {
		t.Errorf("payload after off: %+v", p)
	}
	if aw := e.internal(t, authzReq(viewHost, "/", "")); aw.Code != 404 {
		t.Errorf("the generated address is off but answered %d", aw.Code)
	}
	if aw := e.internal(t, authzReq("morgenfertig.at", "/", "")); aw.Code != 200 {
		t.Errorf("the custom domain must still serve: %d", aw.Code)
	}

	w = e.public(t, jsonReq("PUT", "/api/sites/"+edit+"/view-address", `{"mode":"redirect","to":"morgenfertig.at","code":308}`, c.EditPassword))
	if w.Code != 200 {
		t.Fatalf("redirect: %d %s", w.Code, w.Body)
	}
	aw := e.internal(t, authzReq(viewHost, "/a/b?c=d", ""))
	if aw.Code != 308 || aw.Header().Get("Location") != "http://morgenfertig.at/a/b?c=d" {
		t.Errorf("generated address redirect: %d %q", aw.Code, aw.Header().Get("Location"))
	}

	if w := e.public(t, jsonReq("PUT", "/api/sites/"+edit+"/view-address", `{"mode":"sideways"}`, c.EditPassword)); w.Code != 400 {
		t.Errorf("unknown mode: %d", w.Code)
	}

	// Removing the target domain brings the generated address back.
	if w := e.public(t, authed(httptest.NewRequest("DELETE", "/api/sites/"+edit+"/domains/morgenfertig.at", nil), c.EditPassword)); w.Code != 200 {
		t.Fatalf("remove domain: %d", w.Code)
	}
	if aw := e.internal(t, authzReq(viewHost, "/", "")); aw.Code != 200 {
		t.Errorf("after removing the target the generated address must serve: %d", aw.Code)
	}

	// With no custom domain, off is refused: it would take the site offline.
	w = e.public(t, jsonReq("PUT", "/api/sites/"+edit+"/view-address", `{"mode":"off"}`, c.EditPassword))
	if w.Code != 400 || !strings.Contains(w.Body.String(), "offline") {
		t.Errorf("off without a serving domain: %d %s", w.Code, w.Body)
	}
}

func TestViewAddressPathMode(t *testing.T) {
	e, c, edit := redirectEnv(t, map[string]string{"SITEBIN_VIEW_ACCESS": "both"}, "morgenfertig.at")
	pathReq := func(uri string) *http.Request {
		req := httptest.NewRequest("GET", "/internal/authz", nil)
		req.Header.Set("X-Sitebin-View", c.ID)
		req.Header.Set("X-Forwarded-Uri", uri)
		return req
	}
	if w := e.public(t, jsonReq("PUT", "/api/sites/"+edit+"/view-address", `{"mode":"redirect","to":"morgenfertig.at"}`, c.EditPassword)); w.Code != 200 {
		t.Fatalf("redirect: %d %s", w.Code, w.Body)
	}
	aw := e.internal(t, pathReq("/v/"+c.ID+"/docs/x.html?q=1"))
	if aw.Code != 301 || aw.Header().Get("Location") != "http://morgenfertig.at/docs/x.html?q=1" {
		t.Errorf("path view redirect: %d %q (the /v/<id> prefix must go)", aw.Code, aw.Header().Get("Location"))
	}
	if w := e.public(t, jsonReq("PUT", "/api/sites/"+edit+"/view-address", `{"mode":"off"}`, c.EditPassword)); w.Code != 200 {
		t.Fatalf("off: %d", w.Code)
	}
	if aw := e.internal(t, pathReq("/v/"+c.ID+"/")); aw.Code != 404 {
		t.Errorf("path view while off: %d", aw.Code)
	}
}

// A lock and an expiry say so on every address; a redirect comes before the
// password gate, which is asked on the domain the visitor lands on.
func TestAddressRuleOrder(t *testing.T) {
	e, c, edit := redirectEnv(t, nil, "a.example", "b.example")
	if w := e.public(t, jsonReq("PUT", "/api/sites/"+edit+"/domains/b.example/redirect", `{"to":"a.example"}`, c.EditPassword)); w.Code != 200 {
		t.Fatal(w.Body)
	}
	if w := e.public(t, jsonReq("PUT", "/api/sites/"+edit, `{"view_password":"sesame"}`, c.EditPassword)); w.Code != 200 {
		t.Fatal(w.Body)
	}
	if aw := e.internal(t, authzReq("b.example", "/", "")); aw.Code != 301 {
		t.Errorf("redirect before the gate: %d", aw.Code)
	}
	if aw := e.internal(t, authzReq("a.example", "/", "")); aw.Code != 401 {
		t.Errorf("the target is gated: %d", aw.Code)
	}

	site, _ := e.st.ByViewID(c.ID)
	if _, err := e.st.SetLock(site, &store.SiteLock{Reason: "test"}); err != nil {
		t.Fatal(err)
	}
	if aw := e.internal(t, authzReq("b.example", "/", "")); aw.Code != 410 {
		t.Errorf("a locked site redirects: %d", aw.Code)
	}
}

// An address that does not serve the site answers nothing of it: no form
// posts, no unlock.
func TestNonServingAddressHasNoBackendRoutes(t *testing.T) {
	e, c, edit := redirectEnv(t, nil, "a.example", "b.example")
	if w := e.public(t, jsonReq("PUT", "/api/sites/"+edit+"/domains/b.example/redirect", `{"to":"a.example"}`, c.EditPassword)); w.Code != 200 {
		t.Fatal(w.Body)
	}
	if w := e.public(t, jsonReq("PUT", "/api/sites/"+edit+"/view-address", `{"mode":"off"}`, c.EditPassword)); w.Code != 200 {
		t.Fatal(w.Body)
	}
	if w := e.public(t, jsonReq("PUT", "/api/sites/"+edit, `{"view_password":"sesame"}`, c.EditPassword)); w.Code != 200 {
		t.Fatal(w.Body)
	}
	unlock := func(host string) int {
		req := httptest.NewRequest("POST", "/_sitebin/unlock", strings.NewReader("password=sesame&redirect=%2F"))
		req.Host = host
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		return e.public(t, req).Code
	}
	if code := unlock("a.example"); code != 303 {
		t.Errorf("unlock on the serving domain: %d", code)
	}
	for _, host := range []string{"b.example", c.ID + ".sitebin.example"} {
		if code := unlock(host); code != 404 {
			t.Errorf("unlock on non-serving %s: %d, want 404", host, code)
		}
		site, err := e.api.servingSiteByHost(host)
		if err == nil {
			t.Errorf("servingSiteByHost(%s) = %s, want not found", host, site.ViewID)
		}
	}
	if site, err := e.api.servingSiteByHost("a.example"); err != nil || site.ViewID != c.ID {
		t.Errorf("servingSiteByHost(a.example) = %v, %v", site, err)
	}
}

// MCP reaches the same rules through the same helpers as the API.
func TestMCPRedirectTools(t *testing.T) {
	e, c, edit := redirectEnv(t, nil, "example.at", "example.de")
	cs := mcpClient(t, e, nil)

	res := mcpCall(t, cs, "get_site", map[string]any{"edit_id": edit, "edit_password": c.EditPassword})
	if strings.Contains(mcpText(res), "domain_redirects") || strings.Contains(mcpText(res), "view_address") {
		t.Errorf("a site without rules must return the result it always did: %s", mcpText(res))
	}

	res = mcpCall(t, cs, "set_domain_redirect", map[string]any{"edit_id": edit, "edit_password": c.EditPassword, "domain": "example.de", "to": "example.at"})
	if res.IsError {
		t.Fatalf("set_domain_redirect: %s", mcpText(res))
	}
	if !strings.Contains(mcpText(res), `"domain_redirects":[{"code":301,"domain":"example.de","to":"example.at"}]`) {
		t.Errorf("result does not show the redirect: %s", mcpText(res))
	}
	if aw := e.internal(t, authzReq("example.de", "/x", "")); aw.Code != 301 || aw.Header().Get("Location") != "http://example.at/x" {
		t.Errorf("authz after set_domain_redirect: %d %q", aw.Code, aw.Header().Get("Location"))
	}

	res = mcpCall(t, cs, "set_domain_redirect", map[string]any{"edit_id": edit, "edit_password": c.EditPassword, "domain": "example.at", "to": "example.at"})
	if !res.IsError || !strings.Contains(mcpText(res), "cannot redirect to itself") {
		t.Errorf("a refused rule must say why: %v %s", res.IsError, mcpText(res))
	}

	res = mcpCall(t, cs, "set_view_address", map[string]any{"edit_id": edit, "edit_password": c.EditPassword, "mode": "off"})
	if res.IsError || !strings.Contains(mcpText(res), `"view_address":{"mode":"off"}`) {
		t.Fatalf("set_view_address: %v %s", res.IsError, mcpText(res))
	}
	if aw := e.internal(t, authzReq(c.ID+".sitebin.example", "/", "")); aw.Code != 404 {
		t.Errorf("generated address after off: %d", aw.Code)
	}

	res = mcpCall(t, cs, "set_domain_redirect", map[string]any{"edit_id": edit, "edit_password": c.EditPassword, "domain": "example.de"})
	if res.IsError || strings.Contains(mcpText(res), "domain_redirects") {
		t.Errorf("an empty to must clear the redirect: %v %s", res.IsError, mcpText(res))
	}
}

// The seam's view of a site carries the rules in effect, for the dashboard.
func TestSiteInfoCarriesAddressRules(t *testing.T) {
	e, c, edit := redirectEnv(t, nil, "example.at", "example.de")
	for _, r := range []*http.Request{
		jsonReq("PUT", "/api/sites/"+edit+"/domains/example.de/redirect", `{"to":"example.at"}`, c.EditPassword),
		jsonReq("PUT", "/api/sites/"+edit+"/view-address", `{"mode":"redirect","to":"example.at"}`, c.EditPassword),
	} {
		if w := e.public(t, r); w.Code != 200 {
			t.Fatalf("%s: %d %s", r.URL, w.Code, w.Body)
		}
	}
	info, ok := siteService{a: e.api}.Info(c.ID)
	if !ok {
		t.Fatal("no info for the site")
	}
	if !info.ViewOff || info.ViewRedirectTo != "example.at" {
		t.Errorf("ViewOff/ViewRedirectTo = %v %q", info.ViewOff, info.ViewRedirectTo)
	}
	var redirects []string
	for _, l := range info.DomainLinks {
		if l.RedirectTo != "" {
			redirects = append(redirects, l.Domain+"→"+l.RedirectTo)
		}
	}
	if len(redirects) != 1 || redirects[0] != "example.de→example.at" {
		t.Errorf("DomainLinks redirects = %v", redirects)
	}
}
