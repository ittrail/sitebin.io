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
	p64, sig64, ok := strings.Cut(token, ".")
	if !ok {
		return false
	}
	payload, err := base64.RawURLEncoding.DecodeString(p64)
	if err != nil {
		return false
	}
	sig, err := base64.RawURLEncoding.DecodeString(sig64)
	if err != nil {
		return false
	}
	if !hmac.Equal(sig, s.mac(string(payload))) {
		return false
	}
	id, expStr, ok := strings.Cut(string(payload), "|")
	if !ok || id != siteID {
		return false
	}
	exp, err := strconv.ParseInt(expStr, 10, 64)
	if err != nil {
		return false
	}
	return now.Unix() < exp
}

func (s TokenSigner) mac(payload string) []byte {
	m := hmac.New(sha256.New, s.Secret)
	m.Write([]byte(payload))
	return m.Sum(nil)
}
