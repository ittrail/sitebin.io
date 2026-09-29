package store

import (
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/ittrail/sitebin.io/internal/abuse"
)

// A lock is the operator's evidence hold on a site: it is served to nobody,
// changed by nobody but the operator, and deleted only by the operator's
// explicit takedown (ForceDelete) or — once it is older than the instance's
// lock retention and carries no evidence hold — by the cleanup sweep
// (PurgeLocked, retention.go). Expiry does not touch it. See
// docs/superpowers/specs/2026-09-28-site-lock-and-account-suspension.md and
// its lock-retention addendum.
//
// The store holds the record and guards the one irreversible operation,
// Delete. Every other write surface refuses a locked site at its own gate
// (withEditAuth, WebDAV, FTP, MCP, the dashboard's seam methods), because the
// store's writers are also the operator's and the runtime's.

// Who placed a lock. An operator lock replaces any lock; an account lock —
// one a stack-level suspension places on every site the account owns — is
// applied only to an unlocked site, and an unsuspension lifts only those.
const (
	LockByAdmin   = "admin"
	LockByAccount = "account"
)

// maxLockReason caps the reason. It is shown to the owner and in the
// register, where a paragraph would wreck the row.
const maxLockReason = 200

// ErrLocked refuses a change to a locked site. Surfaces show the owner
// LockedMessage, never the error's own text.
var ErrLocked = errors.New("site is locked by the operator")

// msgLocked is what an owner who runs into a lock is told, on every surface.
const msgLocked = "This site is locked by the operator"

// SiteLock is the lock record in meta.json. Nil (the value every meta.json
// written before locks existed has) means unlocked.
type SiteLock struct {
	// At is when the site was locked: the start of its CONTINUOUS lock, and
	// the lock retention's clock. A lock that replaces a lock keeps it — the
	// operator's "Keep" over a scanner's or a suspension's lock, a re-lock
	// with a new reason — and only an unlock ends it. Otherwise re-locking
	// (scanner → Keep → suspension) would hold a site forever.
	At     time.Time `json:"at"`
	Reason string    `json:"reason,omitempty"`
	By     string    `json:"by"`
	// Hold is the operator's evidence hold ("case open"): while it stands
	// the lock retention does not apply. Nil — every lock written before the
	// retention existed — means none. See SetHold.
	Hold *LockHold `json:"hold,omitempty"`
}

// LockHold is an evidence hold on a lock: a case, investigation or
// proceeding is still open, so the site is kept past the lock retention.
type LockHold struct {
	At time.Time `json:"at"`
	// By is who placed it: the admin's account id (register) or HoldByCLI.
	By string `json:"by,omitempty"`
}

// HoldByCLI marks an evidence hold placed with `sitebin hold`, which runs on
// the host and knows no account.
const HoldByCLI = "cli"

// ErrNotLocked refuses an evidence hold on a site that is not locked: there
// would be nothing for it to hold.
var ErrNotLocked = errors.New("site is not locked")

// IsLocked reports whether the site carries a lock.
func (m Meta) IsLocked() bool { return m.Locked != nil }

// LockedMessage is what an owner who runs into the lock is told: the fixed
// sentence, and the lock's reason when there is one.
func LockedMessage(l *SiteLock) string {
	if l == nil || l.Reason == "" {
		return msgLocked
	}
	return msgLocked + ": " + l.Reason
}

// CleanLockReason makes an operator's free text safe to store and show:
// control characters become spaces, runs of space collapse, and it is cut to
// maxLockReason characters. Unlike a site name it never refuses — a lock must
// not fail because of how its note was typed.
func CleanLockReason(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) > maxLockReason {
		s = string([]rune(s)[:maxLockReason])
	}
	return strings.TrimSpace(s)
}

// SetLock locks the site, or lifts its lock when lock is nil, and reports
// whether anything changed. An account lock (and a scanner's) is applied
// only to an unlocked site: it never replaces the operator's lock, and a
// repeated suspension keeps the first date. An operator lock that replaces a
// lock keeps the lock's date and evidence hold — the site has been locked
// since then, and the retention clock must not restart — and takes only its
// author and reason from the new one. Lifting a lock hands a container
// project back to the runtime as a restart (see unlockMeta), and ends any
// evidence hold with it.
//
// Lifting is the operator's act — the register and `sitebin unlock` are its
// only callers — and it is also their review: the abuse guard's findings
// become reviewed fingerprints, so the same content is not held again.
func (s *Store) SetLock(site *Site, lock *SiteLock) (changed bool, err error) {
	err = s.Update(site, func(m *Meta) error {
		changed = false
		if lock == nil {
			changed = unlockMeta(m)
			if reviewMeta(m) {
				changed = true
			}
			return nil
		}
		if (lock.By == LockByAccount || lock.By == LockByScanner) && m.Locked != nil {
			return nil
		}
		l := *lock
		l.Reason = CleanLockReason(l.Reason)
		l.Hold = nil // only SetHold places one
		if l.At.IsZero() {
			l.At = time.Now().UTC()
		}
		if l.By == "" {
			l.By = LockByAdmin
		}
		if m.Locked != nil {
			l.At, l.Hold = m.Locked.At, m.Locked.Hold
		}
		l.At = l.At.UTC()
		m.Locked = &l
		changed = true
		return nil
	})
	return changed, err
}

// ReleaseLock lifts the site's lock only if it was placed by by, and reports
// whether it did. It is how an unsuspension leaves the operator's own locks
// in place.
//
// An unsuspension does not serve a kit the abuse guard found while the
// account lock stood (the tripwire, `sitebin scan --lock`, a write racing
// the suspension): if an unreviewed blocking finding remains and the site
// would have been held, the account lock becomes a scanner lock instead,
// and released is false. The site stays locked throughout, so the scanner
// lock keeps the date and any evidence hold.
//
// Nor does it end a case: a lock the operator put an evidence hold on is
// the operator's own from then on, as if kept — it becomes an operator lock
// (date and hold unchanged) and released is false. Lifting it would serve
// the site, hand it back to its owner and its expiry, and let the account's
// deletion through while the case is still open.
func (s *Store) ReleaseLock(site *Site, by string) (released bool, err error) {
	err = s.Update(site, func(m *Meta) error {
		released = false
		if m.Locked == nil || m.Locked.By != by {
			return nil
		}
		if m.Locked.Hold != nil {
			m.Locked.By = LockByAdmin
			return nil
		}
		if by == LockByAccount && m.Abuse != nil {
			for _, f := range m.Abuse.Findings {
				if f.Severity == string(abuse.Block) && s.exemption(site, m, false) == "" {
					m.Locked = &SiteLock{At: m.Locked.At, Hold: m.Locked.Hold, By: LockByScanner,
						Reason: CleanLockReason("held for review: " + f.Rule + " in " + f.Path + " (found while the owner was suspended)")}
					return nil
				}
			}
		}
		released = unlockMeta(m)
		return nil
	})
	return released, err
}

// SetHold places the operator's evidence hold on a locked site, or releases
// it when hold is nil, and reports whether anything changed. A site that is
// not locked is ErrNotLocked. A second hold keeps the first one's date and
// author: the case has been open since then.
func (s *Store) SetHold(site *Site, hold *LockHold) (changed bool, err error) {
	err = s.Update(site, func(m *Meta) error {
		changed = false
		if m.Locked == nil {
			return ErrNotLocked
		}
		switch {
		case hold == nil:
			changed = m.Locked.Hold != nil
			m.Locked.Hold = nil
		case m.Locked.Hold == nil:
			h := *hold
			if h.At.IsZero() {
				h.At = time.Now()
			}
			h.At = h.At.UTC()
			m.Locked.Hold = &h
			changed = true
		}
		return nil
	})
	return changed, err
}

// unlockMeta lifts the lock. A container project the lock stopped is started
// again only when its restart sequence moves — the runtime re-applies on a
// changed (compose, restart_seq) and not otherwise — so an enabled project is
// bumped: the site comes back in the state its owner left it.
func unlockMeta(m *Meta) bool {
	if m.Locked == nil {
		return false
	}
	m.Locked = nil
	if m.Mode == ModeContainer && m.Container != nil && m.Container.Enabled {
		m.Container.RestartSeq++
		m.Container.Status = ContainerStarting
		m.Container.Message = ""
	}
	return true
}
