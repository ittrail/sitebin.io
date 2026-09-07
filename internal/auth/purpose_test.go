package auth

import (
	"os"
	"testing"
	"time"
)

// One instance secret signs view cookies, account sessions, OAuth state and
// e-mail tokens. They used to be told apart only by a prefix convention in
// the subject, so a token minted for one purpose was a valid signature for
// every other parser. The purpose is now part of the MAC.
func TestTokenPurposesAreSeparated(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	now := time.Unix(1_700_000_000, 0)
	view := TokenSigner{Secret: secret, Purpose: "view"}
	session := TokenSigner{Secret: secret, Purpose: "session"}

	tok := view.Sign("acct-1|3", now, time.Hour)
	if _, ok := view.Parse(tok, now); !ok {
		t.Fatal("a token does not verify under its own purpose")
	}
	if _, ok := session.Parse(tok, now); ok {
		t.Fatal("a view token parsed as a session token: purposes share a signature")
	}
	if (TokenSigner{Secret: secret}).Verify(tok, "acct-1|3", now) {
		t.Fatal("a purposed token verifies under an unpurposed signer")
	}
}

// A .secret of the wrong length is a broken install, not a cue to mint a new
// one: silently replacing it invalidates every cookie and every token the
// instance ever issued, and hides whatever corrupted the file.
func TestLoadOrCreateSecretRefusesAWrongLengthFile(t *testing.T) {
	path := t.TempDir() + "/.secret"
	if _, err := LoadOrCreateSecret(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("short"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrCreateSecret(path); err == nil {
		t.Fatal("a 5-byte .secret was accepted or replaced; it must refuse to start")
	}
}
