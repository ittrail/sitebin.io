package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/ittrail/sitebin.io/internal/ext"
	"github.com/ittrail/sitebin.io/internal/store"
)

// putSettings sends a settings document and returns the response.
func (e *env) putSettings(t *testing.T, edit, pw, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := authed(httptest.NewRequest("PUT", "/api/sites/"+edit, strings.NewReader(body)), pw)
	req.Header.Set("Content-Type", "application/json")
	return e.public(t, req)
}

func payloadName(t *testing.T, w *httptest.ResponseRecorder) (string, bool) {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
		t.Fatalf("payload: %v (%s)", err, w.Body)
	}
	v, ok := m["name"]
	if !ok {
		return "", false
	}
	s, _ := v.(string)
	return s, true
}

func TestPutSetsAndClearsSiteName(t *testing.T) {
	e := newEnv(t, nil)
	c := e.createSite(t, nil, map[string]string{"index.html": "x"})
	edit := editIDFrom(t, c.EditURL)

	w := e.putSettings(t, edit, c.EditPassword, `{"name":"  Client docs  "}`)
	if w.Code != 200 {
		t.Fatalf("PUT name: %d %s", w.Code, w.Body)
	}
	if got, _ := payloadName(t, w); got != "Client docs" {
		t.Errorf("payload name = %q, want the trimmed name", got)
	}

	// A settings change that does not mention the name leaves it alone.
	if w := e.putSettings(t, edit, c.EditPassword, `{"spa_fallback":true}`); w.Code != 200 {
		t.Fatalf("PUT spa: %d %s", w.Code, w.Body)
	}
	site, _ := e.st.ByEditID(edit)
	if site.Meta.Name != "Client docs" {
		t.Errorf("an unrelated PUT changed the name to %q", site.Meta.Name)
	}

	// A bad name is refused with the rule, and the old name stays.
	w = e.putSettings(t, edit, c.EditPassword, `{"name":"`+strings.Repeat("a", 61)+`"}`)
	if w.Code != 400 || !strings.Contains(w.Body.String(), "60 characters") {
		t.Errorf("over-long name: %d %s", w.Code, w.Body)
	}
	w = e.putSettings(t, edit, c.EditPassword, `{"name":"two\nlines"}`)
	if w.Code != 400 {
		t.Errorf("name with a newline: %d %s", w.Code, w.Body)
	}
	site, _ = e.st.ByEditID(edit)
	if site.Meta.Name != "Client docs" {
		t.Errorf("a refused name overwrote the old one: %q", site.Meta.Name)
	}

	w = e.putSettings(t, edit, c.EditPassword, `{"name":""}`)
	if w.Code != 200 {
		t.Fatalf("PUT clear: %d %s", w.Code, w.Body)
	}
	if got, present := payloadName(t, w); got != "" || !present {
		t.Errorf("after clearing: name = %q, present = %v; want an empty name that is still reported", got, present)
	}
}

// A client must never have to tell "absent" from "unnamed".
func TestPayloadReportsAnUnnamedSite(t *testing.T) {
	e := newEnv(t, nil)
	c := e.createSite(t, nil, map[string]string{"index.html": "x"})
	req := authed(httptest.NewRequest("GET", "/api/sites/"+editIDFrom(t, c.EditURL), nil), c.EditPassword)
	w := e.public(t, req)
	if got, present := payloadName(t, w); !present || got != "" {
		t.Errorf("unnamed site: name = %q, present = %v", got, present)
	}
}

func TestCreateWithName(t *testing.T) {
	e := newEnv(t, nil)
	c := e.createSite(t, map[string]string{"name": "Launch page"}, map[string]string{"index.html": "x"})
	site, err := e.st.ByViewID(c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if site.Meta.Name != "Launch page" {
		t.Errorf("multipart create: name = %q", site.Meta.Name)
	}

	req := httptest.NewRequest("POST", "/api/sites", strings.NewReader(`{"name":"From JSON"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	w := e.public(t, req)
	if w.Code != 201 {
		t.Fatalf("JSON create: %d %s", w.Code, w.Body)
	}
	if got, _ := payloadName(t, w); got != "From JSON" {
		t.Errorf("JSON create: name = %q", got)
	}
}

// A bad name at creation fails the creation rather than leaving an unnamed
// site behind that the caller believes is named.
func TestCreateWithBadNameLeavesNothing(t *testing.T) {
	e := newEnv(t, nil)
	req := httptest.NewRequest("POST", "/api/sites", strings.NewReader(`{"name":"bad\u0007bell"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	w := e.public(t, req)
	if w.Code != 400 {
		t.Fatalf("bad name at create: %d %s", w.Code, w.Body)
	}
	all, err := e.st.AllSites()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 0 {
		t.Errorf("a refused creation left %d site(s) behind", len(all))
	}
}

// The account dashboard lists a site by what the seam says about it: its name,
// and its custom domains with the URL each serves at — the verified ones first,
// then the claims still waiting for their DNS proof.
func TestSiteServiceInfoCarriesNameAndDomains(t *testing.T) {
	e := newEnv(t, nil)
	e.st.SetDomainVerifier(&apiVerifier{ok: map[string]bool{"docs.example.com": true}}, e.cfg.ViewDomain)
	c := e.createSite(t, map[string]string{"name": "Docs"}, map[string]string{"index.html": "x"})
	site, _ := e.st.ByViewID(c.ID)
	if err := e.st.AddDomain(site, "docs.example.com"); err != nil {
		t.Fatalf("verified domain: %v", err)
	}
	if err := e.st.AddDomain(site, "pending.example.com"); !errors.Is(err, store.ErrDomainPending) {
		t.Fatalf("pending domain: %v", err)
	}

	info, ok := e.api.SiteService().Info(c.ID)
	if !ok {
		t.Fatal("Info: site not found")
	}
	if info.Name != "Docs" {
		t.Errorf("Info.Name = %q", info.Name)
	}
	want := []ext.DomainLink{
		{Domain: "docs.example.com", URL: e.cfg.SiteURL("docs.example.com")},
		{Domain: "pending.example.com", Pending: true},
	}
	if !reflect.DeepEqual(info.DomainLinks, want) {
		t.Errorf("Info.DomainLinks = %+v, want %+v", info.DomainLinks, want)
	}
	if !reflect.DeepEqual(info.Domains, []string{"docs.example.com"}) {
		t.Errorf("Info.Domains must stay the verified domains only: %v", info.Domains)
	}
}

func TestSiteServiceSetName(t *testing.T) {
	e := newEnv(t, nil)
	c := e.createSite(t, nil, map[string]string{"index.html": "x"})
	ss := e.api.SiteService()

	if err := ss.SetName(c.ID, "  Renamed  "); err != nil {
		t.Fatalf("SetName: %v", err)
	}
	if info, _ := ss.Info(c.ID); info.Name != "Renamed" {
		t.Errorf("name = %q", info.Name)
	}
	if err := ss.SetName(c.ID, strings.Repeat("x", 61)); !errors.Is(err, store.ErrBadSiteName) {
		t.Errorf("over-long name: %v, want ErrBadSiteName", err)
	}
	if info, _ := ss.Info(c.ID); info.Name != "Renamed" {
		t.Errorf("a refused name changed the site: %q", info.Name)
	}
	if err := ss.SetName(c.ID, ""); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if info, _ := ss.Info(c.ID); info.Name != "" {
		t.Errorf("name after clearing = %q", info.Name)
	}
	if err := ss.SetName("nosuchsiteatallxxxxxxxxxxx", "x"); !errors.Is(err, ext.ErrSiteGone) {
		t.Errorf("missing site: %v, want ErrSiteGone", err)
	}
}

// ---- MCP: the name is a setting like any other ----

func TestMCPSiteName(t *testing.T) {
	e := newEnv(t, nil)
	cs := mcpClient(t, e, nil)

	res := mcpCall(t, cs, "create_site", map[string]any{
		"files":    []any{map[string]any{"path": "index.html", "text": "hi"}},
		"settings": map[string]any{"name": "Agent draft"},
	})
	if res.IsError {
		t.Fatalf("create_site: %s", mcpText(res))
	}
	m := res.StructuredContent.(map[string]any)
	editID, _ := m["edit_id"].(string)
	pw, _ := m["edit_password"].(string)
	if m["name"] != "Agent draft" {
		t.Errorf("create_site result name = %v", m["name"])
	}

	res = mcpCall(t, cs, "update_site", map[string]any{
		"edit_id": editID, "edit_password": pw,
		"settings": map[string]any{"name": "Final"},
	})
	if res.IsError {
		t.Fatalf("update_site: %s", mcpText(res))
	}
	if got := res.StructuredContent.(map[string]any)["name"]; got != "Final" {
		t.Errorf("update_site result name = %v", got)
	}

	res = mcpCall(t, cs, "update_site", map[string]any{
		"edit_id": editID, "edit_password": pw,
		"settings": map[string]any{"name": strings.Repeat("n", 61)},
	})
	if !res.IsError || !strings.Contains(mcpText(res), "60 characters") {
		t.Errorf("an over-long name must be a tool error that states the rule: %s", mcpText(res))
	}

	res = mcpCall(t, cs, "update_site", map[string]any{
		"edit_id": editID, "edit_password": pw,
		"settings": map[string]any{"name": ""},
	})
	if res.IsError {
		t.Fatalf("update_site clear: %s", mcpText(res))
	}
	site, _ := e.st.ByEditID(editID)
	if site.Meta.Name != "" {
		t.Errorf("an empty name did not clear it: %q", site.Meta.Name)
	}
}

func TestMCPListSitesCarriesNames(t *testing.T) {
	e := newEnv(t, nil)
	p := &fakeProvider{
		enabled: true,
		owner:   "acct-1",
		bearer:  map[string]string{"sbp_tok": "acct-1"},
		owned:   map[string][]string{},
	}
	ext.Register(p)
	defer ext.Reset()

	cs := mcpClient(t, e, http.Header{"Authorization": {"Bearer sbp_tok"}})
	res := mcpCall(t, cs, "create_site", map[string]any{
		"files":    []any{map[string]any{"path": "index.html", "text": "hi"}},
		"settings": map[string]any{"name": "Portfolio"},
	})
	if res.IsError {
		t.Fatalf("create_site: %s", mcpText(res))
	}
	editID := res.StructuredContent.(map[string]any)["edit_id"].(string)
	site, _ := e.st.ByEditID(editID)
	p.owned["acct-1"] = []string{site.ViewID}

	res = mcpCall(t, cs, "list_sites", map[string]any{})
	if res.IsError {
		t.Fatalf("list_sites: %s", mcpText(res))
	}
	sites := res.StructuredContent.(map[string]any)["sites"].([]any)
	if len(sites) != 1 || sites[0].(map[string]any)["name"] != "Portfolio" {
		t.Errorf("list_sites does not carry the name: %v", sites)
	}
}
