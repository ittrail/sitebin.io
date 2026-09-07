//go:build ee

package account

import (
	"os"
	"path/filepath"
	"testing"
)

// An erased account leaves no row naming it: every token-index entry that
// points at it goes with the account, whether or not the token was ever
// revoked on its own.
func TestDeleteRemovesTheTokenIndex(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	acc, err := s.CreateOAuth(OIDCProv, "11111111-1111-4111-8111-111111111111", "gone@example.com", true, "free")
	if err != nil {
		t.Fatal(err)
	}
	keep, err := s.CreateLocal("keep@example.com", "$hash", "free")
	if err != nil {
		t.Fatal(err)
	}
	_, secret1, err := s.CreateToken(acc, "one")
	if err != nil {
		t.Fatal(err)
	}
	_, secret2, err := s.CreateToken(acc, "two")
	if err != nil {
		t.Fatal(err)
	}
	_, keepSecret, err := s.CreateToken(keep, "theirs")
	if err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(filepath.Join(s.root, "account-index", "token"))
	if len(entries) != 3 {
		t.Fatalf("token index has %d entries before the deletion, want 3", len(entries))
	}

	if err := s.Delete(acc, nil); err != nil {
		t.Fatal(err)
	}

	for _, sec := range []string{secret1, secret2} {
		if _, ok := s.ByToken(sec); ok {
			t.Error("a token of the erased account still authenticates")
		}
	}
	entries, _ = os.ReadDir(filepath.Join(s.root, "account-index", "token"))
	if len(entries) != 1 {
		t.Errorf("token index has %d entries after the deletion, want only the other account's", len(entries))
	}
	// And the other account's token is untouched.
	if got, ok := s.ByToken(keepSecret); !ok || got.ID != keep.ID {
		t.Error("the other account's token stopped working")
	}
	// The rest of the erased account, for completeness.
	if _, err := s.ByOAuth(OIDCProv, "11111111-1111-4111-8111-111111111111"); err == nil {
		t.Error("the oauth index still resolves the erased subject")
	}
	if _, err := s.ByEmail("gone@example.com"); err == nil {
		t.Error("the email index still resolves the erased address")
	}
	if _, err := os.Stat(s.accountDir(acc.ID)); !os.IsNotExist(err) {
		t.Error("the account directory survived")
	}
}

// Revoking a token removes its index entry as well as its record: a hash
// nobody can reverse is still a row that names an account.
func TestDeleteTokenRemovesTheIndexEntry(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	acc, err := s.CreateLocal("tok@example.com", "$hash", "free")
	if err != nil {
		t.Fatal(err)
	}
	tok, secret, err := s.CreateToken(acc, "one")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteToken(acc, tok.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.ByToken(secret); ok {
		t.Error("a revoked token still authenticates")
	}
	entries, _ := os.ReadDir(filepath.Join(s.root, "account-index", "token"))
	if len(entries) != 0 {
		t.Errorf("token index has %d entries after revocation, want none", len(entries))
	}
}
