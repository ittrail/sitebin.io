package ids

import (
	"strings"
	"testing"
)

const base32Alphabet = "abcdefghijklmnopqrstuvwxyz234567"

func TestNewViewID(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 1000; i++ {
		id := NewViewID()
		if len(id) != 26 {
			t.Fatalf("len = %d (%q)", len(id), id)
		}
		for _, c := range id {
			if !strings.ContainsRune(base32Alphabet, c) {
				t.Fatalf("bad char %q in %q", c, id)
			}
		}
		if seen[id] {
			t.Fatalf("duplicate id %q", id)
		}
		seen[id] = true
	}
}

func TestNewEditID(t *testing.T) {
	id := NewEditID()
	if len(id) != 26 || !ValidID(id) {
		t.Fatalf("bad edit id %q", id)
	}
}

func TestNewEditPassword(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		pw := NewEditPassword()
		if len(pw) != 22 {
			t.Fatalf("len = %d (%q)", len(pw), pw)
		}
		for _, c := range pw {
			if !strings.ContainsRune("0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz", c) {
				t.Fatalf("bad char %q in %q", c, pw)
			}
		}
		if seen[pw] {
			t.Fatalf("duplicate password")
		}
		seen[pw] = true
	}
}

func TestValidID(t *testing.T) {
	good := NewViewID()
	if !ValidID(good) {
		t.Errorf("ValidID(%q) = false", good)
	}
	for _, bad := range []string{
		"", "short", strings.Repeat("a", 25), strings.Repeat("a", 27),
		strings.Repeat("A", 26), // uppercase
		"abcdefghijklmnopqrstuvwx..", "abcdefghijklmnopqrstuvwx/z",
		"abcdefghijklmnopqrstuvwx1z", // '1' not in base32 alphabet
	} {
		if ValidID(bad) {
			t.Errorf("ValidID(%q) = true", bad)
		}
	}
}
