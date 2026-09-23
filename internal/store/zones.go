package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

// Account zones: a customer's own wildcard zone, proven once.
//
// An account that owns kunde.at points *.kunde.at at the instance and claims
// the zone. One TXT record, _sitebin-zone.kunde.at = sitebin-zone=<token>,
// proves it for the whole subtree: from then on every name under it — the
// apex included — attaches to any of THAT account's sites with no record per
// name, and every other account is refused there. A CNAME cannot prove a zone:
// the wildcard pointing here is exactly what everyone in the zone would share,
// and it says nothing about which account owns it.
//
// Zones never overlap — not each other, not operator zones, not the
// instance's own domains — so at most one zone answers for a name. A pending
// zone reserves nothing; whoever proves it first holds it. A zone does not
// verify while another account holds a verified domain inside it: nobody's
// proven domain is taken away automatically. When a zone is released (its
// proof gone for revokeAfter, removed by its owner, or its account deleted)
// the names that relied on it fall back to ordinary per-name proof, so a
// mistake is recoverable for the length of the ordinary revocation window.
//
// Design: docs/superpowers/specs/2026-09-23-account-zones-design.md.

var (
	// ErrZonePending reports that the zone claim was recorded but is not
	// verified yet: the TXT record is missing, or another account holds a
	// verified domain inside the zone (Zone.Conflicts names them).
	ErrZonePending = errors.New("zone is pending verification")
	// ErrTooManyZones reports that the account's plan allows no more zones.
	ErrTooManyZones = errors.New("zone limit reached")
)

const (
	zoneChallengeLabel  = "_sitebin-zone."
	zoneChallengePrefix = "sitebin-zone="
	zonesDirName        = "zones"
)

// Zone is one zone an account has claimed, verified or not. A VERIFIED zone
// lives in data/zones/<zone>.json, so finding the zone of a name is one stat
// per label; a PENDING claim lives in data/zones/pending/<zone>~<account>.json,
// one per claimant, because a pending claim reserves nothing and two accounts
// may both be trying to prove the same zone.
type Zone struct {
	Zone        string     `json:"zone"`
	AccountID   string     `json:"account_id"`
	Token       string     `json:"token"`
	RequestedAt time.Time  `json:"requested_at"`
	VerifiedAt  *time.Time `json:"verified_at,omitempty"`
	CheckedAt   *time.Time `json:"checked_at,omitempty"`
	// FailingSince is set when a verified zone's TXT record is first found
	// definitively absent, and cleared the moment it is seen again.
	FailingSince *time.Time `json:"failing_since,omitempty"`
	// Conflicts are verified domains of OTHER accounts inside the zone, as of
	// the last check. While there are any the zone stays pending.
	Conflicts []string `json:"conflicts,omitempty"`
}

// Verified reports whether the zone holds.
func (z Zone) Verified() bool { return z.VerifiedAt != nil }

// TXTName is the owner name of the TXT record that proves the zone.
func (z Zone) TXTName() string { return zoneChallengeLabel + z.Zone }

// TXTValue is the value that TXT record must carry.
func (z Zone) TXTValue() string { return zoneChallengePrefix + z.Token }

// ZoneVerifier is implemented by a DomainVerifier that can also prove a zone.
// A verifier without it proves no zone, which is the safe default.
type ZoneVerifier interface {
	VerifyZone(ctx context.Context, zone, token string) (ok bool, err error)
}

// VerifyZone implements ZoneVerifier: every zone is trusted.
func (TrustingVerifier) VerifyZone(context.Context, string, string) (bool, error) {
	return true, nil
}

// VerifyZone implements ZoneVerifier over the TXT record only.
func (v *DNSVerifier) VerifyZone(ctx context.Context, zone, token string) (bool, error) {
	tctx, cancel := context.WithTimeout(ctx, lookupTimeout)
	txts, err := v.lookupTXT(tctx, zoneChallengeLabel+zone)
	cancel()
	if err != nil {
		if isNotFound(err) {
			return false, nil
		}
		return false, err
	}
	want := zoneChallengePrefix + token
	for _, t := range txts {
		if strings.TrimSpace(t) == want {
			return true, nil
		}
	}
	return false, nil
}

// zoneState is the store's zone configuration and the in-memory throttle.
type zoneState struct {
	mu sync.Mutex // serializes every zone file write
	// allowed answers how many zones the account's CURRENT plan permits. nil
	// means the extension offers no zones (the community build).
	allowed func(accountID string) (int, error)
	// perHour throttles newly attached zone names per account; 0 = off.
	perHour int
	rateMu  sync.Mutex
	adds    map[string][]time.Time
}

// SetZoneCheck installs the plan lookup. With none installed, no account can
// claim a zone or add a name through one; verified zones on disk still
// reserve their names, because a reservation is about who proved the zone,
// not about who pays.
func (s *Store) SetZoneCheck(allowed func(accountID string) (int, error)) {
	s.zones.allowed = allowed
}

// SetZoneNamesPerHour sets the per-account throttle on newly attached zone
// names; 0 turns it off. It protects the instance's shared certificate
// budget, not a product limit: the total stays unlimited.
func (s *Store) SetZoneNamesPerHour(n int) { s.zones.perHour = n }

func (s *Store) zonesDir() string { return filepath.Join(s.root, zonesDirName) }

func (s *Store) pendingZonesDir() string { return filepath.Join(s.zonesDir(), "pending") }

// zonePath is where z lives: by name when verified, by name and claimant
// while pending.
func (s *Store) zonePath(z *Zone) string {
	if z.Verified() {
		return filepath.Join(s.zonesDir(), z.Zone+".json")
	}
	return filepath.Join(s.pendingZonesDir(), z.Zone+"~"+z.AccountID+".json")
}

func readZoneFile(path string) (*Zone, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var z Zone
	if err := json.Unmarshal(b, &z); err != nil {
		return nil, fmt.Errorf("zone file %s: %w", filepath.Base(path), err)
	}
	return &z, nil
}

// readZone loads the VERIFIED zone of that name; os.ErrNotExist when there is none.
func (s *Store) readZone(zone string) (*Zone, error) {
	return readZoneFile(filepath.Join(s.zonesDir(), zone+".json"))
}

// readPending loads accountID's pending claim on zone.
func (s *Store) readPending(zone, accountID string) (*Zone, error) {
	return readZoneFile(filepath.Join(s.pendingZonesDir(), zone+"~"+accountID+".json"))
}

// writeZone persists a zone atomically at the path its state implies.
// Callers hold s.zones.mu.
func (s *Store) writeZone(z *Zone) error {
	path := s.zonePath(z)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(z, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// allZones lists every zone, verified and pending. Unreadable files are
// logged and skipped.
func (s *Store) allZones() ([]*Zone, error) {
	var out []*Zone
	for _, dir := range []string{s.zonesDir(), s.pendingZonesDir()} {
		entries, err := os.ReadDir(dir)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
				continue
			}
			z, err := readZoneFile(filepath.Join(dir, e.Name()))
			if err != nil {
				slog.Error("zones: unreadable zone file skipped", "file", e.Name(), "err", err)
				continue
			}
			out = append(out, z)
		}
	}
	return out, nil
}

// Zones returns the account's zones, verified or not, sorted by name.
func (s *Store) Zones(accountID string) ([]Zone, error) {
	all, err := s.allZones()
	if err != nil {
		return nil, err
	}
	var out []Zone
	for _, z := range all {
		if z.AccountID == accountID {
			out = append(out, *z)
		}
	}
	slices.SortFunc(out, func(a, b Zone) int { return strings.Compare(a.Zone, b.Zone) })
	return out, nil
}

// accountZoneFor returns the VERIFIED account zone d lies in (d itself or a
// parent), walking the labels upward: one stat per label, no scan. Zones never
// overlap, so the first hit is the only one.
func (s *Store) accountZoneFor(d string) (*Zone, bool) {
	d = strings.ToLower(d)
	for {
		if strings.Count(d, ".") < 1 {
			return nil, false
		}
		if z, err := s.readZone(d); err == nil && z.Verified() {
			return z, true
		}
		_, rest, ok := strings.Cut(d, ".")
		if !ok {
			return nil, false
		}
		d = rest
	}
}

// ZoneOf reports the verified account zone that holds d for accountID, or "".
// It is what the edit page shows as "via zone".
func (s *Store) ZoneOf(d, accountID string) string {
	if accountID == "" {
		return ""
	}
	if z, ok := s.accountZoneFor(d); ok && z.AccountID == accountID {
		return z.Zone
	}
	return ""
}

// overlaps reports whether a and b are the same name or one lies under the other.
func overlaps(a, b string) bool {
	return a == b || strings.HasSuffix(a, "."+b) || strings.HasSuffix(b, "."+a)
}

// ClaimZone records accountID's claim on zone and tries to verify it at once.
// It returns the zone with nil when verified, with ErrZonePending when the
// record (or a conflict) is still in the way, or an error: ErrBadDomain for a
// name that overlaps something it may not, ErrTooManyZones over the plan.
// Calling again re-checks with the same token.
func (s *Store) ClaimZone(ctx context.Context, accountID, zone string) (Zone, error) {
	if accountID == "" {
		return Zone{}, fmt.Errorf("%w: only an account can hold a zone", ErrBadDomain)
	}
	d, err := normalizeDomain(strings.TrimPrefix(strings.TrimSpace(zone), "*."))
	if err != nil {
		return Zone{}, err
	}
	for _, r := range s.reserved {
		if overlaps(d, r) {
			return Zone{}, fmt.Errorf("%w: %s overlaps this Sitebin instance's own domain", ErrBadDomain, d)
		}
	}
	for _, oz := range s.operatorZones {
		if overlaps(d, oz) {
			return Zone{}, fmt.Errorf("%w: %s overlaps a zone reserved for this instance's operator", ErrBadDomain, d)
		}
	}

	s.zones.mu.Lock()
	all, err := s.allZones()
	if err != nil {
		s.zones.mu.Unlock()
		return Zone{}, err
	}
	var z *Zone
	fresh := false
	mine := 0
	for _, o := range all {
		if o.AccountID == accountID {
			mine++
		}
		if o.Zone == d && o.AccountID == accountID {
			z = o // asking again: re-check with the same token
			continue
		}
		if o.Verified() && overlaps(d, o.Zone) {
			s.zones.mu.Unlock()
			if o.AccountID == accountID {
				return Zone{}, fmt.Errorf("%w: %s overlaps your zone %s; zones cannot be nested", ErrBadDomain, d, o.Zone)
			}
			return Zone{}, fmt.Errorf("%w: %s overlaps a zone another account holds", ErrBadDomain, d)
		}
	}
	if z == nil {
		max, err := s.zonesAllowed(accountID)
		if err != nil {
			s.zones.mu.Unlock()
			return Zone{}, err
		}
		if mine >= max {
			s.zones.mu.Unlock()
			if max == 0 {
				return Zone{}, fmt.Errorf("%w: your plan does not include zones", ErrTooManyZones)
			}
			return Zone{}, fmt.Errorf("%w: your plan allows %d zone(s)", ErrTooManyZones, max)
		}
		z = &Zone{Zone: d, AccountID: accountID, Token: newClaimToken(), RequestedAt: time.Now().UTC()}
		if err := s.writeZone(z); err != nil {
			s.zones.mu.Unlock()
			return Zone{}, err
		}
		fresh = true
	}
	s.zones.mu.Unlock()

	if z.Verified() {
		return *z, nil
	}
	now := time.Now().UTC()
	if fresh && !provesUnseen(s.verifier) {
		// The token was minted a moment ago, so its TXT record cannot exist
		// yet — and asking anyway is worse than useless: resolvers cache the
		// NXDOMAIN for the zone's negative TTL (an hour on Hetzner), so the
		// owner's "check now" a minute later would be answered from that cache
		// and the zone would sit pending for an hour. Show the record and the
		// conflicts, and look it up when asked again or at the next sweep.
		z = s.recordZoneCheck(z.Zone, accountID, false, s.zoneConflicts(z), now)
		if z == nil {
			return Zone{}, ErrNotFound
		}
		return *z, ErrZonePending
	}
	ok, conflicts, verr := s.checkZone(ctx, z)
	if verr != nil {
		return *z, fmt.Errorf("%w: the DNS lookup failed (%v); try again in a moment", ErrZonePending, verr)
	}
	z = s.recordZoneCheck(z.Zone, accountID, ok, conflicts, now)
	if z == nil {
		return Zone{}, fmt.Errorf("%w: %s overlaps a zone another account has just proved", ErrBadDomain, d)
	}
	if !ok {
		return *z, ErrZonePending
	}
	slog.Info("zone verified", "zone", z.Zone, "account", accountID)
	return *z, nil
}

// provesUnseen reports whether the verifier proves without looking at any
// record (SITEBIN_DOMAIN_VERIFICATION=off), where a fresh claim may verify
// at once because there is no DNS cache to poison.
func provesUnseen(v DomainVerifier) bool {
	_, ok := v.(TrustingVerifier)
	return ok
}

// zonesAllowed asks the plan how many zones the account may hold. No check
// installed means none.
func (s *Store) zonesAllowed(accountID string) (int, error) {
	if s.zones.allowed == nil {
		return 0, nil
	}
	return s.zones.allowed(accountID)
}

// checkZone asks DNS for the zone's TXT record and lists the conflicts: other
// accounts' verified domains inside the zone. ok means both are clear.
func (s *Store) checkZone(ctx context.Context, z *Zone) (ok bool, conflicts []string, err error) {
	conflicts = s.zoneConflicts(z)
	zv, isZV := s.verifier.(ZoneVerifier)
	if !isZV {
		return false, conflicts, nil
	}
	proven, err := zv.VerifyZone(ctx, z.Zone, z.Token)
	if err != nil {
		return false, conflicts, err
	}
	return proven && len(conflicts) == 0, conflicts, nil
}

// zoneConflicts lists attached domains inside the zone that belong to a site
// the zone's account does not own.
func (s *Store) zoneConflicts(z *Zone) []string {
	var out []string
	for _, zd := range s.zoneDomains(z.Zone) {
		if zd.Owner != z.AccountID {
			out = append(out, zd.Domain)
		}
	}
	return out
}

// ZoneDomain is one attached domain inside a zone.
type ZoneDomain struct {
	Domain string
	ViewID string
	Owner  string
}

// ZoneDomains lists the attached domains inside zone: one ReadDir of the
// domain index, which is what the account page shows as "names in use".
func (s *Store) ZoneDomains(zone string) []ZoneDomain { return s.zoneDomains(zone) }

func (s *Store) zoneDomains(zone string) []ZoneDomain {
	entries, err := os.ReadDir(s.domainIndexDir())
	if err != nil {
		return nil
	}
	var out []ZoneDomain
	for _, e := range entries {
		d := e.Name()
		if d != zone && !strings.HasSuffix(d, "."+zone) {
			continue
		}
		site, err := s.ByDomain(d)
		if err != nil {
			continue // dangling; the sweep prunes it
		}
		out = append(out, ZoneDomain{Domain: d, ViewID: site.ViewID, Owner: site.Meta.OwnerAccountID})
	}
	return out
}

// recordZoneCheck applies a definitive answer to the zone file and returns the
// zone as written, or nil if it is gone.
func (s *Store) recordZoneCheck(zone, accountID string, ok bool, conflicts []string, now time.Time) *Zone {
	s.zones.mu.Lock()
	defer s.zones.mu.Unlock()
	z, err := s.readPending(zone, accountID)
	if err != nil {
		return nil
	}
	t := now.UTC()
	z.CheckedAt = &t
	z.Conflicts = conflicts
	if !ok {
		if err := s.writeZone(z); err != nil {
			slog.Error("zones: could not record a check", "zone", zone, "err", err)
		}
		return z
	}
	// Promotion. Re-checked under the lock: another claimant may have proved
	// an overlapping zone since this one was looked up, and then this claim
	// can never hold.
	all, err := s.allZones()
	if err != nil {
		return z
	}
	for _, o := range all {
		if o.Verified() && overlaps(o.Zone, z.Zone) {
			os.Remove(s.zonePath(z))
			return nil
		}
	}
	pendingPath := s.zonePath(z)
	z.VerifiedAt = &t
	z.FailingSince = nil
	if err := s.writeZone(z); err != nil {
		slog.Error("zones: could not record a verified zone", "zone", zone, "err", err)
		z.VerifiedAt = nil
		return z
	}
	os.Remove(pendingPath)
	return z
}

// ReleaseZone removes the account's zone. Names that relied on it fall back
// to per-name proof (see ReconcileDomains); nothing is detached on the spot.
func (s *Store) ReleaseZone(accountID, zone string) error {
	d, err := normalizeDomain(strings.TrimPrefix(strings.TrimSpace(zone), "*."))
	if err != nil {
		return err
	}
	s.zones.mu.Lock()
	defer s.zones.mu.Unlock()
	found := false
	if z, err := s.readZone(d); err == nil && z.AccountID == accountID {
		if err := os.Remove(s.zonePath(z)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		found = true
	}
	if z, err := s.readPending(d, accountID); err == nil {
		if err := os.Remove(s.zonePath(z)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		found = true
	}
	if !found {
		return ErrNotFound
	}
	slog.Info("zone released", "zone", d, "account", accountID)
	return nil
}

// ReleaseZones removes every zone of the account: account deletion.
func (s *Store) ReleaseZones(accountID string) error {
	zs, err := s.Zones(accountID)
	if err != nil {
		return err
	}
	var errs []error
	for _, z := range zs {
		if err := s.ReleaseZone(accountID, z.Zone); err != nil && !errors.Is(err, ErrNotFound) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// ReconcileZones is the sweep's pass over zones, run before the per-site
// domain pass so a zone verified now attaches its pending names in the same
// sweep. Pending zones are re-checked, dropped past pendingClaimTTL, or
// dropped when a zone they overlap has verified; verified zones are re-checked
// every reverifyEvery and released only after their proof has been
// definitively absent for revokeAfter. A lookup error changes nothing.
func (s *Store) ReconcileZones(ctx context.Context, now time.Time) {
	all, err := s.allZones()
	if err != nil {
		slog.Error("zones: could not list zones", "err", err)
		return
	}
	for _, z := range all {
		if !z.Verified() {
			if now.Sub(z.RequestedAt) > pendingClaimTTL {
				s.dropZone(z, "a claim that never verified")
				continue
			}
			if s.overlapsVerified(z, all) {
				s.dropZone(z, "a claim overlapping a zone another account proved")
				continue
			}
			ok, conflicts, err := s.checkZone(ctx, z)
			if err != nil {
				slog.Warn("zones: verification lookup failed; will retry", "zone", z.Zone, "err", err)
				continue
			}
			if got := s.recordZoneCheck(z.Zone, z.AccountID, ok, conflicts, now); got != nil && ok {
				z.VerifiedAt = got.VerifiedAt // later zones in this pass see it
				slog.Info("zone verified by the sweep", "zone", z.Zone, "account", z.AccountID)
			}
			continue
		}
		if z.CheckedAt != nil && now.Sub(*z.CheckedAt) < reverifyEvery {
			continue
		}
		zv, isZV := s.verifier.(ZoneVerifier)
		if !isZV {
			continue // nothing can answer; a verified zone is kept
		}
		ok, err := zv.VerifyZone(ctx, z.Zone, z.Token)
		if err != nil {
			slog.Warn("zones: re-verification lookup failed; keeping the zone", "zone", z.Zone, "err", err)
			continue
		}
		s.zones.mu.Lock()
		cur, rerr := s.readZone(z.Zone)
		if rerr != nil || cur.AccountID != z.AccountID {
			s.zones.mu.Unlock()
			continue
		}
		t := now.UTC()
		cur.CheckedAt = &t
		switch {
		case ok:
			cur.FailingSince = nil
		case cur.FailingSince == nil:
			cur.FailingSince = &t
			slog.Warn("zone: proof is gone; the zone is released if it stays gone", "zone", cur.Zone, "account", cur.AccountID, "after", revokeAfter)
		case now.Sub(*cur.FailingSince) >= revokeAfter:
			if err := os.Remove(s.zonePath(cur)); err != nil {
				slog.Error("zone: could not release", "zone", cur.Zone, "err", err)
			} else {
				slog.Warn("zone released; its proof has been gone for longer than the revocation window", "zone", cur.Zone, "account", cur.AccountID)
			}
			s.zones.mu.Unlock()
			continue
		}
		if err := s.writeZone(cur); err != nil {
			slog.Error("zones: could not record a re-check", "zone", cur.Zone, "err", err)
		}
		s.zones.mu.Unlock()
	}
}

// overlapsVerified reports whether a verified zone of ANOTHER account overlaps z.
func (s *Store) overlapsVerified(z *Zone, all []*Zone) bool {
	for _, o := range all {
		if o != z && o.Verified() && o.AccountID != z.AccountID && overlaps(o.Zone, z.Zone) {
			return true
		}
	}
	return false
}

func (s *Store) dropZone(z *Zone, why string) {
	s.zones.mu.Lock()
	defer s.zones.mu.Unlock()
	cur, err := s.readPending(z.Zone, z.AccountID)
	if err != nil {
		return
	}
	if err := os.Remove(s.zonePath(cur)); err == nil {
		slog.Info("zones: dropped "+why, "zone", z.Zone, "account", z.AccountID)
	}
}

// zoneNameAllowed is AddDomain's throttle for a NEW name through the owner's
// zone. It records the add when it allows it.
func (s *Store) zoneNameAllowed(accountID string, now time.Time) error {
	n := s.zones.perHour
	if n <= 0 {
		return nil
	}
	s.zones.rateMu.Lock()
	defer s.zones.rateMu.Unlock()
	if s.zones.adds == nil {
		s.zones.adds = map[string][]time.Time{}
	}
	recent := slices.DeleteFunc(s.zones.adds[accountID], func(t time.Time) bool { return now.Sub(t) >= time.Hour })
	if len(recent) >= n {
		wait := time.Hour - now.Sub(recent[0])
		s.zones.adds[accountID] = recent
		return fmt.Errorf("%w: at most %d new names per hour through a zone; try again in %d minute(s)", ErrTooManyDomain, n, int(wait.Minutes())+1)
	}
	s.zones.adds[accountID] = append(recent, now)
	return nil
}

// refuseForeignZone is AddDomain's gate for zones of either kind. It returns
// viaZone=true when d attaches through the site owner's own account zone:
// no DNS proof, exempt from the per-site cap, throttled per hour.
func (s *Store) refuseForeignZone(site *Site, d string, now time.Time) (viaZone bool, err error) {
	if err := s.refuseOperatorZone(site, d); err != nil {
		return false, err
	}
	z, ok := s.accountZoneFor(d)
	if !ok {
		return false, nil
	}
	owner := site.Meta.OwnerAccountID
	if owner == "" || owner != z.AccountID {
		return false, fmt.Errorf("%w: %s is reserved for the owner of the zone %s", ErrBadDomain, d, z.Zone)
	}
	if slices.Contains(site.Meta.CustomDomains, d) || claimIndex(&site.Meta, d) >= 0 {
		return true, nil // already this site's; a re-check, not a new name
	}
	max, err := s.zonesAllowed(owner)
	if err != nil {
		return false, err
	}
	if max <= 0 {
		return false, fmt.Errorf("%w: your plan no longer includes zones, so new names cannot be added through %s", ErrTooManyDomain, z.Zone)
	}
	if err := s.zoneNameAllowed(owner, now); err != nil {
		return false, err
	}
	return true, nil
}

// countedClaims is the number of the site's domains that count against its
// per-site cap: every claim and claimless domain except names held through
// the owner's own zone, which are unlimited.
func (s *Store) countedClaims(site *Site) int {
	owner := site.Meta.OwnerAccountID
	n := 0
	for _, c := range site.Meta.DomainClaims {
		if s.ZoneOf(c.Domain, owner) == "" {
			n++
		}
	}
	for _, d := range site.Meta.CustomDomains {
		if claimIndex(&site.Meta, d) < 0 && s.ZoneOf(d, owner) == "" {
			n++
		}
	}
	return n
}
