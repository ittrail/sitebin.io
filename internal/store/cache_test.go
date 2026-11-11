package store

import (
	"errors"
	"strings"
	"testing"
)

func TestCleanCacheRules(t *testing.T) {
	got, err := CleanCacheRules([]CacheRule{
		{Pattern: " stable/*.exe ", Cache: "Immutable"},
		{Pattern: "*.dmg", Cache: "year"},
		{Pattern: "stable/latest*", Cache: "auto"},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []CacheRule{{"stable/*.exe", "immutable"}, {"*.dmg", "year"}, {"stable/latest*", ""}}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("rule %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	if got, err := CleanCacheRules(nil); err != nil || got != nil {
		t.Errorf("no rules: %v, %v", got, err)
	}

	bad := map[string][]CacheRule{
		"empty pattern":    {{Pattern: " ", Cache: "year"}},
		"leading slash":    {{Pattern: "/stable/*", Cache: "year"}},
		"bad glob":         {{Pattern: "stable/[", Cache: "year"}},
		"bad policy":       {{Pattern: "*.exe", Cache: "forever"}},
		"too long":         {{Pattern: strings.Repeat("a", 201), Cache: "year"}},
		"control char":     {{Pattern: "a\nb", Cache: "year"}},
		"parent directory": {{Pattern: "../*", Cache: "year"}},
	}
	for name, rules := range bad {
		if _, err := CleanCacheRules(rules); !errors.Is(err, ErrBadCacheRule) {
			t.Errorf("%s: %v, want ErrBadCacheRule", name, err)
		}
	}
	many := make([]CacheRule, MaxCacheRules+1)
	for i := range many {
		many[i] = CacheRule{Pattern: "*.exe", Cache: "year"}
	}
	if _, err := CleanCacheRules(many); !errors.Is(err, ErrBadCacheRule) {
		t.Errorf("%d rules: %v", len(many), err)
	}
}

func TestCacheRuleFor(t *testing.T) {
	m := Meta{CacheRules: []CacheRule{
		{"stable/latest*", "revalidate"},
		{"stable/*", "immutable"},
		{"*.dmg", "year"},
	}}
	for path, want := range map[string]string{
		"stable/Atoo-Mail-Setup-1.0.0.exe": "immutable",
		"stable/latest-mac.zip":            "revalidate", // the first rule that matches decides
		"beta/Atoo-Mail.dmg":               "year",       // no "/": the name, in any folder
		"Atoo-Mail.dmg":                    "year",
		"stable/win/app.exe":               "", // * never crosses a folder
		"other/app.exe":                    "",
	} {
		got, ok := m.CacheRuleFor(path)
		if want == "" {
			if ok {
				t.Errorf("%s: matched %q, want no rule", path, got)
			}
			continue
		}
		if !ok || got != want {
			t.Errorf("%s: %q, %v, want %q", path, got, ok, want)
		}
	}
}
