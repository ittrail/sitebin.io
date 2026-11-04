package ftp

import (
	"errors"
	"os"
	"strings"
	"testing"
)

func TestQuotaBytesEnforced(t *testing.T) {
	q := newQuotaFs(t.TempDir(), 10, 100)
	f, err := q.Create("/a.txt")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := f.Write([]byte("12345")); err != nil {
		t.Fatalf("write within budget: %v", err)
	}
	// second write would exceed the 10-byte cap
	if _, err := f.Write([]byte("678901")); !errors.Is(err, errQuota) {
		t.Fatalf("over-budget write = %v, want errQuota", err)
	}
	f.Close()
}

func TestQuotaFileCountEnforced(t *testing.T) {
	q := newQuotaFs(t.TempDir(), 1000, 2)
	for _, name := range []string{"/a", "/b"} {
		f, err := q.Create(name)
		if err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
		f.Write([]byte("x"))
		f.Close()
	}
	if _, err := q.Create("/c"); !errors.Is(err, errQuota) {
		t.Fatalf("over-count create = %v, want errQuota", err)
	}
}

func TestQuotaRejectsBadPaths(t *testing.T) {
	q := newQuotaFs(t.TempDir(), 1000, 100)
	for _, bad := range []string{"/../escape", "/_sitebin/x", "/meta.json", "/a/../../x"} {
		if _, err := q.OpenFile(bad, os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
			t.Errorf("write to %q should be rejected", bad)
		}
		if err := q.Mkdir(bad, 0o755); err == nil {
			t.Errorf("mkdir %q should be rejected", bad)
		}
	}
}

func TestQuotaAllowsNormalUse(t *testing.T) {
	q := newQuotaFs(t.TempDir(), 1000, 100)
	if err := q.Mkdir("/assets", 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	f, err := q.Create("/assets/app.js")
	if err != nil {
		t.Fatalf("nested create: %v", err)
	}
	f.Write([]byte("console.log(1)"))
	f.Close()

	// read it back
	rf, err := q.Open("/assets/app.js")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	buf := make([]byte, 64)
	n, _ := rf.Read(buf)
	rf.Close()
	if !strings.Contains(string(buf[:n]), "console.log") {
		t.Errorf("read back = %q", buf[:n])
	}
}

// Appending must not credit the existing size back: only a truncating open
// replaces the file, so only then is its old size free budget again.
func TestQuotaAppendDoesNotCreditTheExistingSize(t *testing.T) {
	q := newQuotaFs(t.TempDir(), 10, 100)
	f, err := q.OpenFile("a.txt", os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte("12345678")); err != nil {
		t.Fatal(err)
	}
	f.Close()
	f, err = q.OpenFile("a.txt", os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.Write([]byte("123")); err == nil {
		t.Fatal("append past the cap succeeded: the existing size was credited back")
	}
	if _, err := f.Write([]byte("12")); err != nil {
		t.Fatalf("append within the cap refused: %v", err)
	}
}

// The repository shares the site's byte cap: a transfer that fits the files'
// budget but not files + repository is refused, and the repository is asked
// at every check (a push can land between two transfers).
func TestFTPCountsRepository(t *testing.T) {
	outside := int64(8)
	q := newQuotaFs(t.TempDir(), 10, 100)
	q.usedOutside = func() int64 { return outside }
	f, err := q.Create("/a.txt")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := f.Write([]byte("123")); err == nil {
		t.Fatal("3 bytes beside an 8-byte repository under a 10-byte cap were accepted")
	} else if !errors.Is(err, errQuota) {
		t.Fatalf("write = %v, want errQuota", err)
	}
	if _, err := f.Write([]byte("12")); err != nil {
		t.Fatalf("2 bytes fill the cap exactly: %v", err)
	}
	f.Close()

	outside = 10 // a push since
	if _, err := q.Create("/b.txt"); err != nil {
		t.Fatalf("create: %v", err)
	} else if f, _ := q.Create("/c.txt"); f != nil {
		if _, err := f.Write([]byte("x")); !errors.Is(err, errQuota) {
			t.Fatalf("write once the repository holds the cap = %v, want errQuota", err)
		}
	}
}
