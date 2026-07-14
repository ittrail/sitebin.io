//go:build ee

package licensing

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// vendorPublicKeyB64 is the Sitebin vendor's Ed25519 public key (base64). The
// matching private key is held by the vendor and never in this repo. Override
// at build time with -ldflags "-X .../ee/licensing.vendorPublicKeyB64=<base64>".
var vendorPublicKeyB64 = "xHYcS/rmNDRdRvUKlRiEa/IMhNF4Kdbxz3ts48uQVus="

// License is a verified key's contents.
type License struct {
	Holder    string    `json:"holder"`
	Plan      string    `json:"plan"`
	IssuedAt  time.Time `json:"issued_at"`
	ExpiresAt time.Time `json:"expires_at"` // zero = perpetual
}

// Expired reports whether the license has an expiry in the past.
func (l License) Expired(now time.Time) bool {
	return !l.ExpiresAt.IsZero() && now.After(l.ExpiresAt)
}

var (
	ErrMalformed = errors.New("malformed license key")
	ErrSignature = errors.New("license signature invalid")
	ErrExpired   = errors.New("license has expired")
)

// VendorKey returns the embedded vendor public key.
func VendorKey() (ed25519.PublicKey, error) {
	b, err := base64.StdEncoding.DecodeString(vendorPublicKeyB64)
	if err != nil || len(b) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("bad embedded vendor key")
	}
	return ed25519.PublicKey(b), nil
}

// Verify checks a license key against pub at time now and returns its contents.
func Verify(key string, pub ed25519.PublicKey, now time.Time) (License, error) {
	payloadB64, sigB64, ok := strings.Cut(strings.TrimSpace(key), ".")
	if !ok {
		return License{}, ErrMalformed
	}
	payload, err := base64.RawURLEncoding.DecodeString(payloadB64)
	if err != nil {
		return License{}, ErrMalformed
	}
	sig, err := base64.RawURLEncoding.DecodeString(sigB64)
	if err != nil || len(sig) != ed25519.SignatureSize {
		return License{}, ErrMalformed
	}
	if !ed25519.Verify(pub, payload, sig) {
		return License{}, ErrSignature
	}
	var lic License
	if err := json.Unmarshal(payload, &lic); err != nil {
		return License{}, ErrMalformed
	}
	if lic.Expired(now) {
		return lic, ErrExpired
	}
	return lic, nil
}

// Sign produces a license key from a payload and the vendor private key. Used
// by the vendor's offline key-issuing tool (not shipped in the product).
func Sign(lic License, priv ed25519.PrivateKey) (string, error) {
	payload, err := json.Marshal(lic)
	if err != nil {
		return "", err
	}
	sig := ed25519.Sign(priv, payload)
	return base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}
