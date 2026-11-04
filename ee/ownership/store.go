//go:build ee

// Package ownership keeps the extension's records of site transfers and
// shares: offers (a bundle of sites offered to an email), invitations (an
// account made admin of one site) and the index of sites shared with an
// account.
//
// Like the rest of Sitebin the filesystem is the database. Under
// <data>/ownership/:
//
//	offers/<id>.json         one Offer
//	invites/<id>.json        one Invite
//	shares/<account>/<site>  empty marker: the site is shared with the account
//
// Every JSON file is written the way account.json is (a temporary file
// renamed over it), so a crash leaves the old file whole and a stray
// "<id>.json.tmp-*" that no read looks at.
//
// Lookups SCAN the two directories. The records are dozens, written by hand
// and by one person at a time (an account may create 20 a day), and a scan is
// always correct where an index can drift after a crash. Revisit only when a
// directory holds tens of thousands of files.
//
// No password is ever written here: a transfer's new edit passwords are shown
// once in the accept response and kept nowhere.
package ownership

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ittrail/sitebin.io/ee/account"
	"github.com/ittrail/sitebin.io/internal/ids"
)

// The states of an offer or an invitation. An invitation uses open, accepted,
// declined, expired and cancelled; an offer also paused, accepting and ended.
const (
	StateOpen      = "open"
	StatePaused    = "paused"
	StateAccepting = "accepting"
	StateAccepted  = "accepted"
	StateDeclined  = "declined"
	StateExpired   = "expired"
	StateCancelled = "cancelled"
	// StateEnded is an accepted offer of which no site could be moved: the
	// forward completion gave up, and every site stayed with the sender.
	StateEnded = "ended"
)

// IsFinal reports whether a state ends an offer or invitation. Open, paused
// and accepting are not final: the offer still holds its sites.
func IsFinal(state string) bool {
	switch state {
	case StateAccepted, StateDeclined, StateExpired, StateCancelled, StateEnded:
		return true
	}
	return false
}

var (
	// ErrNotFound is a missing (or malformed) offer or invitation id.
	ErrNotFound = errors.New("no such offer or invitation")
	// ErrSiteOffered is a site that another offer which is not final
	// already holds. A site is in at most one such offer.
	ErrSiteOffered = errors.New("a site is already in an open offer")
	// ErrDailyLimit is an account that has already created its limit of
	// offers and invitations on the UTC day of the new record.
	ErrDailyLimit = errors.New("the daily limit of offers and invitations is reached")
)

// Offer is a bundle of sites one account offers to an email address.
// AvailableFrom, when set, is when the offer can first be accepted; AcceptBy
// is the last moment it can be. Done lists the sites already moved while the
// offer is accepting. There is deliberately no password field.
type Offer struct {
	ID            string     `json:"id"`
	Sites         []string   `json:"sites"`
	From          string     `json:"from"`
	ToEmail       string     `json:"to_email"`
	AvailableFrom *time.Time `json:"available_from,omitempty"`
	AcceptBy      time.Time  `json:"accept_by"`
	Created       time.Time  `json:"created"`
	State         string     `json:"state"`
	Done          []string   `json:"done,omitempty"`
	// AcceptedBy is the account that accepted the offer, set together with
	// the state accepting. The forward completion moves the remaining sites
	// to this account, never to whoever holds the address by then.
	AcceptedBy string `json:"accepted_by,omitempty"`
	// NotifiedAt is when the recipient was first mailed (best effort, only to
	// a verified account). nil: not yet, so the background loop mails once the
	// available-from date has passed.
	NotifiedAt *time.Time `json:"notified_at,omitempty"`
}

// Invite makes the recipient's account an admin of one site, until Until
// when it is set.
type Invite struct {
	ID      string     `json:"id"`
	Site    string     `json:"site"`
	From    string     `json:"from"`
	ToEmail string     `json:"to_email"`
	Until   *time.Time `json:"until,omitempty"`
	Created time.Time  `json:"created"`
	State   string     `json:"state"`
}

// Store is the ownership store. Safe for concurrent use: one mutex covers
// every read-modify-write, which is what makes the one-open-offer rule hold.
type Store struct {
	root string
	mu   sync.Mutex
}

// New opens (creating) the store under dataDir/ownership.
func New(dataDir string) (*Store, error) {
	s := &Store{root: filepath.Join(dataDir, "ownership")}
	for _, d := range []string{s.offersDir(), s.invitesDir(), s.sharesDir()} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return nil, fmt.Errorf("init ownership store: %w", err)
		}
	}
	return s, nil
}

func (s *Store) offersDir() string  { return filepath.Join(s.root, "offers") }
func (s *Store) invitesDir() string { return filepath.Join(s.root, "invites") }
func (s *Store) sharesDir() string  { return filepath.Join(s.root, "shares") }

// NormEmail is the stored (and compared) form of an address: trimmed and
// lowercased. Whoever matches an account against an offer compares this.
func NormEmail(e string) string { return strings.ToLower(strings.TrimSpace(e)) }

func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return account.WriteFileAtomic(path, append(b, '\n'))
}

func readJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return ErrNotFound
		}
		return err
	}
	return json.Unmarshal(b, v)
}

// records reads every "<id>.json" in dir into a T. Anything else (a crashed
// temporary file) is ignored; an unreadable record is logged and skipped, so
// one damaged file cannot take the listing down.
func records[T any](dir string) ([]T, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []T
	for _, e := range ents {
		id, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok || !ids.ValidID(id) {
			continue
		}
		var v T
		if err := readJSON(filepath.Join(dir, e.Name()), &v); err != nil {
			if !errors.Is(err, ErrNotFound) {
				slog.Warn("could not read an ownership record; skipping it", "file", e.Name(), "err", err)
			}
			continue
		}
		out = append(out, v)
	}
	return out, nil
}

func sortOffers(os []Offer) {
	sort.SliceStable(os, func(i, j int) bool {
		if !os[i].Created.Equal(os[j].Created) {
			return os[i].Created.After(os[j].Created)
		}
		return os[i].ID < os[j].ID
	})
}

func sortInvites(is []Invite) {
	sort.SliceStable(is, func(i, j int) bool {
		if !is[i].Created.Equal(is[j].Created) {
			return is[i].Created.After(is[j].Created)
		}
		return is[i].ID < is[j].ID
	})
}

// ---- offers ----

// checkSitesFree refuses (ErrSiteOffered) any of sites that an offer other
// than self, still not final, holds. The caller holds s.mu.
func (s *Store) checkSitesFree(self string, sites []string) error {
	all, err := records[Offer](s.offersDir())
	if err != nil {
		return err
	}
	held := map[string]bool{}
	for _, o := range all {
		if o.ID == self || IsFinal(o.State) {
			continue
		}
		for _, id := range o.Sites {
			held[id] = true
		}
	}
	for _, id := range sites {
		if held[id] {
			return fmt.Errorf("%w: %s", ErrSiteOffered, id)
		}
	}
	return nil
}

// CreateOffer stores o under a fresh id. The email is lowercased and trimmed,
// an empty State becomes open, a zero Created becomes now. It refuses a site
// another non-final offer holds with ErrSiteOffered, and writes nothing then.
// The stored offer is returned.
func (s *Store) CreateOffer(o Offer, dailyLimit int) (Offer, error) {
	if len(o.Sites) == 0 {
		return Offer{}, errors.New("an offer needs at least one site")
	}
	o.ID = ids.New()
	o.ToEmail = NormEmail(o.ToEmail)
	if o.State == "" {
		o.State = StateOpen
	}
	if o.Created.IsZero() {
		o.Created = time.Now().UTC()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkDailyLimit(o.From, o.Created, dailyLimit); err != nil {
		return Offer{}, err
	}
	if !IsFinal(o.State) {
		if err := s.checkSitesFree(o.ID, o.Sites); err != nil {
			return Offer{}, err
		}
	}
	if err := writeJSON(filepath.Join(s.offersDir(), o.ID+".json"), o); err != nil {
		return Offer{}, err
	}
	return o, nil
}

// GetOffer reads one offer; a missing or malformed id is ErrNotFound.
func (s *Store) GetOffer(id string) (Offer, error) {
	if !ids.ValidID(id) {
		return Offer{}, ErrNotFound
	}
	var o Offer
	if err := readJSON(filepath.Join(s.offersDir(), id+".json"), &o); err != nil {
		return Offer{}, err
	}
	return o, nil
}

// UpdateOffer reads the offer, applies mutate and writes the result, all
// under the store's mutex. An error from mutate is returned unchanged with
// nothing written, so the callback can refuse (for example a state change
// that is no longer allowed). The id cannot be changed. If the result is not
// final, none of its sites may be in another non-final offer (ErrSiteOffered):
// that is what refuses adding a site that is already offered. The stored
// offer is returned.
func (s *Store) UpdateOffer(id string, mutate func(*Offer) error) (Offer, error) {
	if !ids.ValidID(id) {
		return Offer{}, ErrNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	path := filepath.Join(s.offersDir(), id+".json")
	var o Offer
	if err := readJSON(path, &o); err != nil {
		return Offer{}, err
	}
	if err := mutate(&o); err != nil {
		return Offer{}, err
	}
	o.ID = id
	o.ToEmail = NormEmail(o.ToEmail)
	if !IsFinal(o.State) {
		if err := s.checkSitesFree(id, o.Sites); err != nil {
			return Offer{}, err
		}
	}
	if err := writeJSON(path, o); err != nil {
		return Offer{}, err
	}
	return o, nil
}

func (s *Store) offersWhere(keep func(Offer) bool) ([]Offer, error) {
	s.mu.Lock()
	all, err := records[Offer](s.offersDir())
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	var out []Offer
	for _, o := range all {
		if keep(o) {
			out = append(out, o)
		}
	}
	sortOffers(out)
	return out, nil
}

// OffersBySender lists the offers an account made, newest first.
func (s *Store) OffersBySender(account string) ([]Offer, error) {
	return s.offersWhere(func(o Offer) bool { return o.From == account })
}

// OffersByEmail lists the offers made to an address (case and surrounding
// space ignored), newest first. Like every lookup here it scans the offers;
// an index by email is the next step when they number in the thousands, since
// every dashboard render asks this.
func (s *Store) OffersByEmail(email string) ([]Offer, error) {
	e := NormEmail(email)
	return s.offersWhere(func(o Offer) bool { return o.ToEmail == e })
}

// OffersBySite lists every offer, final or not, that names the site.
func (s *Store) OffersBySite(viewID string) ([]Offer, error) {
	return s.offersWhere(func(o Offer) bool {
		for _, id := range o.Sites {
			if id == viewID {
				return true
			}
		}
		return false
	})
}

// OpenOffers lists the offers that are not final (open, paused, accepting):
// the ones the background loop has to look at.
func (s *Store) OpenOffers() ([]Offer, error) {
	return s.offersWhere(func(o Offer) bool { return !IsFinal(o.State) })
}

// ---- invitations ----

// CreateInvite stores i under a fresh id; the same normalising as CreateOffer.
func (s *Store) CreateInvite(i Invite, dailyLimit int) (Invite, error) {
	i.ID = ids.New()
	i.ToEmail = NormEmail(i.ToEmail)
	if i.State == "" {
		i.State = StateOpen
	}
	if i.Created.IsZero() {
		i.Created = time.Now().UTC()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkDailyLimit(i.From, i.Created, dailyLimit); err != nil {
		return Invite{}, err
	}
	if err := writeJSON(filepath.Join(s.invitesDir(), i.ID+".json"), i); err != nil {
		return Invite{}, err
	}
	return i, nil
}

// GetInvite reads one invitation; a missing or malformed id is ErrNotFound.
func (s *Store) GetInvite(id string) (Invite, error) {
	if !ids.ValidID(id) {
		return Invite{}, ErrNotFound
	}
	var i Invite
	if err := readJSON(filepath.Join(s.invitesDir(), id+".json"), &i); err != nil {
		return Invite{}, err
	}
	return i, nil
}

// UpdateInvite is UpdateOffer for an invitation (no site rule).
func (s *Store) UpdateInvite(id string, mutate func(*Invite) error) (Invite, error) {
	if !ids.ValidID(id) {
		return Invite{}, ErrNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	path := filepath.Join(s.invitesDir(), id+".json")
	var i Invite
	if err := readJSON(path, &i); err != nil {
		return Invite{}, err
	}
	if err := mutate(&i); err != nil {
		return Invite{}, err
	}
	i.ID = id
	i.ToEmail = NormEmail(i.ToEmail)
	if err := writeJSON(path, i); err != nil {
		return Invite{}, err
	}
	return i, nil
}

func (s *Store) invitesWhere(keep func(Invite) bool) ([]Invite, error) {
	s.mu.Lock()
	all, err := records[Invite](s.invitesDir())
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	var out []Invite
	for _, i := range all {
		if keep(i) {
			out = append(out, i)
		}
	}
	sortInvites(out)
	return out, nil
}

// InvitesBySender lists the invitations an account made, newest first.
func (s *Store) InvitesBySender(account string) ([]Invite, error) {
	return s.invitesWhere(func(i Invite) bool { return i.From == account })
}

// InvitesByEmail lists the invitations made to an address, newest first.
func (s *Store) InvitesByEmail(email string) ([]Invite, error) {
	e := NormEmail(email)
	return s.invitesWhere(func(i Invite) bool { return i.ToEmail == e })
}

// InvitesBySite lists every invitation for the site.
func (s *Store) InvitesBySite(viewID string) ([]Invite, error) {
	return s.invitesWhere(func(i Invite) bool { return i.Site == viewID })
}

// OpenInvites lists the invitations that are not final.
func (s *Store) OpenInvites() ([]Invite, error) {
	return s.invitesWhere(func(i Invite) bool { return !IsFinal(i.State) })
}

// ---- the daily limit ----

// DailyCount counts the offers and invitations the account CREATED on the
// UTC day of day. Cancelling one later does not take it back. A listing
// error is logged and counts what could be read.
func (s *Store) DailyCount(accountID string, day time.Time) int {
	start := day.UTC().Truncate(24 * time.Hour)
	end := start.Add(24 * time.Hour)
	in := func(t time.Time) bool { t = t.UTC(); return !t.Before(start) && t.Before(end) }
	n := 0
	offers, err := s.OffersBySender(accountID)
	if err != nil {
		slog.Error("could not count an account's offers", "account", accountID, "err", err)
	}
	for _, o := range offers {
		if in(o.Created) {
			n++
		}
	}
	invites, err := s.InvitesBySender(accountID)
	if err != nil {
		slog.Error("could not count an account's invitations", "account", accountID, "err", err)
	}
	for _, i := range invites {
		if in(i.Created) {
			n++
		}
	}
	return n
}

// strictCount counts the records in dir that match, reading every one. Unlike
// the listings it does NOT skip a record it cannot read: a count that missed
// one could let a request past a limit, so any read error is returned.
func strictCount[T any](dir string, match func(T) bool) (int, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, e := range ents {
		id, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok || !ids.ValidID(id) {
			continue
		}
		var v T
		if err := readJSON(filepath.Join(dir, e.Name()), &v); err != nil {
			if errors.Is(err, ErrNotFound) {
				continue // vanished between the listing and the read
			}
			return 0, err
		}
		if match(v) {
			n++
		}
	}
	return n, nil
}

// checkDailyLimit refuses (ErrDailyLimit) a new record when the account has
// already created limit offers and invitations on the UTC day of at. It runs
// under s.mu, in the same locked section as the write, so parallel creates
// cannot all pass. limit <= 0 means no limit. A read error is an error, never
// a partial count. The caller holds s.mu.
func (s *Store) checkDailyLimit(accountID string, at time.Time, limit int) error {
	if limit <= 0 {
		return nil
	}
	start := at.UTC().Truncate(24 * time.Hour)
	end := start.Add(24 * time.Hour)
	in := func(t time.Time) bool { t = t.UTC(); return !t.Before(start) && t.Before(end) }
	no, err := strictCount(s.offersDir(), func(o Offer) bool { return o.From == accountID && in(o.Created) })
	if err != nil {
		return fmt.Errorf("could not count the offers: %w", err)
	}
	ni, err := strictCount(s.invitesDir(), func(i Invite) bool { return i.From == accountID && in(i.Created) })
	if err != nil {
		return fmt.Errorf("could not count the invitations: %w", err)
	}
	if no+ni >= limit {
		return ErrDailyLimit
	}
	return nil
}

// ---- the share index ----

func (s *Store) shareDir(accountID string) string {
	return filepath.Join(s.sharesDir(), accountID)
}

// LinkShare records that the site is shared with the account (an empty
// marker, like account.LinkSite). Idempotent.
func (s *Store) LinkShare(accountID, viewID string) error {
	if !ids.ValidID(accountID) || !ids.ValidID(viewID) {
		return errors.New("invalid account or site id")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(s.shareDir(accountID), 0o755); err != nil {
		return err
	}
	path := filepath.Join(s.shareDir(accountID), viewID)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	// The marker's age is how the background reconciliation tells a stray
	// one from one whose admin entry is being written right now, so a
	// re-link of an old marker makes it young again.
	now := time.Now()
	return os.Chtimes(path, now, now)
}

// UnlinkShare removes the marker; no error if it is already gone.
func (s *Store) UnlinkShare(accountID, viewID string) error {
	if !ids.ValidID(accountID) || !ids.ValidID(viewID) {
		return errors.New("invalid account or site id")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	err := os.Remove(filepath.Join(s.shareDir(accountID), viewID))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// SharedSites lists the view ids with a marker for the account, sorted. A
// marker proves nothing by itself: the site's meta.json is the truth.
func (s *Store) SharedSites(accountID string) ([]string, error) {
	if !ids.ValidID(accountID) {
		return nil, nil
	}
	ents, err := os.ReadDir(s.shareDir(accountID))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []string
	for _, e := range ents {
		if !e.IsDir() && ids.ValidID(e.Name()) {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out, nil
}

// ShareMarker is a share marker and when it was last linked.
type ShareMarker struct {
	Site string
	At   time.Time
}

// ShareMarkers lists the account's markers with their ages, sorted by site.
// Like SharedSites, a marker proves nothing by itself.
func (s *Store) ShareMarkers(accountID string) ([]ShareMarker, error) {
	if !ids.ValidID(accountID) {
		return nil, nil
	}
	ents, err := os.ReadDir(s.shareDir(accountID))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []ShareMarker
	for _, e := range ents {
		if e.IsDir() || !ids.ValidID(e.Name()) {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		out = append(out, ShareMarker{Site: e.Name(), At: fi.ModTime()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Site < out[j].Site })
	return out, nil
}

// SharingAccounts lists the accounts that have a share-marker folder, sorted:
// the accounts a walk over the shares has to visit.
func (s *Store) SharingAccounts() ([]string, error) {
	ents, err := os.ReadDir(s.sharesDir())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []string
	for _, e := range ents {
		if e.IsDir() && ids.ValidID(e.Name()) {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out, nil
}

// RemoveShares removes the account's whole share-marker folder: the account
// is being erased, and the folder is its data. A folder that is not there is
// no error.
func (s *Store) RemoveShares(accountID string) error {
	if !ids.ValidID(accountID) {
		return errors.New("invalid account id")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.RemoveAll(s.shareDir(accountID)); err != nil {
		return err
	}
	return nil
}
