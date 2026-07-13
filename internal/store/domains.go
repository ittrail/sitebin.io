package store

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

var domainLabelRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

// normalizeDomain lowercases and validates a hostname (no scheme, no port).
func normalizeDomain(domain string) (string, error) {
	d := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(domain), "."))
	if d == "" || len(d) > 253 {
		return "", ErrBadDomain
	}
	labels := strings.Split(d, ".")
	if len(labels) < 2 {
		return "", ErrBadDomain
	}
	for _, l := range labels {
		if len(l) > 63 || !domainLabelRe.MatchString(l) {
			return "", ErrBadDomain
		}
	}
	return d, nil
}

// AddDomain validates and indexes a custom domain for the site. Domains on
// the instance's base domain are refused: user content never lives there.
func (s *Store) AddDomain(site *Site, domain string) error {
	d, err := normalizeDomain(domain)
	if err != nil {
		return err
	}
	if d == s.baseDomain || strings.HasSuffix(d, "."+s.baseDomain) {
		return fmt.Errorf("%w: %s is reserved by this Sitebin instance", ErrBadDomain, d)
	}
	if !slices.Contains(site.Meta.CustomDomains, d) && len(site.Meta.CustomDomains) >= maxDomainsPerSite {
		return fmt.Errorf("%w: at most %d custom domains per site", ErrTooManyDomain, maxDomainsPerSite)
	}
	link := filepath.Join(s.domainIndexDir(), d)
	if linkExists(link) {
		if got, err := s.ByDomain(d); err == nil && got.ViewID == site.ViewID {
			return nil // already ours, idempotent
		}
		return ErrDomainTaken
	}
	if err := makeLink(link, filepath.Join("..", sitesDirName, site.ViewID), site.dir); err != nil {
		return fmt.Errorf("index domain: %w", err)
	}
	if err := s.Update(site, func(m *Meta) error {
		if !slices.Contains(m.CustomDomains, d) {
			m.CustomDomains = append(m.CustomDomains, d)
		}
		return nil
	}); err != nil {
		os.Remove(link)
		return err
	}
	return nil
}

// RemoveDomain deletes the domain index link and meta entry.
func (s *Store) RemoveDomain(site *Site, domain string) error {
	d, err := normalizeDomain(domain)
	if err != nil {
		return err
	}
	if !slices.Contains(site.Meta.CustomDomains, d) {
		return ErrNotFound
	}
	if err := os.Remove(filepath.Join(s.domainIndexDir(), d)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return s.Update(site, func(m *Meta) error {
		m.CustomDomains = slices.DeleteFunc(m.CustomDomains, func(x string) bool { return x == d })
		return nil
	})
}

// DanglingIndexLinks returns index entries whose target site no longer
// exists (for the cleanup worker).
func (s *Store) DanglingIndexLinks() ([]string, error) {
	var out []string
	for _, dir := range []string{s.editIndexDir(), s.domainIndexDir()} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			p := filepath.Join(dir, e.Name())
			if linkDangling(p) {
				out = append(out, p)
			}
		}
	}
	return out, nil
}
