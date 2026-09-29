//go:build ee

package session

import (
	"net/http"
	"strings"
)

// The logout hint: the raw ID token of the sign-in, kept so that signing out
// of Sitebin can end the identity provider's session too without the
// provider asking "do you want to log out?" (the id_token_hint of OpenID
// Connect RP-Initiated Logout). See
// docs/superpowers/specs/2026-09-29-sign-out-ends-the-sso-session.md.
//
// It is kept for that one request and nothing else: the cookie's path is the
// sign-out route, so the browser sends it nowhere else, and nothing but the
// sign-out ever reads it. It carries the person's email and name, so it is
// never logged.

// HintCookieName is the logout-hint cookie. Over TLS it carries the
// __Secure- prefix (see Manager.HintName); __Host- is impossible, because it
// requires Path=/ and the point of this cookie is its narrow path.
const HintCookieName = "sitebin_idt"

// hintPath is the only route the hint is sent to.
const hintPath = "/account/logout"

// maxHint bounds what is stored: browsers cap a cookie's name and value
// together at 4096 bytes, and one they cannot carry is silently dropped. A
// token over this goes unstored and the sign-out goes without a hint.
const maxHint = 3800

// HintName is the hint cookie's name on this instance.
func (m *Manager) HintName() string {
	if m.secure {
		return "__Secure-" + HintCookieName
	}
	return HintCookieName
}

// HintCookie returns the Set-Cookie that keeps raw for the next sign-out --
// or, when raw is empty, too large, or not a compact JWS, the one that clears
// whatever hint an earlier sign-in left, so a hint never outlives the sign-in
// it came from.
func (m *Manager) HintCookie(raw string) *http.Cookie {
	if !validHint(raw) {
		return m.ClearHint()
	}
	return &http.Cookie{
		Name:     m.HintName(),
		Value:    raw,
		Path:     hintPath,
		MaxAge:   int(m.ttl.Seconds()),
		HttpOnly: true,
		Secure:   m.secure,
		SameSite: http.SameSiteLaxMode,
	}
}

// ClearHint returns the Set-Cookie that deletes the hint. Name and path must
// be the stored cookie's, or the browser keeps it.
func (m *Manager) ClearHint() *http.Cookie {
	return &http.Cookie{
		Name:     m.HintName(),
		Value:    "",
		Path:     hintPath,
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   m.secure,
		SameSite: http.SameSiteLaxMode,
	}
}

// Hint returns the stored hint, or "" when there is none that could be one.
func (m *Manager) Hint(r *http.Request) string {
	c, err := r.Cookie(m.HintName())
	if err != nil || !validHint(c.Value) {
		return ""
	}
	return c.Value
}

// validHint reports whether s can be a stored hint: a compact JWS (three
// base64url segments) of a size a cookie carries.
func validHint(s string) bool {
	if s == "" || len(s) > maxHint || strings.Count(s, ".") != 2 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '-', c == '_', c == '.':
		default:
			return false
		}
	}
	return true
}
