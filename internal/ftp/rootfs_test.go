package ftp

import (
	"os"
	"path/filepath"
	"testing"
)

// A link a container planted in a site folder must not be an FTP path out of
// the site, for reading or for writing.
func TestRootFsRefusesEscapingLinks(t *testing.T) {
	site := t.TempDir()
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret")
	if err := os.WriteFile(secret, []byte("s"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(site, "out")); err != nil {
		t.Skipf("cannot create symlinks here: %v", err)
	}
	fs := newQuotaFs(site, 1<<20, 100)

	if f, err := fs.Open("/out/secret"); err == nil {
		f.Close()
		t.Error("read followed a link out of the site")
	}
	if f, err := fs.OpenFile("/out/planted", os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
		f.Close()
		t.Error("write followed a link out of the site")
	}
	if _, err := os.Stat(filepath.Join(outside, "planted")); err == nil {
		t.Error("a file appeared outside the site")
	}
	if err := fs.Remove("/out/secret"); err == nil {
		t.Error("remove followed a link out of the site")
	}
	if err := fs.Rename("/out/secret", "/stolen"); err == nil {
		t.Error("rename moved a file from outside the site")
	}
	// Ordinary paths still work.
	f, err := fs.Create("/app/index.js")
	if err == nil {
		t.Error("create in a missing folder should fail like the OS does")
		f.Close()
	}
	if err := fs.MkdirAll("/app", 0o755); err != nil {
		t.Fatal(err)
	}
	f, err = fs.Create("/app/index.js")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	f.Close()
	if _, err := fs.Stat("/"); err != nil {
		t.Errorf("stat root: %v", err)
	}
}
