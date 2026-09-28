//go:build ee

package account

import (
	"errors"
	"regexp"
	"strings"
	"time"
)

// Provider is how an account authenticates.
type Provider string

const (
	Local     Provider = "local"
	Google    Provider = "google"
	Microsoft Provider = "microsoft"
	OIDCProv  Provider = "oidc" // generic OIDC issuer (saas-stack, Keycloak, …)
)

var (
	ErrNotFound   = errors.New("account not found")
	ErrEmailTaken = errors.New("email already registered")
	ErrOAuthTaken = errors.New("this identity is already linked to an account")
	ErrBadEmail   = errors.New("invalid email address")
)

// Billing captures an account's state with a direct payment provider.
type Billing struct {
	Provider     string `json:"provider,omitempty"` // stripe | paddle
	Customer     string `json:"customer,omitempty"`
	Subscription string `json:"subscription,omitempty"`
	Status       string `json:"status,omitempty"` // active | canceled | past_due
}

// Account is one user record, stored as accounts/<id>/account.json.
type Account struct {
	ID            string   `json:"id"`
	Provider      Provider `json:"provider"`
	Email         string   `json:"email"`
	EmailVerified bool     `json:"email_verified"`
	OAuthSubject  string   `json:"oauth_subject,omitempty"`
	PasswordHash  string   `json:"password_hash,omitempty"` // local accounts only
	Tier          string   `json:"tier,omitempty"`
	QuotaOverride *int64   `json:"quota_override,omitempty"`
	Billing       *Billing `json:"billing,omitempty"`
	TokenVersion  int      `json:"token_version"`
	// SuspendedAt is set while the stack has the account suspended (see
	// ee/suspend.go): every way in is refused and its sites are locked.
	// Nil — every account written before suspensions existed — is active.
	SuspendedAt     *time.Time `json:"suspended_at,omitempty"`
	SuspendedReason string     `json:"suspended_reason,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

// Suspended reports whether the account is suspended.
func (a *Account) Suspended() bool { return a != nil && a.SuspendedAt != nil }

var emailRe = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)

// normalizeEmail lowercases, trims, and validates an email address.
func normalizeEmail(e string) (string, error) {
	e = strings.ToLower(strings.TrimSpace(e))
	if len(e) > 254 || !emailRe.MatchString(e) {
		return "", ErrBadEmail
	}
	return e, nil
}
