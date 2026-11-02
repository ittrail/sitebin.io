package store

import (
	"fmt"
	"strings"
)

// A custom domain may carry a path: morgenfertig.at/demos/kunde123 serves a
// site under that path of the host while other sites of the same account
// serve the rest. The address — host, or host plus path — is the custom
// domain's name everywhere: in meta.json, in redirects and targets, in the
// API and over MCP. A plain host is the address with an empty path. See
// docs/superpowers/specs/2026-11-02-custom-domains-with-a-path-design.md.

const (
	// maxPathSegments bounds how deep an address's path may go, and so how
	// many prefixes a request's lookup ever tries.
	maxPathSegments = 8
	// maxPathLen bounds an address's path, leading slash included.
	maxPathLen = 200
)

// normalizeAddress validates a custom domain with an optional path and
// returns it in the one form everything stores: the host lowercased (as
// normalizeDomain does), the path exactly as given. A path is refused rather
// than rewritten when it is not already in that form — lowercasing it would
// make it a different path from the one the site's own links use.
func normalizeAddress(s string) (string, error) {
	s = strings.TrimSpace(s)
	host, path, hasPath := strings.Cut(s, "/")
	h, err := normalizeDomain(host)
	if err != nil {
		return "", err
	}
	if !hasPath {
		return h, nil
	}
	path = "/" + path
	if err := validPath(path); err != nil {
		return "", fmt.Errorf("%w: %s%s: %v", ErrBadDomain, h, path, err)
	}
	return h + path, nil
}

// validPath checks an address's path: segments of lowercase letters, digits
// and - _ . ~, none of them empty, "." or "..", and none of them _sitebin —
// <path>/_sitebin/ is Sitebin's own, as /_sitebin/ is at the root.
func validPath(path string) error {
	if len(path) > maxPathLen {
		return fmt.Errorf("a path is at most %d characters", maxPathLen)
	}
	segs := strings.Split(path[1:], "/")
	if len(segs) > maxPathSegments {
		return fmt.Errorf("a path has at most %d segments", maxPathSegments)
	}
	for _, seg := range segs {
		switch seg {
		case "":
			return fmt.Errorf("a path has no empty segment and no trailing slash")
		case ".", "..":
			return fmt.Errorf("a path has no . or .. segment")
		case "_sitebin":
			return fmt.Errorf("_sitebin is reserved for Sitebin")
		}
		for _, r := range seg {
			if !pathRune(r) {
				return fmt.Errorf("paths use lowercase letters, digits, - _ . and ~ only")
			}
		}
	}
	return nil
}

func pathRune(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.' || r == '~'
}

// hostOf is a normalized address's host.
func hostOf(addr string) string {
	h, _, _ := strings.Cut(addr, "/")
	return h
}

// pathOf is a normalized address's path, "" for a plain host.
func pathOf(addr string) string {
	if i := strings.IndexByte(addr, '/'); i >= 0 {
		return addr[i:]
	}
	return ""
}

// HostOf is hostOf for the packages that hold addresses from the store.
func HostOf(addr string) string { return hostOf(addr) }

// PathOf is pathOf for the packages that hold addresses from the store.
func PathOf(addr string) string { return pathOf(addr) }
