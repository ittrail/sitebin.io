package store

import (
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// A lock is the operator's evidence hold on a site: it is served to nobody,
// changed by nobody but the operator, and never deleted except by the
// operator's explicit takedown (ForceDelete). Expiry does not touch it — the
// cleanup sweep skips a locked site entirely. See
// docs/superpowers/specs/2026-09-28-site-lock-and-account-suspension.md.
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
	At     time.Time `json:"at"`
	Reason string    `json:"reason,omitempty"`
	By     string    `json:"by"`
}

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
// whether anything changed. An account lock is applied only to an unlocked
// site: it never replaces the operator's lock, and a repeated suspension
// keeps the first date. Lifting a lock hands a container project back to the
// runtime as a restart (see unlockMeta).
func (s *Store) SetLock(site *Site, lock *SiteLock) (changed bool, err error) {
	err = s.Update(site, func(m *Meta) error {
		changed = false
		if lock == nil {
			changed = unlockMeta(m)
			return nil
		}
		if lock.By == LockByAccount && m.Locked != nil {
			return nil
		}
		l := *lock
		l.Reason = CleanLockReason(l.Reason)
		if l.At.IsZero() {
			l.At = time.Now().UTC()
		}
		if l.By == "" {
			l.By = LockByAdmin
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
func (s *Store) ReleaseLock(site *Site, by string) (released bool, err error) {
	err = s.Update(site, func(m *Meta) error {
		released = false
		if m.Locked != nil && m.Locked.By == by {
			released = unlockMeta(m)
		}
		return nil
	})
	return released, err
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
