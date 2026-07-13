package auth

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHashAndVerifyPassword(t *testing.T) {
	h := HashPassword("hunter2")
	if !strings.HasPrefix(h, "$argon2id$v=19$m=65536,t=1,p=4$") {
		t.Fatalf("unexpected hash format: %q", h)
	}
	if !VerifyPassword(h, "hunter2") {
		t.Error("correct password rejected")
	}
	if VerifyPassword(h, "hunter3") {
		t.Error("wrong password accepted")
	}
	if h2 := HashPassword("hunter2"); h2 == h {
		t.Error("salt not random: two hashes identical")
	}
}

func TestVerifyPasswordMalformed(t *testing.T) {
	for _, bad := range []string{
		"", "plaintext", "$argon2id$v=19$m=abc,t=1,p=4$xx$yy",
		"$argon2id$v=19$m=65536,t=1,p=4$!!notb64!!$zz",
		"$argon2i$v=19$m=65536,t=1,p=4$c2FsdA$aGFzaA",
	} {
		if VerifyPassword(bad, "x") {
			t.Errorf("malformed hash %q verified", bad)
		}
	}
}

func TestLoadOrCreateSecret(t *testing.T) {
	p := filepath.Join(t.TempDir(), ".secret")
	s1, err := LoadOrCreateSecret(p)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if len(s1) != 32 {
		t.Fatalf("len = %d", len(s1))
	}
	s2, err := LoadOrCreateSecret(p)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if string(s1) != string(s2) {
		t.Error("secret not persisted")
	}
	if _, err := os.Stat(p); err != nil {
		t.Errorf("secret file missing: %v", err)
	}
}

func TestTokenSignVerify(t *testing.T) {
	s := TokenSigner{Secret: []byte("0123456789abcdef0123456789abcdef")}
	now := time.Unix(1_700_000_000, 0)
	tok := s.Sign("site1", now, time.Hour)

	if !s.Verify(tok, "site1", now.Add(30*time.Minute)) {
		t.Error("valid token rejected")
	}
	if s.Verify(tok, "site1", now.Add(2*time.Hour)) {
		t.Error("expired token accepted")
	}
	if s.Verify(tok, "site2", now) {
		t.Error("token accepted for wrong site")
	}
	if s.Verify(tok+"x", "site1", now) {
		t.Error("tampered token accepted")
	}
	other := TokenSigner{Secret: []byte("differentsecretdifferentsecret!!")}
	if other.Verify(tok, "site1", now) {
		t.Error("token verified with wrong secret")
	}
	if s.Verify("garbage", "site1", now) || s.Verify("", "site1", now) {
		t.Error("garbage token accepted")
	}
}

func TestVerifyCache(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	c := NewVerifyCache(5 * time.Minute)
	c.Now = func() time.Time { return now }

	if c.Check("k") {
		t.Error("empty cache hit")
	}
	c.Put("k")
	if !c.Check("k") {
		t.Error("cached key missed")
	}
	now = now.Add(6 * time.Minute)
	if c.Check("k") {
		t.Error("expired entry hit")
	}
}

func TestLimiter(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	l := NewLimiter(60, 3) // 60 per hour, burst 3
	l.Now = func() time.Time { return now }

	for i := 0; i < 3; i++ {
		if !l.Allow("ip1") {
			t.Fatalf("burst request %d denied", i)
		}
	}
	if l.Allow("ip1") {
		t.Error("over-burst request allowed")
	}
	if !l.Allow("ip2") {
		t.Error("independent key denied")
	}
	now = now.Add(time.Minute) // 60/h = 1 per minute refill
	if !l.Allow("ip1") {
		t.Error("refilled request denied")
	}
	if l.Allow("ip1") {
		t.Error("second request after single refill allowed")
	}
}
