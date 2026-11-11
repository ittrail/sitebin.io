package store

import (
	"errors"
	"sync"
	"time"
)

// The account storage cap (Meta.QuotaAccountBytes) bounds what all sites of
// one owner hold together. Summing an account's sites on every write would
// walk up to every site it owns per file, so the store keeps the figures in
// memory instead (accountIndex) and keeps them current through what already
// happens: every SiteUsage notes its answer, every delete forgets the site,
// every ownership change moves it, and MeasureAccounts rebuilds the whole
// index from disk at startup and in every sweep.
//
// The index can be LOW (growth nobody has measured yet: a container's
// database, a parallel upload to another site), never permanently high, and a
// figure that could refuse a write is never older than accountRemeasure. See
// docs/superpowers/specs/2026-11-11-account-storage-cap-design.md.

// accountRemeasure is how old the owner's figures may be when the account
// cap is the one that binds a write: near the cap, the other sites are
// measured again first, so a write is never refused for space that deleting
// files elsewhere freed more than this long ago.
const accountRemeasure = 30 * time.Second

// ErrAccountFull refuses a write because the owner's ACCOUNT storage is full,
// not the site's own cap. It is ErrTooLarge as well (errors.Is), so every
// size refusal that exists still applies; surfaces that can say more check
// for it first.
var ErrAccountFull error = accountFull{}

type accountFull struct{}

func (accountFull) Error() string        { return "account storage limit exceeded" }
func (accountFull) Is(target error) bool { return target == ErrTooLarge }

type indexedSite struct {
	owner string
	bytes int64
	at    time.Time // when it was noted
}

type accountIndex struct {
	mu       sync.Mutex
	sites    map[string]indexedSite // by view id
	measured map[string]time.Time   // by owner: the last re-measure of its sites
	now      func() time.Time       // tests move time
}

func (x *accountIndex) clock() time.Time {
	if x.now != nil {
		return x.now()
	}
	return time.Now()
}

// note records what a site holds. An anonymous or locked site counts for
// nobody, and is dropped.
func (x *accountIndex) note(site *Site, bytes int64) {
	x.mu.Lock()
	defer x.mu.Unlock()
	if site.Meta.OwnerAccountID == "" || site.Meta.IsLocked() {
		delete(x.sites, site.ViewID)
		return
	}
	if x.sites == nil {
		x.sites = map[string]indexedSite{}
	}
	x.sites[site.ViewID] = indexedSite{owner: site.Meta.OwnerAccountID, bytes: bytes, at: x.clock()}
}

func (x *accountIndex) drop(viewID string) {
	x.mu.Lock()
	defer x.mu.Unlock()
	delete(x.sites, viewID)
}

// reown moves a site's figure to its new owner (a transfer or an adoption);
// a site the index does not know yet is noted at its next measure.
func (x *accountIndex) reown(viewID, owner string) {
	x.mu.Lock()
	defer x.mu.Unlock()
	e, ok := x.sites[viewID]
	if !ok {
		return
	}
	if owner == "" {
		delete(x.sites, viewID)
		return
	}
	e.owner = owner
	x.sites[viewID] = e
}

// others sums owner's sites but except, and lists them.
func (x *accountIndex) others(owner, except string) (sum int64, ids []string) {
	x.mu.Lock()
	defer x.mu.Unlock()
	for id, e := range x.sites {
		if e.owner == owner && id != except {
			sum += e.bytes
			ids = append(ids, id)
		}
	}
	return sum, ids
}

// due reports whether owner's figures are older than accountRemeasure, and
// if so marks them measured now, so parallel writes walk once.
func (x *accountIndex) due(owner string) bool {
	x.mu.Lock()
	defer x.mu.Unlock()
	now := x.clock()
	if at, ok := x.measured[owner]; ok && now.Sub(at) < accountRemeasure {
		return false
	}
	if x.measured == nil {
		x.measured = map[string]time.Time{}
	}
	x.measured[owner] = now
	return true
}

// forgetUnseen drops every site noted before since that a full measure did
// not see: removed behind the store's back, or no longer owned.
func (x *accountIndex) forgetUnseen(seen map[string]bool, since time.Time) {
	x.mu.Lock()
	defer x.mu.Unlock()
	for id, e := range x.sites {
		if !seen[id] && e.at.Before(since) {
			delete(x.sites, id)
		}
	}
}

// MeasureAccounts measures every owned site again and forgets the ones that
// are gone. It runs at startup (in the background) and at the start of every
// sweep; a site measured in between is noted by SiteUsage anyway. A site that
// cannot be measured keeps its last figure.
func (s *Store) MeasureAccounts() error {
	start := s.acct.clock()
	sites, err := s.AllSites()
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, site := range sites {
		if site.Meta.OwnerAccountID == "" || site.Meta.IsLocked() {
			s.acct.drop(site.ViewID)
			continue
		}
		seen[site.ViewID] = true
		s.SiteUsage(site) // notes it
	}
	s.acct.forgetUnseen(seen, start)
	return nil
}

// AccountUsage is the account cap stamped on site and what the owner's OTHER
// sites hold; cap 0 means the site has no account cap (anonymous, a plan
// without one, a community build). When the cap leaves the site less than its
// own cap — the account binds — and the owner's figures are older than
// accountRemeasure, the other sites are measured again first.
func (s *Store) AccountUsage(site *Site) (cap, others int64) {
	cap, owner := site.Meta.QuotaAccountBytes, site.Meta.OwnerAccountID
	if cap <= 0 || owner == "" {
		return 0, 0
	}
	others, ids := s.acct.others(owner, site.ViewID)
	if cap-others < s.EffMaxBytes(site) && s.acct.due(owner) {
		for _, id := range ids {
			other, err := s.ByViewID(id)
			if errors.Is(err, ErrNotFound) {
				s.acct.drop(id)
				continue
			}
			if err != nil {
				continue // keeps its last figure
			}
			s.SiteUsage(other) // notes it, under its current owner
		}
		others, _ = s.acct.others(owner, site.ViewID)
	}
	return cap, others
}

// ByteCap is the cap a write to site checks the site's bytes (files plus
// repository, SiteUsage) against: its own cap (EffMaxBytes), or — when the
// owner's account cap leaves it less — what the account has left for it,
// with account true. It is negative when the account is already over, which
// every funnel treats as full. Reports keep showing EffMaxBytes as the site's
// own cap.
func (s *Store) ByteCap(site *Site) (max int64, account bool) {
	max = s.EffMaxBytes(site)
	if cap, others := s.AccountUsage(site); cap > 0 && cap-others < max {
		return cap - others, true
	}
	return max, false
}

// refusal turns a size refusal into the account's when ByteCap said the
// account bound the write: with account true the account left less than the
// site's own cap, so every byte refused was the account's. Any other error
// passes unchanged.
func refusal(err error, account bool) error {
	if account && errors.Is(err, ErrTooLarge) && !errors.Is(err, ErrAccountFull) {
		return ErrAccountFull
	}
	return err
}
