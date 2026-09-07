package store

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
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

// AddDomain claims a custom domain for the site and attaches it if its DNS
// proves control (see domainverify.go). Domains on the instance's own domains
// are refused: user content never lives there.
//
// It returns nil when the domain is attached (now, or already), and
// ErrDomainPending when the claim is recorded but the proof is not in place —
// the claim, with the records to create, is then in site.Meta.DomainClaims.
// Calling again re-checks with the same token.
func (s *Store) AddDomain(site *Site, domain string) error {
	d, err := normalizeDomain(domain)
	if err != nil {
		return err
	}
	for _, r := range s.reserved {
		if d == r || strings.HasSuffix(d, "."+r) {
			return fmt.Errorf("%w: %s is reserved by this Sitebin instance", ErrBadDomain, d)
		}
	}
	if slices.Contains(site.Meta.CustomDomains, d) {
		if got, err := s.ByDomain(d); err == nil && got.ViewID == site.ViewID {
			return nil // already ours, idempotent
		}
	}
	// Attached elsewhere: someone else proved control. A pending claim on
	// another site is not that, and is not a reason to refuse.
	if linkExists(filepath.Join(s.domainIndexDir(), d)) {
		if got, err := s.ByDomain(d); err == nil && got.ViewID != site.ViewID {
			return ErrDomainTaken
		}
	}
	// The per-site cap counts verified AND pending, so claims cannot be
	// sprayed: the owner's tier value if stamped, else the instance default.
	cap := maxDomainsPerSite
	if site.Meta.QuotaDomains != nil {
		cap = *site.Meta.QuotaDomains
	}
	if claimIndex(&site.Meta, d) < 0 && len(site.Meta.DomainClaims)+claimlessDomains(&site.Meta) >= cap {
		return fmt.Errorf("%w: at most %d custom domain(s) allowed for this site", ErrTooManyDomain, cap)
	}

	now := time.Now().UTC()
	var claim DomainClaim
	err = s.Update(site, func(m *Meta) error {
		if i := claimIndex(m, d); i >= 0 {
			claim = m.DomainClaims[i]
			return nil
		}
		claim = DomainClaim{Domain: d, Token: newClaimToken(), RequestedAt: now}
		m.DomainClaims = append(m.DomainClaims, claim)
		return nil
	})
	if err != nil {
		return err
	}
	if claim.Verified() {
		// Verified in meta but not indexed (a lost link): repair the index.
		return s.attach(site, d, now)
	}
	ok, verr := s.verify(context.Background(), site, claim)
	if verr != nil {
		return fmt.Errorf("%w: the DNS lookup failed (%v); try again in a moment", ErrDomainPending, verr)
	}
	if !ok {
		s.stampCheck(site, d, now)
		return ErrDomainPending
	}
	return s.attach(site, d, now)
}

// claimlessDomains counts verified domains that predate claim records, so the
// cap still sees them.
func claimlessDomains(m *Meta) int {
	n := 0
	for _, d := range m.CustomDomains {
		if claimIndex(m, d) < 0 {
			n++
		}
	}
	return n
}

// attach indexes a domain for the site and records it as verified. It is the
// ONLY place a domain enters the index and CustomDomains.
func (s *Store) attach(site *Site, d string, now time.Time) error {
	link := filepath.Join(s.domainIndexDir(), d)
	if linkExists(link) {
		got, err := s.ByDomain(d)
		if err != nil || got.ViewID != site.ViewID {
			if err == nil {
				return ErrDomainTaken
			}
			// dangling: the sweep would prune it; take it now
			os.Remove(link)
		}
	}
	if !linkExists(link) {
		if err := makeLink(link, filepath.Join("..", sitesDirName, site.ViewID), site.dir); err != nil {
			return fmt.Errorf("index domain: %w", err)
		}
	}
	err := s.Update(site, func(m *Meta) error {
		if !slices.Contains(m.CustomDomains, d) {
			m.CustomDomains = append(m.CustomDomains, d)
		}
		t := now.UTC()
		if i := claimIndex(m, d); i >= 0 {
			c := &m.DomainClaims[i]
			if c.VerifiedAt == nil {
				c.VerifiedAt = &t
			}
			c.CheckedAt = &t
			c.FailingSince = nil
		} else {
			m.DomainClaims = append(m.DomainClaims, DomainClaim{Domain: d, Token: newClaimToken(), RequestedAt: t, VerifiedAt: &t, CheckedAt: &t})
		}
		return nil
	})
	if err != nil {
		os.Remove(link)
		return err
	}
	return nil
}

// removeLink drops the index entry for d, tolerating its absence.
func (s *Store) removeLink(d string) error {
	if err := os.Remove(filepath.Join(s.domainIndexDir(), d)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// RemoveDomain detaches a verified domain, or forgets a pending claim.
func (s *Store) RemoveDomain(site *Site, domain string) error {
	d, err := normalizeDomain(domain)
	if err != nil {
		return err
	}
	if !slices.Contains(site.Meta.CustomDomains, d) && claimIndex(&site.Meta, d) < 0 {
		return ErrNotFound
	}
	if err := s.removeLink(d); err != nil {
		return err
	}
	return s.Update(site, func(m *Meta) error {
		m.CustomDomains = slices.DeleteFunc(m.CustomDomains, func(x string) bool { return x == d })
		m.DomainClaims = slices.DeleteFunc(m.DomainClaims, func(c DomainClaim) bool { return c.Domain == d })
		return nil
	})
}

// PendingDomains returns the site's claims that are not attached yet.
func (s *Site) PendingDomains() []DomainClaim {
	var out []DomainClaim
	for _, c := range s.Meta.DomainClaims {
		if !c.Verified() {
			out = append(out, c)
		}
	}
	return out
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
