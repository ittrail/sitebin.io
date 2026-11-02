package store

import (
	"errors"
	"strings"
	"testing"
)

func TestNormalizeAddress(t *testing.T) {
	ok := map[string]string{
		"Morgenfertig.AT":                       "morgenfertig.at",
		" morgenfertig.at ":                     "morgenfertig.at",
		"morgenfertig.at.":                      "morgenfertig.at",
		"morgenfertig.at/demos/kunde123":        "morgenfertig.at/demos/kunde123",
		"MORGENFERTIG.at/demos/kunde123":        "morgenfertig.at/demos/kunde123",
		"morgenfertig.at./demos":                "morgenfertig.at/demos",
		"m.at/a-b_c.d~e/1":                      "m.at/a-b_c.d~e/1",
		"m.at/" + strings.Repeat("a/", 7) + "a": "m.at/" + strings.Repeat("a/", 7) + "a",
	}
	for in, want := range ok {
		got, err := normalizeAddress(in)
		if err != nil || got != want {
			t.Errorf("normalizeAddress(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	bad := []string{
		"",
		"morgenfertig.at/",
		"morgenfertig.at/Demos",
		"morgenfertig.at/a//b",
		"morgenfertig.at/a/../b",
		"morgenfertig.at/./a",
		"morgenfertig.at/..",
		"morgenfertig.at/_sitebin",
		"morgenfertig.at/a/_sitebin/b",
		"morgenfertig.at/a?x=1",
		"morgenfertig.at/a#f",
		"morgenfertig.at/a b",
		"morgenfertig.at/a%20b",
		"m.at/" + strings.Repeat("a/", 8) + "a", // 9 segments
		"m.at/" + strings.Repeat("a", 200),      // 201-char path
		"https://morgenfertig.at/a",
		"localhost/a",
		"/a",
	}
	for _, in := range bad {
		if got, err := normalizeAddress(in); !errors.Is(err, ErrBadDomain) {
			t.Errorf("normalizeAddress(%q) = %q, %v; want ErrBadDomain", in, got, err)
		}
	}
}

func TestHostPathOf(t *testing.T) {
	if got := hostOf("a.at/x/y"); got != "a.at" {
		t.Errorf("hostOf = %q", got)
	}
	if got := pathOf("a.at/x/y"); got != "/x/y" {
		t.Errorf("pathOf = %q", got)
	}
	if got := hostOf("a.at"); got != "a.at" {
		t.Errorf("hostOf(plain) = %q", got)
	}
	if got := pathOf("a.at"); got != "" {
		t.Errorf("pathOf(plain) = %q", got)
	}
	if HostOf("a.at/x") != "a.at" {
		t.Error("HostOf must match hostOf")
	}
}
