package main

import (
	"archive/tar"
	"compress/gzip"
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
