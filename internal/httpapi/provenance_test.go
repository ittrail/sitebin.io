package httpapi

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ittrail/sitebin.io/internal/ext"
	"github.com/ittrail/sitebin.io/internal/provenance"
)

// provProvider is fakeProvider plus an account log, so the core's mirror of
// site creation and deletion into it can be seen.
type provProvider struct {
	*fakeProvider
	mu      sync.Mutex
	entries map[string][]provenance.Entry
	purged  []time.Time
}

func (p *provProvider) RecordAccountProvenance(id string, e provenance.Entry) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.entries == nil {
		p.entries = map[string][]provenance.Entry{}
	}
	p.entries[id] = append(p.entries[id], e)
}

// The interface is optional and asserted, so a fake that drifts from it
// stops being one without a compile error; this keeps it honest.
var _ ext.AccountProvenance = (*provProvider)(nil)

func (p *provProvider) PurgeProvenance(before, _ time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.purged = append(p.purged, before)
}

func (p *provProvider) account(id string) []provenance.Entry {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]provenance.Entry(nil), p.entries[id]...)
}

// trail returns the site's log.
func (e *env) trail(t *testing.T, viewID string) []provenance.Entry {
	t.Helper()
	site, err := e.st.ByViewID(viewID)
	if err != nil {
		t.Fatal(err)
	}
	es, err := e.st.Provenance(site)
	if err != nil {
		t.Fatal(err)
	}
	return es
}

func lastEntry(t *testing.T, es []provenance.Entry) provenance.Entry {
	t.Helper()
	if len(es) == 0 {
		t.Fatal("no provenance recorded")
	}
	return es[len(es)-1]
}

func provBody(t *testing.T, files map[string]string, zips map[string]map[string]string) (*bytes.Buffer, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for name, content := range files {
		h := textproto.MIMEHeader{}
		h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="files"; filename="%s"`, name))
		p, _ := mw.CreatePart(h)
		p.Write([]byte(content))
	}
	for name, entries := range zips {
		var zb bytes.Buffer
		zw := zip.NewWriter(&zb)
		for n, c := range entries {
			f, _ := zw.Create(n)
			f.Write([]byte(c))
		}
		zw.Close()
		h := textproto.MIMEHeader{}
		h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="zip"; filename="%s"`, name))
		p, _ := mw.CreatePart(h)
		p.Write(zb.Bytes())
	}
	mw.Close()
	return &buf, mw.FormDataContentType()
}

// The address is the one Caddy appended — the LAST X-Forwarded-For entry —
// never one the client wrote in front of it.
func TestProvenanceCreateFromTheUI(t *testing.T) {
	e := newEnv(t, nil)
	body, ct := provBody(t, map[string]string{"bug.html": "<h1>x</h1>", "a.css": "x"}, nil)
	req := httptest.NewRequest("POST", "/api/sites", body)
	req.Header.Set("Content-Type", ct)
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.Header.Set("X-Forwarded-For", "10.9.9.9, 203.0.113.7")
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11) Firefox/140")
	w := e.public(t, req)
	if w.Code != 201 {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	var c createResp
	decodeJSON(t, w, &c)
	es := e.trail(t, c.ID)
	if len(es) != 1 {
		t.Fatalf("want one entry, got %+v", es)
	}
	got := es[0]
	if got.Action != provenance.ActionCreate || got.Surface != provenance.SurfaceUI || got.Auth != provenance.AuthNone ||
		got.Account != "" || got.IP != "203.0.113.7" || got.UA != "Mozilla/5.0 (X11) Firefox/140" || got.Files != 2 {
		t.Fatalf("create entry: %+v", got)
	}
	if !strings.Contains(got.Detail, "bug.html") {
		t.Errorf("detail should name the files: %q", got.Detail)
	}
}

// A script with an account token: surface api, credential token, the
// account — and the account's own log learns the site was created.
func TestProvenanceCreateWithATokenMirrorsToTheAccount(t *testing.T) {
	ext.Reset()
	t.Cleanup(ext.Reset)
	pp := &provProvider{fakeProvider: &fakeProvider{enabled: true, ownerFromBearer: true, bearer: map[string]string{"sbp_good": "acct-1"}}}
	ext.Register(pp)
	e := newEnv(t, nil)

	body, ct := provBody(t, map[string]string{"index.html": "x"}, nil)
	req := bearer(httptest.NewRequest("POST", "/api/sites", body), "sbp_good")
	req.Header.Set("Content-Type", ct)
	req.Header.Set("X-Forwarded-For", "198.51.100.23")
	req.Header.Set("User-Agent", "python-requests/2.32")
	w := e.public(t, req)
	if w.Code != 201 {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	var c createResp
	decodeJSON(t, w, &c)
	got := lastEntry(t, e.trail(t, c.ID))
	if got.Surface != provenance.SurfaceAPI || got.Auth != provenance.AuthToken || got.Account != "acct-1" || got.IP != "198.51.100.23" {
		t.Fatalf("create entry: %+v", got)
	}
	acc := pp.account("acct-1")
	if len(acc) != 1 || acc[0].Action != provenance.ActionSiteCreate || acc[0].Site != c.ID || acc[0].IP != "198.51.100.23" {
		t.Fatalf("account mirror: %+v", acc)
	}

	// Its deletion goes to the account log too: the site's own log goes with
	// the site.
	del := bearer(httptest.NewRequest("DELETE", "/api/sites/"+editIDFrom(t, c.EditURL), nil), "sbp_good")
	del.Header.Set("X-Forwarded-For", "198.51.100.24")
	if w := e.public(t, del); w.Code != 200 {
		t.Fatalf("delete: %d %s", w.Code, w.Body)
	}
	acc = pp.account("acct-1")
	if len(acc) != 2 || acc[1].Action != provenance.ActionSiteDelete || acc[1].Site != c.ID || acc[1].IP != "198.51.100.24" || acc[1].Auth != provenance.AuthToken {
		t.Fatalf("account mirror after delete: %+v", acc)
	}
}

func TestProvenanceUploadReplaceAndZip(t *testing.T) {
	e := newEnv(t, nil)
	c := e.createSite(t, nil, map[string]string{"index.html": "x"})
	edit := editIDFrom(t, c.EditURL)

	body, ct := provBody(t, map[string]string{"one.html": "1"}, map[string]map[string]string{"site.zip": {"a.html": "a", "b/c.html": "c", "d/": ""}})
	req := authed(httptest.NewRequest("POST", "/api/sites/"+edit+"/files", body), c.EditPassword)
	req.Header.Set("Content-Type", ct)
	req.Header.Set("X-Forwarded-For", "203.0.113.50")
	if w := e.public(t, req); w.Code != 200 {
		t.Fatalf("upload: %d %s", w.Code, w.Body)
	}
	got := lastEntry(t, e.trail(t, c.ID))
	if got.Action != provenance.ActionUpload || got.Files != 3 || got.Auth != provenance.AuthPassword || got.Surface != provenance.SurfaceAPI || got.IP != "203.0.113.50" {
		t.Fatalf("upload entry: %+v", got)
	}

	body, ct = provBody(t, map[string]string{"index.html": "new"}, nil)
	req = authed(httptest.NewRequest("POST", "/api/sites/"+edit+"/files?replace=true", body), c.EditPassword)
	req.Header.Set("Content-Type", ct)
	req.Header.Set("X-Forwarded-For", "203.0.113.51")
	if w := e.public(t, req); w.Code != 200 {
		t.Fatalf("replace: %d %s", w.Code, w.Body)
	}
	got = lastEntry(t, e.trail(t, c.ID))
	if got.Action != provenance.ActionReplace || got.Files != 1 || got.Detail != "index.html" {
		t.Fatalf("replace entry: %+v", got)
	}
}

// Only what happened is recorded: a refused request leaves no line.
func TestProvenanceSkipsFailures(t *testing.T) {
	e := newEnv(t, map[string]string{"SITEBIN_MAX_FILES": "2"})
	c := e.createSite(t, nil, map[string]string{"index.html": "x"})
	edit := editIDFrom(t, c.EditURL)
	before := len(e.trail(t, c.ID))

	body, ct := provBody(t, map[string]string{"a": "1"}, nil)
	req := authed(httptest.NewRequest("POST", "/api/sites/"+edit+"/files", body), "wrong-password")
	req.Header.Set("Content-Type", ct)
	if w := e.public(t, req); w.Code != 401 {
		t.Fatalf("wrong password: %d", w.Code)
	}
	body, ct = provBody(t, map[string]string{"a": "1", "b": "2", "c": "3"}, nil)
	req = authed(httptest.NewRequest("POST", "/api/sites/"+edit+"/files?replace=true", body), c.EditPassword)
	req.Header.Set("Content-Type", ct)
	if w := e.public(t, req); w.Code != 413 {
		t.Fatalf("over the cap: %d %s", w.Code, w.Body)
	}
	if after := len(e.trail(t, c.ID)); after != before {
		t.Fatalf("a refused write was recorded: %+v", e.trail(t, c.ID))
	}
}

func TestProvenanceSettingsDeleteFileAndDomains(t *testing.T) {
	ext.Reset()
	t.Cleanup(ext.Reset)
	ext.Register(&fakeProvider{domainsOK: true})
	e := newEnv(t, nil)
	c := e.createSite(t, nil, map[string]string{"index.html": "x", "old.txt": "o"})
	edit := editIDFrom(t, c.EditURL)

	req := authed(httptest.NewRequest("PUT", "/api/sites/"+edit, strings.NewReader(`{"view_password":"s3cret-view","name":"Mine"}`)), c.EditPassword)
	if w := e.public(t, req); w.Code != 200 {
		t.Fatalf("settings: %d %s", w.Code, w.Body)
	}
	got := lastEntry(t, e.trail(t, c.ID))
	if got.Action != provenance.ActionSettings || !strings.Contains(got.Detail, "view_password") || !strings.Contains(got.Detail, "name") {
		t.Fatalf("settings entry: %+v", got)
	}
	for _, en := range e.trail(t, c.ID) {
		if strings.Contains(en.Detail, "s3cret-view") || strings.Contains(en.Detail, "Mine") {
			t.Fatalf("a setting's value was recorded: %+v", en)
		}
	}

	if w := e.public(t, authed(httptest.NewRequest("DELETE", "/api/sites/"+edit+"/files/old.txt", nil), c.EditPassword)); w.Code != 200 {
		t.Fatalf("delete file: %d %s", w.Code, w.Body)
	}
	if got := lastEntry(t, e.trail(t, c.ID)); got.Action != provenance.ActionDeleteFile || got.Detail != "old.txt" {
		t.Fatalf("delete-file entry: %+v", got)
	}

	req = authed(httptest.NewRequest("POST", "/api/sites/"+edit+"/domains", strings.NewReader(`{"domain":"Docs.Example.com"}`)), c.EditPassword)
	if w := e.public(t, req); w.Code != 200 {
		t.Fatalf("add domain: %d %s", w.Code, w.Body)
	}
	if got := lastEntry(t, e.trail(t, c.ID)); got.Action != provenance.ActionDomainAdd || got.Detail != "docs.example.com" {
		t.Fatalf("domain-add entry: %+v", got)
	}
	if w := e.public(t, authed(httptest.NewRequest("DELETE", "/api/sites/"+edit+"/domains/docs.example.com", nil), c.EditPassword)); w.Code != 200 {
		t.Fatalf("remove domain: %d %s", w.Code, w.Body)
	}
	if got := lastEntry(t, e.trail(t, c.ID)); got.Action != provenance.ActionDomainRemove || got.Detail != "docs.example.com" {
		t.Fatalf("domain-remove entry: %+v", got)
	}
}

func TestProvenanceForms(t *testing.T) {
	e, _ := formsEnv(t, nil)
	id, pw, viewID := newFormSite(t, e)
	w, out := e.formsCallFrom(t, "203.0.113.77", "POST", id, pw, "", map[string]any{"name": "Contact", "recipient": "office@example.com"})
	if w.Code != 201 {
		t.Fatalf("add form: %d %s", w.Code, w.Body)
	}
	got := lastEntry(t, e.trail(t, viewID))
	if got.Action != provenance.ActionFormAdd || got.IP != "203.0.113.77" || got.Detail != "Contact" {
		t.Fatalf("form-add entry: %+v", got)
	}
	for _, en := range e.trail(t, viewID) {
		if strings.Contains(en.Detail, "office@example.com") {
			t.Fatalf("the recipient was recorded: %+v", en)
		}
	}
	key := out.Forms[0].Key
	if w, _ := e.formsCall(t, "DELETE", id, pw, "/"+key, nil); w.Code != 204 {
		t.Fatalf("remove form: %d", w.Code)
	}
	if got := lastEntry(t, e.trail(t, viewID)); got.Action != provenance.ActionFormRemove || got.Detail != key {
		t.Fatalf("form-remove entry: %+v", got)
	}
}

// An upload token's writes are the account's that opened it through MCP.
func TestProvenanceUploadTokenCarriesItsAccount(t *testing.T) {
	e := newEnv(t, nil)
	c := e.createSite(t, nil, map[string]string{"index.html": "x"})
	edit := editIDFrom(t, c.EditURL)
	tok, _, err := e.api.uploads.issueFor(c.ID, edit, "acct-9")
	if err != nil {
		t.Fatal(err)
	}
	body, ct := provBody(t, map[string]string{"big.bin": "payload"}, nil)
	req := bearer(httptest.NewRequest("POST", "/api/sites/"+edit+"/files", body), tok)
	req.Header.Set("Content-Type", ct)
	req.Header.Set("X-Forwarded-For", "192.0.2.10")
	req.Header.Set("User-Agent", "curl/8.9")
	if w := e.public(t, req); w.Code != 200 {
		t.Fatalf("upload: %d %s", w.Code, w.Body)
	}
	got := lastEntry(t, e.trail(t, c.ID))
	if got.Surface != provenance.SurfaceUploadToken || got.Auth != provenance.AuthUploadToken || got.Account != "acct-9" ||
		got.IP != "192.0.2.10" || got.UA != "curl/8.9" || got.Files != 1 {
		t.Fatalf("upload-token entry: %+v", got)
	}

	// WebDAV through the same token.
	req = bearer(httptest.NewRequest("PUT", "/dav/"+edit+"/more.bin", strings.NewReader("m")), tok)
	req.Header.Set("X-Forwarded-For", "192.0.2.10")
	if w := e.public(t, req); w.Code != 201 {
		t.Fatalf("dav put: %d %s", w.Code, w.Body)
	}
	got = lastEntry(t, e.trail(t, c.ID))
	if got.Surface != provenance.SurfaceWebDAV || got.Auth != provenance.AuthUploadToken || got.Account != "acct-9" || got.Detail != "more.bin" {
		t.Fatalf("webdav token entry: %+v", got)
	}
}

// A WebDAV sync is a burst of writes; the log keeps it as one line.
func TestProvenanceWebDAVBurstIsOneLine(t *testing.T) {
	e := newEnv(t, nil)
	c := e.createSite(t, map[string]string{"webdav": "true"}, map[string]string{"index.html": "x"})
	edit := editIDFrom(t, c.EditURL)
	before := len(e.trail(t, c.ID))
	for i := 0; i < 4; i++ {
		req := httptest.NewRequest("PUT", fmt.Sprintf("/dav/%s/f%d.txt", edit, i), strings.NewReader("x"))
		req.SetBasicAuth("u", c.EditPassword)
		req.Header.Set("X-Forwarded-For", "203.0.113.90")
		req.Header.Set("User-Agent", "rclone/1.68")
		if w := e.public(t, req); w.Code != 201 {
			t.Fatalf("PUT: %d %s", w.Code, w.Body)
		}
	}
	es := e.trail(t, c.ID)
	if len(es) != before+1 {
		t.Fatalf("want the burst as one line, got %+v", es)
	}
	got := lastEntry(t, es)
	if got.Surface != provenance.SurfaceWebDAV || got.Auth != provenance.AuthPassword || got.Action != provenance.ActionUpload ||
		got.Times() != 4 || got.Files != 4 || got.UA != "rclone/1.68" {
		t.Fatalf("webdav entry: %+v", got)
	}

	req := httptest.NewRequest("MOVE", "/dav/"+edit+"/f0.txt", nil)
	req.SetBasicAuth("u", c.EditPassword)
	req.Header.Set("Destination", "http://sitebin.example/dav/"+edit+"/moved.txt")
	if w := e.public(t, req); w.Code >= 300 {
		t.Fatalf("MOVE: %d %s", w.Code, w.Body)
	}
	if got := lastEntry(t, e.trail(t, c.ID)); got.Action != provenance.ActionMove || got.Detail != "f0.txt → moved.txt" {
		t.Fatalf("move entry: %+v", got)
	}
	// A read is not a write.
	n := len(e.trail(t, c.ID))
	req = httptest.NewRequest("GET", "/dav/"+edit+"/moved.txt", nil)
	req.SetBasicAuth("u", c.EditPassword)
	e.public(t, req)
	if len(e.trail(t, c.ID)) != n {
		t.Fatal("a WebDAV read was recorded")
	}
}

func TestProvenanceFTP(t *testing.T) {
	e := newEnv(t, nil)
	c := e.createSite(t, nil, map[string]string{"index.html": "x"})
	e.api.FTPWrote(editIDFrom(t, c.EditURL), "198.51.100.200", provenance.ActionUpload, "up.txt")
	got := lastEntry(t, e.trail(t, c.ID))
	if got.Surface != provenance.SurfaceFTP || got.Auth != provenance.AuthPassword || got.IP != "198.51.100.200" || got.Files != 1 || got.Detail != "up.txt" {
		t.Fatalf("ftp entry: %+v", got)
	}
}

// MCP, community build: no account, the edit password opens the site.
func TestProvenanceMCPWrites(t *testing.T) {
	e := newEnv(t, nil)
	h := http.Header{}
	h.Set("User-Agent", "claude-code/2.1")
	h.Set("X-Forwarded-For", "203.0.113.200")
	cs := mcpClient(t, e, h)
	editID, pw := mcpCreate(t, cs, "<h1>agent</h1>")
	site, err := e.st.ByEditID(editID)
	if err != nil {
		t.Fatal(err)
	}
	got := lastEntry(t, e.trail(t, site.ViewID))
	if got.Action != provenance.ActionCreate || got.Surface != provenance.SurfaceMCP || got.Auth != provenance.AuthNone ||
		got.IP != "203.0.113.200" || got.UA != "claude-code/2.1" || got.Files != 1 {
		t.Fatalf("mcp create entry: %+v", got)
	}

	for _, call := range []struct {
		tool   string
		args   map[string]any
		action string
	}{
		{"write_files", map[string]any{"files": []any{map[string]any{"path": "a.js", "text": "1"}, map[string]any{"path": "b.js", "text": "2"}}}, provenance.ActionUpload},
		{"delete_file", map[string]any{"path": "a.js"}, provenance.ActionDeleteFile},
		{"update_site", map[string]any{"settings": map[string]any{"spa_fallback": true}}, provenance.ActionSettings},
		{"open_upload", map[string]any{}, provenance.ActionOpenUpload},
	} {
		args := map[string]any{"edit_id": editID, "edit_password": pw}
		for k, v := range call.args {
			args[k] = v
		}
		if res := mcpCall(t, cs, call.tool, args); res.IsError {
			t.Fatalf("%s: %s", call.tool, mcpText(res))
		}
		got := lastEntry(t, e.trail(t, site.ViewID))
		if got.Action != call.action || got.Surface != provenance.SurfaceMCP || got.Auth != provenance.AuthPassword || got.IP != "203.0.113.200" {
			t.Fatalf("%s entry: %+v", call.tool, got)
		}
	}
	es := e.trail(t, site.ViewID)
	if es[1].Files != 2 || !strings.Contains(es[1].Detail, "a.js") {
		t.Fatalf("write_files entry: %+v", es[1])
	}
}

// MCP with the owning account's token: the account and "token" are recorded,
// and the upload token open_upload hands out carries the account.
func TestProvenanceMCPWithAnAccountToken(t *testing.T) {
	ext.Reset()
	t.Cleanup(ext.Reset)
	pp := &provProvider{fakeProvider: &fakeProvider{enabled: true, ownerFromBearer: true, bearer: map[string]string{"sbp_good": "acct-1"}, owned: map[string][]string{}}}
	ext.Register(pp)
	e := newEnv(t, nil)
	h := http.Header{}
	h.Set("Authorization", "Bearer sbp_good")
	cs := mcpClient(t, e, h)
	editID, _ := mcpCreate(t, cs, "<h1>mine</h1>")
	site, _ := e.st.ByEditID(editID)
	got := lastEntry(t, e.trail(t, site.ViewID))
	if got.Auth != provenance.AuthToken || got.Account != "acct-1" {
		t.Fatalf("create entry: %+v", got)
	}
	if acc := pp.account("acct-1"); len(acc) != 1 || acc[0].Action != provenance.ActionSiteCreate || acc[0].Surface != provenance.SurfaceMCP {
		t.Fatalf("account mirror: %+v", acc)
	}

	res := mcpCall(t, cs, "open_upload", map[string]any{"edit_id": editID})
	if res.IsError {
		t.Fatalf("open_upload: %s", mcpText(res))
	}
	tok, _ := res.StructuredContent.(map[string]any)["token"].(string)
	if got := e.api.uploads.issuer(tok); got != "acct-1" {
		t.Fatalf("upload token issuer = %q", got)
	}

	if res := mcpCall(t, cs, "delete_site", map[string]any{"edit_id": editID}); res.IsError {
		t.Fatalf("delete_site: %s", mcpText(res))
	}
	acc := pp.account("acct-1")
	if last := acc[len(acc)-1]; last.Action != provenance.ActionSiteDelete || last.Site != site.ViewID || last.Surface != provenance.SurfaceMCP {
		t.Fatalf("delete mirror: %+v", acc)
	}
}

// The register's address search: every site whose log names an address in
// the range, with the matching entries.
func TestSitesSeenFrom(t *testing.T) {
	e := newEnv(t, nil)
	mk := func(ip string) string {
		body, ct := provBody(t, map[string]string{"index.html": "x"}, nil)
		req := httptest.NewRequest("POST", "/api/sites", body)
		req.Header.Set("Content-Type", ct)
		req.Header.Set("X-Forwarded-For", ip)
		w := e.public(t, req)
		var c createResp
		decodeJSON(t, w, &c)
		return c.ID
	}
	a, b, _ := mk("203.0.113.7"), mk("203.0.113.99"), mk("198.51.100.1")
	sp := e.api.SiteService().(ext.SiteProvenance)
	m, _ := provenance.ParseMatch("203.0.113.0/24")
	got, err := sp.SitesSeenFrom(m)
	if err != nil || len(got) != 2 || len(got[a]) != 1 || len(got[b]) != 1 {
		t.Fatalf("seen from: %v %v", got, err)
	}
	if es, err := sp.SiteProvenance(a); err != nil || len(es) != 1 {
		t.Fatalf("site provenance: %+v %v", es, err)
	}
	if _, err := sp.SiteProvenance("aaaaaaaaaaaaaaaaaaaaaaaaaa"); err == nil {
		t.Fatal("an unknown site has a log")
	}
	sp.RecordSiteProvenance(a, provenance.Entry{Action: provenance.ActionRename, Surface: provenance.SurfaceDashboard, IP: "203.0.113.8"})
	if es, _ := sp.SiteProvenance(a); len(es) != 2 || es[1].Action != provenance.ActionRename {
		t.Fatalf("recorded through the seam: %+v", es)
	}
}

func decodeJSON(t *testing.T, w *httptest.ResponseRecorder, v any) {
	t.Helper()
	if err := json.Unmarshal(w.Body.Bytes(), v); err != nil {
		t.Fatalf("decode %s: %v", w.Body, err)
	}
}
