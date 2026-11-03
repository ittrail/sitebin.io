package store

import (
	"errors"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	// MaxTagRunes caps one tag or group name, counted in characters.
	MaxTagRunes = 30
	// MaxSiteTags caps the tags one site carries.
	MaxSiteTags = 10
)

// ErrBadTag is a tag CleanTag refused. Its message is the rule, so every
// surface can show it to the person who typed the tag.
var ErrBadTag = errors.New("a tag is 1 to 30 characters of plain text, without commas")

// ErrTooManyTags is a tag list longer than MaxSiteTags after merging.
var ErrTooManyTags = errors.New("a site carries at most 10 tags")

// TagKey is a tag's identity: tags that differ only in case are the same tag.
// Every comparison goes through it.
func TagKey(s string) string { return strings.ToLower(s) }

// CleanTag validates one tag (or group name). Control characters and commas
// are refused rather than stripped, so a stored tag never silently differs
// from what the owner typed; surrounding space is trimmed and inner runs of
// space collapse to one.
func CleanTag(s string) (string, error) {
	if !utf8.ValidString(s) || strings.IndexFunc(s, unicode.IsControl) >= 0 || strings.Contains(s, ",") {
		return "", ErrBadTag
	}
	s = strings.Join(strings.Fields(s), " ")
	if n := utf8.RuneCountInString(s); n < 1 || n > MaxTagRunes {
		return "", ErrBadTag
	}
	return s, nil
}

// CleanTags cleans every tag, merges case-duplicates (the first spelling
// wins), sorts by TagKey and enforces MaxSiteTags. It is the one definition
// every writer uses. Empty input gives a non-nil empty slice.
func CleanTags(in []string) ([]string, error) {
	out := make([]string, 0, len(in))
	seen := make(map[string]bool, len(in))
	for _, raw := range in {
		t, err := CleanTag(raw)
		if err != nil {
			return nil, err
		}
		if k := TagKey(t); !seen[k] {
			seen[k] = true
			out = append(out, t)
		}
	}
	if len(out) > MaxSiteTags {
		return nil, ErrTooManyTags
	}
	sort.SliceStable(out, func(i, j int) bool { return TagKey(out[i]) < TagKey(out[j]) })
	return out, nil
}
