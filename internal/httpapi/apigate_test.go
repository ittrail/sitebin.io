package httpapi

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"testing"

	"github.com/ittrail/sitebin.io/internal/ext"
	"github.com/ittrail/sitebin.io/internal/store"
)

func TestFromOwnBrowser(t *testing.T) {
	e := newEnv(t, map[string]string{"SITEBIN_EMBED_ORIGINS": "https://sitebin.io"})
	ext.Register(&fakeProvider{enabled: true, embedOK: true})
	defer ext.Reset()

	cases := []struct {
		name    string
		headers map[string]string
		want    bool
	}{
		{"bare script", nil, false},
		// A host-matching Origin is browser-shaped even with no Sec-Fetch-Site
		// at all: older Safari, embedded WebViews and header-stripping
		// corporate proxies never send it (it only shipped in Safari 16.4).
		{"own-host origin without fetch metadata", map[string]string{"Origin": "http://sitebin.example"}, true},
		{"foreign origin without fetch metadata", map[string]string{"Origin": "http://evil.example"}, false},
		{"same-origin fetch", map[string]string{"Sec-Fetch-Site": "same-origin"}, true},
		{"same-origin fetch with origin", map[string]string{
			"Sec-Fetch-Site": "same-origin", "Origin": "http://sitebin.example",
		}, true},
		// SITEBIN_HTTP_ONLY=true behind an external TLS terminator: the
		// backend computes "http://" for itself but the browser sends
		// "https://". Comparing by host, not full URL, tolerates the mismatch.
		{"own-host origin, mismatched scheme", map[string]string{"Origin": "https://sitebin.example"}, true},
		{"allowlisted embed", map[string]string{
			"Sec-Fetch-Site": "cross-site", "Origin": "https://sitebin.io",
		}, true},
		{"foreign origin", map[string]string{
			"Sec-Fetch-Site": "cross-site", "Origin": "https://evil.example",
		}, false},
		{"cross-site with no origin", map[string]string{"Sec-Fetch-Site": "cross-site"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", "/api/sites", nil)
			req.Host = "sitebin.example"
			for k, v := range tc.headers {
				req.Header.Set(k, v)
			}
			if got := e.api.fromOwnBrowser(req); got != tc.want {
				t.Fatalf("fromOwnBrowser = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestAPIAllowedFor(t *testing.T) {
	e := newEnv(t, nil)

	script := func() *http.Request { return httptest.NewRequest("GET", "/api/sites/x", nil) }
	browser := func() *http.Request {
		r := script()
		r.Header.Set("Sec-Fetch-Site", "same-origin")
		return r
	}

	site, _, err := e.st.Create()
	if err != nil {
		t.Fatal(err)
	}

	// community build: no provider, everything open
	if !e.api.apiAllowedFor(site, script()) {
		t.Error("community build must allow the API")
	}

	ext.Register(&fakeProvider{enabled: true})
	defer ext.Reset()

	if e.api.apiAllowedFor(site, script()) {
		t.Error("anonymous site must refuse a scripted call")
	}
	if !e.api.apiAllowedFor(site, browser()) {
		t.Error("anonymous site must allow its own edit page")
	}

	if err := e.st.Update(site, func(m *store.Meta) error { m.OwnerAccountID = "acct-1"; return nil }); err != nil {
		t.Fatal(err)
	}
	if !e.api.apiAllowedFor(site, script()) {
		t.Error("owned site must allow a scripted call")
	}
}

func TestAnonymousSiteAPIRefusesScripts(t *testing.T) {
	ext.Register(&fakeProvider{enabled: true})
	defer ext.Reset()
	e := newEnv(t, nil)
	c := e.createSite(t, nil, map[string]string{"index.html": "x"})
	edit := editIDFrom(t, c.EditURL)

	// script-shaped: correct password, no browser fetch metadata
	req := authed(httptest.NewRequest("GET", "/api/sites/"+edit, nil), c.EditPassword)
	if w := e.public(t, req); w.Code != 403 {
		t.Fatalf("scripted call on an anonymous site: %d %s", w.Code, w.Body)
	}

	// the edit page's own fetch
	req = authed(httptest.NewRequest("GET", "/api/sites/"+edit, nil), c.EditPassword)
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	if w := e.public(t, req); w.Code != 200 {
		t.Fatalf("edit page call: %d %s", w.Code, w.Body)
	}
}

func TestOwnedSiteAPIAllowsScripts(t *testing.T) {
	ext.Register(&fakeProvider{enabled: true, owner: "acct-1"})
	defer ext.Reset()
	e := newEnv(t, nil)
	c := e.createSite(t, nil, map[string]string{"index.html": "x"})
	edit := editIDFrom(t, c.EditURL)

	req := authed(httptest.NewRequest("GET", "/api/sites/"+edit, nil), c.EditPassword)
	if w := e.public(t, req); w.Code != 200 {
		t.Fatalf("scripted call on an owned site: %d %s", w.Code, w.Body)
	}
}

// scriptCreate posts a minimal multipart create with no browser headers.
func scriptCreate(t *testing.T, e *env, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", `form-data; name="files"; filename="index.html"`)
	p, _ := mw.CreatePart(h)
	p.Write([]byte("<h1>hi</h1>"))
	mw.Close()
	req := httptest.NewRequest("POST", "/api/sites", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return e.public(t, req)
}

func TestAnonymousCreateRefusesScripts(t *testing.T) {
	ext.Register(&fakeProvider{enabled: true})
	defer ext.Reset()
	e := newEnv(t, nil)

	if w := scriptCreate(t, e, nil); w.Code != 401 {
		t.Fatalf("scripted anonymous create: %d %s", w.Code, w.Body)
	}
	if w := scriptCreate(t, e, map[string]string{"Sec-Fetch-Site": "same-origin"}); w.Code != 201 {
		t.Fatalf("browser anonymous create: %d %s", w.Code, w.Body)
	}
}

func TestAnonymousCreateAllowsAllowlistedEmbed(t *testing.T) {
	ext.Register(&fakeProvider{enabled: true, embedOK: true})
	defer ext.Reset()
	e := newEnv(t, map[string]string{"SITEBIN_EMBED_ORIGINS": "https://sitebin.io"})

	w := scriptCreate(t, e, map[string]string{
		"Sec-Fetch-Site": "cross-site",
		"Origin":         "https://sitebin.io",
	})
	if w.Code != 201 {
		t.Fatalf("embed create: %d %s", w.Code, w.Body)
	}
}

func TestCommunityBuildCreateAllowsScripts(t *testing.T) {
	e := newEnv(t, nil) // no provider registered: community build stays fully open
	if w := scriptCreate(t, e, nil); w.Code != 201 {
		t.Fatalf("community build scripted create: %d %s", w.Code, w.Body)
	}
}

func TestOwnedCreateAllowsScripts(t *testing.T) {
	ext.Register(&fakeProvider{enabled: true, owner: "acct-1"})
	defer ext.Reset()
	e := newEnv(t, nil)

	if w := scriptCreate(t, e, nil); w.Code != 201 {
		t.Fatalf("owned scripted create: %d %s", w.Code, w.Body)
	}
}
