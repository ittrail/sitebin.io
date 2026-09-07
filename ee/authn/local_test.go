//go:build ee

package authn

import (
	"errors"
	"strings"
	"testing"

	"github.com/ittrail/sitebin.io/ee/account"
)

func newLocal(t *testing.T) *Local {
	t.Helper()
	s, err := account.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return NewLocal(s)
}

func TestSignupAndLogin(t *testing.T) {
	l := newLocal(t)
	a, err := l.Signup("alice@example.com", "correct horse", "free")
	if err != nil {
		t.Fatalf("Signup: %v", err)
	}
	if a.PasswordHash == "" || a.PasswordHash == "correct horse" {
		t.Error("password not hashed")
	}
	got, err := l.Login("alice@example.com", "correct horse")
	if err != nil || got.ID != a.ID {
		t.Fatalf("Login: %v", err)
	}
}

func TestSignupWeakPassword(t *testing.T) {
	l := newLocal(t)
	if _, err := l.Signup("bob@example.com", "short", "free"); !errors.Is(err, ErrWeakPassword) {
		t.Fatalf("expected ErrWeakPassword, got %v", err)
	}
}

func TestSignupDuplicate(t *testing.T) {
	l := newLocal(t)
	if _, err := l.Signup("carol@example.com", "password1", "free"); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Signup("carol@example.com", "password2", "free"); !errors.Is(err, account.ErrEmailTaken) {
		t.Fatalf("expected ErrEmailTaken, got %v", err)
	}
}

func TestLoginWrongPassword(t *testing.T) {
	l := newLocal(t)
	l.Signup("dave@example.com", "password1", "free")
	if _, err := l.Login("dave@example.com", "wrong"); !errors.Is(err, ErrBadCredentials) {
		t.Fatalf("wrong password: %v", err)
	}
}

func TestLoginUnknownEmail(t *testing.T) {
	l := newLocal(t)
	if _, err := l.Login("ghost@example.com", "whatever"); !errors.Is(err, ErrBadCredentials) {
		t.Fatalf("unknown email should be ErrBadCredentials, got %v", err)
	}
}

func TestLoginOAuthOnlyAccountHasNoPassword(t *testing.T) {
	l := newLocal(t)
	if _, err := l.store.CreateOAuth(account.Google, "sub-9", "erin@example.com", "free"); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Login("erin@example.com", "anything"); !errors.Is(err, ErrBadCredentials) {
		t.Fatalf("oauth-only login: %v", err)
	}
}

func TestChangePasswordBumpsTokenVersion(t *testing.T) {
	l := newLocal(t)
	a, _ := l.Signup("fay@example.com", "password1", "free")
	if a.TokenVersion != 0 {
		t.Fatalf("initial token version = %d", a.TokenVersion)
	}
	if err := l.ChangePassword(a, "password2"); err != nil {
		t.Fatalf("ChangePassword: %v", err)
	}
	if a.TokenVersion != 1 {
		t.Errorf("token version after change = %d, want 1", a.TokenVersion)
	}
	if _, err := l.Login("fay@example.com", "password2"); err != nil {
		t.Errorf("login with new password: %v", err)
	}
	if _, err := l.Login("fay@example.com", "password1"); !errors.Is(err, ErrBadCredentials) {
		t.Error("old password still works")
	}
}

// Argon2 hashes whatever it is given, and a form field can be megabytes.
// A password longer than MaxPasswordLen is refused before any hashing.
func TestPasswordLengthIsCapped(t *testing.T) {
	l := newLocal(t)
	long := string(make([]byte, MaxPasswordLen+1))
	if _, err := l.Signup("long@example.com", long, "free"); !errors.Is(err, ErrPasswordTooLong) {
		t.Fatalf("Signup with %d bytes: %v, want ErrPasswordTooLong", len(long), err)
	}
	a, err := l.Signup("ok@example.com", "password123", "free")
	if err != nil {
		t.Fatal(err)
	}
	if err := l.ChangePassword(a, long); !errors.Is(err, ErrPasswordTooLong) {
		t.Fatalf("ChangePassword: %v", err)
	}
	// Login with an over-long password can never succeed, and must not cost
	// a hash to find that out.
	if _, err := l.Login("ok@example.com", long); !errors.Is(err, ErrBadCredentials) {
		t.Fatalf("Login: %v", err)
	}
	exact := strings.Repeat("x", MaxPasswordLen)
	if _, err := l.Signup("exact@example.com", exact, "free"); err != nil {
		t.Fatalf("a password of exactly MaxPasswordLen must be accepted: %v", err)
	}
}
