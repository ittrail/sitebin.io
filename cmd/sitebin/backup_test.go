package main

import (
	"archive/tar"
	"compress/gzip"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func writeEvilArchive(t *testing.T, path string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	body := []byte("pwned")
	hdr := &tar.Header{Name: "../../escape.txt", Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}
	tw.WriteHeader(hdr)
	tw.Write(body)
	tw.Close()
	gz.Close()
}

func TestBackupRestoreRoundTrip(t *testing.T) {
	src := t.TempDir()
	// a nested file + a symlink (indexes are symlinks in production)
	os.MkdirAll(filepath.Join(src, "sites", "abc", "files"), 0o755)
	os.WriteFile(filepath.Join(src, "sites", "abc", "meta.json"), []byte(`{"id":"abc"}`), 0o644)
	os.WriteFile(filepath.Join(src, "sites", "abc", "files", "index.html"), []byte("hi"), 0o644)
	haveSymlink := false
	if runtime.GOOS != "windows" {
		os.MkdirAll(filepath.Join(src, "edit-index"), 0o755)
		if os.Symlink(filepath.Join("..", "sites", "abc"), filepath.Join(src, "edit-index", "e1")) == nil {
			haveSymlink = true
		}
	}

	archive := filepath.Join(t.TempDir(), "backup.tar.gz")
	if err := backupData(src, archive); err != nil {
		t.Fatalf("backup: %v", err)
	}

	dst := t.TempDir()
	if err := restoreData(dst, archive); err != nil {
		t.Fatalf("restore: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(dst, "sites", "abc", "files", "index.html"))
	if err != nil || string(b) != "hi" {
		t.Fatalf("restored file wrong: %q %v", b, err)
	}
	if _, err := os.Stat(filepath.Join(dst, "sites", "abc", "meta.json")); err != nil {
		t.Errorf("meta not restored: %v", err)
	}
	if haveSymlink {
		fi, err := os.Lstat(filepath.Join(dst, "edit-index", "e1"))
		if err != nil || fi.Mode()&os.ModeSymlink == 0 {
			t.Errorf("symlink not restored as symlink: %v", err)
		}
	}
}

func TestRestoreRejectsUnsafePath(t *testing.T) {
	// build a malicious tar with a path escaping the root, then ensure restore refuses.
	dir := t.TempDir()
	archive := filepath.Join(dir, "evil.tar.gz")
	writeEvilArchive(t, archive)
	if err := restoreData(filepath.Join(dir, "data"), archive); err == nil {
		t.Fatal("restore accepted a path-escaping archive")
	}
}

// A symlink in an archive that points outside the data root, followed by a
// regular entry written THROUGH it, lands outside the root — the lexical
// guard sees a path under the root and the filesystem follows the link.
func TestRestoreRefusesSymlinksThatEscapeTheRoot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need elevation on windows")
	}
	base := t.TempDir()
	outside := filepath.Join(base, "outside")
	os.MkdirAll(outside, 0o755)
	archive := filepath.Join(base, "evil.tar.gz")
	f, _ := os.Create(archive)
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	tw.WriteHeader(&tar.Header{Name: "escape", Typeflag: tar.TypeSymlink, Linkname: "../outside", Mode: 0o777})
	body := []byte("pwned")
	tw.WriteHeader(&tar.Header{Name: "escape/pwned.txt", Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(body))})
	tw.Write(body)
	tw.Close()
	gz.Close()
	f.Close()

	root := filepath.Join(base, "data")
	if err := restoreData(root, archive); err == nil {
		t.Fatal("an archive with an escaping symlink was restored")
	}
	if _, err := os.Stat(filepath.Join(outside, "pwned.txt")); err == nil {
		t.Fatal("a file was written outside the data root")
	}
	// absolute link targets are refused too
	archive2 := filepath.Join(base, "abs.tar.gz")
	f, _ = os.Create(archive2)
	gz = gzip.NewWriter(f)
	tw = tar.NewWriter(gz)
	tw.WriteHeader(&tar.Header{Name: "abs", Typeflag: tar.TypeSymlink, Linkname: "/etc", Mode: 0o777})
	tw.Close()
	gz.Close()
	f.Close()
	if err := restoreData(filepath.Join(base, "data2"), archive2); err == nil {
		t.Fatal("an absolute symlink target was restored")
	}
}

// What a container leaves in a site folder — a link out of the data root and a
// unix socket — must neither abort the backup nor make its restore refuse
// the whole archive. Both are skipped; everything else survives.
func TestBackupSkipsContainerDebris(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("links and sockets are the production platform's")
	}
	src := t.TempDir()
	files := filepath.Join(src, "sites", "abc", "files", "app")
	os.MkdirAll(files, 0o755)
	os.WriteFile(filepath.Join(files, "index.js"), []byte("ok"), 0o644)
	if err := os.Symlink("/etc", filepath.Join(files, "out")); err != nil {
		t.Skipf("symlink: %v", err)
	}
	if err := os.Symlink("index.js", filepath.Join(files, "inside")); err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(files, "s.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Skipf("unix socket: %v", err)
	}
	defer l.Close()

	archive := filepath.Join(t.TempDir(), "backup.tar.gz")
	if err := backupData(src, archive); err != nil {
		t.Fatalf("backup: %v", err)
	}
	dst := t.TempDir()
	if err := restoreData(dst, archive); err != nil {
		t.Fatalf("restore: %v", err)
	}
	out := filepath.Join(dst, "sites", "abc", "files", "app")
	if b, err := os.ReadFile(filepath.Join(out, "index.js")); err != nil || string(b) != "ok" {
		t.Errorf("file not restored: %q %v", b, err)
	}
	if _, err := os.Lstat(filepath.Join(out, "out")); !os.IsNotExist(err) {
		t.Error("an escaping link was archived")
	}
	if _, err := os.Lstat(filepath.Join(out, "s.sock")); !os.IsNotExist(err) {
		t.Error("a socket was archived")
	}
	if fi, err := os.Lstat(filepath.Join(out, "inside")); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("a link inside the root was lost: %v", err)
	}
}
