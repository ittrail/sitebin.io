package main

import (
	"archive/tar"
	"bytes"
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
	// a nested folder, a viewer site's _raw content, and a site with no files/
	os.MkdirAll(filepath.Join(src, "sites", "abc", "files", "assets", "img"), 0o755)
	os.WriteFile(filepath.Join(src, "sites", "abc", "files", "assets", "img", "logo.png"), []byte("png"), 0o644)
	os.MkdirAll(filepath.Join(src, "sites", "view", "files", "_raw"), 0o755)
	os.WriteFile(filepath.Join(src, "sites", "view", "files", "_raw", "doc.md"), []byte("# doc"), 0o644)
	os.MkdirAll(filepath.Join(src, "sites", "bare"), 0o755)
	os.WriteFile(filepath.Join(src, "sites", "bare", "meta.json"), []byte(`{"id":"bare"}`), 0o644)
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
	for p, want := range map[string]string{
		"sites/abc/files/assets/img/logo.png": "png",
		"sites/view/files/_raw/doc.md":        "# doc",
		"sites/bare/meta.json":                `{"id":"bare"}`,
	} {
		if b, err := os.ReadFile(filepath.Join(dst, filepath.FromSlash(p))); err != nil || string(b) != want {
			t.Errorf("%s not restored: %q %v", p, b, err)
		}
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

// tmp/ holds the zip spool and replace staging, and a site folder can hold a
// commit's .replace-commit-* directory: in-flight uploads, not data. They are
// left out of the backup — they change under the walk and nothing restores
// them to any use.
func TestBackupSkipsUploadsInFlight(t *testing.T) {
	src := t.TempDir()
	site := filepath.Join(src, "sites", "abc")
	for _, d := range []string{
		filepath.Join(site, "files"),
		filepath.Join(site, ".replace-commit-1234"),
		filepath.Join(src, "tmp", "replace-abc-99"),
	} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	os.WriteFile(filepath.Join(site, "meta.json"), []byte("{}"), 0o644)
	os.WriteFile(filepath.Join(site, "files", "index.html"), []byte("ok"), 0o644)
	os.WriteFile(filepath.Join(site, ".replace-commit-1234", "half.html"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(src, "tmp", "replace-abc-99", "part.bin"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(src, "tmp", "sitebin-zip-1"), []byte("x"), 0o644)
	// a user's own folder that merely shares the name is content, and kept
	userDir := filepath.Join(site, "files", ".replace-notes")
	os.MkdirAll(userDir, 0o755)
	os.WriteFile(filepath.Join(userDir, "n.txt"), []byte("mine"), 0o644)

	archive := filepath.Join(t.TempDir(), "backup.tar.gz")
	if err := backupData(src, archive); err != nil {
		t.Fatalf("backup: %v", err)
	}
	dst := t.TempDir()
	if err := restoreData(dst, archive); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(dst, "sites", "abc", "files", "index.html")); err != nil || string(b) != "ok" {
		t.Errorf("site content not restored: %q %v", b, err)
	}
	if b, err := os.ReadFile(filepath.Join(dst, "sites", "abc", "files", ".replace-notes", "n.txt")); err != nil || string(b) != "mine" {
		t.Errorf("a user's folder named .replace-* was left out: %q %v", b, err)
	}
	for _, gone := range []string{
		filepath.Join(dst, "tmp"),
		filepath.Join(dst, "sites", "abc", ".replace-commit-1234"),
	} {
		if _, err := os.Lstat(gone); !os.IsNotExist(err) {
			t.Errorf("%s was archived", gone)
		}
	}
}

// A file that disappears between the walk listing it and the backup reading
// it — a meta.json.tmp renamed into place, a staging file removed — is
// skipped; it does not abort the whole backup or corrupt the archive.
func TestBackupToleratesAFileThatVanishes(t *testing.T) {
	src := t.TempDir()
	os.MkdirAll(filepath.Join(src, "sites", "abc"), 0o755)
	os.WriteFile(filepath.Join(src, "sites", "abc", "meta.json"), []byte("{}"), 0o644)
	vanishing := filepath.Join(src, "sites", "abc", "meta.json.tmp")
	os.WriteFile(vanishing, []byte("x"), 0o644)

	real := openForBackup
	openForBackup = func(r *os.Root, name string) (*os.File, error) {
		if name == "sites/abc/meta.json.tmp" {
			return nil, &os.PathError{Op: "open", Path: vanishing, Err: os.ErrNotExist}
		}
		return real(r, name)
	}
	defer func() { openForBackup = real }()

	archive := filepath.Join(t.TempDir(), "backup.tar.gz")
	if err := backupData(src, archive); err != nil {
		t.Fatalf("a vanished file aborted the backup: %v", err)
	}
	dst := t.TempDir()
	if err := restoreData(dst, archive); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, "sites", "abc", "meta.json")); err != nil {
		t.Errorf("meta.json not restored: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(dst, "sites", "abc", "meta.json.tmp")); !os.IsNotExist(err) {
		t.Error("the vanished file was archived")
	}
}

// A file that shrinks between the header claiming its size and the copy —
// a WebDAV PUT truncating it in place, a database checkpoint — must not
// abort the backup: the entry is padded to the size the header promised.
func TestCopyPaddedFillsAFileThatShrank(t *testing.T) {
	var out bytes.Buffer
	short, err := copyPadded(&out, bytes.NewReader([]byte("abc")), 8)
	if err != nil {
		t.Fatal(err)
	}
	if !short {
		t.Error("a short read was not reported")
	}
	if got := out.Bytes(); !bytes.Equal(got, []byte("abc\x00\x00\x00\x00\x00")) {
		t.Errorf("padded copy = %q", got)
	}
	out.Reset()
	short, err = copyPadded(&out, bytes.NewReader([]byte("abcdefghij")), 4)
	if err != nil || short || out.String() != "abcd" {
		t.Errorf("a file that grew: %q short=%v err=%v", out.String(), short, err)
	}
}

// A regular file swapped for a link after the walk saw it must not be
// followed: the backup would store the link target's content as the file's.
func TestBackupDoesNotFollowAFileSwappedForALink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("O_NOFOLLOW is the production platform's")
	}
	src := t.TempDir()
	os.MkdirAll(filepath.Join(src, "sites", "abc", "files"), 0o755)
	secret := filepath.Join(src, ".secret")
	os.WriteFile(secret, []byte("instance secret"), 0o600)
	victim := filepath.Join(src, "sites", "abc", "files", "page.html")
	os.WriteFile(victim, []byte("page"), 0o644)

	real := openForBackup
	openForBackup = func(r *os.Root, name string) (*os.File, error) {
		if name == "page.html" { // the swap happens between the lstat and the open
			os.Remove(victim)
			if err := os.Symlink(secret, victim); err != nil {
				t.Fatal(err)
			}
		}
		return real(r, name)
	}
	defer func() { openForBackup = real }()

	archive := filepath.Join(t.TempDir(), "backup.tar.gz")
	if err := backupData(src, archive); err != nil {
		t.Fatalf("backup: %v", err)
	}
	dst := t.TempDir()
	if err := restoreData(dst, archive); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(dst, "sites", "abc", "files", "page.html")); err == nil && string(b) == "instance secret" {
		t.Fatal("the backup followed a swapped-in link and stored the secret as the page")
	}
}

// A directory swapped for a link while the walk is busy with an earlier
// sibling: WalkDir still holds the old entry saying "directory" and would
// descend into the link's target unless the callback says SkipDir.
func TestBackupDoesNotDescendIntoADirectorySwappedForALink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("links are the production platform's")
	}
	src := t.TempDir()
	outside := t.TempDir()
	os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("outside secret"), 0o644)
	files := filepath.Join(src, "sites", "abc", "files")
	os.MkdirAll(filepath.Join(files, "b"), 0o755)
	os.WriteFile(filepath.Join(files, "a.txt"), []byte("a"), 0o644)
	os.WriteFile(filepath.Join(files, "b", "page.html"), []byte("page"), 0o644)

	real := openForBackup
	openForBackup = func(r *os.Root, name string) (*os.File, error) {
		if name == "a.txt" { // b is swapped while a.txt is being read
			os.RemoveAll(filepath.Join(files, "b"))
			if err := os.Symlink(outside, filepath.Join(files, "b")); err != nil {
				t.Fatal(err)
			}
		}
		return real(r, name)
	}
	defer func() { openForBackup = real }()

	archive := filepath.Join(t.TempDir(), "backup.tar.gz")
	if err := backupData(src, archive); err != nil {
		t.Fatalf("backup: %v", err)
	}
	dst := t.TempDir()
	if err := restoreData(dst, archive); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, "sites", "abc", "files", "b", "secret.txt")); err == nil {
		t.Fatal("the backup descended into a directory swapped for a link and archived what it points at")
	}
}

// A file a container made unreadable (chmod 000) inside a customer site must
// not stop the backup of the whole instance: it is reported and skipped.
func TestBackupSkipsAnUnreadableFileInsideASite(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("permission bits bind a non-root user on the production platform")
	}
	src := t.TempDir()
	files := filepath.Join(src, "sites", "abc", "files")
	os.MkdirAll(files, 0o755)
	os.WriteFile(filepath.Join(src, "sites", "abc", "meta.json"), []byte("{}"), 0o644)
	locked := filepath.Join(files, "locked.db")
	os.WriteFile(locked, []byte("x"), 0o644)
	os.Chmod(locked, 0)
	defer os.Chmod(locked, 0o644)

	archive := filepath.Join(t.TempDir(), "backup.tar.gz")
	if err := backupData(src, archive); err != nil {
		t.Fatalf("an unreadable site file stopped the backup: %v", err)
	}
	dst := t.TempDir()
	if err := restoreData(dst, archive); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, "sites", "abc", "meta.json")); err != nil {
		t.Errorf("the rest of the site was not backed up: %v", err)
	}
}

// The last window: a directory swapped for a link AFTER it was checked and
// before the walk reads it. The link points at another site — inside the data
// root, so no link check objects to it. Read through the site's own root, it
// leads nowhere: the other site's files never end up in this site's backup.
func TestBackupSiteContentNeverLeavesTheSite(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("links are the production platform's")
	}
	src := t.TempDir()
	mine := filepath.Join(src, "sites", "abc", "files")
	theirs := filepath.Join(src, "sites", "xyz", "files")
	os.MkdirAll(filepath.Join(mine, "b"), 0o755)
	os.MkdirAll(theirs, 0o755)
	os.WriteFile(filepath.Join(mine, "b", "page.html"), []byte("mine"), 0o644)
	os.WriteFile(filepath.Join(theirs, "secret.txt"), []byte("another customer's"), 0o644)

	afterDirCheck = func(rel string) {
		if rel == "sites/abc/files/b" {
			os.RemoveAll(filepath.Join(mine, "b"))
			if err := os.Symlink(filepath.Join("..", "..", "xyz", "files"), filepath.Join(mine, "b")); err != nil {
				t.Fatal(err)
			}
		}
	}
	defer func() { afterDirCheck = nil }()

	archive := filepath.Join(t.TempDir(), "backup.tar.gz")
	if err := backupData(src, archive); err != nil {
		t.Fatalf("backup: %v", err)
	}
	dst := t.TempDir()
	if err := restoreData(dst, archive); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(dst, "sites", "abc", "files", "b", "secret.txt")); err == nil {
		t.Fatalf("another site's file was archived inside this site: %q", b)
	}
	if b, err := os.ReadFile(filepath.Join(dst, "sites", "xyz", "files", "secret.txt")); err != nil || string(b) != "another customer's" {
		t.Errorf("the other site itself was not backed up: %q %v", b, err)
	}
}
