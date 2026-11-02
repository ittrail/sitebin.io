package store

import (
	"slices"
	"time"
)

// Sharing a host between sites of one account.
//
// Every address on a host belongs to one owner account. An address is held
// either by its own proof — its TXT or CNAME, a name in the owner's verified
// zone, an operator domain — or THROUGH THE HOST: attached because another
// address of the same account on that host is held by its own proof. Only an
// address held by its own proof vouches for a host. Were addresses held
// through the host allowed to vouch, two of them would keep each other
// attached after the owner's DNS moved away, and the host would never be
// revoked.
//
// When no address on a host is held by its own proof any more, every
// address held through it runs ONE clock: from the earliest moment a voucher's
// proof was found missing, or from the first check that finds no voucher at
// all. One of them proving itself within revokeAfter vouches for the rest
// again. Without accounts there is no owner to compare, and every address
// proves itself. See
// docs/superpowers/specs/2026-11-02-custom-domains-with-a-path-design.md.

// hostForeign reports whether another owner holds an attached address on
// host. An anonymous site's owner is "", which differs from every account;
// two anonymous sites (an instance without accounts) are not foreign to each
// other — each of their addresses proves itself instead.
func (s *Store) hostForeign(site *Site, host string) bool {
	for _, ha := range s.HostAddresses(host) {
		if ha.Site.ViewID != site.ViewID && ha.Site.Meta.OwnerAccountID != site.Meta.OwnerAccountID {
			return true
		}
	}
	return false
}

// hostVouch is what the other addresses on a host say about addr: Healthy
// when one of them is held by its own proof with no revocation window
// running (By names it); otherwise FailingSince is the earliest window among
// those held by their own proof, nil when there are none.
type hostVouch struct {
	Healthy      bool
	FailingSince *time.Time
	By           string
}

// vouchFor asks the other attached addresses of site's owner on addr's host.
func (s *Store) vouchFor(site *Site, addr string) hostVouch {
	var v hostVouch
	owner := site.Meta.OwnerAccountID
	if owner == "" {
		return v
	}
	for _, ha := range s.HostAddresses(hostOf(addr)) {
		if ha.Addr == addr || ha.Site.Meta.OwnerAccountID != owner {
			continue
		}
		c, hasClaim := claimOf(ha.Site.Meta, ha.Addr)
		if hasClaim && (!c.Verified() || c.ViaHost) {
			continue
		}
		// A verified domain with no claim predates verification and is
		// never re-checked: it holds by its own standing.
		if !hasClaim || c.FailingSince == nil {
			return hostVouch{Healthy: true, By: ha.Addr}
		}
		if v.FailingSince == nil || c.FailingSince.Before(*v.FailingSince) {
			t := *c.FailingSince
			v.FailingSince = &t
		}
	}
	return v
}

// Dependents are the addresses of the site's owner on addr's host that are
// held through the host and would have nothing to hold them if addr went:
// empty unless addr is the host's only address held by its own proof. The
// edit page says so before the owner removes it.
func (s *Store) Dependents(site *Site, addr string) []string {
	owner := site.Meta.OwnerAccountID
	if owner == "" {
		return nil
	}
	if c, ok := claimOf(site.Meta, addr); ok && (!c.Verified() || c.ViaHost || c.FailingSince != nil) {
		return nil // not a voucher; removing it changes nothing for the others
	}
	var deps []string
	for _, ha := range s.HostAddresses(hostOf(addr)) {
		if ha.Addr == addr || ha.Site.Meta.OwnerAccountID != owner {
			continue
		}
		c, ok := claimOf(ha.Site.Meta, ha.Addr)
		if !ok || (c.Verified() && !c.ViaHost && c.FailingSince == nil) {
			return nil // another voucher holds the host
		}
		if c.Verified() && c.ViaHost {
			deps = append(deps, ha.Addr)
		}
	}
	slices.Sort(deps)
	return deps
}

// claimOf is the claim on addr in m.
func claimOf(m Meta, addr string) (DomainClaim, bool) {
	if i := claimIndex(&m, addr); i >= 0 {
		return m.DomainClaims[i], true
	}
	return DomainClaim{}, false
}

// earliest is the earlier of two optional times, nil when both are.
func earliest(a, b *time.Time) *time.Time {
	switch {
	case a == nil:
		return b
	case b == nil || a.Before(*b):
		return a
	default:
		return b
	}
}
