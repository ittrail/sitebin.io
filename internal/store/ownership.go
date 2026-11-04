package store

import (
	"errors"
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

// commitOwnership writes m and, when stamped, applies the trust marker the
// stamp decided, still under the site lock.
func (s *Store) commitOwnership(site *Site, m Meta, trusted bool) error {
	m.UpdatedAt = time.Now().UTC()
	if err := writeMeta(site.dir, m); err != nil {
		return err
	}
	site.Meta = m
	return s.SetTrusted(site, trusted)
}

// TransferOwner hands the site from account from to account to, under one
// site lock. It refuses unless from owns the site (ErrNotOwner) and the site
// is not locked (ErrLocked). Each address in detach is detached; Tags and
// Admins are cleared; stamp restamps the new owner's caps (and says whether
// the site is trusted); and a fresh edit password is set, returned once, so
// the old owner's password stops working. A push the old owner started is
// cancelled before the site changes hands.
func (s *Store) TransferOwner(site *Site, from, to string, detach []string, stamp func(*Meta, time.Time) bool) (newPassword string, err error) {
	l := s.lockSite(site.ViewID)
	l.Lock()
	defer l.Unlock()

	m, err := ownershipMeta(site)
	if err != nil {
		return "", err
	}
	if from == "" || m.OwnerAccountID != from {
		return "", ErrNotOwner
	}
	if m.Locked != nil {
		return "", ErrLocked
	}
	for _, a := range detach {
		d, err := normalizeAddress(a)
		if err != nil {
			return "", err
		}
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
	if err := s.commitOwnership(site, m, trusted); err != nil {
		return "", err
	}
	s.stopGitWrite(site.ViewID)
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
	if m.OwnerAccountID != "" || m.Locked != nil || m.Expired(now) {
		return ErrNotAdoptable
	}
	m.OwnerAccountID = to
	trusted := s.Trusted(site)
	if stamp != nil {
		trusted = stamp(&m, now)
	}
	return s.commitOwnership(site, m, trusted)
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
