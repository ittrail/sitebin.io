package httpapi

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A link a container planted in a site folder must not be a WebDAV path out
// of the site: webdav.Dir followed it, siteFS must not.
func TestSiteFSRefusesEscapingLinks(t *testing.T) {
	e := newEnv(t, nil)
	site, _, err := e.st.Create()
	if err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret")
	if err := os.WriteFile(secret, []byte("s"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(site.FilesDir(), "out")); err != nil {
		t.Skipf("cannot create symlinks here: %v", err)
	}
	fs := newSiteFS(e.st, site, false)
	ctx := context.Background()

	if f, err := fs.OpenFile(ctx, "/out/secret", os.O_RDONLY, 0); err == nil {
		f.Close()
		t.Error("read followed a link out of the site")
	}
	if f, err := fs.OpenFile(ctx, "/out/planted", os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
		f.Close()
		t.Error("write followed a link out of the site")
	}
	if err := fs.RemoveAll(ctx, "/out/secret"); err == nil {
		if _, serr := os.Stat(secret); serr != nil {
			t.Error("RemoveAll deleted a file outside the site")
		}
	}
	if _, err := fs.Stat(ctx, "/out/secret"); err == nil {
		t.Error("stat followed a link out of the site")
	}
	if err := fs.Rename(ctx, "/out/secret", "/stolen"); err == nil {
		t.Error("rename moved a file from outside the site")
	}
	if _, err := os.Stat(secret); err != nil {
		t.Errorf("secret is gone: %v", err)
	}

	// Ordinary use still works.
	if err := fs.Mkdir(ctx, "/app", 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := fs.OpenFile(ctx, "/app/a.txt", os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	f.Write([]byte("hi"))
	f.Close()
	if fi, err := fs.Stat(ctx, "/app/a.txt"); err != nil || fi.Size() != 2 {
		t.Errorf("stat = %v, %v", fi, err)
	}
	if err := fs.RemoveAll(ctx, "/"); err == nil {
		t.Error("RemoveAll of the root succeeded")
	}
}

// A PUT creates the folders its path names, as a multipart upload does: an
// update feed is published file by file over WebDAV, and every new folder
// answered 409 until a MKCOL (Atoo Mail, 2026-11-11).
func TestWebDAVPutCreatesMissingFolders(t *testing.T) {
	e := newEnv(t, nil)
	c := e.createSite(t, map[string]string{"webdav": "true"}, map[string]string{"index.html": "x"})
	base := "/dav/" + editIDFrom(t, c.EditURL) + "/"
	if w := davReq(t, e, "PUT", base+"stable/win/app.exe", c.EditPassword, strings.NewReader("MZ")); w.Code != 201 {
		t.Fatalf("PUT into missing folders: %d %s", w.Code, w.Body)
	}
	if w := davReq(t, e, "GET", base+"stable/win/app.exe", c.EditPassword, nil); w.Code != 200 || w.Body.String() != "MZ" {
		t.Fatalf("GET: %d %q", w.Code, w.Body)
	}
	// A folder cannot be made where a file is.
	if w := davReq(t, e, "PUT", base+"index.html/x.css", c.EditPassword, strings.NewReader("x")); w.Code < 400 {
		t.Fatalf("PUT under a file: %d %s", w.Code, w.Body)
	}
	// Nor under a name the store refuses.
	if w := davReq(t, e, "PUT", base+".sitebin-trusted/x.css", c.EditPassword, strings.NewReader("x")); w.Code < 400 {
		t.Fatalf("PUT under a reserved name: %d %s", w.Code, w.Body)
	}
	site, _ := e.st.ByViewID(c.ID)
	if _, err := os.Stat(filepath.Join(site.ContentDir(), ".sitebin-trusted")); err == nil {
		fi, _ := os.Stat(filepath.Join(site.ContentDir(), ".sitebin-trusted"))
		if fi.IsDir() {
			t.Fatal("a refused PUT made a reserved folder")
		}
	}
}

// A link a container planted must not become a folder a PUT creates under.
func TestSiteFSCreatesNoFolderThroughAnEscapingLink(t *testing.T) {
	e := newEnv(t, nil)
	site, _, err := e.st.Create()
	if err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(site.FilesDir(), "out")); err != nil {
		t.Skipf("cannot create symlinks here: %v", err)
	}
	fs := newSiteFS(e.st, site, false)
	if f, err := fs.OpenFile(context.Background(), "/out/new/planted", os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
		f.Close()
		t.Error("a write created folders through a link out of the site")
	}
	if _, err := os.Stat(filepath.Join(outside, "new")); err == nil {
		t.Error("a folder was created outside the site")
	}
}
