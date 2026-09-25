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
	if w := e.public(t, req); w.Code == 200 {
		t.Fatalf("a corrupt zip replaced the site: %s", w.Body)
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
	if w := e.public(t, req); w.Code == 200 {
		t.Fatalf("a truncated upload replaced the site: %s", w.Body)
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
