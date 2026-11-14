package store

import (
	"errors"
	"fmt"
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
//
// ErrNotFound means nothing is there — no address attached, or an index link
// that leads to no site — and says which. Any other error is a site that is
// attached but cannot be read (meta.json unreadable or corrupt, an I/O or
// permission fault): the caller must not answer it as a deleted site. A path
// address whose site cannot be read is that error, never a fall back to a
// shorter path or the host, which would serve another site's files there.
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
		site, err := s.load(link)
		if err == nil {
			return site, addr, nil
		}
		if !errors.Is(err, ErrNotFound) {
			return nil, "", fmt.Errorf("address %s: %w", addr, err)
		}
		// A dangling path link (its site is gone): what a shorter path or
		// the host serves applies, as if the link were not there.
	}
	link := s.addrLink(h)
	site, err := s.load(link)
	switch {
	case err == nil:
		return site, h, nil
	case !errors.Is(err, ErrNotFound):
		return nil, "", fmt.Errorf("address %s: %w", h, err)
	case linkExists(link):
		return nil, "", fmt.Errorf("%w: the index link of %s leads to no site", ErrNotFound, h)
	default:
		return nil, "", fmt.Errorf("%w: nothing is attached at %s", ErrNotFound, h)
	}
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
	out, _ := s.HostAddressesStrict(host)
	return out
}

// HostAddressesStrict is HostAddresses that reports what it could not read:
// an address whose site's meta.json is unreadable, or an index folder that
// cannot be listed, is an error (the addresses that did read are still
// returned). An address whose site is gone is simply not there.
func (s *Store) HostAddressesStrict(host string) ([]HostAddress, error) {
	h, err := normalizeDomain(host)
	if err != nil {
		return nil, nil
	}
	var out []HostAddress
	var failed []error
	note := func(addr string, err error) {
		if !errors.Is(err, ErrNotFound) {
			failed = append(failed, fmt.Errorf("address %s: %w", addr, err))
		}
	}
	if site, err := s.load(s.addrLink(h)); err == nil {
		out = append(out, HostAddress{Addr: h, Site: site})
	} else {
		note(h, err)
	}
	entries, err := os.ReadDir(filepath.Join(s.pathIndexDir(), h))
	if err != nil {
		if !os.IsNotExist(err) {
			failed = append(failed, err)
		}
		return out, errors.Join(failed...)
	}
	for _, e := range entries {
		addr := h + "/" + strings.ReplaceAll(e.Name(), "+", "/")
		if site, err := s.load(filepath.Join(s.pathIndexDir(), h, e.Name())); err == nil {
			out = append(out, HostAddress{Addr: addr, Site: site})
		} else {
			note(addr, err)
		}
	}
	return out, errors.Join(failed...)
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
