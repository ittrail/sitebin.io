package store

import (
	"errors"
	"fmt"
	"path"
	"strings"
)

// A site's cache policy decides the Cache-Control its files are served with.
// The empty string is CacheAuto, the default every site written before
// policies existed has. See
// docs/superpowers/specs/2026-10-16-browser-caching-design.md.
const (
	CacheAuto       = "auto"
	CacheRevalidate = "revalidate"
	CacheHour       = "hour"
	CacheDay        = "day"
	CacheWeek       = "week"
	CacheYear       = "year"
	CacheOff        = "off"
)

// CachePolicies are the policies, in the order the edit page offers them.
var CachePolicies = []string{CacheAuto, CacheRevalidate, CacheHour, CacheDay, CacheWeek, CacheYear, CacheOff}

// ErrBadCachePolicy is a policy CleanCachePolicy refused; its message is the
// rule.
var ErrBadCachePolicy = errors.New("cache must be one of auto, revalidate, hour, day, week, year or off")

// CleanCachePolicy validates a policy and returns it as stored: CacheAuto is
// stored as "". It is the one definition every writer uses.
func CleanCachePolicy(s string) (string, error) {
	p := strings.ToLower(strings.TrimSpace(s))
	switch p {
	case "", CacheAuto:
		return "", nil
	case CacheRevalidate, CacheHour, CacheDay, CacheWeek, CacheYear, CacheOff:
		return p, nil
	}
	return "", ErrBadCachePolicy
}

// CachePolicy is the site's policy by name, CacheAuto when unset.
func (m Meta) CachePolicy() string {
	if m.Cache == "" {
		return CacheAuto
	}
	return m.Cache
}

// CacheImmutable is a cache rule's extra policy: a year, marked immutable.
// Only rules take it — a whole site is never that sure of every file.
const CacheImmutable = "immutable"

// MaxCacheRules bounds a site's rules: authz walks them on every request.
const MaxCacheRules = 20

// maxCacheRulePattern bounds one rule's pattern.
const maxCacheRulePattern = 200

// CacheRule is one of a site's cache rules: the files Pattern matches are
// served with Cache (a policy, or CacheImmutable) instead of the site's
// policy. See docs/superpowers/specs/2026-11-11-release-hosting-design.md.
type CacheRule struct {
	// Pattern is a path.Match glob over the path inside the site
	// ("stable/*.exe"); one without "/" matches the file name in any folder
	// ("*.dmg").
	Pattern string `json:"pattern"`
	// Cache is the policy as stored: "" is auto.
	Cache string `json:"cache"`
}

// ErrBadCacheRule is a rule list CleanCacheRules refused; its message says
// which rule and why.
var ErrBadCacheRule = errors.New("invalid cache rule")

// CleanCacheRules validates a site's rules and returns them as stored, in
// order (the first that matches decides). nil and an empty list are no rules.
// It is the one definition every writer uses.
func CleanCacheRules(rules []CacheRule) ([]CacheRule, error) {
	if len(rules) == 0 {
		return nil, nil
	}
	if len(rules) > MaxCacheRules {
		return nil, fmt.Errorf("%w: at most %d rules", ErrBadCacheRule, MaxCacheRules)
	}
	out := make([]CacheRule, 0, len(rules))
	for i, r := range rules {
		bad := func(why string) error {
			return fmt.Errorf("%w %d (%q): %s", ErrBadCacheRule, i+1, r.Pattern, why)
		}
		p := strings.TrimSpace(r.Pattern)
		switch {
		case p == "":
			return nil, bad("the pattern is empty")
		case len(p) > maxCacheRulePattern:
			return nil, bad(fmt.Sprintf("the pattern is longer than %d characters", maxCacheRulePattern))
		case strings.HasPrefix(p, "/"):
			return nil, bad(`the pattern is a path inside the site, without a leading "/" (stable/*.exe)`)
		case strings.ContainsFunc(p, func(c rune) bool { return c < 0x20 || c == 0x7f }):
			return nil, bad("the pattern holds a control character")
		case p == ".." || strings.HasPrefix(p, "../") || strings.Contains(p, "/../") || strings.HasSuffix(p, "/.."):
			return nil, bad(`the pattern names a parent folder ("..")`)
		}
		if _, err := path.Match(p, ""); err != nil {
			return nil, bad("the pattern is not a valid glob")
		}
		c := strings.ToLower(strings.TrimSpace(r.Cache))
		if c != CacheImmutable {
			var err error
			if c, err = CleanCachePolicy(c); err != nil {
				return nil, bad("cache must be one of auto, revalidate, hour, day, week, year, immutable or off")
			}
		}
		out = append(out, CacheRule{Pattern: p, Cache: c})
	}
	return out, nil
}

// CacheRuleFor is the policy of the first rule matching rel (a path inside
// the site, without a leading "/"), and whether one did.
func (m Meta) CacheRuleFor(rel string) (string, bool) {
	for _, r := range m.CacheRules {
		subject := rel
		if !strings.Contains(r.Pattern, "/") {
			subject = path.Base(rel)
		}
		if ok, _ := path.Match(r.Pattern, subject); ok {
			return r.Cache, true
		}
	}
	return "", false
}
