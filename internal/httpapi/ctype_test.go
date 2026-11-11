package httpapi

import (
	"strings"
	"testing"

	"github.com/ittrail/sitebin.io/internal/ext"
)

// Release files get a Content-Type: the image's mime.types (mailcap) has none
// for .dmg, .yml, .AppImage, .msix …, and Caddy then sends none at all, beside
// nosniff (Atoo Mail, 2026-11-11). A file without an extension is text or a
// download — never sniffed into anything a browser would run.
func TestFileType(t *testing.T) {
	head := func(s string) func() []byte { return func() []byte { return []byte(s) } }
	none := func() []byte { t.Fatal("read a file whose name decides its type"); return nil }
	for _, c := range []struct {
		name string
		head func() []byte
		want string
	}{
		{"stable/Atoo-Mail-1.0.0-x64.dmg", none, "application/x-apple-diskimage"},
		{"stable/latest-mac.yml", none, "application/yaml"},
		{"config.YAML", none, "application/yaml"},
		{"Atoo-Mail-1.0.0.AppImage", none, "application/vnd.appimage"},
		{"Atoo-Mail.msix", none, "application/msix"},
		{"Atoo-Mail.msixbundle", none, "application/msixbundle"},
		{"Atoo-Mail.appx", none, "application/appx"},
		{"Atoo-Mail.appxbundle", none, "application/appxbundle"},
		{"Atoo-Mail.appinstaller", none, "application/appinstaller"},
		{"Atoo-Mail-Setup-1.0.0.exe.blockmap", none, "application/octet-stream"},
		{"Atoo-Mail.msi", none, "application/x-msi"},
		{"app.dmg.sha256", none, "text/plain; charset=utf-8"},
		{"app.dmg.sha512", none, "text/plain; charset=utf-8"},
		{"app.dmg.minisig", none, "text/plain; charset=utf-8"},
		{"src.7z", none, "application/x-7z-compressed"},
		// What the system's table answers stays Caddy's, unchanged.
		{"index.html", none, ""},
		{"app.exe", none, ""},
		{"a.deb", none, ""},
		{"style.css", none, ""},
		{"unknown.xyz", none, ""},
		{".htaccess", none, ""},
		// No extension: text or a download.
		{"stable/SHA256SUMS", head("3f2a…  Atoo-Mail-1.0.0-x64.dmg\n"), "text/plain; charset=utf-8"},
		{"LICENSE", head(""), "text/plain; charset=utf-8"},
		{"atoo-mail-linux", head("\x7fELF\x02\x01\x01\x00\x00\x00"), "application/octet-stream"},
		{"page", head("<!DOCTYPE html><script>alert(1)</script>"), "application/octet-stream"},
		{"img", head("\x89PNG\r\n\x1a\n"), "application/octet-stream"},
		{"stable/", none, ""},
	} {
		if got := fileType(c.name, c.head); got != c.want {
			t.Errorf("fileType(%q) = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestAuthzSendsTheTypeOfReleaseFiles(t *testing.T) {
	e := newEnv(t, nil)
	c := e.createSite(t, nil, map[string]string{
		"index.html":                     "x",
		"stable/Atoo-Mail-1.0.0-x64.dmg": "\x00\x01koly",
		"stable/SHA256SUMS":              "3f2a  Atoo-Mail-1.0.0-x64.dmg\n",
		"stable/atoo-mail-linux":         "\x7fELF\x02\x01\x01\x00",
		"stable/latest.yml":              "version: 1.0.0\n",
	})
	host := c.ID + ".sitebin.example"
	get := func(uri string) string {
		t.Helper()
		w := e.internal(t, authzReq(host, uri, ""))
		if w.Code != 200 {
			t.Fatalf("authz %s: %d", uri, w.Code)
		}
		return w.Header().Get(typeHeader)
	}
	for uri, want := range map[string]string{
		"/stable/Atoo-Mail-1.0.0-x64.dmg": "application/x-apple-diskimage",
		"/stable/SHA256SUMS":              "text/plain; charset=utf-8",
		"/stable/atoo-mail-linux":         "application/octet-stream",
		"/stable/latest.yml?t=1":          "application/yaml",
		"/stable/%53HA256SUMS":            "text/plain; charset=utf-8",
		"/index.html":                     "",
		"/":                               "",
		"/stable/":                        "",
		"/stable":                         "",
		"/stable/missing":                 "",
		"/stable/missing.dmg":             "",
	} {
		if got := get(uri); got != want {
			t.Errorf("%s: %q, want %q", uri, got, want)
		}
	}
	// The type does not depend on the cache policy.
	edit := editIDFrom(t, c.EditURL)
	if w := e.public(t, jsonReq("PUT", "/api/sites/"+edit, `{"cache":"off"}`, c.EditPassword)); w.Code != 200 {
		t.Fatalf("set off: %d %s", w.Code, w.Body)
	}
	if got := get("/stable/SHA256SUMS"); got != "text/plain; charset=utf-8" {
		t.Errorf("with caching off: %q", got)
	}
}

// In viewer mode the files are served from /_raw/.
func TestAuthzTypeAsksTheServedFolder(t *testing.T) {
	e := newEnv(t, nil)
	c := e.createSite(t, map[string]string{"mode": "viewer"}, map[string]string{"SHA256SUMS": "abc  x\n"})
	w := e.internal(t, authzReq(c.ID+".sitebin.example", "/_raw/SHA256SUMS", ""))
	if got := w.Header().Get(typeHeader); !strings.HasPrefix(got, "text/plain") {
		t.Errorf("viewer raw file: %q", got)
	}
}

// A container's answers keep the app's own Content-Type: authz names none,
// even for a path that is a file without an extension in the site's folder.
func TestAuthzNamesNoTypeForAContainer(t *testing.T) {
	withContainers(t, &fakeRuntime{})
	e := newEnv(t, nil)
	c := e.createSite(t, map[string]string{"mode": "container"}, nil)
	running(t, e, c.ID, ext.ContainerService{Name: "app", Host: "sb-" + c.ID + "-app", Ports: []int{3000}})
	if err := e.api.SiteService().SyncContainerEndpoints(c.ID, []string{"app:3000"}); err != nil {
		t.Fatal(err)
	}
	site, _ := e.st.ByViewID(c.ID)
	if err := e.st.SaveFile(site, "SHA256SUMS", strings.NewReader("abc\n")); err != nil {
		t.Fatal(err)
	}
	w := e.internal(t, authzReq(c.ID+"."+e.cfg.ViewDomain, "/SHA256SUMS", ""))
	if w.Code != 200 || w.Header().Get(upstreamHeader) == "" {
		t.Fatalf("container not routed: %d %q", w.Code, w.Header().Get(upstreamHeader))
	}
	if got := w.Header().Get(typeHeader); got != "" {
		t.Errorf("container: %s = %q, want none (the app names its own types)", typeHeader, got)
	}
}
