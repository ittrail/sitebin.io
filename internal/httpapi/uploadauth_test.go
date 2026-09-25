package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ittrail/sitebin.io/internal/ext"
)

// uploadTokenFor issues an upload token for a site made by env.createSite,
// the way open_upload does.
func uploadTokenFor(t *testing.T, e *env, c createResp) string {
	t.Helper()
	secret, _, err := e.api.uploads.issue(c.ID, editIDFrom(t, c.EditURL))
	if err != nil {
		t.Fatal(err)
	}
	return secret
}

func bearer(req *http.Request, token string) *http.Request {
	req.Header.Set("Authorization", "Bearer "+token)
	return req
}

func TestUploadTokenOpensWebDAVWithTheToggleOff(t *testing.T) {
	e := newEnv(t, nil)
	c := e.createSite(t, nil, map[string]string{"index.html": "x"}) // webdav off
	edit := editIDFrom(t, c.EditURL)
	tok := uploadTokenFor(t, e, c)

	req := bearer(httptest.NewRequest("PUT", "/dav/"+edit+"/big.bin", strings.NewReader("payload")), tok)
	if w := e.public(t, req); w.Code != 201 {
		t.Fatalf("PUT with an upload token: %d %s", w.Code, w.Body)
	}
	site, _ := e.st.ByViewID(c.ID)
	if b, err := e.st.ReadContentFile(site, "big.bin"); err != nil || string(b) != "payload" {
		t.Fatalf("big.bin = %q, %v", b, err)
	}
	if site.Meta.WebDAVEnabled {
		t.Error("the upload token switched the site's WebDAV toggle on")
	}
	// The toggle still governs the edit password.
	if w := davReq(t, e, "GET", "/dav/"+edit+"/big.bin", c.EditPassword, nil); w.Code != 404 {
		t.Errorf("edit password over WebDAV with the toggle off: %d", w.Code)
	}
}

// Review focus 5: an agent lists and cleans up with the same token.
func TestUploadTokenListsAndDeletesOverWebDAV(t *testing.T) {
	e := newEnv(t, nil)
	c := e.createSite(t, nil, map[string]string{"index.html": "x", "old.txt": "o"})
	edit := editIDFrom(t, c.EditURL)
	tok := uploadTokenFor(t, e, c)

	req := bearer(httptest.NewRequest("PROPFIND", "/dav/"+edit+"/", nil), tok)
	req.Header.Set("Depth", "1")
	if w := e.public(t, req); w.Code != 207 || !strings.Contains(w.Body.String(), "old.txt") {
		t.Fatalf("PROPFIND with a token: %d %s", w.Code, w.Body)
	}
	if w := e.public(t, bearer(httptest.NewRequest("DELETE", "/dav/"+edit+"/old.txt", nil), tok)); w.Code != 204 {
		t.Fatalf("DELETE with a token: %d %s", w.Code, w.Body)
	}
}

func TestUploadTokenWorksAsTheBasicAuthPassword(t *testing.T) {
	e := newEnv(t, nil)
	c := e.createSite(t, nil, map[string]string{"index.html": "x"})
	tok := uploadTokenFor(t, e, c)
	if w := davReq(t, e, "PUT", "/dav/"+editIDFrom(t, c.EditURL)+"/a.txt", tok, strings.NewReader("a")); w.Code != 201 {
		t.Fatalf("PUT with the token as Basic password: %d %s", w.Code, w.Body)
	}
}

// Review focus 1: "bearer" is case-insensitive in HTTP, and an agent's client
// may send it in lower case.
func TestUploadTokenLowerCaseBearer(t *testing.T) {
	e := newEnv(t, nil)
	c := e.createSite(t, nil, map[string]string{"index.html": "x"})
	tok := uploadTokenFor(t, e, c)
	req := httptest.NewRequest("PUT", "/dav/"+editIDFrom(t, c.EditURL)+"/a.txt", strings.NewReader("a"))
	req.Header.Set("Authorization", "bearer "+tok)
	if w := e.public(t, req); w.Code != 201 {
		t.Fatalf("lower-case bearer: %d %s", w.Code, w.Body)
	}
}

func TestUploadTokenRespectsTheInstanceWebDAVSwitch(t *testing.T) {
	e := newEnv(t, map[string]string{"SITEBIN_WEBDAV_ENABLED": "false"})
	c := e.createSite(t, nil, map[string]string{"index.html": "x"})
	tok := uploadTokenFor(t, e, c)
	req := bearer(httptest.NewRequest("PUT", "/dav/"+editIDFrom(t, c.EditURL)+"/a.txt", strings.NewReader("a")), tok)
	if w := e.public(t, req); w.Code != 404 {
		t.Fatalf("WebDAV off instance-wide, upload token: %d", w.Code)
	}
}

func TestUploadTokenIsBoundToOneSiteOverWebDAV(t *testing.T) {
	e := newEnv(t, nil)
	a := e.createSite(t, nil, map[string]string{"index.html": "a"})
	b := e.createSite(t, map[string]string{"webdav": "true"}, map[string]string{"index.html": "b"})
	tok := uploadTokenFor(t, e, a)
	req := bearer(httptest.NewRequest("PUT", "/dav/"+editIDFrom(t, b.EditURL)+"/x.txt", strings.NewReader("x")), tok)
	if w := e.public(t, req); w.Code != 401 {
		t.Fatalf("site A's token on site B: %d", w.Code)
	}
}

func TestExpiredUploadTokenRefusedOverWebDAV(t *testing.T) {
	e := newEnv(t, nil)
	clock := &uploadClock{t: time.Now()}
	e.api.uploads.now = clock.now
	c := e.createSite(t, nil, map[string]string{"index.html": "x"})
	tok := uploadTokenFor(t, e, c)
	clock.advance(uploadTokenIdle)
	req := bearer(httptest.NewRequest("PUT", "/dav/"+editIDFrom(t, c.EditURL)+"/x.txt", strings.NewReader("x")), tok)
	w := e.public(t, req)
	if w.Code != 401 || !strings.Contains(w.Body.String(), "open_upload") {
		t.Fatalf("expired token: %d %s", w.Code, w.Body)
	}
}

func TestUploadTokenCannotOpenAnAnonymousSiteOnAGatedInstance(t *testing.T) {
	e := newEnv(t, nil)
	site, _, err := e.st.Create()
	if err != nil {
		t.Fatal(err)
	}
	ext.Register(&fakeProvider{enabled: true})
	defer ext.Reset()
	tok, _, _ := e.api.uploads.issue(site.ViewID, site.EditID)
	req := bearer(httptest.NewRequest("PUT", "/dav/"+site.EditID+"/x.txt", strings.NewReader("x")), tok)
	if w := e.public(t, req); w.Code != 403 {
		t.Fatalf("anonymous site on a gated instance: %d %s", w.Code, w.Body)
	}
}

func TestUploadTokenKeepsTheSiteQuota(t *testing.T) {
	ext.Register(&fakeProvider{enabled: true, owner: "acct-1", grant: ext.CreateGrant{MaxSiteBytes: 20}})
	defer ext.Reset()
	e := newEnv(t, nil)
	c := e.createSite(t, nil, map[string]string{"index.html": "x"})
	tok := uploadTokenFor(t, e, c)
	req := bearer(httptest.NewRequest("PUT", "/dav/"+editIDFrom(t, c.EditURL)+"/big.txt", strings.NewReader(strings.Repeat("a", 30))), tok)
	if w := e.public(t, req); w.Code != http.StatusInsufficientStorage {
		t.Fatalf("over the tier byte quota: %d %s", w.Code, w.Body)
	}
}
