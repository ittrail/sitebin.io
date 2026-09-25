package httpapi

import (
	"archive/zip"
	"bytes"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
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

// uploadBody builds a multipart upload: zipFiles (when non-nil) become one
// "zip" part holding an archive of them, and each files entry becomes a
// "files" part whose filename is its path.
func uploadBody(t *testing.T, zipFiles, files map[string]string) (*bytes.Buffer, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if zipFiles != nil {
		var zb bytes.Buffer
		zw := zip.NewWriter(&zb)
		for name, content := range zipFiles {
			f, err := zw.Create(name)
			if err != nil {
				t.Fatal(err)
			}
			f.Write([]byte(content))
		}
		if err := zw.Close(); err != nil {
			t.Fatal(err)
		}
		h := textproto.MIMEHeader{}
		h.Set("Content-Disposition", `form-data; name="zip"; filename="site.zip"`)
		p, _ := mw.CreatePart(h)
		p.Write(zb.Bytes())
	}
	for name, content := range files {
		h := textproto.MIMEHeader{}
		h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="files"; filename="%s"`, name))
		p, _ := mw.CreatePart(h)
		p.Write([]byte(content))
	}
	mw.Close()
	return &buf, mw.FormDataContentType()
}

func TestUploadTokenReplacesASiteWithAZip(t *testing.T) {
	e := newEnv(t, nil)
	c := e.createSite(t, nil, map[string]string{"index.html": "old", "old.txt": "x"})
	tok := uploadTokenFor(t, e, c)
	body, ct := uploadBody(t, map[string]string{"index.html": "new", "assets/app.js": "js"}, nil)
	req := bearer(httptest.NewRequest("POST", "/api/sites/"+editIDFrom(t, c.EditURL)+"/files?replace=true", body), tok)
	req.Header.Set("Content-Type", ct)
	if w := e.public(t, req); w.Code != 200 {
		t.Fatalf("zip upload with a token: %d %s", w.Code, w.Body)
	}
	site, _ := e.st.ByViewID(c.ID)
	files, _ := e.st.ListFiles(site)
	got := map[string]bool{}
	for _, f := range files {
		got[f.Path] = true
	}
	if len(files) != 2 || !got["index.html"] || !got["assets/app.js"] {
		t.Fatalf("after replace: %+v", files)
	}
}

func TestUploadTokenStoresAFileAtANestedPath(t *testing.T) {
	e := newEnv(t, nil)
	c := e.createSite(t, nil, map[string]string{"index.html": "x"})
	tok := uploadTokenFor(t, e, c)
	body, ct := uploadBody(t, nil, map[string]string{"media/video.mp4": "frames"})
	req := bearer(httptest.NewRequest("POST", "/api/sites/"+editIDFrom(t, c.EditURL)+"/files", body), tok)
	req.Header.Set("Content-Type", ct)
	if w := e.public(t, req); w.Code != 200 {
		t.Fatalf("nested upload: %d %s", w.Code, w.Body)
	}
	site, _ := e.st.ByViewID(c.ID)
	if b, err := e.st.ReadContentFile(site, "media/video.mp4"); err != nil || string(b) != "frames" {
		t.Fatalf("media/video.mp4 = %q, %v", b, err)
	}
}

// Review focus 2: a credential presented as an upload token either works as
// one or fails. A correct edit password riding along does not rescue it.
func TestBadUploadTokenNeverFallsBackToThePassword(t *testing.T) {
	e := newEnv(t, nil)
	c := e.createSite(t, nil, map[string]string{"index.html": "x"})
	body, ct := uploadBody(t, nil, map[string]string{"a.txt": "a"})
	req := authed(httptest.NewRequest("POST", "/api/sites/"+editIDFrom(t, c.EditURL)+"/files", body), c.EditPassword)
	req = bearer(req, "sbu_notarealtoken")
	req.Header.Set("Content-Type", ct)
	if w := e.public(t, req); w.Code != 401 {
		t.Fatalf("bad token with a good password alongside: %d %s", w.Code, w.Body)
	}
}

func TestUploadTokenIsBoundToOneSiteOverTheAPI(t *testing.T) {
	e := newEnv(t, nil)
	a := e.createSite(t, nil, map[string]string{"index.html": "a"})
	b := e.createSite(t, nil, map[string]string{"index.html": "b"})
	tok := uploadTokenFor(t, e, a)
	body, ct := uploadBody(t, nil, map[string]string{"x.txt": "x"})
	req := bearer(httptest.NewRequest("POST", "/api/sites/"+editIDFrom(t, b.EditURL)+"/files", body), tok)
	req.Header.Set("Content-Type", ct)
	if w := e.public(t, req); w.Code != 401 {
		t.Fatalf("site A's token on site B's upload: %d", w.Code)
	}
}

// TestUploadTokenRefusedEverywhereElse runs the refusal loop once per channel
// a credential can travel on — Authorization: Bearer, X-Edit-Password, and
// the Basic-auth password — because withEditAuth's password path only reads
// X-Edit-Password and Basic, never Bearer. A version that sent the token only
// as Bearer would stay green even if the X-Edit-Password (or Basic) branch of
// uploadCredential were deleted, since that channel's "edit password
// required" 401 looks like a refusal too. Running all three, then checking the
// real edit password still verifies under a rate limit of one, proves none of
// the three channels reached the password path.
func TestUploadTokenRefusedEverywhereElse(t *testing.T) {
	e := newEnv(t, map[string]string{"SITEBIN_RATE_AUTH_PER_5MIN": "1"})
	c := e.createSite(t, nil, map[string]string{"index.html": "x"})
	edit := editIDFrom(t, c.EditURL)
	tok := uploadTokenFor(t, e, c)
	routes := []struct{ method, path, body string }{
		{"GET", "/api/sites/" + edit, ""},
		{"PUT", "/api/sites/" + edit, `{"view_password":"x"}`},
		{"DELETE", "/api/sites/" + edit, ""},
		{"GET", "/api/sites/" + edit + "/download", ""},
		{"GET", "/api/sites/" + edit + "/content/index.html", ""},
		{"DELETE", "/api/sites/" + edit + "/files/index.html", ""},
		{"POST", "/api/sites/" + edit + "/domains", `{"domain":"d.example.com"}`},
		{"GET", "/api/sites/" + edit + "/forms", ""},
		{"POST", "/api/sites", ""},
	}
	channels := []struct {
		name string
		with func(*http.Request) *http.Request
	}{
		{"Bearer", func(r *http.Request) *http.Request { return bearer(r, tok) }},
		{"X-Edit-Password", func(r *http.Request) *http.Request { return authed(r, tok) }},
		{"Basic", func(r *http.Request) *http.Request { r.SetBasicAuth("sitebin", tok); return r }},
	}
	for _, ch := range channels {
		for _, rt := range routes {
			req := ch.with(httptest.NewRequest(rt.method, rt.path, strings.NewReader(rt.body)))
			w := e.public(t, req)
			if w.Code != 403 || !strings.Contains(w.Body.String(), "upload token") {
				t.Errorf("%s: %s %s with an upload token: %d %s", ch.name, rt.method, rt.path, w.Code, w.Body)
			}
		}
	}
	// With a rate limit of one attempt, the edit password still verifies: none
	// of the refusals above, on any channel, reached the rate-limited password
	// path.
	if w := e.public(t, authed(httptest.NewRequest("GET", "/api/sites/"+edit, nil), c.EditPassword)); w.Code != 200 {
		t.Fatalf("edit password after the refusals: %d %s", w.Code, w.Body)
	}
	site, err := e.st.ByViewID(c.ID)
	if err != nil {
		t.Fatal("the site was deleted through an upload token")
	}
	if site.Meta.ViewPasswordProtected {
		t.Error("an upload token changed a setting")
	}
}

// A good upload token works the JSON API upload route through X-Edit-Password
// alone, with no Bearer header at all — an agent's HTTP client may not offer
// a way to set Authorization on a plain form upload.
func TestUploadTokenAsXEditPasswordUploads(t *testing.T) {
	e := newEnv(t, nil)
	c := e.createSite(t, nil, map[string]string{"index.html": "x"})
	tok := uploadTokenFor(t, e, c)
	body, ct := uploadBody(t, nil, map[string]string{"a.txt": "a"})
	req := authed(httptest.NewRequest("POST", "/api/sites/"+editIDFrom(t, c.EditURL)+"/files", body), tok)
	req.Header.Set("Content-Type", ct)
	if w := e.public(t, req); w.Code != 200 {
		t.Fatalf("X-Edit-Password upload token: %d %s", w.Code, w.Body)
	}
	site, _ := e.st.ByViewID(c.ID)
	if b, err := e.st.ReadContentFile(site, "a.txt"); err != nil || string(b) != "a" {
		t.Fatalf("a.txt = %q, %v", b, err)
	}
}

// Review fix round 1, Finding 2: an sbu_ credential is never tried as an edit
// password, on any surface. verifyEditIP refuses it before the verify cache,
// the rate limiters, and Argon2 — so an upload token thrown at the password
// path never spends the limiter budget a real wrong password would.
func TestUploadTokenNeverVerifiesAsAnEditPassword(t *testing.T) {
	e := newEnv(t, map[string]string{"SITEBIN_RATE_AUTH_PER_5MIN": "1"})
	c := e.createSite(t, nil, map[string]string{"index.html": "x"})
	site, err := e.st.ByEditID(editIDFrom(t, c.EditURL))
	if err != nil {
		t.Fatal(err)
	}
	if got := e.api.verifyEditIP("192.0.2.1", site, "sbu_x"); got != verifyFailed {
		t.Fatalf("verifyEditIP(upload token) = %v, want verifyFailed", got)
	}
	// The limiter allows exactly one attempt. If the upload token above had
	// spent it, this real password would come back throttled instead of OK.
	if got := e.api.verifyEditIP("192.0.2.1", site, c.EditPassword); got != verifyOK {
		t.Fatalf("verifyEditIP(edit password) after an upload-token attempt = %v, want verifyOK (limiter must not have been spent)", got)
	}
}

func TestRotatingTheEditPasswordRevokesUploadTokens(t *testing.T) {
	e := newEnv(t, nil)
	c := e.createSite(t, nil, map[string]string{"index.html": "x"})
	tok := uploadTokenFor(t, e, c)
	if _, err := (siteService{a: e.api}).RotateEditPassword(c.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.api.uploads.begin(tok, editIDFrom(t, c.EditURL)); ok {
		t.Fatal("an upload token survived a password rotation")
	}
}

func TestDeletingASiteRevokesItsUploadTokens(t *testing.T) {
	e := newEnv(t, nil)
	deletes := map[string]func(c createResp){
		"api": func(c createResp) {
			e.public(t, authed(httptest.NewRequest("DELETE", "/api/sites/"+editIDFrom(t, c.EditURL), nil), c.EditPassword))
		},
		"site service": func(c createResp) {
			if err := (siteService{a: e.api}).Delete(c.ID); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, del := range deletes {
		c := e.createSite(t, nil, map[string]string{"index.html": "x"})
		uploadTokenFor(t, e, c)
		del(c)
		for _, tk := range e.api.uploads.m {
			if tk.viewID == c.ID {
				t.Errorf("%s delete left an upload token behind", name)
			}
		}
	}
}

// Review focus 4: a token whose site is gone gets a clean 404, not a 500.
func TestUploadTokenForADeletedSiteIsANotFound(t *testing.T) {
	e := newEnv(t, nil)
	c := e.createSite(t, nil, map[string]string{"index.html": "x"})
	edit := editIDFrom(t, c.EditURL)
	tok := uploadTokenFor(t, e, c)
	site, _ := e.st.ByViewID(c.ID)
	if err := e.st.Delete(site); err != nil { // the cleanup sweep's path: no revocation
		t.Fatal(err)
	}
	if w := e.public(t, bearer(httptest.NewRequest("PUT", "/dav/"+edit+"/x.txt", strings.NewReader("x")), tok)); w.Code != 404 {
		t.Errorf("WebDAV on a deleted site: %d", w.Code)
	}
	body, ct := uploadBody(t, nil, map[string]string{"x.txt": "x"})
	req := bearer(httptest.NewRequest("POST", "/api/sites/"+edit+"/files", body), tok)
	req.Header.Set("Content-Type", ct)
	if w := e.public(t, req); w.Code != 404 {
		t.Errorf("API upload on a deleted site: %d %s", w.Code, w.Body)
	}
}
