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
// The password is hashed before the lock is taken: argon2id takes 64 MiB and
// real time, and nothing else on the site should wait for it.
func (s *Store) TransferOwner(site *Site, from, to string, detach func(m *Meta) ([]string, error), stamp func(*Meta, time.Time) bool) (newPassword string, err error) {
	if to == "" || from == "" {
		return "", ErrNotOwner
	}
	pw := ids.NewEditPassword()
	hash := auth.HashPassword(pw)

	l := s.lockSite(site.ViewID)
	l.Lock()
	defer l.Unlock()

	m, err := ownershipMeta(site)
	if err != nil {
		return "", err
	}
	if m.OwnerAccountID != from {
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
	m.EditPasswordHash = hash
	if err := s.commitOwnership(site, m); err != nil {
		return "", err
	}
	s.stopGitWrite(site.ViewID)
	s.applyMarker(site, trusted)
	return pw, nil
}

// SetEditPasswordIfOwner is SetEditPassword for a request its owner made:
// the new password is hashed first, outside the site lock, and written only
// if, under the lock, meta.json still names owner (never empty) as the
// site's owner (ErrNotOwner otherwise) and the site is not locked
// (ErrLocked). Checked anywhere earlier, a transfer that committed in
// between would leave the old owner holding the new owner's password.
func (s *Store) SetEditPasswordIfOwner(site *Site, owner string) (string, error) {
	if owner == "" {
		return "", ErrNotOwner
	}
	pw := ids.NewEditPassword()
	hash := auth.HashPassword(pw)
	if err := s.Update(site, func(m *Meta) error {
		if m.OwnerAccountID != owner {
			return ErrNotOwner
		}
		if m.Locked != nil {
			return ErrLocked
		}
		m.EditPasswordHash = hash
		return nil
	}); err != nil {
		return "", err
	}
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
// (ErrTooManyAdmins). A locked site is refused (ErrLocked). The write is
// guarded like DeleteIfOwner: under the site lock, meta.json's owner must be
// expectedOwner (never empty), else ErrNotOwner and nothing is written -- a
// transfer that landed after the caller looked must not leave an admin on the
// new owner's site. A running push is cancelled when an admin stops standing.
func (s *Store) UpdateAdmins(site *Site, expectedOwner string, edit func([]SiteAdmin) ([]SiteAdmin, error)) error {
	l := s.lockSite(site.ViewID)
	l.Lock()
	defer l.Unlock()

	m, err := ownershipMeta(site)
	if err != nil {
		return err
	}
	if expectedOwner == "" || m.OwnerAccountID != expectedOwner {
		return ErrNotOwner
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
	before := m
	m.Admins = admins
	m.UpdatedAt = time.Now().UTC()
	if err := writeMeta(site.dir, m); err != nil {
		return err
	}
	site.Meta = m
	// A push an admin started re-checks the standing only once, when it is
	// registered: one running when the admin stops standing (removed, or the
	// end date moved into the past) is cancelled, as a transfer cancels the
	// old owner's. The registry does not know whose push runs, so any
	// running push is; git refuses the admin's next one from meta.
	for _, a := range before.Admins {
		if before.LiveAdmin(a.Account, m.UpdatedAt) && !m.LiveAdmin(a.Account, m.UpdatedAt) {
			s.stopGitWrite(site.ViewID)
			break
		}
	}
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

// SiteOwnership is one site's owner and admin accounts from a single
// meta.json read.
type SiteOwnership struct {
	Owner  string
	Admins []string
}

// SiteOwners maps each site folder's view id to the owner in its meta.json
// ("" for an anonymous site) and the accounts it lists as admins. It reads
// meta.json only. A folder that vanishes
// during the walk is skipped; any other read error is joined into the
// returned error while the sites that did read stay in the map, so the
// caller knows the list is incomplete and never reads an unreadable site as
// a deleted one.
func (s *Store) SiteOwners() (map[string]SiteOwnership, error) {
	entries, err := os.ReadDir(s.sitesDir())
	if err != nil {
		return nil, err
	}
	out := make(map[string]SiteOwnership, len(entries))
	var failed []error
	for _, e := range entries {
		if !e.IsDir() || !ids.ValidID(e.Name()) {
			continue
		}
		site, err := s.ByViewID(e.Name())
		switch {
		case errors.Is(err, ErrNotFound):
		case err != nil:
			failed = append(failed, fmt.Errorf("site %s: %w", e.Name(), err))
		default:
			so := SiteOwnership{Owner: site.Meta.OwnerAccountID}
			for _, a := range site.Meta.Admins {
				so.Admins = append(so.Admins, a.Account)
			}
			out[site.ViewID] = so
		}
	}
	return out, errors.Join(failed...)
}
