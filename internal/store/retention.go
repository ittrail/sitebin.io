package store

import (
	"errors"
	"time"
)

// The lock retention: a lock keeps a site as evidence for as long as a case
// needs it, and at most this long after the lock unless the operator placed
// an evidence hold (SiteLock.Hold, "case open"). The cleanup sweep purges a
// locked site past it — completely, as the operator's takedown would. See
// the lock-retention addendum of
// docs/superpowers/specs/2026-09-28-site-lock-and-account-suspension.md.

// SetLockRetention sets how long a lock keeps a site (SITEBIN_LOCK_RETENTION_DAYS).
// Zero — the value of a store nobody configured — never purges: keeping a
// site too long is recoverable, purging it is not.
func (s *Store) SetLockRetention(d time.Duration) {
	if d < 0 {
		d = 0
	}
	s.lockRetention = d
}

// LockRetention is the configured retention; zero means locks never expire.
func (s *Store) LockRetention() time.Duration { return s.lockRetention }

// LockRetentionEnds is when lock l's retention r runs out — the lock's date
// plus the retention, whatever holds it — and false for no lock or no
// retention. An evidence hold does not move it; it only stops the purge.
func LockRetentionEnds(l *SiteLock, r time.Duration) (time.Time, bool) {
	if l == nil || r <= 0 {
		return time.Time{}, false
	}
	return l.At.Add(r), true
}

// LockPurgeAt is when the sweep purges a site under lock l with retention r,
// and false when it never does: no lock, an evidence hold, or no retention.
// The one statement of the rule — the sweep, the register and the CLI all
// ask it.
func LockPurgeAt(l *SiteLock, r time.Duration) (time.Time, bool) {
	if l != nil && l.Hold != nil {
		return time.Time{}, false
	}
	return LockRetentionEnds(l, r)
}

// LockRetentionEnds is LockRetentionEnds with the store's retention.
func (s *Store) LockRetentionEnds(l *SiteLock) (time.Time, bool) {
	return LockRetentionEnds(l, s.lockRetention)
}

// LockPurgeAt is LockPurgeAt with the store's retention.
func (s *Store) LockPurgeAt(l *SiteLock) (time.Time, bool) {
	return LockPurgeAt(l, s.lockRetention)
}

// lockPurgeDue reports whether a site under lock l is past its retention at now.
func lockPurgeDue(l *SiteLock, r time.Duration, now time.Time) bool {
	at, ok := LockPurgeAt(l, r)
	return ok && now.After(at)
}

// LockPurgeDue reports whether the sweep should purge a site with meta m at now.
func (s *Store) LockPurgeDue(m Meta, now time.Time) bool {
	return lockPurgeDue(m.Locked, s.lockRetention, now)
}

// LockPurge describes a site the sweep purged, for the operator's log and
// alert. Lock is the lock as it stood; the site no longer exists.
type LockPurge struct {
	ViewID    string
	Owner     string
	Domains   []string
	CreatedAt time.Time
	Lock      SiteLock
	Retention time.Duration
	At        time.Time
}

// SetPurgeHook installs the function every retention purge is reported to
// (the running server's alert mailer). Nil: purges are only logged.
func (s *Store) SetPurgeHook(fn func(LockPurge)) { s.purgeHook = fn }

// errNotDue stops a purge whose site is no longer due when it is re-read.
var errNotDue = errors.New("lock purge no longer due")

// PurgeLocked deletes a locked site whose lock is past the retention at now,
// and reports whether it did. The decision is taken again under the site
// lock, from meta.json: an evidence hold placed, an unlock or a lock lifted
// while the sweep ran — from the register or the CLI in another process —
// wins, and the site is kept. Deletion is complete, like ForceDelete: the
// folder (files, meta.json with its findings, stats, provenance) and the
// edit-index and domain-index links. Stopping a container project first is
// the caller's, as for an expired site.
func (s *Store) PurgeLocked(site *Site, now time.Time) (bool, error) {
	r := s.lockRetention
	var gone Meta
	err := s.deleteChecked(site, func(m *Meta) error {
		if m == nil || !lockPurgeDue(m.Locked, r, now) {
			// An unreadable meta.json is not a lock past its retention:
			// the purge never deletes on a guess.
			return errNotDue
		}
		gone = *m
		return nil
	})
	if errors.Is(err, errNotDue) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if s.purgeHook != nil {
		s.purgeHook(LockPurge{
			ViewID:    site.ViewID,
			Owner:     gone.OwnerAccountID,
			Domains:   gone.CustomDomains,
			CreatedAt: gone.CreatedAt,
			Lock:      *gone.Locked,
			Retention: r,
			At:        now,
		})
	}
	return true, nil
}
