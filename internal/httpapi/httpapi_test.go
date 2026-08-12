package httpapi

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"os"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/ittrail/sitebin.io/internal/config"
	"github.com/ittrail/sitebin.io/internal/ext"
	"github.com/ittrail/sitebin.io/internal/store"
)

var testFS = fstest.MapFS{
	"static/index.html":     {Data: []byte("<html>landing</html>")},
	"static/edit.html":      {Data: []byte("<html>edit</html>")},
	"static/app.css":        {Data: []byte("body{}")},
	"viewer/viewer.js":      {Data: []byte("// viewer")},
	"static/favicon.svg":    {Data: []byte("<svg/>")},
	"static/embed.js":       {Data: []byte("// sitebin-drop")},
	"vendor/markdown-it.js": {Data: []byte("// md")},
}

type env struct {
	api *API
	st  *store.Store
	cfg config.Config
}

func newEnv(t *testing.T, over map[string]string) *env {
	t.Helper()
	vars := map[string]string{
		"SITEBIN_BASE_DOMAIN": "sitebin.example",
		"SITEBIN_HTTP_ONLY":   "true",
		"SITEBIN_DATA_DIR":    t.TempDir(),
	}
	for k, v := range over {
		vars[k] = v
	}
	cfg, err := config.Load(func(k string) string { return vars[k] })
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.New(cfg.DataDir, cfg.BaseDomain, cfg.MaxSiteBytes, cfg.MaxFiles)
	if err != nil {
		t.Fatal(err)
	}
	api, err := New(cfg, st, []byte("0123456789abcdef0123456789abcdef"), testFS)
	if err != nil {
		t.Fatal(err)
	}
	return &env{api: api, st: st, cfg: cfg}
}

func (e *env) public(t *testing.T, req *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	if req.Host == "" || req.Host == "example.com" {
		req.Host = "sitebin.example"
	}
	w := httptest.NewRecorder()
	e.api.Public().ServeHTTP(w, req)
	return w
}

func (e *env) internal(t *testing.T, req *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	e.api.Internal().ServeHTTP(w, req)
	return w
}

type createResp struct {
	ID              string   `json:"id"`
	ViewURL         string   `json:"view_url"`
	EditURL         string   `json:"edit_url"`
	EditPassword    string   `json:"edit_password"`
	Mode            string   `json:"mode"`
	WebDAVURL       string   `json:"webdav_url"`
	FTPURL          string   `json:"ftp_url"`
	Domains         []string `json:"custom_domains"`
	ExpiryCapDays   int      `json:"expiry_cap_days"`
	ExpiryRenews    bool     `json:"expiry_renews"`
	AccountsEnabled bool     `json:"accounts_enabled"`
	WebDAVAvailable bool     `json:"webdav_available"`
	FTPAvailable    bool     `json:"ftp_available"`
}

// createSite posts a multipart create request with the given files.
func (e *env) createSite(t *testing.T, fields map[string]string, files map[string]string) createResp {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for k, v := range fields {
		mw.WriteField(k, v)
	}
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
	var out createResp
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("create response: %v", err)
	}
	return out
}

func editIDFrom(t *testing.T, editURL string) string {
	t.Helper()
	i := strings.LastIndex(editURL, "/e/")
	if i < 0 {
		t.Fatalf("bad edit url %q", editURL)
	}
	return editURL[i+3:]
}

func authed(req *http.Request, pw string) *http.Request {
	req.Header.Set("X-Edit-Password", pw)
	return req
}

func TestCreateSite(t *testing.T) {
	e := newEnv(t, nil)
	c := e.createSite(t, nil, map[string]string{"index.html": "<h1>hello</h1>"})
	if c.EditPassword == "" || len(c.EditPassword) != 22 {
		t.Errorf("edit password %q", c.EditPassword)
	}
	if !strings.HasPrefix(c.ViewURL, "http://") || !strings.Contains(c.ViewURL, ".sitebin.example") {
		t.Errorf("view url %q", c.ViewURL)
	}
	if !strings.Contains(c.EditURL, "sitebin.example/e/") {
		t.Errorf("edit url %q", c.EditURL)
	}
	if c.Mode != "webserver" {
		t.Errorf("mode %q", c.Mode)
	}
	site, err := e.st.ByViewID(c.ID)
	if err != nil {
		t.Fatalf("site not stored: %v", err)
	}
	files, _ := e.st.ListFiles(site)
	if len(files) != 1 || files[0].Path != "index.html" {
		t.Errorf("files = %+v", files)
	}
}

func TestCreateSiteViewerModeAndSettings(t *testing.T) {
	e := newEnv(t, nil)
	c := e.createSite(t, map[string]string{
		"mode":          "viewer",
		"view_password": "sesame",
		"webdav":        "true",
		"expires_at":    time.Now().Add(48 * time.Hour).UTC().Format(time.RFC3339),
	}, map[string]string{"doc.md": "# doc"})
	if c.Mode != "viewer" {
		t.Errorf("mode = %q", c.Mode)
	}
	if c.WebDAVURL == "" {
		t.Error("webdav url missing")
	}
	site, _ := e.st.ByViewID(c.ID)
	if !site.Meta.ViewPasswordProtected || site.Meta.ViewPasswordHash == "" {
		t.Error("view password not set")
	}
	if !site.Meta.WebDAVEnabled {
		t.Error("webdav not enabled")
	}
	if site.Meta.ExpiresAt == nil {
		t.Error("expiry not set")
	}
	if site.Meta.EntryFile != "doc.md" {
		t.Errorf("entry = %q", site.Meta.EntryFile)
	}
}

func TestCreateRateLimit(t *testing.T) {
	e := newEnv(t, map[string]string{"SITEBIN_RATE_CREATE_PER_HOUR": "30", "SITEBIN_RATE_CREATE_BURST": "3"})
	for i := 0; i < 3; i++ {
		req := httptest.NewRequest("POST", "/api/sites", nil)
		if w := e.public(t, req); w.Code != 201 {
			t.Fatalf("create %d: %d", i, w.Code)
		}
	}
	req := httptest.NewRequest("POST", "/api/sites", nil)
	if w := e.public(t, req); w.Code != 429 {
		t.Fatalf("expected 429, got %d", w.Code)
	}
}

func TestReadOnlyBlocksCreate(t *testing.T) {
	e := newEnv(t, map[string]string{"SITEBIN_READONLY": "true"})
	req := httptest.NewRequest("POST", "/api/sites", nil)
	if w := e.public(t, req); w.Code != 503 {
		t.Fatalf("expected 503, got %d", w.Code)
	}
}

func TestGetSiteAuth(t *testing.T) {
	e := newEnv(t, nil)
	c := e.createSite(t, nil, map[string]string{"a.txt": "x"})
	edit := editIDFrom(t, c.EditURL)

	// no password
	w := e.public(t, httptest.NewRequest("GET", "/api/sites/"+edit, nil))
	if w.Code != 401 {
		t.Fatalf("no pw: %d", w.Code)
	}
	// wrong password
	w = e.public(t, authed(httptest.NewRequest("GET", "/api/sites/"+edit, nil), "wrong"))
	if w.Code != 401 {
		t.Fatalf("wrong pw: %d", w.Code)
	}
	// right password
	w = e.public(t, authed(httptest.NewRequest("GET", "/api/sites/"+edit, nil), c.EditPassword))
	if w.Code != 200 {
		t.Fatalf("right pw: %d %s", w.Code, w.Body)
	}
	body := w.Body.String()
	if strings.Contains(body, "hash") || strings.Contains(body, "$argon2id$") {
		t.Errorf("response leaks hash: %s", body)
	}
	var got struct {
		Files []store.FileInfo `json:"files"`
		Usage struct {
			MaxBytes int64 `json:"max_bytes"`
		} `json:"usage"`
	}
	json.Unmarshal(w.Body.Bytes(), &got)
	if len(got.Files) != 1 || got.Usage.MaxBytes == 0 {
		t.Errorf("payload incomplete: %s", body)
	}
	// unknown edit id
	w = e.public(t, authed(httptest.NewRequest("GET", "/api/sites/aaaaaaaaaaaaaaaaaaaaaaaaaa", nil), "x"))
	if w.Code != 404 {
		t.Fatalf("unknown id: %d", w.Code)
	}
}

func TestEditPasswordRateLimit(t *testing.T) {
	e := newEnv(t, map[string]string{"SITEBIN_RATE_AUTH_PER_5MIN": "3"})
	c := e.createSite(t, nil, nil)
	edit := editIDFrom(t, c.EditURL)
	last := 0
	for i := 0; i < 5; i++ {
		w := e.public(t, authed(httptest.NewRequest("GET", "/api/sites/"+edit, nil), "wrong"))
		last = w.Code
	}
	if last != 429 {
		t.Fatalf("expected 429 after repeated failures, got %d", last)
	}
}

func TestUpdateSettings(t *testing.T) {
	e := newEnv(t, nil)
	c := e.createSite(t, nil, map[string]string{"report.pdf": "%PDF", "readme.md": "# r"})
	edit := editIDFrom(t, c.EditURL)

	put := func(body string) *httptest.ResponseRecorder {
		req := authed(httptest.NewRequest("PUT", "/api/sites/"+edit, strings.NewReader(body)), c.EditPassword)
		req.Header.Set("Content-Type", "application/json")
		return e.public(t, req)
	}

	// switch to viewer
	if w := put(`{"mode":"viewer","entry_file":"report.pdf"}`); w.Code != 200 {
		t.Fatalf("PUT viewer: %d %s", w.Code, w.Body)
	}
	site, _ := e.st.ByViewID(c.ID)
	if site.Meta.Mode != "viewer" {
		t.Error("mode not persisted")
	}
	if b, err := os.ReadFile(site.FilesDir() + "/index.html"); err != nil || !strings.Contains(string(b), "report.pdf") {
		t.Errorf("wrapper not generated: %v", err)
	}

	// enable view password + webdav + expiry
	exp := time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339)
	if w := put(`{"view_password":"sesame","webdav_enabled":true,"expires_at":"` + exp + `"}`); w.Code != 200 {
		t.Fatalf("PUT settings: %d %s", w.Code, w.Body)
	}
	site, _ = e.st.ByViewID(c.ID)
	if !site.Meta.ViewPasswordProtected || site.Meta.ViewPasswordHash == "" || !site.Meta.WebDAVEnabled || site.Meta.ExpiresAt == nil {
		t.Errorf("settings not applied: %+v", site.Meta)
	}

	// clear password + expiry
	if w := put(`{"view_password":"","expires_at":null}`); w.Code != 200 {
		t.Fatalf("PUT clear: %d %s", w.Code, w.Body)
	}
	site, _ = e.st.ByViewID(c.ID)
	if site.Meta.ViewPasswordProtected || site.Meta.ViewPasswordHash != "" {
		t.Error("view password not cleared")
	}
	// note: expires_at:null currently means "clear" — verified via API response
	if site.Meta.ExpiresAt != nil {
		t.Error("expiry not cleared")
	}

	// back to webserver: original tree restored
	if w := put(`{"mode":"webserver"}`); w.Code != 200 {
		t.Fatalf("PUT webserver: %d %s", w.Code, w.Body)
	}
	site, _ = e.st.ByViewID(c.ID)
	files, _ := e.st.ListFiles(site)
	if len(files) != 2 {
		t.Errorf("restored files = %+v", files)
	}

	// invalid mode
	if w := put(`{"mode":"php"}`); w.Code != 400 {
		t.Fatalf("bad mode: %d", w.Code)
	}
}

func TestExpiryCap(t *testing.T) {
	e := newEnv(t, map[string]string{"SITEBIN_MAX_EXPIRY_DAYS": "7"})
	far := time.Now().Add(30 * 24 * time.Hour).UTC().Format(time.RFC3339)
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	mw.WriteField("expires_at", far)
	mw.Close()
	req := httptest.NewRequest("POST", "/api/sites", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	w := e.public(t, req)
	if w.Code != 400 {
		t.Fatalf("expected 400 for expiry beyond cap, got %d %s", w.Code, w.Body)
	}
}

func TestUploadAndDeleteFiles(t *testing.T) {
	e := newEnv(t, nil)
	c := e.createSite(t, nil, map[string]string{"index.html": "v1"})
	edit := editIDFrom(t, c.EditURL)

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", `form-data; name="files"; filename="assets/deep/app.js"`)
	p, _ := mw.CreatePart(h)
	p.Write([]byte("js"))
	mw.Close()
	req := authed(httptest.NewRequest("POST", "/api/sites/"+edit+"/files", &buf), c.EditPassword)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if w := e.public(t, req); w.Code != 200 {
		t.Fatalf("upload: %d %s", w.Code, w.Body)
	}
	site, _ := e.st.ByViewID(c.ID)
	files, _ := e.st.ListFiles(site)
	if len(files) != 2 {
		t.Fatalf("files = %+v", files)
	}

	// traversal rejected
	var buf2 bytes.Buffer
	mw2 := multipart.NewWriter(&buf2)
	h2 := textproto.MIMEHeader{}
	h2.Set("Content-Disposition", `form-data; name="files"; filename="../../evil.txt"`)
	p2, _ := mw2.CreatePart(h2)
	p2.Write([]byte("x"))
	mw2.Close()
	req = authed(httptest.NewRequest("POST", "/api/sites/"+edit+"/files", &buf2), c.EditPassword)
	req.Header.Set("Content-Type", mw2.FormDataContentType())
	if w := e.public(t, req); w.Code != 400 {
		t.Fatalf("traversal upload: %d", w.Code)
	}

	// delete nested file
	req = authed(httptest.NewRequest("DELETE", "/api/sites/"+edit+"/files/assets/deep/app.js", nil), c.EditPassword)
	if w := e.public(t, req); w.Code != 200 {
		t.Fatalf("delete: %d %s", w.Code, w.Body)
	}
	files, _ = e.st.ListFiles(site)
	if len(files) != 1 {
		t.Errorf("after delete: %+v", files)
	}
}

func TestCustomDomainsGatedInCommunity(t *testing.T) {
	e := newEnv(t, nil) // no provider registered = community
	c := e.createSite(t, nil, nil)
	edit := editIDFrom(t, c.EditURL)
	req := authed(httptest.NewRequest("POST", "/api/sites/"+edit+"/domains", strings.NewReader(`{"domain":"client.example.org"}`)), c.EditPassword)
	req.Header.Set("Content-Type", "application/json")
	if w := e.public(t, req); w.Code != 403 {
		t.Fatalf("custom domains should be gated in community: %d", w.Code)
	}
}

func TestReplaceAllUpload(t *testing.T) {
	// deploy's update mode uses POST /files?replace=true: old files are wiped
	// and only the new upload remains.
	e := newEnv(t, nil)
	c := e.createSite(t, nil, map[string]string{"index.html": "v1", "css/old.css": "x"})
	edit := editIDFrom(t, c.EditURL)

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", `form-data; name="files"; filename="index.html"`)
	p, _ := mw.CreatePart(h)
	p.Write([]byte("v2"))
	mw.Close()
	req := authed(httptest.NewRequest("POST", "/api/sites/"+edit+"/files?replace=true", &buf), c.EditPassword)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if w := e.public(t, req); w.Code != 200 {
		t.Fatalf("replace upload: %d %s", w.Code, w.Body)
	}
	site, _ := e.st.ByViewID(c.ID)
	files, _ := e.st.ListFiles(site)
	if len(files) != 1 || files[0].Path != "index.html" {
		t.Errorf("after replace: %+v (old files should be gone)", files)
	}
}

func TestDomains(t *testing.T) {
	ext.Register(&fakeProvider{domainsOK: true}) // enterprise: custom domains on
	defer ext.Reset()
	e := newEnv(t, nil)
	c := e.createSite(t, nil, nil)
	edit := editIDFrom(t, c.EditURL)

	req := authed(httptest.NewRequest("POST", "/api/sites/"+edit+"/domains", strings.NewReader(`{"domain":"Client.Example.org"}`)), c.EditPassword)
	req.Header.Set("Content-Type", "application/json")
	w := e.public(t, req)
	if w.Code != 200 {
		t.Fatalf("add domain: %d %s", w.Code, w.Body)
	}
	if !strings.Contains(w.Body.String(), "client.example.org") {
		t.Errorf("normalized domain missing: %s", w.Body)
	}

	// tls-check now approves it
	iw := e.internal(t, httptest.NewRequest("GET", "/internal/tls-check?domain=client.example.org", nil))
	if iw.Code != 200 {
		t.Fatalf("tls-check yes: %d", iw.Code)
	}
	iw = e.internal(t, httptest.NewRequest("GET", "/internal/tls-check?domain=other.example.org", nil))
	if iw.Code != 404 {
		t.Fatalf("tls-check no: %d", iw.Code)
	}

	// reserved base domain refused
	req = authed(httptest.NewRequest("POST", "/api/sites/"+edit+"/domains", strings.NewReader(`{"domain":"x.sitebin.example"}`)), c.EditPassword)
	req.Header.Set("Content-Type", "application/json")
	if w := e.public(t, req); w.Code != 400 {
		t.Fatalf("reserved domain: %d", w.Code)
	}

	// remove
	req = authed(httptest.NewRequest("DELETE", "/api/sites/"+edit+"/domains/client.example.org", nil), c.EditPassword)
	if w := e.public(t, req); w.Code != 200 {
		t.Fatalf("remove domain: %d %s", w.Code, w.Body)
	}
	iw = e.internal(t, httptest.NewRequest("GET", "/internal/tls-check?domain=client.example.org", nil))
	if iw.Code != 404 {
		t.Fatalf("tls-check after removal: %d", iw.Code)
	}
}

func TestDeleteSite(t *testing.T) {
	e := newEnv(t, nil)
	c := e.createSite(t, nil, map[string]string{"index.html": "x"})
	edit := editIDFrom(t, c.EditURL)
	req := authed(httptest.NewRequest("DELETE", "/api/sites/"+edit, nil), c.EditPassword)
	if w := e.public(t, req); w.Code != 200 {
		t.Fatalf("delete: %d %s", w.Code, w.Body)
	}
	if _, err := e.st.ByViewID(c.ID); err == nil {
		t.Error("site still exists")
	}
}

// ---- authz / gate ----

func authzReq(host, uri, cookie string) *http.Request {
	req := httptest.NewRequest("GET", "/internal/authz", nil)
	req.Header.Set("X-Forwarded-Host", host)
	req.Header.Set("X-Forwarded-Uri", uri)
	req.Header.Set("X-Forwarded-Method", "GET")
	if cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	return req
}

func TestAuthzOpenSite(t *testing.T) {
	e := newEnv(t, nil)
	c := e.createSite(t, nil, map[string]string{"index.html": "x"})
	if w := e.internal(t, authzReq(c.ID+".sitebin.example", "/", "")); w.Code != 200 {
		t.Fatalf("open site: %d", w.Code)
	}
	// unknown host
	if w := e.internal(t, authzReq("nosuch.sitebin.example", "/", "")); w.Code != 404 {
		t.Fatalf("unknown: %d", w.Code)
	}
	// host with port is fine
	if w := e.internal(t, authzReq(c.ID+".sitebin.example:8085", "/", "")); w.Code != 200 {
		t.Fatalf("with port: %d", w.Code)
	}
}

func TestAuthzPasswordFlow(t *testing.T) {
	e := newEnv(t, nil)
	c := e.createSite(t, map[string]string{"view_password": "sesame"}, map[string]string{"index.html": "secret"})
	host := c.ID + ".sitebin.example"

	w := e.internal(t, authzReq(host, "/page?x=1", ""))
	if w.Code != 401 {
		t.Fatalf("gate: %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "/_sitebin/unlock") || !strings.Contains(body, "password") {
		t.Errorf("gate page incomplete: %s", body)
	}

	// wrong password at unlock
	form := strings.NewReader("password=nope&redirect=%2Fpage%3Fx%3D1")
	req := httptest.NewRequest("POST", "/_sitebin/unlock", form)
	req.Host = host
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	uw := e.public(t, req)
	if uw.Code != 401 {
		t.Fatalf("unlock wrong pw: %d", uw.Code)
	}

	// correct password
	form = strings.NewReader("password=sesame&redirect=%2Fpage%3Fx%3D1")
	req = httptest.NewRequest("POST", "/_sitebin/unlock", form)
	req.Host = host
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	uw = e.public(t, req)
	if uw.Code != 303 {
		t.Fatalf("unlock: %d %s", uw.Code, uw.Body)
	}
	if loc := uw.Header().Get("Location"); loc != "/page?x=1" {
		t.Errorf("redirect = %q", loc)
	}
	cookie := uw.Header().Get("Set-Cookie")
	if !strings.Contains(cookie, "sitebin_v=") || !strings.Contains(cookie, "HttpOnly") {
		t.Fatalf("cookie = %q", cookie)
	}

	// cookie passes the gate
	if w := e.internal(t, authzReq(host, "/", strings.Split(cookie, ";")[0])); w.Code != 200 {
		t.Fatalf("with cookie: %d", w.Code)
	}

	// cookie stops working after password change (cache-bust via hash binding)
	edit := editIDFrom(t, c.EditURL)
	preq := authed(httptest.NewRequest("PUT", "/api/sites/"+edit, strings.NewReader(`{"view_password":"newpass"}`)), c.EditPassword)
	preq.Header.Set("Content-Type", "application/json")
	if w := e.public(t, preq); w.Code != 200 {
		t.Fatalf("PUT: %d", w.Code)
	}
	if w := e.internal(t, authzReq(host, "/", strings.Split(cookie, ";")[0])); w.Code != 401 {
		t.Fatalf("stale cookie still valid: %d", w.Code)
	}

	// open redirect prevented
	form = strings.NewReader("password=newpass&redirect=https%3A%2F%2Fevil.example")
	req = httptest.NewRequest("POST", "/_sitebin/unlock", form)
	req.Host = host
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	uw = e.public(t, req)
	if loc := uw.Header().Get("Location"); loc != "/" {
		t.Errorf("open redirect: %q", loc)
	}
}

func TestAuthzUsesForwardedHostNotRequestHost(t *testing.T) {
	// authz must resolve the site from X-Forwarded-Host (which Caddy pins to
	// the real request host), independent of the connection's own Host header.
	e := newEnv(t, nil)
	prot := e.createSite(t, map[string]string{"view_password": "sesame"}, map[string]string{"index.html": "secret"})
	open := e.createSite(t, nil, map[string]string{"index.html": "public"})

	// A request whose X-Forwarded-Host is the protected site must gate (401),
	// even if the raw Host header names the open site.
	req := authzReq(prot.ID+".sitebin.example", "/", "")
	req.Host = open.ID + ".sitebin.example"
	if w := e.internal(t, req); w.Code != 401 {
		t.Fatalf("protected site via forwarded host should gate: %d", w.Code)
	}
	// And the open site resolved via forwarded host serves.
	req = authzReq(open.ID+".sitebin.example", "/", "")
	if w := e.internal(t, req); w.Code != 200 {
		t.Fatalf("open site via forwarded host: %d", w.Code)
	}
}

func TestAuthzExpired(t *testing.T) {
	e := newEnv(t, nil)
	past := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	c := e.createSite(t, map[string]string{"expires_at": past}, map[string]string{"index.html": "x"})
	w := e.internal(t, authzReq(c.ID+".sitebin.example", "/", ""))
	if w.Code != 410 {
		t.Fatalf("expired: %d", w.Code)
	}
}

func TestAuthzCustomDomain(t *testing.T) {
	ext.Register(&fakeProvider{domainsOK: true})
	defer ext.Reset()
	e := newEnv(t, nil)
	c := e.createSite(t, nil, map[string]string{"index.html": "x"})
	edit := editIDFrom(t, c.EditURL)
	req := authed(httptest.NewRequest("POST", "/api/sites/"+edit+"/domains", strings.NewReader(`{"domain":"client.example.org"}`)), c.EditPassword)
	req.Header.Set("Content-Type", "application/json")
	if w := e.public(t, req); w.Code != 200 {
		t.Fatalf("add domain: %d", w.Code)
	}
	if w := e.internal(t, authzReq("client.example.org", "/", "")); w.Code != 200 {
		t.Fatalf("custom domain authz: %d", w.Code)
	}
}

func TestAuthzPathMode(t *testing.T) {
	e := newEnv(t, nil)
	c := e.createSite(t, nil, map[string]string{"index.html": "x"})

	req := httptest.NewRequest("GET", "/internal/authz", nil)
	req.Header.Set("X-Sitebin-View", c.ID)
	req.Header.Set("X-Forwarded-Uri", "/v/"+c.ID+"/")
	if w := e.internal(t, req); w.Code != 200 {
		t.Fatalf("path authz open site: %d", w.Code)
	}
	// unknown id → 404
	req = httptest.NewRequest("GET", "/internal/authz", nil)
	req.Header.Set("X-Sitebin-View", "aaaaaaaaaaaaaaaaaaaaaaaaaa")
	if w := e.internal(t, req); w.Code != 404 {
		t.Fatalf("path authz unknown: %d", w.Code)
	}
}

func TestPathModeGateAndUnlock(t *testing.T) {
	e := newEnv(t, nil)
	c := e.createSite(t, map[string]string{"view_password": "sesame"}, map[string]string{"index.html": "secret"})

	// gate via path mode
	req := httptest.NewRequest("GET", "/internal/authz", nil)
	req.Header.Set("X-Sitebin-View", c.ID)
	req.Header.Set("X-Forwarded-Uri", "/v/"+c.ID+"/page")
	w := e.internal(t, req)
	if w.Code != 401 {
		t.Fatalf("path gate: %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), `name="site" value="`+c.ID+`"`) {
		t.Errorf("gate form missing site field:\n%s", w.Body)
	}

	// unlock carrying the site id
	form := "password=sesame&redirect=%2Fv%2F" + c.ID + "%2Fpage&site=" + c.ID
	ureq := httptest.NewRequest("POST", "/_sitebin/unlock", strings.NewReader(form))
	ureq.Host = "sitebin.example"
	ureq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	uw := e.public(t, ureq)
	if uw.Code != 303 {
		t.Fatalf("unlock: %d %s", uw.Code, uw.Body)
	}
	if loc := uw.Header().Get("Location"); loc != "/v/"+c.ID+"/page" {
		t.Errorf("redirect = %q", loc)
	}
	cookie := uw.Header().Get("Set-Cookie")
	if !strings.Contains(cookie, "Path=/v/"+c.ID+"/") {
		t.Fatalf("cookie not path-scoped: %q", cookie)
	}

	// cookie passes the path-mode gate
	creq := httptest.NewRequest("GET", "/internal/authz", nil)
	creq.Header.Set("X-Sitebin-View", c.ID)
	creq.Header.Set("Cookie", strings.Split(cookie, ";")[0])
	if w := e.internal(t, creq); w.Code != 200 {
		t.Fatalf("cookie path authz: %d", w.Code)
	}
}

func TestReadFileContent(t *testing.T) {
	e := newEnv(t, nil)
	c := e.createSite(t, nil, map[string]string{"js/app.js": "console.log(42)"})
	edit := editIDFrom(t, c.EditURL)

	w := e.public(t, authed(httptest.NewRequest("GET", "/api/sites/"+edit+"/content/js/app.js", nil), c.EditPassword))
	if w.Code != 200 || w.Body.String() != "console.log(42)" {
		t.Fatalf("read file = %d %q", w.Code, w.Body)
	}
	// missing file → 404
	if w := e.public(t, authed(httptest.NewRequest("GET", "/api/sites/"+edit+"/content/nope.txt", nil), c.EditPassword)); w.Code != 404 {
		t.Errorf("missing file: %d", w.Code)
	}
	// traversal rejected
	if w := e.public(t, authed(httptest.NewRequest("GET", "/api/sites/"+edit+"/content/../meta.json", nil), c.EditPassword)); w.Code == 200 {
		t.Errorf("traversal read allowed: %d", w.Code)
	}
	// auth required
	if w := e.public(t, httptest.NewRequest("GET", "/api/sites/"+edit+"/content/js/app.js", nil)); w.Code != 401 {
		t.Errorf("no auth: %d", w.Code)
	}
}

func TestDownloadZip(t *testing.T) {
	e := newEnv(t, nil)
	c := e.createSite(t, nil, map[string]string{"index.html": "<h1>hi</h1>", "css/x.css": "body{}"})
	edit := editIDFrom(t, c.EditURL)

	w := e.public(t, authed(httptest.NewRequest("GET", "/api/sites/"+edit+"/download", nil), c.EditPassword))
	if w.Code != 200 {
		t.Fatalf("download: %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/zip" {
		t.Errorf("content-type = %q", ct)
	}
	zr, err := zip.NewReader(bytes.NewReader(w.Body.Bytes()), int64(w.Body.Len()))
	if err != nil {
		t.Fatalf("zip parse: %v", err)
	}
	names := map[string]bool{}
	for _, f := range zr.File {
		names[f.Name] = true
	}
	if !names["index.html"] || !names["css/x.css"] {
		t.Errorf("zip missing files: %v", names)
	}
	// requires auth
	if w := e.public(t, httptest.NewRequest("GET", "/api/sites/"+edit+"/download", nil)); w.Code != 401 {
		t.Errorf("download without auth: %d", w.Code)
	}
}

func TestFTPAuth(t *testing.T) {
	e := newEnv(t, map[string]string{"SITEBIN_FTP_ENABLED": "true"})
	c := e.createSite(t, map[string]string{"ftp": "true"}, map[string]string{"index.html": "x"})
	edit := editIDFrom(t, c.EditURL)

	dir, maxBytes, _, err := e.api.FTPAuth(edit, c.EditPassword, "1.2.3.4")
	if err != nil {
		t.Fatalf("FTPAuth: %v", err)
	}
	if dir == "" || maxBytes == 0 {
		t.Errorf("dir=%q maxBytes=%d", dir, maxBytes)
	}
	// wrong password
	if _, _, _, err := e.api.FTPAuth(edit, "wrong", "1.2.3.4"); err == nil {
		t.Error("wrong password accepted")
	}
	// unknown site
	if _, _, _, err := e.api.FTPAuth("aaaaaaaaaaaaaaaaaaaaaaaaaa", c.EditPassword, "1.2.3.4"); err == nil {
		t.Error("unknown site accepted")
	}
}

func TestFTPAuthDisabledPerSite(t *testing.T) {
	e := newEnv(t, map[string]string{"SITEBIN_FTP_ENABLED": "true"})
	c := e.createSite(t, nil, nil) // ftp not enabled on the site
	edit := editIDFrom(t, c.EditURL)
	if _, _, _, err := e.api.FTPAuth(edit, c.EditPassword, "1.2.3.4"); err == nil {
		t.Error("ftp should be off for this site")
	}
}

func TestFTPAuthGloballyDisabled(t *testing.T) {
	e := newEnv(t, nil) // SITEBIN_FTP_ENABLED unset = off
	c := e.createSite(t, map[string]string{"ftp": "true"}, nil)
	edit := editIDFrom(t, c.EditURL)
	site, _ := e.st.ByViewID(c.ID)
	if site.Meta.FTPEnabled {
		t.Error("per-site ftp should not enable when global ftp is off")
	}
	if _, _, _, err := e.api.FTPAuth(edit, c.EditPassword, "1.2.3.4"); err == nil {
		t.Error("ftp should be globally disabled")
	}
}

func TestFTPAuthRefusesAnonymousSiteWhenAccountsEnabled(t *testing.T) {
	// FTP authenticates with the edit password directly, bypassing the JSON
	// API entirely. ee/eeconfig.Tier has no FTP field at all, so nothing else
	// stops an anonymous drop from toggling ftp_enabled on its own edit page
	// and then being driven by an FTP client unless FTPAuth enforces the
	// "no account, no API" rule itself.
	ext.Register(&fakeProvider{enabled: true})
	defer ext.Reset()
	e := newEnv(t, map[string]string{"SITEBIN_FTP_ENABLED": "true"})
	c := e.createSite(t, map[string]string{"ftp": "true"}, map[string]string{"index.html": "x"})
	edit := editIDFrom(t, c.EditURL)

	if _, _, _, err := e.api.FTPAuth(edit, c.EditPassword, "1.2.3.4"); err == nil {
		t.Error("anonymous site should have no FTP access when accounts are enabled")
	}
}

func TestFTPAuthAllowsOwnedSiteWhenAccountsEnabled(t *testing.T) {
	ext.Register(&fakeProvider{enabled: true, owner: "acct-1"})
	defer ext.Reset()
	e := newEnv(t, map[string]string{"SITEBIN_FTP_ENABLED": "true"})
	c := e.createSite(t, map[string]string{"ftp": "true"}, map[string]string{"index.html": "x"})
	edit := editIDFrom(t, c.EditURL)

	if _, _, _, err := e.api.FTPAuth(edit, c.EditPassword, "1.2.3.4"); err != nil {
		t.Errorf("owned site should have FTP access: %v", err)
	}
}

func TestFTPAuthCommunityBuildStaysOpen(t *testing.T) {
	e := newEnv(t, map[string]string{"SITEBIN_FTP_ENABLED": "true"}) // no provider registered
	c := e.createSite(t, map[string]string{"ftp": "true"}, map[string]string{"index.html": "x"})
	edit := editIDFrom(t, c.EditURL)

	if _, _, _, err := e.api.FTPAuth(edit, c.EditPassword, "1.2.3.4"); err != nil {
		t.Errorf("community build should have FTP access: %v", err)
	}
}

func TestAbuseReport(t *testing.T) {
	e := newEnv(t, nil)
	c := e.createSite(t, nil, map[string]string{"index.html": "x"})

	req := httptest.NewRequest("POST", "/api/report",
		strings.NewReader(`{"target":"`+c.ViewURL+`","reason":"phishing","details":"looks fake"}`))
	req.Header.Set("Content-Type", "application/json")
	if w := e.public(t, req); w.Code != 202 {
		t.Fatalf("report: %d %s", w.Code, w.Body)
	}
	reports, err := e.st.ListReports()
	if err != nil || len(reports) != 1 {
		t.Fatalf("reports = %v, %v", reports, err)
	}
	if reports[0].Reason != "phishing" || reports[0].ViewID != c.ID {
		t.Errorf("report resolved wrong: %+v", reports[0])
	}
	// reason required
	req = httptest.NewRequest("POST", "/api/report", strings.NewReader(`{"target":"x"}`))
	req.Header.Set("Content-Type", "application/json")
	if w := e.public(t, req); w.Code != 400 {
		t.Errorf("missing reason: %d", w.Code)
	}
}

func TestViewCounter(t *testing.T) {
	e := newEnv(t, nil)
	c := e.createSite(t, nil, map[string]string{"index.html": "x"})

	page := func() *http.Request {
		r := authzReq(c.ID+".sitebin.example", "/", "")
		r.Header.Set("Accept", "text/html")
		return r
	}
	for i := 0; i < 3; i++ {
		if w := e.internal(t, page()); w.Code != 200 {
			t.Fatalf("authz %d: %d", i, w.Code)
		}
	}
	// an asset fetch (no text/html Accept) must NOT be counted
	if w := e.internal(t, authzReq(c.ID+".sitebin.example", "/app.js", "")); w.Code != 200 {
		t.Fatal("asset authz")
	}

	site, _ := e.st.ByViewID(c.ID)
	st := e.st.Stats(site)
	if st.Views != 3 {
		t.Errorf("views = %d, want 3", st.Views)
	}
	if st.LastSeen == nil {
		t.Error("last_seen not set")
	}

	// payload surfaces the count
	edit := editIDFrom(t, c.EditURL)
	w := e.public(t, authed(httptest.NewRequest("GET", "/api/sites/"+edit, nil), c.EditPassword))
	if !strings.Contains(w.Body.String(), `"views":3`) {
		t.Errorf("payload missing views: %s", w.Body)
	}
}

func TestViewCounterDisabled(t *testing.T) {
	e := newEnv(t, map[string]string{"SITEBIN_TRACK_VIEWS": "false"})
	c := e.createSite(t, nil, map[string]string{"index.html": "x"})
	r := authzReq(c.ID+".sitebin.example", "/", "")
	r.Header.Set("Accept", "text/html")
	e.internal(t, r)
	site, _ := e.st.ByViewID(c.ID)
	if e.st.Stats(site).Views != 0 {
		t.Error("views counted while tracking disabled")
	}
}

func TestHealth(t *testing.T) {
	e := newEnv(t, nil)
	w := e.internal(t, httptest.NewRequest("GET", "/internal/health", nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), "ok") {
		t.Fatalf("health: %d %s", w.Code, w.Body)
	}
}

// ---- WebDAV ----

func davReq(t *testing.T, e *env, method, path, pw string, body io.Reader) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, body)
	req.SetBasicAuth("sitebin", pw)
	return e.public(t, req)
}

func TestWebDAV(t *testing.T) {
	e := newEnv(t, nil)
	c := e.createSite(t, map[string]string{"webdav": "true"}, map[string]string{"index.html": "x"})
	edit := editIDFrom(t, c.EditURL)
	base := "/dav/" + edit + "/"

	// PROPFIND listing
	req := httptest.NewRequest("PROPFIND", base, nil)
	req.SetBasicAuth("u", c.EditPassword)
	req.Header.Set("Depth", "1")
	w := e.public(t, req)
	if w.Code != 207 {
		t.Fatalf("PROPFIND: %d %s", w.Code, w.Body)
	}
	if !strings.Contains(w.Body.String(), "index.html") {
		t.Errorf("listing misses file: %s", w.Body)
	}

	// PUT new file
	if w := davReq(t, e, "PUT", base+"new.txt", c.EditPassword, strings.NewReader("dav!")); w.Code != 201 {
		t.Fatalf("PUT: %d %s", w.Code, w.Body)
	}
	// GET it back
	w = davReq(t, e, "GET", base+"new.txt", c.EditPassword, nil)
	if w.Code != 200 || w.Body.String() != "dav!" {
		t.Fatalf("GET: %d %q", w.Code, w.Body)
	}
	// DELETE
	if w := davReq(t, e, "DELETE", base+"new.txt", c.EditPassword, nil); w.Code != 204 {
		t.Fatalf("DELETE: %d", w.Code)
	}

	// wrong password
	if w := davReq(t, e, "GET", base+"index.html", "wrong", nil); w.Code != 401 {
		t.Fatalf("wrong pw: %d", w.Code)
	}
	// no auth at all → challenge
	req = httptest.NewRequest("PROPFIND", base, nil)
	w = e.public(t, req)
	if w.Code != 401 || !strings.Contains(w.Header().Get("WWW-Authenticate"), "Basic") {
		t.Fatalf("challenge: %d %q", w.Code, w.Header().Get("WWW-Authenticate"))
	}

	// traversal via DAV path: ServeMux cleans dot segments (redirect), and
	// CleanRelPath rejects anything that slips through — never a 2xx write
	if w := davReq(t, e, "PUT", base+"../evil.txt", c.EditPassword, strings.NewReader("x")); w.Code < 300 {
		t.Fatalf("dav traversal: %d", w.Code)
	}
}

func TestWebDAVDestinationGuard(t *testing.T) {
	e := newEnv(t, nil)
	a := e.createSite(t, map[string]string{"webdav": "true"}, map[string]string{"index.html": "a"})
	b := e.createSite(t, map[string]string{"webdav": "true"}, map[string]string{"index.html": "b"})
	aEdit, bEdit := editIDFrom(t, a.EditURL), editIDFrom(t, b.EditURL)

	// MOVE with a Destination pointing at another site must be refused.
	req := httptest.NewRequest("MOVE", "/dav/"+aEdit+"/index.html", nil)
	req.SetBasicAuth("u", a.EditPassword)
	req.Header.Set("Destination", "http://sitebin.example/dav/"+bEdit+"/stolen.html")
	if w := e.public(t, req); w.Code != 400 {
		t.Fatalf("cross-site MOVE should be 400, got %d", w.Code)
	}
	// MOVE with a traversal Destination must be refused.
	req = httptest.NewRequest("MOVE", "/dav/"+aEdit+"/index.html", nil)
	req.SetBasicAuth("u", a.EditPassword)
	req.Header.Set("Destination", "http://sitebin.example/dav/"+aEdit+"/../../../escape.html")
	if w := e.public(t, req); w.Code != 400 {
		t.Fatalf("traversal MOVE should be 400, got %d", w.Code)
	}
}

func TestWebDAVDisabled(t *testing.T) {
	e := newEnv(t, nil)
	c := e.createSite(t, nil, nil) // webdav off (default)
	edit := editIDFrom(t, c.EditURL)
	if w := davReq(t, e, "PROPFIND", "/dav/"+edit+"/", c.EditPassword, nil); w.Code != 404 {
		t.Fatalf("disabled per-site: %d", w.Code)
	}
}

func TestWebDAVGloballyDisabled(t *testing.T) {
	e := newEnv(t, map[string]string{"SITEBIN_WEBDAV_ENABLED": "false"})
	c := e.createSite(t, map[string]string{"webdav": "true"}, nil)
	edit := editIDFrom(t, c.EditURL)
	site, _ := e.st.ByViewID(c.ID)
	if site.Meta.WebDAVEnabled {
		t.Error("per-site webdav enabled despite global off")
	}
	if w := davReq(t, e, "PROPFIND", "/dav/"+edit+"/", c.EditPassword, nil); w.Code != 404 {
		t.Fatalf("globally disabled: %d", w.Code)
	}
}

func TestWebDAVWriteRenewsExpiry(t *testing.T) {
	ext.Register(&fakeProvider{enabled: true, owner: "acct-1", grant: ext.CreateGrant{MaxExpiryDays: 7}})
	defer ext.Reset()
	e := newEnv(t, nil)
	c := e.createSite(t, map[string]string{"webdav": "true"}, map[string]string{"index.html": "x"})
	edit := editIDFrom(t, c.EditURL)

	// wind the expiry back so a renewal is visible
	site, _ := e.st.ByViewID(c.ID)
	soon := time.Now().Add(2 * time.Hour).UTC()
	if err := e.st.Update(site, func(m *store.Meta) error { m.ExpiresAt = &soon; return nil }); err != nil {
		t.Fatal(err)
	}

	if w := davReq(t, e, "PUT", "/dav/"+edit+"/new.html", c.EditPassword, strings.NewReader("hi")); w.Code != 201 {
		t.Fatalf("PUT: %d %s", w.Code, w.Body)
	}

	got, _ := e.st.ByViewID(c.ID)
	want := time.Now().Add(7 * 24 * time.Hour)
	if got.Meta.ExpiresAt == nil {
		t.Fatal("expiry cleared")
	}
	if d := got.Meta.ExpiresAt.Sub(want); d > time.Minute || d < -time.Minute {
		t.Fatalf("expiry = %v, want ~%v", got.Meta.ExpiresAt, want)
	}
}

func TestWebDAVUsesPerSiteTierByteQuota(t *testing.T) {
	// A tight per-site tier cap must be enforced even though it is far below
	// the instance-wide global (WebDAV previously checked only the global).
	ext.Register(&fakeProvider{enabled: true, owner: "acct-1", grant: ext.CreateGrant{MaxSiteBytes: 20}})
	defer ext.Reset()
	e := newEnv(t, nil)
	c := e.createSite(t, map[string]string{"webdav": "true"}, map[string]string{"index.html": "x"})
	edit := editIDFrom(t, c.EditURL)
	base := "/dav/" + edit + "/"

	if w := davReq(t, e, "PUT", base+"big.txt", c.EditPassword, strings.NewReader(strings.Repeat("a", 30))); w.Code != http.StatusInsufficientStorage {
		t.Fatalf("PUT over tier byte quota: %d %s", w.Code, w.Body)
	}
}

func TestWebDAVUsesPerSiteTierFileQuota(t *testing.T) {
	ext.Register(&fakeProvider{enabled: true, owner: "acct-1", grant: ext.CreateGrant{MaxFiles: 1}})
	defer ext.Reset()
	e := newEnv(t, nil)
	c := e.createSite(t, map[string]string{"webdav": "true"}, map[string]string{"index.html": "x"}) // already at the 1-file cap
	edit := editIDFrom(t, c.EditURL)
	base := "/dav/" + edit + "/"

	if w := davReq(t, e, "PUT", base+"second.txt", c.EditPassword, strings.NewReader("x")); w.Code != http.StatusInsufficientStorage {
		t.Fatalf("PUT over tier file quota: %d %s", w.Code, w.Body)
	}
}

func TestWebDAVRefusesAnonymousSiteWhenAccountsEnabled(t *testing.T) {
	// WebDAV authenticates with the edit password through its own path, so it
	// must enforce the same "no account, no API" rule the JSON API does —
	// otherwise it's a back door around the gate.
	ext.Register(&fakeProvider{enabled: true})
	defer ext.Reset()
	e := newEnv(t, nil)
	c := e.createSite(t, map[string]string{"webdav": "true"}, map[string]string{"index.html": "x"})
	edit := editIDFrom(t, c.EditURL)

	if w := davReq(t, e, "GET", "/dav/"+edit+"/index.html", c.EditPassword, nil); w.Code != 403 {
		t.Fatalf("anonymous site over webdav: %d %s", w.Code, w.Body)
	}
}

func TestWebDAVAllowsOwnedSiteWhenAccountsEnabled(t *testing.T) {
	ext.Register(&fakeProvider{enabled: true, owner: "acct-1"})
	defer ext.Reset()
	e := newEnv(t, nil)
	c := e.createSite(t, map[string]string{"webdav": "true"}, map[string]string{"index.html": "x"})
	edit := editIDFrom(t, c.EditURL)

	if w := davReq(t, e, "GET", "/dav/"+edit+"/index.html", c.EditPassword, nil); w.Code != 200 {
		t.Fatalf("owned site over webdav: %d %s", w.Code, w.Body)
	}
}

func TestWebDAVCommunityBuildStaysOpen(t *testing.T) {
	e := newEnv(t, nil) // no provider registered
	c := e.createSite(t, map[string]string{"webdav": "true"}, map[string]string{"index.html": "x"})
	edit := editIDFrom(t, c.EditURL)

	if w := davReq(t, e, "GET", "/dav/"+edit+"/index.html", c.EditPassword, nil); w.Code != 200 {
		t.Fatalf("community build over webdav: %d %s", w.Code, w.Body)
	}
}

// ---- pages & assets ----

func TestPages(t *testing.T) {
	e := newEnv(t, nil)
	c := e.createSite(t, nil, nil)
	edit := editIDFrom(t, c.EditURL)

	w := e.public(t, httptest.NewRequest("GET", "/", nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), "landing") {
		t.Fatalf("landing: %d", w.Code)
	}
	if csp := w.Header().Get("Content-Security-Policy"); csp == "" {
		t.Error("missing CSP on landing")
	}
	w = e.public(t, httptest.NewRequest("GET", "/e/"+edit, nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), "edit") {
		t.Fatalf("edit page: %d", w.Code)
	}
	w = e.public(t, httptest.NewRequest("GET", "/e/aaaaaaaaaaaaaaaaaaaaaaaaaa", nil))
	if w.Code != 404 {
		t.Fatalf("edit page unknown id: %d", w.Code)
	}
	w = e.public(t, httptest.NewRequest("GET", "/_sitebin/assets/static/app.css", nil))
	if w.Code != 200 {
		t.Fatalf("assets: %d", w.Code)
	}
	w = e.public(t, httptest.NewRequest("GET", "/_sitebin/assets/../../go.mod", nil))
	if w.Code == 200 {
		t.Fatalf("asset traversal: %d", w.Code)
	}
}

func TestEmbedScriptRoute(t *testing.T) {
	e := newEnv(t, nil)
	rr := e.public(t, httptest.NewRequest("GET", "/_sitebin/embed.js", nil))
	if rr.Code != 200 {
		t.Fatalf("status = %d", rr.Code)
	}
	if got := rr.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Fatalf("ACAO = %q, want *", got)
	}
	if ct := rr.Header().Get("Content-Type"); !strings.Contains(ct, "javascript") {
		t.Fatalf("content-type = %q", ct)
	}
}

// corsCreate posts a minimal create request with an Origin header and
// returns the recorder (no status assertion — CORS headers matter here).
func (e *env) corsCreate(t *testing.T, origin string) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", `form-data; name="files"; filename="index.html"`)
	p, _ := mw.CreatePart(h)
	p.Write([]byte("<html>hi</html>"))
	mw.Close()
	req := httptest.NewRequest("POST", "/api/sites", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	return e.public(t, req)
}

func TestCreateCORS(t *testing.T) {
	cases := []struct {
		name     string
		provider bool // register an embed-capable fake provider
		origins  string
		origin   string
		wantACAO string
	}{
		{"community ignores allowlist", false, "https://sitebin.io", "https://sitebin.io", ""},
		{"ee allowed origin", true, "https://sitebin.io", "https://sitebin.io", "https://sitebin.io"},
		{"ee allowed origin case-insensitive", true, "https://sitebin.io", "https://Sitebin.IO", "https://Sitebin.IO"},
		{"ee wildcard", true, "*", "https://anything.example", "https://anything.example"},
		{"ee disallowed origin", true, "https://sitebin.io", "https://evil.example", ""},
		{"no origin header", true, "*", "", ""},
		{"ee no allowlist", true, "", "https://sitebin.io", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.provider {
				ext.Register(&fakeProvider{embedOK: true})
				defer ext.Reset()
			}
			over := map[string]string{}
			if c.origins != "" {
				over["SITEBIN_EMBED_ORIGINS"] = c.origins
			}
			e := newEnv(t, over)
			rr := e.corsCreate(t, c.origin)
			if rr.Code != 201 {
				t.Fatalf("create: %d %s", rr.Code, rr.Body)
			}
			if got := rr.Header().Get("Access-Control-Allow-Origin"); got != c.wantACAO {
				t.Fatalf("ACAO = %q, want %q", got, c.wantACAO)
			}
			if c.wantACAO != "" && !strings.Contains(rr.Header().Get("Vary"), "Origin") {
				t.Fatalf("Vary = %q, want to contain Origin", rr.Header().Get("Vary"))
			}
		})
	}
}

func TestCreatePreflight(t *testing.T) {
	// EE + allowed origin → 204 with methods.
	ext.Register(&fakeProvider{embedOK: true})
	e := newEnv(t, map[string]string{"SITEBIN_EMBED_ORIGINS": "https://sitebin.io"})
	req := httptest.NewRequest("OPTIONS", "/api/sites", nil)
	req.Header.Set("Origin", "https://sitebin.io")
	rr := e.public(t, req)
	if rr.Code != 204 {
		t.Fatalf("preflight status = %d", rr.Code)
	}
	if m := rr.Header().Get("Access-Control-Allow-Methods"); !strings.Contains(m, "POST") {
		t.Fatalf("allow-methods = %q", m)
	}
	ext.Reset()

	// Community → gate closed.
	e2 := newEnv(t, map[string]string{"SITEBIN_EMBED_ORIGINS": "https://sitebin.io"})
	req2 := httptest.NewRequest("OPTIONS", "/api/sites", nil)
	req2.Header.Set("Origin", "https://sitebin.io")
	rr2 := e2.public(t, req2)
	if rr2.Code == 204 {
		t.Fatalf("community preflight should not succeed, got %d", rr2.Code)
	}
	if got := rr2.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("community ACAO = %q, want empty", got)
	}
}

func TestExpiryCapDefaultsExpiry(t *testing.T) {
	// A tier with max_expiry_days must not just cap chosen expiry — sites
	// created without an explicit expiry default to the cap (the hosted
	// "Drop" tier's 7-day lifetime).
	ext.Register(&fakeProvider{enabled: true, grant: ext.CreateGrant{MaxExpiryDays: 7}})
	defer ext.Reset()
	e := newEnv(t, nil)
	c := e.createSite(t, nil, map[string]string{"index.html": "x"})
	site, err := e.st.ByViewID(c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if site.Meta.ExpiresAt == nil {
		t.Fatal("capped-tier site should get a default expiry")
	}
	want := time.Now().Add(7 * 24 * time.Hour)
	if d := site.Meta.ExpiresAt.Sub(want); d > time.Minute || d < -time.Minute {
		t.Fatalf("default expiry = %v, want ~%v", site.Meta.ExpiresAt, want)
	}
}

func TestExpiryCapKeepsExplicitExpiry(t *testing.T) {
	ext.Register(&fakeProvider{enabled: true, grant: ext.CreateGrant{MaxExpiryDays: 7}})
	defer ext.Reset()
	e := newEnv(t, nil)
	exp := time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339)
	c := e.createSite(t, map[string]string{"expires_at": exp}, map[string]string{"index.html": "x"})
	site, _ := e.st.ByViewID(c.ID)
	if site.Meta.ExpiresAt == nil || site.Meta.ExpiresAt.Sub(time.Now()) > 25*time.Hour {
		t.Fatalf("explicit expiry should win, got %v", site.Meta.ExpiresAt)
	}
}

func TestCappedExpiryCannotBeCleared(t *testing.T) {
	ext.Register(&fakeProvider{enabled: true, grant: ext.CreateGrant{MaxExpiryDays: 1}})
	defer ext.Reset()
	e := newEnv(t, nil)
	c := e.createSite(t, nil, map[string]string{"index.html": "x"})
	edit := editIDFrom(t, c.EditURL)

	req := authed(httptest.NewRequest("PUT", "/api/sites/"+edit, strings.NewReader(`{"expires_at":null}`)), c.EditPassword)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	if w := e.public(t, req); w.Code != 400 {
		t.Fatalf("clearing a capped expiry should be 400, got %d %s", w.Code, w.Body)
	}
	site, _ := e.st.ByViewID(c.ID)
	if site.Meta.ExpiresAt == nil {
		t.Fatal("expiry was cleared anyway")
	}
}

func TestCreateResponseFieldsOwnedCappedSite(t *testing.T) {
	// expiry_cap_days, expiry_renews and accounts_enabled drive every piece of
	// user-facing lifetime copy (claim ticket + edit page); pin them directly
	// on the create response instead of only verifying by hand.
	ext.Register(&fakeProvider{enabled: true, owner: "acct-1", grant: ext.CreateGrant{MaxExpiryDays: 7}})
	defer ext.Reset()
	e := newEnv(t, nil)
	c := e.createSite(t, nil, map[string]string{"index.html": "x"})

	if c.ExpiryCapDays != 7 {
		t.Errorf("expiry_cap_days = %d, want 7", c.ExpiryCapDays)
	}
	if !c.ExpiryRenews {
		t.Error("expiry_renews = false, want true for an owned capped site")
	}
	if !c.AccountsEnabled {
		t.Error("accounts_enabled = false, want true")
	}
}

func TestCreateResponseFieldsAnonymousSite(t *testing.T) {
	ext.Register(&fakeProvider{enabled: true, grant: ext.CreateGrant{MaxExpiryDays: 1}})
	defer ext.Reset()
	e := newEnv(t, nil)
	c := e.createSite(t, nil, map[string]string{"index.html": "x"})

	if c.ExpiryCapDays != 1 {
		t.Errorf("expiry_cap_days = %d, want 1", c.ExpiryCapDays)
	}
	if c.ExpiryRenews {
		t.Error("expiry_renews = true, want false for an anonymous site (it never renews)")
	}
	if !c.AccountsEnabled {
		t.Error("accounts_enabled = false, want true")
	}
}

func TestPayloadHidesWebDAVAndFTPForGatedAnonymousSite(t *testing.T) {
	// The edit page must not keep offering a WebDAV/FTP toggle and mount URL
	// that the protocol handlers themselves now refuse with 403 (see
	// gatedAnonymous in apigate.go). Enable both instance-wide and toggle
	// both on for the site so the only thing suppressing them is the gate.
	ext.Register(&fakeProvider{enabled: true})
	defer ext.Reset()
	e := newEnv(t, map[string]string{"SITEBIN_FTP_ENABLED": "true"})
	c := e.createSite(t, map[string]string{"webdav": "true", "ftp": "true"}, map[string]string{"index.html": "x"})

	if c.WebDAVAvailable {
		t.Error("webdav_available = true, want false for a gated anonymous site")
	}
	if c.FTPAvailable {
		t.Error("ftp_available = true, want false for a gated anonymous site")
	}
	if c.WebDAVURL != "" {
		t.Errorf("webdav_url = %q, want empty for a gated anonymous site", c.WebDAVURL)
	}
	if c.FTPURL != "" {
		t.Errorf("ftp_url = %q, want empty for a gated anonymous site", c.FTPURL)
	}
}

func TestPayloadShowsWebDAVAndFTPForOwnedSite(t *testing.T) {
	ext.Register(&fakeProvider{enabled: true, owner: "acct-1"})
	defer ext.Reset()
	e := newEnv(t, map[string]string{"SITEBIN_FTP_ENABLED": "true"})
	c := e.createSite(t, map[string]string{"webdav": "true", "ftp": "true"}, map[string]string{"index.html": "x"})

	if !c.WebDAVAvailable {
		t.Error("webdav_available = false, want true for an owned site")
	}
	if !c.FTPAvailable {
		t.Error("ftp_available = false, want true for an owned site")
	}
	if c.WebDAVURL == "" {
		t.Error("webdav_url missing for an owned site with webdav enabled")
	}
	if c.FTPURL == "" {
		t.Error("ftp_url missing for an owned site with ftp enabled")
	}
}

func TestPayloadShowsWebDAVAndFTPOnCommunityBuild(t *testing.T) {
	e := newEnv(t, map[string]string{"SITEBIN_FTP_ENABLED": "true"}) // no provider registered
	c := e.createSite(t, map[string]string{"webdav": "true", "ftp": "true"}, map[string]string{"index.html": "x"})

	if !c.WebDAVAvailable {
		t.Error("webdav_available = false, want true on the community build")
	}
	if !c.FTPAvailable {
		t.Error("ftp_available = false, want true on the community build")
	}
}

func TestAnonymousCappedExpiryCannotBeExtended(t *testing.T) {
	// A capped anonymous site must not be able to renew itself by repeatedly
	// pushing a new expiry that stays within the cap: that would turn a fixed
	// 24h drop into a permanently-alive site (one PUT per day).
	ext.Register(&fakeProvider{enabled: true, grant: ext.CreateGrant{MaxExpiryDays: 1}})
	defer ext.Reset()
	e := newEnv(t, nil)
	c := e.createSite(t, nil, map[string]string{"index.html": "x"})
	edit := editIDFrom(t, c.EditURL)

	// wind the stored expiry back, as if most of the 24h window had elapsed
	site, _ := e.st.ByViewID(c.ID)
	soon := time.Now().Add(1 * time.Hour).UTC()
	if err := e.st.Update(site, func(m *store.Meta) error { m.ExpiresAt = &soon; return nil }); err != nil {
		t.Fatal(err)
	}

	// still inside the 1-day cap, but later than the current expiry
	extend := time.Now().Add(23 * time.Hour).UTC().Format(time.RFC3339)
	req := authed(httptest.NewRequest("PUT", "/api/sites/"+edit, strings.NewReader(`{"expires_at":"`+extend+`"}`)), c.EditPassword)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	if w := e.public(t, req); w.Code != 400 {
		t.Fatalf("extending an anonymous capped expiry should be 400, got %d %s", w.Code, w.Body)
	}
	got, _ := e.st.ByViewID(c.ID)
	if got.Meta.ExpiresAt == nil || !got.Meta.ExpiresAt.Equal(soon) {
		t.Fatalf("expiry changed anyway: %v, want %v", got.Meta.ExpiresAt, soon)
	}
}

func TestAnonymousCappedExpiryCanBeShortened(t *testing.T) {
	ext.Register(&fakeProvider{enabled: true, grant: ext.CreateGrant{MaxExpiryDays: 1}})
	defer ext.Reset()
	e := newEnv(t, nil)
	c := e.createSite(t, nil, map[string]string{"index.html": "x"})
	edit := editIDFrom(t, c.EditURL)

	shorten := time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339)
	req := authed(httptest.NewRequest("PUT", "/api/sites/"+edit, strings.NewReader(`{"expires_at":"`+shorten+`"}`)), c.EditPassword)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	if w := e.public(t, req); w.Code != 200 {
		t.Fatalf("shortening an anonymous capped expiry should be allowed, got %d %s", w.Code, w.Body)
	}
	got, _ := e.st.ByViewID(c.ID)
	want, _ := time.Parse(time.RFC3339, shorten)
	if got.Meta.ExpiresAt == nil || !got.Meta.ExpiresAt.Equal(want) {
		t.Fatalf("expiry not shortened: %v, want %v", got.Meta.ExpiresAt, want)
	}
}

func TestCommunityBuildExpiryCanBeExtended(t *testing.T) {
	// Regression: the anonymous-capped extend-guard must be gated on a
	// registered provider reporting AccountsEnabled(), exactly like
	// gatedAnonymous gates WebDAV/FTP. Without that gate, a community build
	// with SITEBIN_MAX_EXPIRY_DAYS set would misfire — OwnerAccountID is
	// empty on every site there (it's only ever stamped inside createSite's
	// `if gated` branch) — and freeze a site's expiry permanently on an
	// instance that has no concept of "sign in" at all.
	e := newEnv(t, map[string]string{"SITEBIN_MAX_EXPIRY_DAYS": "30"})
	c := e.createSite(t, nil, map[string]string{"index.html": "x"}) // no explicit expiry: stays nil, no per-site quota stamped
	edit := editIDFrom(t, c.EditURL)

	first := time.Now().Add(20 * 24 * time.Hour).UTC().Format(time.RFC3339)
	req := authed(httptest.NewRequest("PUT", "/api/sites/"+edit, strings.NewReader(`{"expires_at":"`+first+`"}`)), c.EditPassword)
	req.Header.Set("Content-Type", "application/json")
	if w := e.public(t, req); w.Code != 200 {
		t.Fatalf("first PUT: %d %s", w.Code, w.Body)
	}

	// "a week later": push the date further out, still inside the 30-day
	// instance-global cap. Must succeed on a community build.
	later := time.Now().Add(27 * 24 * time.Hour).UTC().Format(time.RFC3339)
	req = authed(httptest.NewRequest("PUT", "/api/sites/"+edit, strings.NewReader(`{"expires_at":"`+later+`"}`)), c.EditPassword)
	req.Header.Set("Content-Type", "application/json")
	if w := e.public(t, req); w.Code != 200 {
		t.Fatalf("moving expiry later on a community build should succeed, got %d %s", w.Code, w.Body)
	}
	site, _ := e.st.ByViewID(c.ID)
	want, _ := time.Parse(time.RFC3339, later)
	if site.Meta.ExpiresAt == nil || !site.Meta.ExpiresAt.Equal(want) {
		t.Fatalf("expiry not moved: %v, want %v", site.Meta.ExpiresAt, want)
	}
}

func TestUncappedExpiryCanBeCleared(t *testing.T) {
	e := newEnv(t, nil) // no provider: no cap stamped, no instance global
	c := e.createSite(t, map[string]string{"expires_at": time.Now().Add(48 * time.Hour).UTC().Format(time.RFC3339)}, map[string]string{"index.html": "x"})
	edit := editIDFrom(t, c.EditURL)

	req := authed(httptest.NewRequest("PUT", "/api/sites/"+edit, strings.NewReader(`{"expires_at":null}`)), c.EditPassword)
	req.Header.Set("Content-Type", "application/json")
	if w := e.public(t, req); w.Code != 200 {
		t.Fatalf("PUT: %d %s", w.Code, w.Body)
	}
	site, _ := e.st.ByViewID(c.ID)
	if site.Meta.ExpiresAt != nil {
		t.Fatalf("expiry not cleared: %v", site.Meta.ExpiresAt)
	}
}

func TestJsonCreationWithNullExpiryAppliesToCappedSites(t *testing.T) {
	// Regression test: JSON creation with explicit expires_at:null should not
	// be blocked by the tier cap. The null is a no-op; the default-lifetime
	// block immediately below applySettings stamps the cap.
	ext.Register(&fakeProvider{enabled: true, grant: ext.CreateGrant{MaxExpiryDays: 1}})
	defer ext.Reset()
	e := newEnv(t, nil)

	req := httptest.NewRequest("POST", "/api/sites", strings.NewReader(`{"expires_at":null}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	w := e.public(t, req)
	if w.Code != 201 {
		t.Fatalf("JSON create with expires_at:null should succeed for capped site: got %d %s", w.Code, w.Body)
	}

	var payload map[string]any
	if err := json.NewDecoder(w.Body).Decode(&payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	expiresAt := payload["expires_at"]
	if expiresAt == nil {
		t.Fatal("capped site created with null expiry should get the default lifetime")
	}
	// expires_at is serialized as an RFC3339 string in the JSON response
	expiresAtStr, ok := expiresAt.(string)
	if !ok {
		t.Fatalf("expires_at should be a string, got %T", expiresAt)
	}
	parsedTime, err := time.Parse(time.RFC3339, expiresAtStr)
	if err != nil {
		t.Fatalf("parse expires_at: %v", err)
	}
	want := time.Now().Add(24 * time.Hour)
	if d := parsedTime.Sub(want); d > time.Minute || d < -time.Minute {
		t.Fatalf("expiry = %v, want ~%v", parsedTime, want)
	}
}

func TestCreationStampMarksExpiryAsTierImposed(t *testing.T) {
	ext.Register(&fakeProvider{enabled: true, owner: "acct-1", grant: ext.CreateGrant{MaxExpiryDays: 7}})
	defer ext.Reset()
	e := newEnv(t, nil)
	c := e.createSite(t, nil, map[string]string{"index.html": "x"})

	site, _ := e.st.ByViewID(c.ID)
	if site.Meta.ExpiresAt == nil {
		t.Fatal("no expiry stamped")
	}
	if !site.Meta.ExpiryFromTier {
		t.Fatal("the tier's default lifetime should be marked as tier-imposed")
	}
}

func TestExplicitExpiryIsNotTierImposed(t *testing.T) {
	ext.Register(&fakeProvider{enabled: true, owner: "acct-1", grant: ext.CreateGrant{MaxExpiryDays: 7}})
	defer ext.Reset()
	e := newEnv(t, nil)
	c := e.createSite(t, nil, map[string]string{"index.html": "x"})
	edit := editIDFrom(t, c.EditURL)

	when := time.Now().Add(48 * time.Hour).UTC().Format(time.RFC3339)
	req := authed(httptest.NewRequest("PUT", "/api/sites/"+edit, strings.NewReader(`{"expires_at":"`+when+`"}`)), c.EditPassword)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	if w := e.public(t, req); w.Code != 200 {
		t.Fatalf("PUT: %d %s", w.Code, w.Body)
	}
	site, _ := e.st.ByViewID(c.ID)
	if site.Meta.ExpiryFromTier {
		t.Fatal("a caller-chosen expiry must not be marked as tier-imposed")
	}
}

// TestSiteServiceApplyQuotaMapsTheGrant covers the join nothing else does. The
// ee tests assert on the grant handed to a fake SiteService; the store tests
// call store.ApplyQuota with a hand-built store.Quota. quotaFromGrant sits
// between them, on the shipping path, exercised by neither — so writing
// ExpiryDays: g.MaxFiles there would leave the whole suite green while every
// downgrade stamped a garbage lifetime. Each cap here is a distinct value, so
// no crossed field can pass.
func TestSiteServiceApplyQuotaMapsTheGrant(t *testing.T) {
	e := newEnv(t, nil) // community build: the new site has no caps and no expiry
	c := e.createSite(t, nil, map[string]string{"index.html": "x"})

	domains := 3
	webdav := true
	err := e.api.SiteService().ApplyQuota(c.ID, ext.CreateGrant{
		OwnerAccountID:  "acct-1",
		MaxSiteBytes:    1 << 30,
		MaxFiles:        4242,
		MaxExpiryDays:   7,
		MaxCustomDomain: &domains,
		WebDAV:          &webdav,
	})
	if err != nil {
		t.Fatalf("ApplyQuota: %v", err)
	}

	site, err := e.st.ByViewID(c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if site.Meta.QuotaBytes != 1<<30 || site.Meta.QuotaFiles != 4242 || site.Meta.QuotaExpiryDays != 7 {
		t.Errorf("caps mismatched: bytes=%d files=%d expiry_days=%d", site.Meta.QuotaBytes, site.Meta.QuotaFiles, site.Meta.QuotaExpiryDays)
	}
	if site.Meta.QuotaDomains == nil || *site.Meta.QuotaDomains != 3 {
		t.Errorf("QuotaDomains = %v, want 3", site.Meta.QuotaDomains)
	}
	if site.Meta.QuotaWebDAV == nil || !*site.Meta.QuotaWebDAV {
		t.Errorf("QuotaWebDAV = %v, want true", site.Meta.QuotaWebDAV)
	}
	// The site had no expiry, so this is a downgrade: it gets the 30-day grace,
	// NOT the tier's own 7-day cap.
	if site.Meta.ExpiresAt == nil {
		t.Fatal("downgrade did not stamp the grace expiry")
	}
	want := time.Now().Add(store.DowngradeGrace)
	if d := site.Meta.ExpiresAt.Sub(want); d > time.Minute || d < -time.Minute {
		t.Errorf("expiry = %v, want ~%v (the 30-day downgrade grace)", site.Meta.ExpiresAt, want)
	}
	if !site.Meta.ExpiryFromTier {
		t.Error("the grace expiry must be marked as tier-imposed, or a later upgrade will never lift it")
	}
}
