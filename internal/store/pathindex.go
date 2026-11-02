package store

import (
	"os"
	"path/filepath"
	"strings"
)

// The index of custom domains with a path. A plain host is indexed as it
// always was, domain-index/<host>; an address with a path is
// path-index/<host>/<enc>, where enc is the path without its leading slash and
// with every "/" written as "+" — unambiguous, because "+" is not a character
// a path may hold. addrLink is the one place that decides which, so attach,
// detach, delete and every lookup agree.

const pathIndex = "path-index"

func (s *Store) pathIndexDir() string { return filepath.Join(s.root, pathIndex) }

// addrLink is the index link of a normalized address.
func (s *Store) addrLink(addr string) string {
	host, path := hostOf(addr), pathOf(addr)
	if path == "" {
		return filepath.Join(s.domainIndexDir(), host)
	}
	return filepath.Join(s.pathIndexDir(), host, strings.ReplaceAll(path[1:], "/", "+"))
}

// linkTarget is the relative target of addr's index link: one level deeper
// for a path address, whose link sits in its host's folder.
func linkTarget(addr, viewID string) string {
	if pathOf(addr) == "" {
		return filepath.Join("..", sitesDirName, viewID)
	}
	return filepath.Join("..", "..", sitesDirName, viewID)
}

// ResolveAddress finds the site serving reqPath on host: the attached address
// with the longest path that is reqPath or a parent of it on a segment
// boundary, else the host itself. reqPath is the request's path as Caddy
// serves it — decoded and cleaned by the caller. It costs one stat per
// candidate prefix (at most maxPathSegments) and never lists a directory:
// this is on every request to a custom domain.
func (s *Store) ResolveAddress(host, reqPath string) (*Site, string, error) {
	h, err := normalizeDomain(host)
	if err != nil {
		return nil, "", ErrNotFound
	}
	segs := strings.Split(strings.TrimPrefix(reqPath, "/"), "/")
	// Only a run of valid segments can be an address's path: a request for
	// /a+b/x must never find the link of /a/b.
	n := 0
	for n < len(segs) && n < maxPathSegments && validPath("/"+segs[n]) == nil {
		n++
	}
	for k := n; k > 0; k-- {
		addr := h + "/" + strings.Join(segs[:k], "/")
		link := s.addrLink(addr)
		if !linkExists(link) {
			continue
		}
		if site, err := s.load(link); err == nil {
			return site, addr, nil
		}
	}
	site, err := s.load(s.addrLink(h))
	if err != nil {
		return nil, "", ErrNotFound
	}
	return site, h, nil
}

// HostAddress is one attached address on a host and the site it serves.
type HostAddress struct {
	Addr string
	Site *Site
}

// HostAddresses lists the attached addresses on host — the host itself and
// every path under it — with their sites. Dangling links are left out. One
// stat and one ReadDir: off the serving path, but cheap enough for every
// claim and check.
func (s *Store) HostAddresses(host string) []HostAddress {
	h, err := normalizeDomain(host)
	if err != nil {
		return nil
	}
	var out []HostAddress
	if site, err := s.load(s.addrLink(h)); err == nil {
		out = append(out, HostAddress{Addr: h, Site: site})
	}
	entries, err := os.ReadDir(filepath.Join(s.pathIndexDir(), h))
	if err != nil {
		return out
	}
	for _, e := range entries {
		addr := h + "/" + strings.ReplaceAll(e.Name(), "+", "/")
		if site, err := s.load(filepath.Join(s.pathIndexDir(), h, e.Name())); err == nil {
			out = append(out, HostAddress{Addr: addr, Site: site})
		}
	}
	return out
}

// HostKnown reports whether any address is attached on host, which is what
// Caddy's on-demand TLS asks before issuing its certificate.
func (s *Store) HostKnown(host string) bool { return len(s.HostAddresses(host)) > 0 }

// pathLinks lists every link in the path index, for counting and pruning.
func (s *Store) pathLinks() []string {
	hosts, err := os.ReadDir(s.pathIndexDir())
	if err != nil {
		return nil
	}
	var out []string
	for _, h := range hosts {
		dir := filepath.Join(s.pathIndexDir(), h.Name())
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			out = append(out, filepath.Join(dir, e.Name()))
		}
	}
	return out
}
