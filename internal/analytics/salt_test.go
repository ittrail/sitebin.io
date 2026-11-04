package analytics

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"
)

func TestVisitorIDStableWithinDay(t *testing.T) {
	s := NewSalts(t.TempDir())
	salt, err := s.For("2026-11-04")
	if err != nil {
		t.Fatal(err)
	}
	a := VisitorID(salt, "view1", "203.0.113.7", "UA")
	b := VisitorID(salt, "view1", "203.0.113.7", "UA")
	if a != b || len(a) != 16 {
		t.Fatalf("ids %q %q", a, b)
	}
	if VisitorID(salt, "view1", "203.0.113.8", "UA") == a || VisitorID(salt, "view1", "203.0.113.7", "UA2") == a {
		t.Fatal("ip and ua must change the id")
	}
}

func TestVisitorIDDiffersAcrossSitesAndDays(t *testing.T) {
	s := NewSalts(t.TempDir())
	d1, _ := s.For("2026-11-04")
	d2, _ := s.For("2026-11-05")
	if bytes.Equal(d1, d2) {
		t.Fatal("salts of two days must differ")
	}
	base := VisitorID(d1, "view1", "203.0.113.7", "UA")
	if VisitorID(d1, "view2", "203.0.113.7", "UA") == base {
		t.Fatal("site must change the id")
	}
	if VisitorID(d2, "view1", "203.0.113.7", "UA") == base {
		t.Fatal("day must change the id")
	}
}

func TestSaltsPersistAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	a, err := NewSalts(dir).For("2026-11-04")
	if err != nil || len(a) != 32 {
		t.Fatalf("%v len %d", err, len(a))
	}
	b, err := NewSalts(dir).For("2026-11-04")
	if err != nil || !bytes.Equal(a, b) {
		t.Fatalf("salt changed across restart: %v", err)
	}
}

func TestSaltsBadDate(t *testing.T) {
	s := NewSalts(t.TempDir())
	for _, d := range []string{"", "../x", "2026-1-04", "2026-11-04x"} {
		if _, err := s.For(d); err == nil {
			t.Errorf("date %q accepted", d)
		}
	}
}

func TestSaltsConcurrent(t *testing.T) {
	s := NewSalts(t.TempDir())
	var wg sync.WaitGroup
	res := make([][]byte, 16)
	for i := range res {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res[i], _ = s.For("2026-11-04")
		}()
	}
	wg.Wait()
	for _, r := range res {
		if !bytes.Equal(r, res[0]) || len(r) != 32 {
			t.Fatal("concurrent For returned different salts")
		}
	}
}

func TestSaltsPrune(t *testing.T) {
	dir := t.TempDir()
	s := NewSalts(dir)
	for _, d := range []string{"2026-11-01", "2026-11-02", "2026-11-03", "2026-11-04"} {
		if _, err := s.For(d); err != nil {
			t.Fatal(err)
		}
	}
	today := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	if err := s.Prune(today); err != nil {
		t.Fatal(err)
	}
	exists := func(d string) bool {
		_, err := os.Stat(filepath.Join(dir, "analytics", "salts", d))
		return err == nil
	}
	if exists("2026-11-01") {
		t.Fatal("a salt more than two days old must be deleted")
	}
	if !exists("2026-11-02") || !exists("2026-11-03") || !exists("2026-11-04") {
		t.Fatal("salts of the last three dates must stay")
	}
	// A pruned date gets a fresh salt, not a cached one.
	if _, err := s.For("2026-11-01"); err != nil || !exists("2026-11-01") {
		t.Fatalf("recreate: %v", err)
	}
}

func TestSaltFileMode0600(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file modes are not meaningful on Windows")
	}
	dir := t.TempDir()
	if _, err := NewSalts(dir).For("2026-11-04"); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(dir, "analytics", "salts", "2026-11-04"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", fi.Mode().Perm())
	}
}
