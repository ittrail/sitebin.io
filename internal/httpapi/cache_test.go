package httpapi

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ittrail/sitebin.io/internal/store"
)

// Browser caching: the Cache-Control value authz hands Caddy. See
// docs/superpowers/specs/2026-10-16-browser-caching-design.md.

const (
	revalidate = "public, max-age=0, must-revalidate"
	immutable  = "public, max-age=31536000, immutable"
)

func TestFingerprinted(t *testing.T) {
	for _, p := range []string{
		"/assets/index-BqCz7v8e.js",                                      // Vite
		"/static/js/main.8f9e7d6a.js",                                    // create-react-app / webpack
		"/main.3f9a1c2b4d5e6f70.js",                                      // Angular
		"/_expo/static/js/web/entry-3fa9d0c1e2b4a5f60718293a4b5c6d7e.js", // Expo
		"/assets/logo.a1b2c3d4.png",
		"/_next/static/chunks/pages/index.js", // Next keeps the whole tree content-addressed
		"/_next/static/xY3_abcDEF12/_buildManifest.js",
		"/_app/immutable/entry/start.js", // SvelteKit
		"/v/abcdefghijklmnopqrstuvwxyz/assets/index-BqCz7v8e.js",
		"/3f9a1c2b4d5e6f70a1b2.png", // webpack asset modules: [hash][ext]
	} {
		if !fingerprinted(p) {
			t.Errorf("fingerprinted(%q) = false, want true", p)
		}
	}
	for _, p := range []string{
		"/style.css", "/logo.png", "/js/app.js", "/app-settings.js",
		"/report2024.pdf", "/q3-report2024.pdf", "/photo-20240101.jpg",
		"/jquery-3.7.1.min.js", "/bootstrap.bundle.min.js", "/fonts/inter-variable.woff2",
		"/assets/index-AbCdEfGh.js", // no digit: indistinguishable from a word, so not trusted
		"/deadbeef.js",              // a bare name must be a long hash
		"/cafe2024.jpg",
		"/next/static/app.js",
	} {
		if fingerprinted(p) {
			t.Errorf("fingerprinted(%q) = true, want false — a hand-named file would be cached for a year", p)
		}
	}
}

func TestCacheControl(t *testing.T) {
	cases := []struct {
		policy, path  string
		file, private bool
		want          string
	}{
		// auto: pages revalidate, hashed bundles are immutable, the rest revalidates.
		{"", "/", false, false, revalidate},
		{"", "/index.html", true, false, revalidate},
		{"", "/about", false, false, revalidate},
		{"", "/data.json", true, false, revalidate},
		{"", "/assets/index-BqCz7v8e.js", true, false, immutable},
		{"", "/style.css", true, false, revalidate},
		// a hashed name that is not a file is the SPA fallback's index.html,
		// or a 404: never a year.
		{"", "/assets/index-BqCz7v8e.js", false, false, revalidate},
		{"revalidate", "/assets/index-BqCz7v8e.js", true, false, revalidate},
		{"hour", "/style.css", true, false, "public, max-age=3600"},
		{"day", "/logo.png", true, false, "public, max-age=86400"},
		{"week", "/assets/index-BqCz7v8e.js", true, false, "public, max-age=604800"},
		{"year", "/style.css", true, false, "public, max-age=31536000"},
		{"year", "/index.html", true, false, revalidate},
		{"year", "/missing.css", false, false, revalidate},
		{"off", "/style.css", true, false, ""},
		{"off", "/", false, false, ""},
		// a view password keeps shared caches out.
		{"", "/assets/index-BqCz7v8e.js", true, true, "private, max-age=31536000, immutable"},
		{"", "/", false, true, "private, max-age=0, must-revalidate"},
		{"day", "/logo.png", true, true, "private, max-age=86400"},
	}
	for _, c := range cases {
		if got := cacheControl(c.policy, c.path, c.file, c.private); got != c.want {
			t.Errorf("cacheControl(%q, %q, file=%v, private=%v) = %q, want %q", c.policy, c.path, c.file, c.private, got, c.want)
		}
	}
}

func TestCleanCachePolicy(t *testing.T) {
	for in, want := range map[string]string{"": "", "auto": "", "AUTO": "", " day ": "day", "off": "off", "revalidate": "revalidate", "year": "year"} {
		got, err := store.CleanCachePolicy(in)
		if err != nil || got != want {
			t.Errorf("CleanCachePolicy(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := store.CleanCachePolicy("forever"); err == nil {
		t.Error("an unknown policy was accepted")
	}
}

// authz hands Caddy the value for a file site that passed the gate, and for
// nothing else.
func TestAuthzSendsCacheControl(t *testing.T) {
	e := newEnv(t, nil)
	c := e.createSite(t, nil, map[string]string{"index.html": "x", "assets/index-BqCz7v8e.js": "js", "style.css": "css"})
	host := c.ID + ".sitebin.example"
	get := func(uri string) string {
		w := e.internal(t, authzReq(host, uri, ""))
		if w.Code != 200 {
			t.Fatalf("authz %s: %d", uri, w.Code)
		}
		return w.Header().Get(cacheHeader)
	}
	if got := get("/assets/index-BqCz7v8e.js?v=1"); got != immutable {
		t.Errorf("hashed bundle: %q", got)
	}
	if got := get("/style.css"); got != revalidate {
		t.Errorf("hand-named file: %q", got)
	}
	if got := get("/"); got != revalidate {
		t.Errorf("page: %q", got)
	}
	if got := get("/assets/index-Zz9Yy8Xx.js"); got != revalidate {
		t.Errorf("a hashed name that does not exist: %q", got)
	}
	if got := get("/assets/%69ndex-BqCz7v8e.js"); got != immutable {
		t.Errorf("percent-encoded path: %q", got)
	}
	if got := get("/../../../../etc/passwd"); got != revalidate {
		t.Errorf("a path out of the site: %q", got)
	}

	edit := editIDFrom(t, c.EditURL)
	if w := e.public(t, jsonReq("PUT", "/api/sites/"+edit, `{"cache":"off"}`, c.EditPassword)); w.Code != 200 {
		t.Fatalf("set off: %d %s", w.Code, w.Body)
	}
	if got := get("/style.css"); got != "" {
		t.Errorf("off still sends %q", got)
	}

	// The gate page is never cached: no value on a 401.
	if w := e.public(t, jsonReq("PUT", "/api/sites/"+edit, `{"cache":"day","view_password":"sesame"}`, c.EditPassword)); w.Code != 200 {
		t.Fatalf("set day + password: %d %s", w.Code, w.Body)
	}
	w := e.internal(t, authzReq(host, "/style.css", ""))
	if w.Code != 401 || w.Header().Get(cacheHeader) != "" {
		t.Errorf("gate: %d %q", w.Code, w.Header().Get(cacheHeader))
	}

	// Path views name the same files under /v/<id>/.
	req := httptest.NewRequest("GET", "/internal/authz", nil)
	e2 := newEnv(t, map[string]string{"SITEBIN_VIEW_ACCESS": "path"})
	c2 := e2.createSite(t, nil, map[string]string{"assets/index-BqCz7v8e.js": "js"})
	req.Header.Set("X-Sitebin-View", c2.ID)
	req.Header.Set("X-Forwarded-Uri", "/v/"+c2.ID+"/assets/index-BqCz7v8e.js")
	if got := e2.internal(t, req).Header().Get(cacheHeader); got != immutable {
		t.Errorf("path view: %q", got)
	}
}

func TestCacheSettingThroughTheAPI(t *testing.T) {
	e := newEnv(t, nil)
	c := e.createSite(t, map[string]string{"cache": "week"}, map[string]string{"index.html": "x"})
	edit := editIDFrom(t, c.EditURL)
	w := e.public(t, authed(httptest.NewRequest("GET", "/api/sites/"+edit, nil), c.EditPassword))
	if !strings.Contains(w.Body.String(), `"cache":"week"`) {
		t.Errorf("multipart create did not set the policy: %s", w.Body)
	}
	if w := e.public(t, jsonReq("PUT", "/api/sites/"+edit, `{"cache":"forever"}`, c.EditPassword)); w.Code != 400 {
		t.Errorf("unknown policy: %d", w.Code)
	}
	w = e.public(t, jsonReq("PUT", "/api/sites/"+edit, `{"cache":"auto"}`, c.EditPassword))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"cache":"auto"`) {
		t.Errorf("auto: %d %s", w.Code, w.Body)
	}
	site, _ := e.st.ByViewID(c.ID)
	if site.Meta.Cache != "" {
		t.Errorf("auto must be stored as the default, got %q", site.Meta.Cache)
	}
}

// MCP sets the policy through update_site, and a result carries it only when
// it is not auto, so every other result keeps the shape clients cached.
func TestMCPCachePolicy(t *testing.T) {
	e := newEnv(t, nil)
	cs := mcpClient(t, e, nil)
	editID, pw := mcpCreate(t, cs, "hi")
	res := mcpCall(t, cs, "get_site", map[string]any{"edit_id": editID, "edit_password": pw})
	if strings.Contains(mcpText(res), `"cache"`) {
		t.Errorf("an auto site's result carries cache: %s", mcpText(res))
	}
	res = mcpCall(t, cs, "update_site", map[string]any{"edit_id": editID, "edit_password": pw, "settings": map[string]any{"cache": "year"}})
	if res.IsError || !strings.Contains(mcpText(res), `"cache":"year"`) {
		t.Errorf("update_site cache: %v %s", res.IsError, mcpText(res))
	}
	res = mcpCall(t, cs, "update_site", map[string]any{"edit_id": editID, "edit_password": pw, "settings": map[string]any{"cache": "never"}})
	if !res.IsError || !strings.Contains(mcpText(res), "auto, revalidate, hour, day, week, year or off") {
		t.Errorf("a bad policy must say which exist: %v %s", res.IsError, mcpText(res))
	}
}
