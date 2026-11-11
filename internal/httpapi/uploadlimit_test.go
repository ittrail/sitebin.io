package httpapi

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"mime/multipart"
	"net/http/httptest"
	"net/textproto"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ittrail/sitebin.io/internal/ext"
	"github.com/ittrail/sitebin.io/internal/store"
)

// A plan's own size cap decides how large an upload may be, never the
// instance's default: a 198 MB installer to a site whose plan takes a
// terabyte was cut off at the default 100 MB plus 10 MiB and answered 500
// (Atoo Mail, 2026-11-11).

// bigPlanEnv is an instance whose default cap is tiny, with an account whose
// plan stamps a cap far above it.
func bigPlanEnv(t *testing.T, planBytes int64) *env {
	t.Helper()
	ext.Register(&fakeProvider{enabled: true, owner: "acct-1", grant: ext.CreateGrant{MaxSiteBytes: planBytes}})
	t.Cleanup(ext.Reset)
	return newEnv(t, map[string]string{"SITEBIN_MAX_SITE_BYTES": "1000"})
}

func TestUploadLargerThanTheInstanceDefaultFitsThePlansCap(t *testing.T) {
	e := bigPlanEnv(t, 64<<20)
	c := e.createSite(t, nil, map[string]string{"index.html": "hi"})
	big := strings.Repeat("a", 11<<20) // past 1000 B + 10 MiB, well inside 64 MiB
	body, ct := uploadBody(t, nil, map[string]string{"stable/app.dmg": big})
	req := authed(httptest.NewRequest("POST", "/api/sites/"+editIDFrom(t, c.EditURL)+"/files", body), c.EditPassword)
	req.Header.Set("Content-Type", ct)
	if w := e.public(t, req); w.Code != 200 {
		t.Fatalf("an upload inside the plan's cap: %d %s", w.Code, w.Body)
	}
	site, _ := e.st.ByViewID(c.ID)
	if fi, err := os.Stat(filepath.Join(site.ContentDir(), "stable", "app.dmg")); err != nil || fi.Size() != int64(len(big)) {
		t.Fatalf("stored %v, %v", fi, err)
	}
}

func TestReplaceLargerThanTheInstanceDefaultFitsThePlansCap(t *testing.T) {
	e := bigPlanEnv(t, 64<<20)
	c := e.createSite(t, nil, map[string]string{"index.html": "hi"})
	body, ct := uploadBody(t, nil, map[string]string{"index.html": "new", "app.exe": strings.Repeat("a", 11<<20)})
	req := authed(httptest.NewRequest("POST", "/api/sites/"+editIDFrom(t, c.EditURL)+"/files?replace=true", body), c.EditPassword)
	req.Header.Set("Content-Type", ct)
	if w := e.public(t, req); w.Code != 200 {
		t.Fatalf("a replace inside the plan's cap: %d %s", w.Code, w.Body)
	}
}

func TestCreateLargerThanTheInstanceDefaultFitsThePlansCap(t *testing.T) {
	e := bigPlanEnv(t, 64<<20)
	c := e.createSite(t, nil, map[string]string{"index.html": "hi", "app.exe": strings.Repeat("a", 11<<20)})
	site, _ := e.st.ByViewID(c.ID)
	if _, n, err := e.st.Usage(site); err != nil || n != 2 {
		t.Fatalf("files = %d, %v", n, err)
	}
}

func TestZipLargerThanTheInstanceDefaultFitsThePlansCap(t *testing.T) {
	e := bigPlanEnv(t, 64<<20)
	c := e.createSite(t, nil, map[string]string{"index.html": "hi"})
	var zb bytes.Buffer
	zw := zip.NewWriter(&zb)
	f, _ := zw.CreateHeader(&zip.FileHeader{Name: "app.bin", Method: zip.Store})
	f.Write(randomBytes(t, 2000)) // the archive alone is past the 1000 B default
	zw.Close()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", `form-data; name="zip"; filename="site.zip"`)
	p, _ := mw.CreatePart(h)
	p.Write(zb.Bytes())
	mw.Close()
	req := authed(httptest.NewRequest("POST", "/api/sites/"+editIDFrom(t, c.EditURL)+"/files", &buf), c.EditPassword)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if w := e.public(t, req); w.Code != 200 {
		t.Fatalf("a zip inside the plan's cap: %d %s", w.Code, w.Body)
	}
}

// A body past what the site can take is the uploader's to fix, and the answer
// says by how much: 413 with the limit, never a 500.
func TestUploadBodyPastTheLimitIs413WithTheLimit(t *testing.T) {
	e := bigPlanEnv(t, 20)
	c := e.createSite(t, nil, map[string]string{"index.html": "hi"})
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	mw.WriteField("note", strings.Repeat("x", 10<<20+100)) // only the body is too big
	mw.Close()
	req := authed(httptest.NewRequest("POST", "/api/sites/"+editIDFrom(t, c.EditURL)+"/files", &buf), c.EditPassword)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	w := e.public(t, req)
	if w.Code != 413 {
		t.Fatalf("a body past the limit: %d %s", w.Code, w.Body)
	}
	if limit := store.HumanBytes(20 + uploadFormRoom); !strings.Contains(w.Body.String(), limit) {
		t.Fatalf("the answer does not name the limit %s: %s", limit, w.Body)
	}
}

// deadlineBody yields its bytes, then the error a connection's read deadline
// gives (the public server's ReadTimeout).
type deadlineBody struct{ r io.Reader }

func (d deadlineBody) Read(p []byte) (int, error) {
	n, err := d.r.Read(p)
	if err == io.EOF {
		return n, fmt.Errorf("read tcp: %w", os.ErrDeadlineExceeded)
	}
	return n, err
}

// An upload that outlasts the server's read timeout is a 408 that says so.
func TestUploadPastTheReadTimeoutIs408(t *testing.T) {
	e := newEnv(t, nil)
	c := e.createSite(t, nil, map[string]string{"index.html": "hi"})
	body, ct := uploadBody(t, nil, map[string]string{"app.dmg": strings.Repeat("a", 4096)})
	cut := body.Bytes()[:body.Len()/2]
	req := authed(httptest.NewRequest("POST", "/api/sites/"+editIDFrom(t, c.EditURL)+"/files", deadlineBody{bytes.NewReader(cut)}), c.EditPassword)
	req.Header.Set("Content-Type", ct)
	w := e.public(t, req)
	if w.Code != 408 || !strings.Contains(w.Body.String(), "10 minutes") {
		t.Fatalf("an upload past the read timeout: %d %s", w.Code, w.Body)
	}
}
