//go:build ee

package cluster

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sync"
	"testing"
)

var idRE = regexp.MustCompile(`^[a-z2-7]{26}$`)

func TestLoadOrCreateIDCreatesOnce(t *testing.T) {
	dir := t.TempDir()
	a, err := LoadOrCreateID(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !idRE.MatchString(a) {
		t.Fatalf("invalid id %q", a)
	}
	b, err := LoadOrCreateID(dir)
	if err != nil || b != a {
		t.Fatalf("second call = %q, %v; want %q", b, err, a)
	}
	got, err := os.ReadFile(filepath.Join(dir, IDFile))
	if err != nil || string(got) != a+"\n" {
		t.Fatalf("file = %q, %v", got, err)
	}
	entries, _ := os.ReadDir(filepath.Join(dir, "cluster"))
	if len(entries) != 1 {
		t.Fatalf("temp files left behind: %v", entries)
	}
}

func writeID(t *testing.T, dir, content string) {
	t.Helper()
	p := filepath.Join(dir, IDFile)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLoadOrCreateIDKeepsInvalidFile(t *testing.T) {
	dir := t.TempDir()
	writeID(t, dir, "garbage")
	if _, err := LoadOrCreateID(dir); !errors.Is(err, ErrInvalidID) {
		t.Fatalf("err = %v, want ErrInvalidID", err)
	}
	got, _ := os.ReadFile(filepath.Join(dir, IDFile))
	if string(got) != "garbage" {
		t.Fatalf("file was changed: %q", got)
	}
}

func TestLoadOrCreateIDEmptyFile(t *testing.T) {
	dir := t.TempDir()
	writeID(t, dir, "")
	if _, err := LoadOrCreateID(dir); !errors.Is(err, ErrInvalidID) {
		t.Fatalf("err = %v, want ErrInvalidID", err)
	}
}

// The new directory entry is synced after the link, so a power loss right
// after the first start cannot drop it. Windows cannot sync a directory; there
// the call must not fail.
func TestSyncDir(t *testing.T) {
	if err := syncDir(t.TempDir()); err != nil {
		t.Fatalf("syncDir on a directory: %v", err)
	}
	err := syncDir(filepath.Join(t.TempDir(), "missing"))
	if runtime.GOOS == "windows" {
		if err != nil {
			t.Fatalf("on Windows a directory sync is best effort, got %v", err)
		}
	} else if err == nil {
		t.Fatal("a missing directory synced without error")
	}
}

func TestLoadOrCreateIDConcurrent(t *testing.T) {
	dir := t.TempDir()
	const n = 8
	ids := make([]string, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ids[i], errs[i] = LoadOrCreateID(dir)
		}(i)
	}
	wg.Wait()
	for i := range ids {
		if errs[i] != nil {
			t.Fatalf("goroutine %d: %v", i, errs[i])
		}
		if ids[i] != ids[0] {
			t.Fatalf("ids differ: %q vs %q", ids[i], ids[0])
		}
	}
	if !idRE.MatchString(ids[0]) {
		t.Fatalf("invalid id %q", ids[0])
	}
}
