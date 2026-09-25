package httpapi

import (
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

// A page's assets carry the build's version, so a deploy reaches every
// browser at once; a versioned asset may be cached for good, and an
// unversioned one is revalidated (ETag) instead of being kept for an hour.
func TestPagesVersionTheirAssetsAndAssetsRevalidate(t *testing.T) {
	e := newEnv(t, nil)
	fsys := fstest.MapFS{}
	for k, v := range testFS {
		fsys[k] = v
	}
	fsys["static/edit.html"] = &fstest.MapFile{Data: []byte(
		`<link rel="stylesheet" href="/_sitebin/assets/static/app.css">` +
			`<script src="/_sitebin/assets/static/edit.js" defer></script>`)}
	fsys["static/edit.js"] = &fstest.MapFile{Data: []byte("// edit")}
	api, err := New(e.cfg, e.st, []byte("0123456789abcdef0123456789abcdef"), fsys)
	if err != nil {
		t.Fatal(err)
	}
	e.api = api
	c := e.createSite(t, nil, nil)

	w := e.public(t, httptest.NewRequest("GET", "/e/"+editIDFrom(t, c.EditURL), nil))
	v := api.assetVersion()
	for _, want := range []string{
		`href="/_sitebin/assets/static/app.css?v=` + v + `"`,
		`src="/_sitebin/assets/static/edit.js?v=` + v + `"`,
	} {
		if !strings.Contains(w.Body.String(), want) {
			t.Errorf("edit page lacks %s:\n%s", want, w.Body)
		}
	}

	w = e.public(t, httptest.NewRequest("GET", "/_sitebin/assets/static/edit.js?v="+v, nil))
	if cc := w.Header().Get("Cache-Control"); w.Code != 200 || !strings.Contains(cc, "immutable") {
		t.Errorf("versioned asset: %d, Cache-Control %q", w.Code, cc)
	}
	w = e.public(t, httptest.NewRequest("GET", "/_sitebin/assets/static/edit.js", nil))
	etag := w.Header().Get("ETag")
	if cc := w.Header().Get("Cache-Control"); w.Code != 200 || cc != "no-cache" || etag == "" {
		t.Fatalf("unversioned asset: %d, Cache-Control %q, ETag %q", w.Code, cc, etag)
	}
	req := httptest.NewRequest("GET", "/_sitebin/assets/static/edit.js", nil)
	req.Header.Set("If-None-Match", etag)
	if w := e.public(t, req); w.Code != 304 {
		t.Errorf("revalidation with the ETag: %d, want 304", w.Code)
	}
}
