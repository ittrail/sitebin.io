package analytics

import (
	"encoding/json"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"
)

// ClickIDs are recorded by name only, never by value.
var ClickIDs = []string{"gclid", "gbraid", "wbraid", "fbclid", "msclkid", "ttclid", "li_fat_id", "twclid"}

var utmNames = []string{"utm_source", "utm_medium", "utm_campaign", "utm_term", "utm_content"}

var screenRE = regexp.MustCompile(`^\d{2,5}x\d{2,5}$`)

const (
	maxPath      = 300 // bytes
	maxTitle     = 200
	maxReferrer  = 200
	maxQueryVal  = 100
	maxTag       = 50
	maxLanguage  = 35
	maxEventName = 50
	maxDataProps = 20
	maxDataKey   = 50
	maxDataStr   = 200
)

func cutRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	i := 0
	for c := 0; c < n; c++ {
		_, w := utf8.DecodeRuneInString(s[i:])
		i += w
	}
	return s[:i]
}

func cutBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// CleanPath drops the query and fragment and cuts the path at 300 bytes.
func CleanPath(rawPath string) string {
	if i := strings.IndexAny(rawPath, "?#"); i >= 0 {
		rawPath = rawPath[:i]
	}
	return cutBytes(rawPath, maxPath)
}

// KeepQuery keeps the utm_* parameters and the named extras (exact names),
// values cut at 100 runes. Click ids are reported by name only, in
// ClickIDs order.
func KeepQuery(rawQuery string, extra []string) (q map[string]string, clicks []string) {
	vals, _ := url.ParseQuery(rawQuery) // keeps what parsed before an error
	keep := func(name string) {
		if vs, ok := vals[name]; ok && len(vs) > 0 && vs[0] != "" {
			if q == nil {
				q = map[string]string{}
			}
			q[name] = cutRunes(vs[0], maxQueryVal)
		}
	}
	for _, n := range utmNames {
		keep(n)
	}
	for _, n := range extra {
		if !slices.Contains(ClickIDs, n) { // a click id is never kept by value
			keep(n)
		}
	}
	for _, id := range ClickIDs {
		if _, ok := vals[id]; ok {
			clicks = append(clicks, id)
		}
	}
	return q, clicks
}

// ownHost is the host part of an own address (host or host/path), without
// a port, lower-cased.
func ownHost(o string) string {
	if i := strings.IndexByte(o, '/'); i >= 0 {
		o = o[:i]
	}
	if u, err := url.Parse("//" + o); err == nil && u.Hostname() != "" {
		o = u.Hostname()
	}
	return strings.ToLower(strings.TrimSpace(o))
}

// CleanReferrer returns the referrer as host+path (no query, no fragment,
// cut at 200), or "" when it is empty, unparseable or one of the site's own
// addresses. own entries are a host or a host/path address; only the host
// part counts. A non-http(s) referrer is kept as scheme://host.
func CleanReferrer(ref string, own []string) string {
	if ref == "" {
		return ""
	}
	u, err := url.Parse(ref)
	if err != nil || u.Host == "" {
		return ""
	}
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return ""
	}
	for _, o := range own {
		if ownHost(o) == host {
			return ""
		}
	}
	scheme := strings.ToLower(u.Scheme)
	var out string
	if scheme == "http" || scheme == "https" {
		out = u.Host + u.Path
	} else {
		out = scheme + "://" + u.Host
	}
	return cutRunes(out, maxReferrer)
}

// CleanEvent bounds an event: the name must be 1..50 characters; at most 20
// properties (first 20 by sorted key) with keys up to 50; values are
// strings (cut at 200 runes), numbers or booleans, anything else is dropped.
func CleanEvent(name string, data map[string]any) (string, map[string]any, bool) {
	if name == "" || utf8.RuneCountInString(name) > maxEventName {
		return "", nil, false
	}
	keys := make([]string, 0, len(data))
	for k := range data {
		if k != "" && utf8.RuneCountInString(k) <= maxDataKey {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	if len(keys) > maxDataProps {
		keys = keys[:maxDataProps]
	}
	var out map[string]any
	for _, k := range keys {
		var v any
		switch x := data[k].(type) {
		case string:
			v = cutRunes(x, maxDataStr)
		case float64, float32, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, json.Number, bool:
			v = x
		default:
			continue
		}
		if out == nil {
			out = map[string]any{}
		}
		out[k] = v
	}
	return name, out, true
}

// CleanTitle cuts a page title at 200 runes.
func CleanTitle(s string) string { return cutRunes(s, maxTitle) }

// CleanTag cuts a tag at 50 runes.
func CleanTag(s string) string { return cutRunes(s, maxTag) }

// CleanLanguage cuts at 35 characters and returns "" unless the rest looks
// like a language tag (letters, digits and a hyphen).
func CleanLanguage(s string) string {
	s = cutRunes(s, maxLanguage)
	if s == "" {
		return ""
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-':
		default:
			return ""
		}
	}
	return s
}

// CleanScreen accepts only WxH of 2..5 digits each.
func CleanScreen(s string) string {
	if screenRE.MatchString(s) {
		return s
	}
	return ""
}
