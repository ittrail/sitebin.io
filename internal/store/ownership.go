package store

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/ittrail/sitebin.io/internal/auth"
	"github.com/ittrail/sitebin.io/internal/ids"
)

var (
	// ErrNotOwner: the account named as the site's current owner is not it.
	ErrNotOwner = errors.New("account does not own the site")
	// ErrNotAdoptable: only an anonymous, unlocked, unexpired site can be
	// adopted.
	ErrNotAdoptable = errors.New("site cannot be adopted")
	// ErrTooManyAdmins: a site has at most MaxAdmins admins.
	ErrTooManyAdmins = errors.New("too many admins")
)

// MaxAdmins is the most admins a site can have.
const MaxAdmins = 10

// LiveAdmin reports whether account is one of the site's admins at now:
// listed in Admins, with no end date or one still ahead. It is only the
// meta.json half of an admin's standing; the gate (httpapi.accountStanding)
// adds the plan half, and nothing else may treat a listed account as an
// admin. An anonymous site has no admins, whatever its meta says.
func (m Meta) LiveAdmin(account string, now time.Time) bool {
	if account == "" || m.OwnerAccountID == "" || account == m.OwnerAccountID {
		return false
	}
	for _, a := range m.Admins {
		if a.Account == account {
			return a.Until == nil || now.Before(*a.Until)
		}
	}
	return false
}

// ownershipMeta reads meta.json for an ownership change; the caller holds
// the site lock.
func ownershipMeta(site *Site) (Meta, error) {
	m, err := readMeta(site.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return m, ErrNotFound
		}
		return m, err
	}
	return m, nil
}

// commitOwnership writes m, still under the site lock. The caller applies
// the trust marker (applyMarker) after whatever must follow the write at once.
func (s *Store) commitOwnership(site *Site, m Meta) error {
	m.UpdatedAt = time.Now().UTC()
	if err := writeMeta(site.dir, m); err != nil {
		return err
	}
	site.Meta = m
	return nil
}

// applyMarker applies the trust marker the stamp decided. The ownership
// change is already written, so a failure is logged, not returned: the
// sweep's reconcileTrust corrects the marker later, and failing the call
// would lose the new password that meta already holds the hash of.
func (s *Store) applyMarker(site *Site, trusted bool) {
	if err := s.SetTrusted(site, trusted); err != nil {
		slog.Warn("ownership: could not apply the trust marker; the sweep will correct it", "site", site.ViewID, "err", err)
	}
}

// TransferOwner hands the site from account from to account to, under one
// site lock. It refuses unless from owns the site (ErrNotOwner) and the site
// is not locked (ErrLocked), and refuses an empty recipient (ErrNotOwner: it
// would leave an anonymous, adoptable site). detach is asked, on the fresh
// meta under the lock, which addresses go (nil: none); an error from it
// refuses the call with nothing written. Every address it names is
// normalized first, so a bad one refuses the whole call with nothing
// detached; then each is detached, which prunes its redirects and targets.
// Tags and Admins are cleared; stamp restamps the new owner's caps (and says
// whether the site is trusted); and a fresh edit password is set, returned
// once, so the old owner's password stops working. A push the old owner
// started is cancelled right after the write, still under the lock. A
// trust-marker failure is logged, not returned (the sweep corrects the
// marker), so the new password always reaches the caller once meta holds it.
func (s *Store) TransferOwner(site *Site, from, to string, detach func(m *Meta) ([]string, error), stamp func(*Meta, time.Time) bool) (newPassword string, err error) {
	l := s.lockSite(site.ViewID)
	l.Lock()
	defer l.Unlock()

	m, err := ownershipMeta(site)
	if err != nil {
		return "", err
	}
	if to == "" || from == "" || m.OwnerAccountID != from {
		return "", ErrNotOwner
	}
	if m.Locked != nil {
		return "", ErrLocked
	}
	var list []string
	if detach != nil {
		if list, err = detach(&m); err != nil {
			return "", err
		}
	}
	addrs := make([]string, 0, len(list))
	for _, a := range list {
		d, err := normalizeAddress(a)
		if err != nil {
			return "", err
		}
		addrs = append(addrs, d)
	}
	for _, d := range addrs {
		if err := s.removeDomainLocked(site, &m, d); err != nil && !errors.Is(err, ErrNotFound) {
			return "", err
		}
	}
	m.OwnerAccountID = to
	m.Tags = nil
	m.Admins = nil
	now := time.Now().UTC()
	trusted := s.Trusted(site)
	if stamp != nil {
		trusted = stamp(&m, now)
	}
	pw := ids.NewEditPassword()
	m.EditPasswordHash = auth.HashPassword(pw)
	if err := s.commitOwnership(site, m); err != nil {
		return "", err
	}
	s.stopGitWrite(site.ViewID)
	s.applyMarker(site, trusted)
	return pw, nil
}

// AdoptOwner gives an anonymous drop to account to. The site must be
// ownerless, not locked and not expired (ErrNotAdoptable otherwise). stamp
// restamps the caps and says whether the site is trusted.
func (s *Store) AdoptOwner(site *Site, to string, stamp func(*Meta, time.Time) bool) error {
	l := s.lockSite(site.ViewID)
	l.Lock()
	defer l.Unlock()

	m, err := ownershipMeta(site)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	if to == "" || m.OwnerAccountID != "" || m.Locked != nil || m.Expired(now) {
		return ErrNotAdoptable
	}
	m.OwnerAccountID = to
	trusted := s.Trusted(site)
	if stamp != nil {
		trusted = stamp(&m, now)
	}
	if err := s.commitOwnership(site, m); err != nil {
		return err
	}
	s.applyMarker(site, trusted)
	return nil
}

// UpdateAdmins edits the site's admins under the site lock. The result is
// de-duplicated by account (first wins) and capped at MaxAdmins
// (ErrTooManyAdmins). A locked site is refused (ErrLocked).
func (s *Store) UpdateAdmins(site *Site, edit func([]SiteAdmin) ([]SiteAdmin, error)) error {
	l := s.lockSite(site.ViewID)
	l.Lock()
	defer l.Unlock()

	m, err := ownershipMeta(site)
	if err != nil {
		return err
	}
	if m.Locked != nil {
		return ErrLocked
	}
	in := append([]SiteAdmin(nil), m.Admins...)
	out, err := edit(in)
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	var admins []SiteAdmin
	for _, a := range out {
		if a.Account == "" || seen[a.Account] {
			continue
		}
		seen[a.Account] = true
		admins = append(admins, a)
	}
	if len(admins) > MaxAdmins {
		return ErrTooManyAdmins
	}
	m.Admins = admins
	m.UpdatedAt = time.Now().UTC()
	if err := writeMeta(site.dir, m); err != nil {
		return err
	}
	site.Meta = m
	return nil
}

// AccountBoundAddresses lists the site's verified addresses whose proof is
// the account's rather than their own DNS: a name in one of the account's
// verified zones, a name in an operator zone (SITEBIN_OPERATOR_DOMAINS), and
// an address held only through its host (ViaHost). They would not survive
// the site leaving the account, so a transfer detaches them. A zone file that
// cannot be read is an error, never "not in a zone": the caller acts on the
// answer.
func (s *Store) AccountBoundAddresses(m *Meta, account string) ([]string, error) {
	var out []string
	for _, d := range m.CustomDomains {
		bound := s.InOperatorZone(d)
		for _, c := range m.DomainClaims {
			if c.Domain == d && c.ViaHost {
				bound = true
			}
		}
		if !bound && account != "" {
			z, ok, err := s.accountZoneForStrict(d)
			if err != nil {
				return nil, fmt.Errorf("the zone of %s: %w", d, err)
			}
			bound = ok && z.AccountID == account
		}
		if bound {
			out = append(out, d)
		}
	}
	return out, nil
}
