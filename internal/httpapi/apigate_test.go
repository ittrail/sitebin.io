package httpapi

import (
	"net/http"
	"net/http/httptest"
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
		{"origin without fetch metadata", map[string]string{"Origin": "http://sitebin.example"}, false},
		{"same-origin fetch", map[string]string{"Sec-Fetch-Site": "same-origin"}, true},
		{"same-origin fetch with origin", map[string]string{
			"Sec-Fetch-Site": "same-origin", "Origin": "http://sitebin.example",
		}, true},
		{"allowlisted embed", map[string]string{
			"Sec-Fetch-Site": "cross-site", "Origin": "https://sitebin.io",
		}, true},
		{"foreign origin", map[string]string{
			"Sec-Fetch-Site": "cross-site", "Origin": "https://evil.example",
		}, false},
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
