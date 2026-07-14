//go:build ee

package billing

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
)

// Update is the provider-agnostic result of interpreting a webhook: what to
// apply to an account's billing state and tier.
type Update struct {
	Provider     string // stripe | paddle
	AccountID    string // resolved from checkout metadata / customer index
	Customer     string
	Subscription string
	TierID       string // tier to activate; "" when only status changed / canceled
	Status       string // active | canceled | past_due
	Canceled     bool   // true → revert account to the default tier
}

// hmacSHA256Hex returns the lowercase hex HMAC-SHA256 of payload under secret.
func hmacSHA256Hex(secret, payload []byte) string {
	m := hmac.New(sha256.New, secret)
	m.Write(payload)
	return hex.EncodeToString(m.Sum(nil))
}

// constEq compares two hex strings in constant time.
func constEq(a, b string) bool {
	return hmac.Equal([]byte(a), []byte(b))
}
