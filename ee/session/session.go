//go:build ee

package session

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ittrail/sitebin.io/internal/auth"
)

// CookieName is the account-session cookie on the main domain.
const CookieName = "sitebin_s"

// DefaultTTL is the sliding session lifetime.
const DefaultTTL = 30 * 24 * time.Hour

// Manager mints and validates session cookies.
type Manager struct {
	Now func() time.Time

	signer auth.TokenSigner
	ttl    time.Duration
	secure bool
}

// New builds a session Manager. secure sets the cookie Secure flag (false only
// for HTTP-only local instances).
func New(secret []byte, secure bool, ttl time.Duration) *Manager {
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	return &Manager{
		Now:    time.Now,
		signer: auth.TokenSigner{Secret: secret},
		ttl:    ttl,
		secure: secure,
	}
}

func subject(accountID string, version int) string {
	return accountID + "|" + strconv.Itoa(version)
}

// Cookie returns a Set-Cookie for a fresh session bound to the account id and
// its current token version.
func (m *Manager) Cookie(accountID string, version int) *http.Cookie {
	token := m.signer.Sign(subject(accountID, version), m.Now(), m.ttl)
	return &http.Cookie{
		Name:     CookieName,
		Value:    token,
		Path:     "/",
		MaxAge:   int(m.ttl.Seconds()),
		HttpOnly: true,
		Secure:   m.secure,
		SameSite: http.SameSiteLaxMode,
	}
}

// Clear returns a Set-Cookie that deletes the session.
func (m *Manager) Clear() *http.Cookie {
	return &http.Cookie{
		Name:     CookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   m.secure,
		SameSite: http.SameSiteLaxMode,
	}
}

// Validate reads and verifies the session cookie, returning the account id and
// the token version the cookie was issued at. The caller must load the account
// and confirm its current token_version matches (revocation check).
func (m *Manager) Validate(r *http.Request) (accountID string, version int, ok bool) {
	c, err := r.Cookie(CookieName)
	if err != nil {
		return "", 0, false
	}
	return m.parse(c.Value)
}

func (m *Manager) parse(token string) (string, int, bool) {
	subj, ok := m.signer.Parse(token, m.Now())
	if !ok {
		return "", 0, false
	}
	i := strings.LastIndex(subj, "|")
	if i < 0 {
		return "", 0, false
	}
	version, err := strconv.Atoi(subj[i+1:])
	if err != nil {
		return "", 0, false
	}
	return subj[:i], version, true
}
