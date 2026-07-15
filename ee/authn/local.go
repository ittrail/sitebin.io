//go:build ee

package authn

import (
	"errors"
	"strings"

	"github.com/ittrail/sitebin.io/ee/account"
	"github.com/ittrail/sitebin.io/internal/auth"
)

// MinPasswordLen is the minimum local-account password length.
const MinPasswordLen = 8

var (
	ErrWeakPassword   = errors.New("password must be at least 8 characters")
	ErrBadCredentials = errors.New("incorrect email or password")
)

// Local orchestrates local (email+password) authentication over the account
// store.
type Local struct {
	store *account.Store
}

func NewLocal(store *account.Store) *Local { return &Local{store: store} }

// Signup creates a local account. The plaintext password is hashed with
// Argon2id and never stored. tier is the account's initial tier id.
func (l *Local) Signup(email, password, tier string) (*account.Account, error) {
	if len([]rune(strings.TrimSpace(password))) < MinPasswordLen {
		return nil, ErrWeakPassword
	}
	return l.store.CreateLocal(email, auth.HashPassword(password), tier)
}

// Login verifies credentials, returning the account on success. It returns the
// same ErrBadCredentials for unknown emails and wrong passwords to avoid
// account enumeration. OAuth-only accounts (no password hash) never match.
func (l *Local) Login(email, password string) (*account.Account, error) {
	a, err := l.store.ByEmail(email)
	if err != nil {
		// still spend some work to reduce timing signal
		auth.VerifyPassword("$argon2id$v=19$m=65536,t=1,p=4$AAAAAAAAAAAAAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", password)
		return nil, ErrBadCredentials
	}
	if a.PasswordHash == "" || !auth.VerifyPassword(a.PasswordHash, password) {
		return nil, ErrBadCredentials
	}
	return a, nil
}

// ChangePassword sets a new password and bumps the account's token version,
// invalidating existing sessions.
func (l *Local) ChangePassword(a *account.Account, newPassword string) error {
	if len([]rune(strings.TrimSpace(newPassword))) < MinPasswordLen {
		return ErrWeakPassword
	}
	hash := auth.HashPassword(newPassword)
	return l.store.Update(a, func(cur *account.Account) error {
		cur.PasswordHash = hash
		cur.TokenVersion++
		return nil
	})
}
