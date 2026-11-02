package httpapi

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ittrail/sitebin.io/internal/ext"
	"github.com/ittrail/sitebin.io/internal/store"
)

// Custom domains with a path: authz resolves host + longest path to a site
// and tells Caddy which folder to serve and which prefix to strip. See
// docs/superpowers/specs/2026-11-02-custom-domains-with-a-path-design.md.

func mountEnv(t *testing.T) *env {
	t.Helper()
	ext.Register(&fakeProvider{domainsOK: true})
	t.Cleanup(ext.Reset)
	return newEnv(t, nil)
}

// ownedSite is a file site of owner with files, its addrs attached.
func (e *env) ownedSite(t *testing.T, owner string, files map[string]string, addrs ...string) *store.Site {
	t.Helper()
	site, _, err := e.st.Create()
	if err != nil {
		t.Fatal(err)
	}
	if err := e.st.Update(site, func(m *store.Meta) error { m.OwnerAccountID = owner; return nil }); err != nil {
		t.Fatal(err)
	}
	for name, body := range files {
		if err := e.st.SaveFile(site, name, strings.NewReader(body)); err != nil {
			t.Fatal(err)
		}
	}
	for _, a := range addrs {
		if err := e.st.AddDomain(site, a); err != nil {
			t.Fatalf("attach %s: %v", a, err)
		}
	}
	got, err := e.st.ByViewID(site.ViewID)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func hdr(w *httptest.ResponseRecorder, k string) string { return w.Header().Get(k) }

func TestAuthzPlainDomainUnchanged(t *testing.T) {
	e := mountEnv(t)
	a := e.ownedSite(t, "u1", map[string]string{"x.css": "x"}, "m.at")
	w := e.internal(t, authzReq("m.at", "/x.css", ""))
	if w.Code != 200 || hdr(w, siteHeader) != a.ViewID || hdr(w, stripHeader) != "" || hdr(w, mountHeader) != "" {
		t.Fatalf("plain domain: %d site=%q strip=%q mount=%q", w.Code, hdr(w, siteHeader), hdr(w, stripHeader), hdr(w, mountHeader))
	}
	// /foo/_sitebin/x on a host with nothing mounted is the root site's path.
	w = e.internal(t, authzReq("m.at", "/foo/_sitebin/x", ""))
	if w.Code != 200 || hdr(w, upstreamHeader) != "" || hdr(w, siteHeader) != a.ViewID {
		t.Fatalf("a nested _sitebin on an unmounted host: %d upstream=%q", w.Code, hdr(w, upstreamHeader))
	}
	// The generated address carries no site header: its block keys on the label.
	w = e.internal(t, authzReq(a.ViewID+"."+e.cfg.ViewDomain, "/x.css", ""))
	if w.Code != 200 || hdr(w, stripHeader) != "" || hdr(w, mountHeader) != "" {
		t.Fatalf("generated address: %d strip=%q mount=%q", w.Code, hdr(w, stripHeader), hdr(w, mountHeader))
	}
}

func TestAuthzMountedFileSite(t *testing.T) {
	e := mountEnv(t)
	a := e.ownedSite(t, "u1", map[string]string{"index.html": "root"}, "m.at")
	b := e.ownedSite(t, "u1", map[string]string{"index.html": "k1"}, "m.at/demos/k1")
	w := e.internal(t, authzReq("m.at", "/demos/k1/_astro/a.css", ""))
	if w.Code != 200 || hdr(w, siteHeader) != b.ViewID || hdr(w, stripHeader) != "/demos/k1" {
		t.Fatalf("mounted: %d site=%q strip=%q", w.Code, hdr(w, siteHeader), hdr(w, stripHeader))
	}
	w = e.internal(t, authzReq("m.at", "/demos/k1?q=1", ""))
	if w.Code != 308 || hdr(w, "Location") != "/demos/k1/?q=1" {
		t.Fatalf("bare prefix: %d %q, want 308 to the slash form", w.Code, hdr(w, "Location"))
	}
	w = e.internal(t, authzReq("m.at", "/demos/k10", ""))
	if w.Code != 200 || hdr(w, siteHeader) != a.ViewID || hdr(w, stripHeader) != "" {
		t.Fatalf("a sibling path belongs to the root: %d site=%q strip=%q", w.Code, hdr(w, siteHeader), hdr(w, stripHeader))
	}
}

func TestAuthzMountedContainer(t *testing.T) {
	rt := &fakeRuntime{}
	withContainers(t, rt)
	e := newEnv(t, nil)
	c := e.createSite(t, map[string]string{"mode": "container"}, nil)
	running(t, e, c.ID, ext.ContainerService{Name: "web", Host: "sb-" + c.ID + "-web", Ports: []int{3000, 4000}})
	if err := e.api.SiteService().SyncContainerEndpoints(c.ID, []string{"web:3000", "web:4000"}); err != nil {
		t.Fatal(err)
	}
	site, _ := e.st.ByViewID(c.ID)
	for _, a := range []string{"c.at", "c.at/api"} {
		if err := e.st.AddDomain(site, a); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.st.SetDomainTarget(site, "c.at/api", "web:4000"); err != nil {
		t.Fatal(err)
	}
	if err := e.st.SetDomainTarget(site, "c.at", "web:3000"); err != nil {
		t.Fatal(err)
	}
	w := e.internal(t, authzReq("c.at", "/api/login", ""))
	if w.Code != 200 || hdr(w, upstreamHeader) != "sb-"+c.ID+"-web:4000" || hdr(w, mountHeader) != "/api" || hdr(w, stripHeader) != "" {
		t.Fatalf("mounted port: %d upstream=%q mount=%q strip=%q", w.Code, hdr(w, upstreamHeader), hdr(w, mountHeader), hdr(w, stripHeader))
	}
	if w := e.internal(t, authzReq("c.at", "/api", "")); w.Code != 200 || hdr(w, upstreamHeader) == "" {
		t.Fatalf("a container's bare prefix is passed through, not redirected: %d", w.Code)
	}
	w = e.internal(t, authzReq("c.at", "/x", ""))
	if w.Code != 200 || hdr(w, upstreamHeader) != "sb-"+c.ID+"-web:3000" || hdr(w, mountHeader) != "" {
		t.Fatalf("root port: %d upstream=%q mount=%q", w.Code, hdr(w, upstreamHeader), hdr(w, mountHeader))
	}
	// A path containing /_sitebin/ at the root of a container host is the app's.
	if w := e.internal(t, authzReq("c.at", "/foo/_sitebin/x", "")); hdr(w, upstreamHeader) != "sb-"+c.ID+"-web:3000" {
		t.Fatalf("root container's own /foo/_sitebin/x: upstream=%q", hdr(w, upstreamHeader))
	}
	// Sitebin's own routes under the container's path.
	w = e.internal(t, authzReq("c.at", "/api/_sitebin/unlock", ""))
	if w.Code != 200 || hdr(w, upstreamHeader) != publicBackend || hdr(w, mountHeader) != "/api" || hdr(w, stripHeader) != "/api" {
		t.Fatalf("/api/_sitebin/: %d upstream=%q mount=%q strip=%q", w.Code, hdr(w, upstreamHeader), hdr(w, mountHeader), hdr(w, stripHeader))
	}
}

func TestAuthzMountedSitebinRoutes(t *testing.T) {
	e := mountEnv(t)
	e.ownedSite(t, "u1", map[string]string{"index.html": "root"}, "m.at")
	b := e.ownedSite(t, "u1", map[string]string{"index.html": "k1"}, "m.at/demos/k1")
	w := e.internal(t, authzReq("m.at", "/demos/k1/_sitebin/forms/key", ""))
	if w.Code != 200 || hdr(w, upstreamHeader) != publicBackend || hdr(w, mountHeader) != "/demos/k1" || hdr(w, stripHeader) != "/demos/k1" {
		t.Fatalf("form under a path: %d upstream=%q mount=%q strip=%q", w.Code, hdr(w, upstreamHeader), hdr(w, mountHeader), hdr(w, stripHeader))
	}
	// Like /_sitebin/* at the root, they bypass the gate (the unlock form is
	// one of them) and the backend applies a site's own rules.
	if err := e.st.Update(b, func(m *store.Meta) error { m.ViewPasswordProtected = true; return nil }); err != nil {
		t.Fatal(err)
	}
	if w := e.internal(t, authzReq("m.at", "/demos/k1/_sitebin/unlock", "")); w.Code != 200 || hdr(w, upstreamHeader) != publicBackend {
		t.Fatalf("unlock under a protected path: %d", w.Code)
	}
}

func TestAuthzEmptyRoot(t *testing.T) {
	e := mountEnv(t)
	e.ownedSite(t, "u1", map[string]string{"index.html": "a"}, "m.at/a")
	for _, p := range []string{"/", "/b", "/ab"} {
		if w := e.internal(t, authzReq("m.at", p, "")); w.Code != 404 {
			t.Errorf("%s on a host with only /a: %d, want 404", p, w.Code)
		}
	}
	if w := e.internal(t, authzReq("m.at", "/a/", "")); w.Code != 200 {
		t.Errorf("/a/: %d", w.Code)
	}
}

func TestAuthzPathSpellings(t *testing.T) {
	e := mountEnv(t)
	a := e.ownedSite(t, "u1", map[string]string{"index.html": "root"}, "m.at")
	b := e.ownedSite(t, "u1", map[string]string{"x": "secret"}, "m.at/demos/k1")
	if err := e.st.Update(b, func(m *store.Meta) error { m.ViewPasswordProtected = true; return nil }); err != nil {
		t.Fatal(err)
	}
	for _, uri := range []string{
		"/demos/k1/x", "/demos/k1/%2e%2e/k1/x", "/demos//k1/x", "/demos/k1/../k1/x",
		"/demos/%6b1/x", "/demos/k1/./x", "/x/../demos/k1/x", "/demos/k1%2fx",
	} {
		w := e.internal(t, authzReq("m.at", uri, ""))
		if w.Code == 200 && hdr(w, siteHeader) == b.ViewID {
			t.Errorf("%s served the protected site without its password", uri)
		}
		if w.Code == 200 && hdr(w, siteHeader) != a.ViewID {
			t.Errorf("%s: 200 for site %q", uri, hdr(w, siteHeader))
		}
	}
}

func TestAuthzMountedRedirect(t *testing.T) {
	e := mountEnv(t)
	site := e.ownedSite(t, "u1", nil, "m.at/old", "n.at")
	if err := e.st.SetDomainRedirect(site, "m.at/old", "n.at", 301); err != nil {
		t.Fatal(err)
	}
	w := e.internal(t, authzReq("m.at", "/old/a?q=1", ""))
	if w.Code != 301 || hdr(w, "Location") != "http://n.at/a?q=1" {
		t.Fatalf("redirect: %d %q", w.Code, hdr(w, "Location"))
	}
	if w := e.internal(t, authzReq("m.at", "/old", "")); w.Code != 301 || hdr(w, "Location") != "http://n.at/" {
		t.Fatalf("bare redirect: %d %q", w.Code, hdr(w, "Location"))
	}
	// A redirect to an address with a path keeps the remainder under it.
	site2 := e.ownedSite(t, "u2", nil, "o.at/x", "p.at/new")
	if err := e.st.SetDomainRedirect(site2, "o.at/x", "p.at/new", 302); err != nil {
		t.Fatal(err)
	}
	if w := e.internal(t, authzReq("o.at", "/x/a", "")); w.Code != 302 || hdr(w, "Location") != "http://p.at/new/a" {
		t.Fatalf("redirect to a path: %d %q", w.Code, hdr(w, "Location"))
	}
}

func TestAuthzGateUnderMount(t *testing.T) {
	e := mountEnv(t)
	b := e.ownedSite(t, "u1", map[string]string{"index.html": "k1"}, "m.at/demos/k1")
	if err := e.st.Update(b, func(m *store.Meta) error { m.ViewPasswordProtected = true; return nil }); err != nil {
		t.Fatal(err)
	}
	w := e.internal(t, authzReq("m.at", "/demos/k1/", ""))
	if w.Code != 401 || !strings.Contains(w.Body.String(), `action="/demos/k1/_sitebin/unlock"`) {
		t.Fatalf("gate under a path: %d\n%s", w.Code, w.Body)
	}
	if !strings.Contains(w.Body.String(), `value="/demos/k1/"`) {
		t.Errorf("the gate must come back to the page under the path:\n%s", w.Body)
	}
}

func TestAuthzCacheUsesStrippedPath(t *testing.T) {
	e := mountEnv(t)
	e.ownedSite(t, "u1", map[string]string{"_astro/a.1b2c3d4e.css": "x", "index.html": "i"}, "m.at/demos/k1")
	w := e.internal(t, authzReq("m.at", "/demos/k1/_astro/a.1b2c3d4e.css", ""))
	if v := hdr(w, cacheHeader); !strings.Contains(v, "immutable") {
		t.Fatalf("a hashed file under a path: Cache-Control %q, want the long immutable value", v)
	}
	w = e.internal(t, authzReq("m.at", "/demos/k1/", ""))
	if v := hdr(w, cacheHeader); strings.Contains(v, "immutable") {
		t.Fatalf("the page under a path: %q, must revalidate", v)
	}
}

func TestTLSCheckPathOnlyHost(t *testing.T) {
	e := mountEnv(t)
	e.ownedSite(t, "u1", nil, "m.at/a")
	if w := e.internal(t, httptest.NewRequest("GET", "/internal/tls-check?domain=m.at", nil)); w.Code != 200 {
		t.Fatalf("tls-check for a host with only a path address: %d", w.Code)
	}
	if w := e.internal(t, httptest.NewRequest("GET", "/internal/tls-check?domain=x.at", nil)); w.Code != 404 {
		t.Fatalf("tls-check for an unknown host: %d", w.Code)
	}
}
