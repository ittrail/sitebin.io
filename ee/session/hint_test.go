//go:build ee

package session

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// A realistic compact JWS: three base64url segments.
const sampleIDToken = "eyJhbGciOiJSUzI1NiIsImtpZCI6ImsxIn0.eyJzdWIiOiJ1c2VyLTEiLCJhenAiOiJzaXRlYmluLWFwcCJ9.c2lnbmF0dXJlLWJ5dGVz"

// The ID token of the sign-in is kept for one request only -- the sign-out
// -- so the cookie is path-limited to that route, host-only, HttpOnly, and
// lives exactly as long as a session can.
func TestHintCookieAttributes(t *testing.T) {
	m := New(secret, true, 3*time.Hour)
	c := m.HintCookie(sampleIDToken)
	if c.Name != "__Secure-sitebin_idt" || c.Name != m.HintName() {
		t.Errorf("name = %q, want __Secure-sitebin_idt over TLS", c.Name)
	}
	if c.Value != sampleIDToken {
		t.Errorf("value = %q, want the raw ID token", c.Value)
	}
	if c.Path != "/account/logout" {
		t.Errorf("path = %q: the hint must travel only to the sign-out", c.Path)
	}
	if c.Domain != "" {
		t.Errorf("domain = %q, want host-only", c.Domain)
	}
	if !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteLaxMode {
		t.Errorf("attrs = %+v, want HttpOnly, Secure, SameSite=Lax", c)
	}
	if c.MaxAge != int((3 * time.Hour).Seconds()) {
		t.Errorf("max-age = %d, want the session TTL", c.MaxAge)
	}

	// On an HTTP-only instance the prefix is impossible (browsers refuse it
	// without Secure), so the name is bare, as the session's is.
	plain := New(secret, false, time.Hour)
	if got := plain.HintCookie(sampleIDToken); got.Name != "sitebin_idt" || got.Secure {
		t.Errorf("http-only instance: %+v", got)
	}
}

// Nothing the browser could not carry, or that is not a compact JWS, is
// stored: the answer is the cookie that clears whatever was there, and the
// sign-out then goes without a hint.
func TestHintCookieRefusesWhatItCannotCarry(t *testing.T) {
	m := New(secret, true, time.Hour)
	for name, raw := range map[string]string{
		"empty":       "",
		"oversized":   strings.Repeat("a", 2000) + "." + strings.Repeat("b", 2000) + "." + "c",
		"not a jws":   "just-a-string",
		"odd bytes":   "aaa.b b.ccc",
		"cookie meta": "aaa.bbb;path=/.ccc",
	} {
		c := m.HintCookie(raw)
		if c.Value != "" || c.MaxAge >= 0 {
			t.Errorf("%s: stored %q (max-age %d), want a clearing cookie", name, c.Value, c.MaxAge)
		}
		if c.Path != "/account/logout" || c.Name != m.HintName() {
			t.Errorf("%s: a clearing cookie must match the stored one's name and path: %+v", name, c)
		}
	}
}

func TestClearHintMatchesTheStoredCookie(t *testing.T) {
	m := New(secret, true, time.Hour)
	c := m.ClearHint()
	if c.Name != m.HintName() || c.Path != "/account/logout" || c.MaxAge >= 0 || c.Value != "" || !c.Secure || !c.HttpOnly {
		t.Errorf("clear = %+v", c)
	}
}

func TestHintReadsTheCookieBack(t *testing.T) {
	m := New(secret, true, time.Hour)
	r := httptest.NewRequest("POST", "/account/logout", nil)
	if got := m.Hint(r); got != "" {
		t.Errorf("no cookie: hint = %q", got)
	}
	r.AddCookie(m.HintCookie(sampleIDToken))
	if got := m.Hint(r); got != sampleIDToken {
		t.Errorf("hint = %q, want the stored token", got)
	}
	// Something that is not a compact JWS is not a hint.
	bad := httptest.NewRequest("POST", "/account/logout", nil)
	bad.AddCookie(&http.Cookie{Name: m.HintName(), Value: "garbage"})
	if got := m.Hint(bad); got != "" {
		t.Errorf("garbage cookie read back as %q", got)
	}
}
