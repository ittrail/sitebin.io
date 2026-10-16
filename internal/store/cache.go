package store

import (
	"errors"
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
