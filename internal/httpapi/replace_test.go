package httpapi

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"strings"
	"testing"

	"github.com/ittrail/sitebin.io/internal/ext"
)

func indexHTML(t *testing.T, e *env, viewID string) string {
	t.Helper()
	site, err := e.st.ByViewID(viewID)
	if err != nil {
		t.Fatal(err)
	}
	b, err := e.st.ReadContentFile(site, "index.html")
	if err != nil {
		return "<missing: " + err.Error() + ">"
	}
	return string(b)
}

func TestReplaceUploadOverTheQuotaLeavesTheSiteIntact(t *testing.T) {
	ext.Register(&fakeProvider{enabled: true, owner: "acct-1", grant: ext.CreateGrant{MaxSiteBytes: 20}})
	defer ext.Reset()
	e := newEnv(t, nil)
	c := e.createSite(t, nil, map[string]string{"index.html": "old"})
	body, ct := uploadBody(t, map[string]string{"big.txt": strings.Repeat("a", 30)}, nil)
	req := authed(httptest.NewRequest("POST", "/api/sites/"+editIDFrom(t, c.EditURL)+"/files?replace=true", body), c.EditPassword)
	req.Header.Set("Content-Type", ct)
	if w := e.public(t, req); w.Code == 200 {
		t.Fatalf("an over-quota replace succeeded: %s", w.Body)
	}
	if got := indexHTML(t, e, c.ID); got != "old" {
		t.Fatalf("the failed replace touched the site: index.html = %q", got)
	}
}

func TestReplaceUploadWithACorruptZipLeavesTheSiteIntact(t *testing.T) {
	e := newEnv(t, nil)
	c := e.createSite(t, nil, map[string]string{"index.html": "old"})
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", `form-data; name="zip"; filename="site.zip"`)
	p, _ := mw.CreatePart(h)
	p.Write([]byte("not a zip archive"))
	mw.Close()
	req := authed(httptest.NewRequest("POST", "/api/sites/"+editIDFrom(t, c.EditURL)+"/files?replace=true", &buf), c.EditPassword)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if w := e.public(t, req); w.Code != 400 {
		t.Fatalf("a corrupt zip is the uploader's mistake, want 400: %d %s", w.Code, w.Body)
	}
	if got := indexHTML(t, e, c.ID); got != "old" {
		t.Fatalf("the failed replace touched the site: index.html = %q", got)
	}
}

func TestReplaceUploadCutOffMidwayLeavesTheSiteIntact(t *testing.T) {
	e := newEnv(t, nil)
	c := e.createSite(t, nil, map[string]string{"index.html": "old"})
	body, ct := uploadBody(t, nil, map[string]string{"index.html": "new", "b.txt": "b"})
	cut := body.Bytes()[:body.Len()-10] // the connection drops before the closing boundary
	req := authed(httptest.NewRequest("POST", "/api/sites/"+editIDFrom(t, c.EditURL)+"/files?replace=true", bytes.NewReader(cut)), c.EditPassword)
	req.Header.Set("Content-Type", ct)
	if w := e.public(t, req); w.Code != 400 {
		t.Fatalf("a truncated upload is the uploader's problem, want 400: %d %s", w.Code, w.Body)
	}
	if got := indexHTML(t, e, c.ID); got != "old" {
		t.Fatalf("the failed replace touched the site: index.html = %q", got)
	}
}

func TestMCPWriteFilesReplaceOverTheQuotaLeavesTheSiteIntact(t *testing.T) {
	e := newEnv(t, nil)
	ext.Register(&fakeProvider{
		enabled: true,
		owner:   "acct-1",
		bearer:  map[string]string{"sbp_tok": "acct-1"},
		grant:   ext.CreateGrant{MaxSiteBytes: 20},
	})
	defer ext.Reset()
	cs := mcpClient(t, e, http.Header{"Authorization": {"Bearer sbp_tok"}})
	editID, _ := mcpCreate(t, cs, "old")
	res := mcpCall(t, cs, "write_files", map[string]any{
		"edit_id": editID, "replace": true,
		"files": []any{map[string]any{"path": "big.txt", "text": strings.Repeat("a", 30)}},
	})
	if !res.IsError {
		t.Fatal("an over-quota replace succeeded")
	}
	site, _ := e.st.ByEditID(editID)
	if b, err := e.st.ReadContentFile(site, "index.html"); err != nil || string(b) != "old" {
		t.Fatalf("the failed replace touched the site: %q, %v", b, err)
	}
}

const replaceBusyMsg = "another replace or atomic upload of this site is still running — wait for it to finish, then try again"

func TestReplaceUploadWhileAnotherIsRunningIsAConflict(t *testing.T) {
	e := newEnv(t, nil)
	c := e.createSite(t, nil, map[string]string{"index.html": "old"})
	site, err := e.st.ByViewID(c.ID)
	if err != nil {
		t.Fatal(err)
	}
	running, err := e.st.BeginReplace(site)
	if err != nil {
		t.Fatal(err)
	}
	defer running.Abort()

	post := func() *httptest.ResponseRecorder {
		body, ct := uploadBody(t, nil, map[string]string{"index.html": "new"})
		req := authed(httptest.NewRequest("POST", "/api/sites/"+editIDFrom(t, c.EditURL)+"/files?replace=true", body), c.EditPassword)
		req.Header.Set("Content-Type", ct)
		return e.public(t, req)
	}
	if w := post(); w.Code != 409 || !strings.Contains(w.Body.String(), replaceBusyMsg) {
		t.Fatalf("a replace while another is running: %d %s", w.Code, w.Body)
	}
	if got := indexHTML(t, e, c.ID); got != "old" {
		t.Fatalf("the refused replace touched the site: index.html = %q", got)
	}
	running.Abort()
	if w := post(); w.Code != 200 {
		t.Fatalf("a replace once the other finished: %d %s", w.Code, w.Body)
	}
	if got := indexHTML(t, e, c.ID); got != "new" {
		t.Fatalf("index.html = %q", got)
	}
}

func TestMCPWriteFilesReplaceWhileAnotherIsRunning(t *testing.T) {
	e := newEnv(t, nil)
	cs := mcpClient(t, e, nil)
	editID, pw := mcpCreate(t, cs, "old")
	site, err := e.st.ByEditID(editID)
	if err != nil {
		t.Fatal(err)
	}
	running, err := e.st.BeginReplace(site)
	if err != nil {
		t.Fatal(err)
	}
	defer running.Abort()
	res := mcpCall(t, cs, "write_files", map[string]any{
		"edit_id": editID, "edit_password": pw, "replace": true,
		"files": []any{map[string]any{"path": "index.html", "text": "new"}},
	})
	if !res.IsError || !strings.Contains(mcpText(res), replaceBusyMsg) {
		t.Fatalf("write_files replace while another is running: error=%v %s", res.IsError, mcpText(res))
	}
	if got := indexHTML(t, e, site.ViewID); got != "old" {
		t.Fatalf("the refused replace touched the site: index.html = %q", got)
	}
}

// Outside a replace too: a corrupt zip is a 400, not an internal error.
func TestUploadOfACorruptZipIsABadRequest(t *testing.T) {
	e := newEnv(t, nil)
	c := e.createSite(t, nil, map[string]string{"index.html": "old"})
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", `form-data; name="zip"; filename="site.zip"`)
	p, _ := mw.CreatePart(h)
	p.Write([]byte("not a zip archive"))
	mw.Close()
	req := authed(httptest.NewRequest("POST", "/api/sites/"+editIDFrom(t, c.EditURL)+"/files", &buf), c.EditPassword)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	w := e.public(t, req)
	if w.Code != 400 || !strings.Contains(w.Body.String(), "zip") {
		t.Fatalf("corrupt zip upload: %d %s", w.Code, w.Body)
	}
}

// A file part cut off mid-content is a 400, not an internal error.
func TestUploadCutOffInsideAFileIsABadRequest(t *testing.T) {
	e := newEnv(t, nil)
	c := e.createSite(t, nil, map[string]string{"index.html": "old"})
	body, ct := uploadBody(t, nil, map[string]string{"big.txt": strings.Repeat("x", 4096)})
	cut := body.Bytes()[:body.Len()/2] // the connection drops in the middle of the file
	req := authed(httptest.NewRequest("POST", "/api/sites/"+editIDFrom(t, c.EditURL)+"/files", bytes.NewReader(cut)), c.EditPassword)
	req.Header.Set("Content-Type", ct)
	if w := e.public(t, req); w.Code != 400 {
		t.Fatalf("cut-off upload: %d %s", w.Code, w.Body)
	}
}

// A body cut off inside a plain form field is the uploader's problem too.
func TestUploadCutOffInsideAFormFieldIsABadRequest(t *testing.T) {
	e := newEnv(t, nil)
	c := e.createSite(t, nil, map[string]string{"index.html": "old"})
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	mw.WriteField("note", strings.Repeat("n", 4096))
	mw.Close()
	cut := buf.Bytes()[:buf.Len()/2]
	req := authed(httptest.NewRequest("POST", "/api/sites/"+editIDFrom(t, c.EditURL)+"/files", bytes.NewReader(cut)), c.EditPassword)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if w := e.public(t, req); w.Code != 400 {
		t.Fatalf("cut-off form field: %d %s", w.Code, w.Body)
	}
}

// ?atomic=true: every file of the request lands, or none does.
func TestAtomicUploadLandsAllOrNothing(t *testing.T) {
	ext.Register(&fakeProvider{enabled: true, owner: "acct-1", grant: ext.CreateGrant{MaxSiteBytes: 100}})
	defer ext.Reset()
	e := newEnv(t, nil)
	c := e.createSite(t, nil, map[string]string{"index.html": "old", "stable/latest.yml": "version: 0.9"})
	edit := editIDFrom(t, c.EditURL)
	post := func(body *bytes.Buffer, ct string) *httptest.ResponseRecorder {
		req := authed(httptest.NewRequest("POST", "/api/sites/"+edit+"/files?atomic=true", body), c.EditPassword)
		req.Header.Set("Content-Type", ct)
		return e.public(t, req)
	}
	read := func(name string) string {
		site, _ := e.st.ByViewID(c.ID)
		b, err := e.st.ReadContentFile(site, name)
		if err != nil {
			return "<missing>"
		}
		return string(b)
	}

	// The installer fits, the second file does not: neither lands, and the
	// feed is untouched.
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for _, f := range []struct{ name, body string }{
		{"stable/app-1.0.exe", "MZ"},
		{"stable/huge.bin", strings.Repeat("a", 120)},
		{"stable/latest.yml", "version: 1.0"},
	} {
		h := textproto.MIMEHeader{}
		h.Set("Content-Disposition", `form-data; name="files"; filename="`+f.name+`"`)
		p, _ := mw.CreatePart(h)
		p.Write([]byte(f.body))
	}
	mw.Close()
	if w := post(&buf, mw.FormDataContentType()); w.Code != 413 {
		t.Fatalf("an atomic upload past the cap: %d %s", w.Code, w.Body)
	}
	if got := read("stable/app-1.0.exe"); got != "<missing>" {
		t.Fatalf("a failed atomic upload left a file: %q", got)
	}
	if got := read("stable/latest.yml"); got != "version: 0.9" {
		t.Fatalf("a failed atomic upload changed the feed: %q", got)
	}

	// A body cut off in the middle: nothing either.
	body, ct := uploadBody(t, nil, map[string]string{"stable/app-1.0.exe": "MZ", "stable/latest.yml": "version: 1.0"})
	cut := bytes.NewBuffer(body.Bytes()[:body.Len()-10])
	if w := post(cut, ct); w.Code != 400 {
		t.Fatalf("a cut-off atomic upload: %d %s", w.Code, w.Body)
	}
	if got := read("stable/latest.yml"); got != "version: 0.9" {
		t.Fatalf("a cut-off atomic upload changed the feed: %q", got)
	}

	// All of it: everything lands, and the rest of the site stays.
	body, ct = uploadBody(t, nil, map[string]string{"stable/app-1.0.exe": "MZ", "stable/latest.yml": "version: 1.0"})
	if w := post(body, ct); w.Code != 200 {
		t.Fatalf("an atomic upload: %d %s", w.Code, w.Body)
	}
	if read("stable/app-1.0.exe") != "MZ" || read("stable/latest.yml") != "version: 1.0" || read("index.html") != "old" {
		t.Fatalf("after the atomic upload: exe %q, feed %q, index %q", read("stable/app-1.0.exe"), read("stable/latest.yml"), read("index.html"))
	}
}

// An upload token opens the atomic upload too: it is the same route.
func TestAtomicUploadWithAnUploadToken(t *testing.T) {
	e := newEnv(t, nil)
	c := e.createSite(t, nil, map[string]string{"index.html": "x"})
	edit := editIDFrom(t, c.EditURL)
	tok := uploadTokenFor(t, e, c)
	body, ct := uploadBody(t, nil, map[string]string{"stable/app.exe": "MZ"})
	req := bearer(httptest.NewRequest("POST", "/api/sites/"+edit+"/files?atomic=true", body), tok)
	req.Header.Set("Content-Type", ct)
	if w := e.public(t, req); w.Code != 200 {
		t.Fatalf("an atomic upload with an upload token: %d %s", w.Code, w.Body)
	}
}
