package httpapi

import (
	"context"
	"os"
	"path/filepath"
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
	fs := newSiteFS(e.st, site)
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
