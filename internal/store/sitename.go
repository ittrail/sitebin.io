package store

import (
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"
)

// MaxSiteNameRunes caps a site name — the same cap as token and form names.
const MaxSiteNameRunes = 60

// ErrBadSiteName is a name CleanSiteName refused. Its message is the rule, so
// every surface can show it to the person who typed the name.
var ErrBadSiteName = errors.New("a site name is at most 60 characters of plain text")

// CleanSiteName validates a site name. The empty string is valid and means
// "unnamed". Control characters are refused rather than stripped: a stripped
// name would silently differ from what the owner typed. It is the one
// definition every writer — the JSON API, MCP, the account dashboard — uses.
func CleanSiteName(s string) (string, error) {
	if !utf8.ValidString(s) || strings.IndexFunc(s, unicode.IsControl) >= 0 {
		return "", ErrBadSiteName
	}
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) > MaxSiteNameRunes {
		return "", ErrBadSiteName
	}
	return s, nil
}
