package store

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// escapeLink plants what a container can create in a volume folder: a link
// out of the site. The target holds a secret the site must never reach.
// Skips where the platform will not make symlinks (Windows without the
// privilege); the production platform always can.
func escapeLink(t *testing.T, site *Site) (secret string) {
	t.Helper()
	outside := t.TempDir()
	secret = filepath.Join(outside, "secret")
	if err := os.WriteFile(secret, []byte("instance-secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	app := filepath.Join(site.FilesDir(), "app")
	if err := os.MkdirAll(app, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(app, "out")); err != nil {
		t.Skipf("cannot create symlinks here: %v", err)
	}
	if err := os.Symlink(secret, filepath.Join(app, "secret-link")); err != nil {
		t.Skipf("cannot create symlinks here: %v", err)
	}
	return secret
}

func TestSymlinkConfinementRead(t *testing.T) {
	s := newTestStore(t)
	site, _, _ := s.Create()
	escapeLink(t, site)

	for _, p := range []string{"app/out/secret", "app/secret-link"} {
		b, err := s.ReadContentFile(site, p)
		if err == nil {
			t.Errorf("ReadContentFile(%q) followed a link out of the site: %q", p, b)
		}
	}
}

func TestSymlinkConfinementWrite(t *testing.T) {
	s := newTestStore(t)
	site, _, _ := s.Create()
	secret := escapeLink(t, site)

	if err := s.SaveFile(site, "app/out/planted", strings.NewReader("x")); err == nil {
		t.Error("SaveFile wrote through a link out of the site")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(secret), "planted")); err == nil {
		t.Error("a file appeared outside the site")
	}
	// Writing onto the link itself replaces the link, never its target.
	if err := s.SaveFile(site, "app/secret-link", strings.NewReader("new")); err != nil {
		t.Fatalf("SaveFile over a link: %v", err)
	}
	if b, _ := os.ReadFile(secret); string(b) != "instance-secret" {
		t.Errorf("the link's target was overwritten: %q", b)
	}
}

func TestSymlinkConfinementDelete(t *testing.T) {
	s := newTestStore(t)
	site, _, _ := s.Create()
	secret := escapeLink(t, site)

	if err := s.DeleteFile(site, "app/out/secret"); err == nil {
		t.Error("DeleteFile removed a file through a link out of the site")
	}
	if _, err := os.Stat(secret); err != nil {
		t.Errorf("the secret is gone: %v", err)
	}
}

func TestSymlinkConfinementZipAndList(t *testing.T) {
	s := newTestStore(t)
	site, _, _ := s.Create()
	escapeLink(t, site)
	if err := s.SaveFile(site, "app/index.js", strings.NewReader("ok")); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	if err := s.ZipContent(site, &buf); err != nil {
		t.Fatalf("ZipContent: %v", err)
	}
	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range zr.File {
		if f.Name != "app/index.js" {
			t.Errorf("zip contains %q", f.Name)
		}
	}

	files, err := s.ListFiles(site)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].Path != "app/index.js" {
		t.Errorf("listing = %+v, want only app/index.js", files)
	}
}

func TestPurgeSymlinks(t *testing.T) {
	s := newTestStore(t)
	site, _, _ := s.Create()
	secret := escapeLink(t, site)

	n, err := s.PurgeSymlinks(site)
	if err != nil || n != 2 {
		t.Fatalf("PurgeSymlinks = %d, %v; want 2", n, err)
	}
	if _, err := os.Stat(secret); err != nil {
		t.Errorf("purging a link removed its target: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(site.FilesDir(), "app", "out")); !os.IsNotExist(err) {
		t.Error("link survived the purge")
	}
}

func TestPrepareVolume(t *testing.T) {
	s := newTestStore(t)
	site, _, _ := s.Create()

	rel, err := s.PrepareVolume(site, "db")
	if err != nil {
		t.Fatalf("PrepareVolume: %v", err)
	}
	if want := "sites/" + site.ViewID + "/files/db"; rel != want {
		t.Errorf("rel = %q, want %q", rel, want)
	}
	if fi, err := os.Stat(filepath.Join(site.FilesDir(), "db")); err != nil || !fi.IsDir() {
		t.Error("folder was not created")
	}
	// idempotent
	if _, err := s.PrepareVolume(site, "db"); err != nil {
		t.Errorf("second PrepareVolume: %v", err)
	}
	for _, bad := range []string{"", ".", "..", "a/b", ".hidden", "_raw", "_sitebin", "..\\x", "meta.json"} {
		if _, err := s.PrepareVolume(site, bad); !errors.Is(err, ErrBadPath) {
			t.Errorf("PrepareVolume(%q) = %v, want ErrBadPath", bad, err)
		}
	}
}

func TestPrepareVolumeRefusesLink(t *testing.T) {
	s := newTestStore(t)
	site, _, _ := s.Create()
	if err := os.Symlink(t.TempDir(), filepath.Join(site.FilesDir(), "evil")); err != nil {
		t.Skipf("cannot create symlinks here: %v", err)
	}
	if _, err := s.PrepareVolume(site, "evil"); err == nil {
		t.Error("PrepareVolume accepted a link as a volume folder")
	}
}

func TestReadCompose(t *testing.T) {
	s := newTestStore(t)
	site, _, _ := s.Create()
	if _, err := s.ReadCompose(site); !errors.Is(err, ErrNotFound) {
		t.Errorf("no compose: %v, want ErrNotFound", err)
	}
	if err := s.SaveFile(site, ComposeFile, strings.NewReader("services: {}\n")); err != nil {
		t.Fatal(err)
	}
	b, err := s.ReadCompose(site)
	if err != nil || string(b) != "services: {}\n" {
		t.Errorf("ReadCompose = %q, %v", b, err)
	}
	big := strings.Repeat("#", MaxComposeBytes+1)
	if err := s.SaveFile(site, ComposeFile, strings.NewReader(big)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReadCompose(site); !errors.Is(err, ErrTooLarge) {
		t.Errorf("oversized compose: %v, want ErrTooLarge", err)
	}
}

func TestContainerUpstream(t *testing.T) {
	m := &ContainerMeta{
		Enabled: true,
		Status:  ContainerRunning,
		Services: []ContainerService{
			{Name: "app", Host: "sb-x-app", Domains: []ContainerDomain{{Domain: "*", Port: 3000}, {Domain: "shop.example.com", Port: 3001}}},
			{Name: "db", Host: "sb-x-db"},
		},
	}
	cases := []struct {
		host string
		want string
		ok   bool
	}{
		{"x.sitebin.app", "sb-x-app:3000", true},
		{"X.SITEBIN.APP", "sb-x-app:3000", true},
		{"shop.example.com", "sb-x-app:3001", true},
		{"other.example.com", "", false},
	}
	for _, c := range cases {
		got, ok := m.Upstream(c.host, "x.sitebin.app")
		if got != c.want || ok != c.ok {
			t.Errorf("Upstream(%q) = %q,%v want %q,%v", c.host, got, ok, c.want, c.ok)
		}
	}
	m.Status = ContainerStopped
	if _, ok := m.Upstream("x.sitebin.app", "x.sitebin.app"); ok {
		t.Error("a stopped project was routed")
	}
	m.Status, m.Enabled = ContainerRunning, false
	if _, ok := m.Upstream("x.sitebin.app", "x.sitebin.app"); ok {
		t.Error("a disabled project was routed")
	}
	var nilMeta *ContainerMeta
	if _, ok := nilMeta.Upstream("x", "x"); ok {
		t.Error("nil meta routed")
	}
}

func TestContainerModeHasNoFileCap(t *testing.T) {
	s := newTestStore(t)
	site, _, _ := s.Create()
	if s.EffMaxFiles(site) != 100 {
		t.Fatalf("webserver cap = %d", s.EffMaxFiles(site))
	}
	site.Meta.Mode = ModeContainer
	for i := 0; i < 150; i++ {
		if err := s.SaveFile(site, fmt.Sprintf("app/f%03d.txt", i), strings.NewReader("")); err != nil {
			t.Fatalf("file %d: %v", i, err)
		}
	}
}
