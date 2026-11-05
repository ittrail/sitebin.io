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
