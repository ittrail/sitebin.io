package httpapi

import (
	"bytes"
	"encoding/base64"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ittrail/sitebin.io/internal/store"
)

// sitebin.io deploys itself as a Sitebin site, and its repository carries
// public/.well-known/security.txt: a dot-directory must pass every write path
// and be listed like any file — while the .sitebin-* markers stay refused.

const securityTxtBody = "Contact: mailto:abuse@example.com\n"

func hasFile(t *testing.T, e *env, viewID, path string) bool {
	t.Helper()
	site, err := e.st.ByViewID(viewID)
	if err != nil {
		t.Fatal(err)
	}
	b, err := e.st.ReadContentFile(site, path)
	return err == nil && string(b) == securityTxtBody
}

func TestWellKnownThroughCreateAndUpload(t *testing.T) {
	e := newEnv(t, nil)
	c := e.createSite(t, nil, map[string]string{"index.html": "x", ".well-known/security.txt": securityTxtBody})
	if !hasFile(t, e, c.ID, ".well-known/security.txt") {
		t.Fatal("create dropped .well-known/security.txt")
	}
	site, _ := e.st.ByViewID(c.ID)
	files, _ := e.st.ListFiles(site)
	found := false
	for _, f := range files {
		found = found || f.Path == ".well-known/security.txt"
	}
	if !found {
		t.Fatalf("not listed: %+v", files)
	}

	// The website's deploy: a zip, replacing everything.
	edit := editIDFrom(t, c.EditURL)
	body, ct := provBody(t, nil, map[string]map[string]string{"public.zip": {"index.html": "new", ".well-known/security.txt": securityTxtBody, ".well-known/": ""}})
	req := authed(httptest.NewRequest("POST", "/api/sites/"+edit+"/files?replace=true", body), c.EditPassword)
	req.Header.Set("Content-Type", ct)
	if w := e.public(t, req); w.Code != 200 {
		t.Fatalf("replace with a zip: %d %s", w.Code, w.Body)
	}
	if !hasFile(t, e, c.ID, ".well-known/security.txt") {
		t.Fatal("a zip replace dropped .well-known/security.txt")
	}

	// An upload token's plain upload.
	tok, _, _ := e.api.uploads.issue(c.ID, edit)
	body, ct = provBody(t, map[string]string{".well-known/other.txt": "x"}, nil)
	req = bearer(httptest.NewRequest("POST", "/api/sites/"+edit+"/files", body), tok)
	req.Header.Set("Content-Type", ct)
	if w := e.public(t, req); w.Code != 200 {
		t.Fatalf("upload token: %d %s", w.Code, w.Body)
	}
}

func TestWellKnownThroughWebDAVAndMCP(t *testing.T) {
	e := newEnv(t, nil)
	c := e.createSite(t, map[string]string{"webdav": "true"}, map[string]string{"index.html": "x"})
	edit := editIDFrom(t, c.EditURL)
	if w := davReq(t, e, "MKCOL", "/dav/"+edit+"/.well-known", c.EditPassword, nil); w.Code != 201 {
		t.Fatalf("MKCOL .well-known: %d %s", w.Code, w.Body)
	}
	if w := davReq(t, e, "PUT", "/dav/"+edit+"/.well-known/security.txt", c.EditPassword, strings.NewReader(securityTxtBody)); w.Code != 201 {
		t.Fatalf("PUT .well-known/security.txt: %d %s", w.Code, w.Body)
	}
	if !hasFile(t, e, c.ID, ".well-known/security.txt") {
		t.Fatal("WebDAV dropped .well-known/security.txt")
	}

	cs := mcpClient(t, e, nil)
	editID, pw := mcpCreate(t, cs, "<h1>x</h1>")
	res := mcpCall(t, cs, "write_files", map[string]any{"edit_id": editID, "edit_password": pw, "files": []any{
		map[string]any{"path": ".well-known/security.txt", "base64": base64.StdEncoding.EncodeToString([]byte(securityTxtBody))},
	}})
	if res.IsError {
		t.Fatalf("write_files: %s", mcpText(res))
	}
	site, _ := e.st.ByEditID(editID)
	if !hasFile(t, e, site.ViewID, ".well-known/security.txt") {
		t.Fatal("MCP dropped .well-known/security.txt")
	}
}

// The marker protection is untouched: .sitebin-* at the top stays refused on
// every path, whatever else dots may do.
func TestMarkersStayRefused(t *testing.T) {
	for _, p := range []string{".sitebin-trusted", ".sitebin-spa", ".Sitebin-Trusted", "_sitebin/x", "_raw/x"} {
		if _, err := store.CleanRelPath(p); err == nil {
			t.Errorf("%q accepted", p)
		}
	}
	for _, p := range []string{".well-known/security.txt", ".well-known/acme-challenge/x", ".htaccess"} {
		if _, err := store.CleanRelPath(p); err != nil {
			t.Errorf("%q refused: %v", p, err)
		}
	}
	e := newEnv(t, nil)
	c := e.createSite(t, nil, map[string]string{"index.html": "x"})
	edit := editIDFrom(t, c.EditURL)
	var buf bytes.Buffer
	buf.WriteString("x")
	if w := davReq(t, e, "PUT", "/dav/"+edit+"/.sitebin-trusted", c.EditPassword, &buf); w.Code < 400 {
		t.Fatalf("WebDAV wrote the trust marker: %d", w.Code)
	}
}
