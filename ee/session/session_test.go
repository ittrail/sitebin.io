//go:build ee

package session

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

var secret = []byte("0123456789abcdef0123456789abcdef")

func fixedNow(t time.Time) func() time.Time { return func() time.Time { return t } }

func reqWithCookie(c *http.Cookie) *http.Request {
	r := httptest.NewRequest("GET", "/account", nil)
	if c != nil {
		r.AddCookie(c)
	}
	return r
}

func TestIssueAndValidate(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	m := New(secret, true, time.Hour)
	m.Now = fixedNow(now)

	c := m.Cookie("acct-1", 3)
	if c.Name != CookieName || !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteLaxMode {
		t.Fatalf("cookie attrs wrong: %+v", c)
	}
	id, ver, ok := m.Validate(reqWithCookie(c))
	if !ok || id != "acct-1" || ver != 3 {
		t.Fatalf("validate = %q %d %v", id, ver, ok)
	}
}

func TestExpiredSession(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	m := New(secret, true, time.Hour)
	m.Now = fixedNow(now)
	c := m.Cookie("acct-1", 0)

	m.Now = fixedNow(now.Add(2 * time.Hour))
	if _, _, ok := m.Validate(reqWithCookie(c)); ok {
		t.Fatal("expired session accepted")
	}
}

func TestTamperedSession(t *testing.T) {
	m := New(secret, true, time.Hour)
	c := m.Cookie("acct-1", 0)
	c.Value += "x"
	if _, _, ok := m.Validate(reqWithCookie(c)); ok {
		t.Fatal("tampered session accepted")
	}
	// wrong secret
	other := New([]byte("XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX"), true, time.Hour)
	good := New(secret, true, time.Hour).Cookie("acct-1", 0)
	if _, _, ok := other.Validate(reqWithCookie(good)); ok {
		t.Fatal("cross-secret session accepted")
	}
}

func TestNoCookie(t *testing.T) {
	m := New(secret, true, time.Hour)
	if _, _, ok := m.Validate(reqWithCookie(nil)); ok {
		t.Fatal("missing cookie validated")
	}
}

func TestClear(t *testing.T) {
	m := New(secret, false, time.Hour)
	c := m.Clear()
	if c.MaxAge >= 0 || c.Value != "" || c.Secure {
		t.Fatalf("clear cookie wrong: %+v", c)
	}
}

func TestVersionRevocationSignal(t *testing.T) {
	// The manager returns the version it was issued at; a version bump is
	// detected by the caller comparing against the account's current version.
	m := New(secret, true, time.Hour)
	c := m.Cookie("acct-1", 5)
	_, ver, _ := m.Validate(reqWithCookie(c))
	if ver != 5 {
		t.Fatalf("version = %d", ver)
	}
}
