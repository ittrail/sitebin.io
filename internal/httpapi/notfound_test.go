package httpapi

import (
	"bytes"
	"fmt"
	"mime/multipart"
	"net/http/httptest"
	"net/textproto"
	"slices"
	"strings"
	"testing"

	"github.com/ittrail/sitebin.io/internal/ext"
	"github.com/ittrail/sitebin.io/internal/store"
)

func putSettings(t *testing.T, e *env, edit, pw, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := authed(httptest.NewRequest("PUT", "/api/sites/"+edit, strings.NewReader(body)), pw)
	req.Header.Set("Content-Type", "application/json")
	return e.public(t, req)
}

func warningsOf(p map[string]any) []string {
	var out []string
	list, _ := p["warnings"].([]any)
	for _, w := range list {
		out = append(out, fmt.Sprint(w))
	}
	return out
}

func TestSettingsNotFoundPage(t *testing.T) {
	e := newEnv(t, nil)
	c := e.createSite(t, nil, map[string]string{"index.html": "<h1>hi</h1>"})
	edit := editIDFrom(t, c.EditURL)

	w := putSettings(t, e, edit, c.EditPassword, `{"not_found_page":"/errors/x.html"}`)
	if w.Code != 200 {
		t.Fatalf("PUT not_found_page: %d %s", w.Code, w.Body)
	}
	if got := payloadOf(t, w)["not_found_page"]; got != "errors/x.html" {
		t.Errorf("payload not_found_page = %v, want errors/x.html", got)
	}
	site, _ := e.st.ByViewID(c.ID)
	if site.Meta.NotFoundPage != "errors/x.html" {
		t.Errorf("stored NotFoundPage = %q", site.Meta.NotFoundPage)
	}

	w = putSettings(t, e, edit, c.EditPassword, `{"not_found_page":"x.txt"}`)
	if w.Code != 400 || !strings.Contains(w.Body.String(), store.ErrBadNotFoundPage.Error()) {
		t.Errorf("a .txt page: %d %s, want 400 with the rule", w.Code, w.Body)
	}

	if w := putSettings(t, e, edit, c.EditPassword, `{"not_found_page":""}`); w.Code != 200 {
		t.Fatalf("clear: %d %s", w.Code, w.Body)
	}
	site, _ = e.st.ByViewID(c.ID)
	if site.Meta.NotFoundPage != "" {
		t.Errorf("cleared NotFoundPage = %q", site.Meta.NotFoundPage)
	}
}

func TestPayloadNotFoundServes(t *testing.T) {
	e := newEnv(t, nil)
	with := e.createSite(t, nil, map[string]string{"index.html": "<h1>hi</h1>", "404.html": "<h1>gone</h1>"})
	without := e.createSite(t, nil, map[string]string{"index.html": "<h1>hi</h1>"})
	for _, c := range []struct {
		site createResp
		want string
	}{{with, "404.html"}, {without, ""}} {
		req := authed(httptest.NewRequest("GET", "/api/sites/"+editIDFrom(t, c.site.EditURL), nil), c.site.EditPassword)
		w := e.public(t, req)
		if w.Code != 200 {
			t.Fatalf("GET: %d %s", w.Code, w.Body)
		}
		if got := payloadOf(t, w)["not_found_serves"]; got != c.want {
			t.Errorf("not_found_serves = %v, want %q", got, c.want)
		}
	}
}

func createRaw(t *testing.T, e *env, files map[string]string) map[string]any {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for name, content := range files {
		h := textproto.MIMEHeader{}
		h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="files"; filename="%s"`, name))
		p, _ := mw.CreatePart(h)
		p.Write([]byte(content))
	}
	mw.Close()
	req := httptest.NewRequest("POST", "/api/sites", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	w := e.public(t, req)
	if w.Code != 201 {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	return payloadOf(t, w)
}

func TestStartPageWarning(t *testing.T) {
	e := newEnv(t, nil)

	p := createRaw(t, e, map[string]string{"a.pdf": "%PDF-1.4"})
	if !slices.Contains(warningsOf(p), startPageWarning) {
		t.Errorf("a PDF-only web server site: warnings = %v, want the start-page warning", warningsOf(p))
	}
	if p["start_page_missing"] != true {
		t.Errorf("start_page_missing = %v, want true", p["start_page_missing"])
	}

	p = createRaw(t, e, map[string]string{"index.html": "<h1>hi</h1>", "a.pdf": "%PDF-1.4"})
	if slices.Contains(warningsOf(p), startPageWarning) || p["start_page_missing"] != false {
		t.Errorf("a site with index.html: warnings = %v, start_page_missing = %v", warningsOf(p), p["start_page_missing"])
	}

	// An empty site is not warned: its files may still be on their way.
	p = createRaw(t, e, nil)
	if slices.Contains(warningsOf(p), startPageWarning) {
		t.Errorf("an empty site: warnings = %v", warningsOf(p))
	}

	// The upload that leaves it without a start page says so.
	edit := editIDFrom(t, p["edit_url"].(string))
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", `form-data; name="files"; filename="a.pdf"`)
	part, _ := mw.CreatePart(h)
	part.Write([]byte("%PDF-1.4"))
	mw.Close()
	req := authed(httptest.NewRequest("POST", "/api/sites/"+edit+"/files", &buf), p["edit_password"].(string))
	req.Header.Set("Content-Type", mw.FormDataContentType())
	w := e.public(t, req)
	if w.Code != 200 {
		t.Fatalf("upload: %d %s", w.Code, w.Body)
	}
	if !slices.Contains(warningsOf(payloadOf(t, w)), startPageWarning) {
		t.Errorf("upload response warnings = %v, want the start-page warning", warningsOf(payloadOf(t, w)))
	}
}

func TestSettingsDetailNotFoundPage(t *testing.T) {
	page := "404.html"
	if d := settingsDetail(updateSet{NotFoundPage: &page}); !strings.Contains(d, "not_found_page") {
		t.Errorf("settingsDetail = %q, want not_found_page named", d)
	}
}

func TestMCPNotFoundPage(t *testing.T) {
	e := newEnv(t, nil)
	cs := mcpClient(t, e, nil)
	editID, pw := mcpCreate(t, cs, "<h1>hi</h1>")

	// A site that never set it carries no new field: a client holding an
	// output schema cached before the field existed never meets it.
	res := mcpCall(t, cs, "get_site", map[string]any{"edit_id": editID, "edit_password": pw})
	if res.IsError {
		t.Fatalf("get_site: %s", mcpText(res))
	}
	if m, _ := res.StructuredContent.(map[string]any); m == nil {
		t.Fatal("no structured content")
	} else if _, ok := m["not_found_page"]; ok {
		t.Errorf("get_site of a site without the setting carries not_found_page: %v", m)
	}

	res = mcpCall(t, cs, "update_site", map[string]any{
		"edit_id": editID, "edit_password": pw,
		"settings": map[string]any{"not_found_page": "errors/gone.html"},
	})
	if res.IsError {
		t.Fatalf("update_site: %s", mcpText(res))
	}
	m, _ := res.StructuredContent.(map[string]any)
	if m["not_found_page"] != "errors/gone.html" {
		t.Errorf("update_site result not_found_page = %v", m["not_found_page"])
	}

	res = mcpCall(t, cs, "create_site", map[string]any{
		"files": []any{map[string]any{"path": "a.md", "text": "# notes"}},
	})
	if res.IsError {
		t.Fatalf("create_site: %s", mcpText(res))
	}
	m, _ = res.StructuredContent.(map[string]any)
	if !slices.Contains(warningsOf(m), startPageWarning) {
		t.Errorf("create_site of a site without a start page: warnings = %v", warningsOf(m))
	}

	res = mcpCall(t, cs, "create_site", map[string]any{
		"files": []any{map[string]any{"path": "index.html", "text": "<h1>hi</h1>"}},
	})
	m, _ = res.StructuredContent.(map[string]any)
	if slices.Contains(warningsOf(m), startPageWarning) {
		t.Errorf("create_site with index.html: warnings = %v", warningsOf(m))
	}
}

func TestAuthzNamesNotFoundPage(t *testing.T) {
	e := newEnv(t, nil)
	host := func(c createResp) string { return c.ID + ".sitebin.example" }

	web := e.createSite(t, nil, map[string]string{"index.html": "x"})
	if w := e.internal(t, authzReq(host(web), "/missing", "")); w.Code != 200 || w.Header().Get(notFoundHeader) != "/404.html" {
		t.Errorf("web server: %d %q, want /404.html", w.Code, w.Header().Get(notFoundHeader))
	}

	named := e.createSite(t, map[string]string{"not_found_page": "e/m.html"}, map[string]string{"index.html": "x"})
	if w := e.internal(t, authzReq(host(named), "/missing", "")); w.Header().Get(notFoundHeader) != "/e/m.html" {
		t.Errorf("named page: %q, want /e/m.html", w.Header().Get(notFoundHeader))
	}

	spa := e.createSite(t, map[string]string{"spa": "true"}, map[string]string{"index.html": "x"})
	if w := e.internal(t, authzReq(host(spa), "/missing", "")); w.Code != 200 || w.Header().Get(notFoundHeader) != "" {
		t.Errorf("SPA: %d %q, want no header", w.Code, w.Header().Get(notFoundHeader))
	}

	viewer := e.createSite(t, map[string]string{"mode": "viewer"}, map[string]string{"a.md": "# a"})
	if w := e.internal(t, authzReq(host(viewer), "/_raw/", "")); w.Code != 200 || w.Header().Get(notFoundHeader) != "" {
		t.Errorf("viewer: %d %q, want no header", w.Code, w.Header().Get(notFoundHeader))
	}

	// A path view names it too: the page is relative to the site's root.
	req := authzReq("sitebin.example", "/v/"+web.ID+"/missing", "")
	req.Header.Set("X-Sitebin-View", web.ID)
	if w := e.internal(t, req); w.Header().Get(notFoundHeader) != "/404.html" {
		t.Errorf("path view: %q, want /404.html", w.Header().Get(notFoundHeader))
	}
}

func TestAuthzNamesNoNotFoundPageForAContainer(t *testing.T) {
	withContainers(t, &fakeRuntime{})
	e := newEnv(t, nil)
	c := e.createSite(t, map[string]string{"mode": "container"}, nil)
	running(t, e, c.ID, ext.ContainerService{Name: "app", Host: "sb-" + c.ID + "-app", Ports: []int{3000}})
	if err := e.api.SiteService().SyncContainerEndpoints(c.ID, []string{"app:3000"}); err != nil {
		t.Fatal(err)
	}
	w := authzFor(t, e, c.ID+"."+e.cfg.ViewDomain, nil)
	if w.Code != 200 || w.Header().Get(upstreamHeader) == "" {
		t.Fatalf("container not routed: %d %q", w.Code, w.Header().Get(upstreamHeader))
	}
	if got := w.Header().Get(notFoundHeader); got != "" {
		t.Errorf("container: %s = %q, want none (the app answers its own 404s)", notFoundHeader, got)
	}
}

func TestSiteNotFoundRoute(t *testing.T) {
	e := newEnv(t, nil)
	c := e.createSite(t, nil, map[string]string{"index.html": "x"})
	for _, method := range []string{"GET", "HEAD"} {
		req := httptest.NewRequest(method, "/_sitebin/not-found", nil)
		req.Host = c.ID + ".sitebin.example"
		w := httptest.NewRecorder()
		e.api.Public().ServeHTTP(w, req)
		if w.Code != 404 {
			t.Errorf("%s: status %d, want 404", method, w.Code)
		}
		if got := w.Header().Get("Cache-Control"); got != "no-store" {
			t.Errorf("%s: Cache-Control %q, want no-store", method, got)
		}
		if method == "GET" && !strings.Contains(w.Body.String(), "Page not found") {
			t.Errorf("GET body does not say Page not found: %s", w.Body)
		}
	}
}
