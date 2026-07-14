package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// LoadOrCreateSecret returns the 32-byte instance secret stored at path,
// creating it (0600) on first use.
func LoadOrCreateSecret(path string) ([]byte, error) {
	if b, err := os.ReadFile(path); err == nil && len(b) == 32 {
		return b, nil
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return nil, fmt.Errorf("generate secret: %w", err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		return nil, fmt.Errorf("persist secret: %w", err)
	}
	return b, nil
}

// TokenSigner mints and validates the view-session cookie values that let a
// visitor pass the password gate of one specific site.
type TokenSigner struct {
	Secret []byte
}

// Sign returns a token valid for siteID until now+ttl.
func (s TokenSigner) Sign(siteID string, now time.Time, ttl time.Duration) string {
	payload := siteID + "|" + strconv.FormatInt(now.Add(ttl).Unix(), 10)
	b64 := base64.RawURLEncoding.EncodeToString
	return b64([]byte(payload)) + "." + b64(s.mac(payload))
}

// Verify reports whether token grants access to siteID at time now.
func (s TokenSigner) Verify(token, siteID string, now time.Time) bool {
	subject, ok := s.Parse(token, now)
	return ok && subject == siteID
}

// Parse verifies token's signature and expiry and returns its subject. It is
// used when the caller does not know the subject in advance (e.g. a session
// cookie that encodes the account id and token version).
func (s TokenSigner) Parse(token string, now time.Time) (subject string, ok bool) {
	p64, sig64, found := strings.Cut(token, ".")
	if !found {
		return "", false
	}
	payload, err := base64.RawURLEncoding.DecodeString(p64)
	if err != nil {
		return "", false
	}
	sig, err := base64.RawURLEncoding.DecodeString(sig64)
	if err != nil {
		return "", false
	}
	if !hmac.Equal(sig, s.mac(string(payload))) {
		return "", false
	}
	// the subject may itself contain '|'; the expiry is after the last one
	i := strings.LastIndex(string(payload), "|")
	if i < 0 {
		return "", false
	}
	exp, err := strconv.ParseInt(string(payload[i+1:]), 10, 64)
	if err != nil {
		return "", false
	}
	if now.Unix() >= exp {
		return "", false
	}
	return string(payload[:i]), true
}

func (s TokenSigner) mac(payload string) []byte {
	m := hmac.New(sha256.New, s.Secret)
	m.Write([]byte(payload))
	return m.Sum(nil)
}
