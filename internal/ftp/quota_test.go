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
