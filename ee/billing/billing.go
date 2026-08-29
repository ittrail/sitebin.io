//go:build ee

package billing

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"time"

	"github.com/ittrail/sitebin.io/ee/eeconfig"
)

// Customer is what a backend needs to know about who is paying. Which fields
// matter depends on the backend: the direct providers key off AccountID and
// the customer id they issued, PayGate keys off the OIDC subject, because that
// is the identity the stack knows the person by.
type Customer struct {
	AccountID    string
	Email        string
	Subject      string // OIDC subject; empty for locally authenticated accounts
	Provider     string // the provider that issued Customer/Subscription, if any
	Customer     string
	Subscription string
}

// Backend is a way to sell a tier. Sitebin ships three — Stripe direct, Paddle
// direct, and the IT-Trail SaaS Stack's PayGate — and exactly one is active.
//
// Only these two operations are universal. Everything else about billing
// differs so much between "Sitebin owns the subscription" and "the stack owns
// it" that forcing it into this interface would make one of the two lie; those
// capabilities are the optional interfaces below.
type Backend interface {
	// Name is the backend's config name, for logs and errors. It is never
	// shown to a customer: with PayGate the processor is the stack's business,
	// and naming it here would leak a choice Sitebin must not depend on.
	Name() string

	// CheckoutURL starts a purchase of tier and returns where to send the
	// browser. The whole tier is passed rather than a price id so each backend
	// can read the field it needs without the caller knowing which that is.
	CheckoutURL(ctx context.Context, c Customer, tier eeconfig.Tier, successURL, cancelURL string) (string, error)

	// PortalURL returns where an existing subscriber manages their
	// subscription, or "" when this backend has no portal for them.
	PortalURL(ctx context.Context, c Customer, returnURL string) (string, error)
}

// TierSource is implemented by a backend that holds the subscription state
// somewhere Sitebin can ask. PayGate does; the direct providers do not, because
// with them Sitebin is itself the record of what an account holds.
type TierSource interface {
	TierFor(ctx context.Context, subject string) (tier string, ok bool, err error)
}

// WebhookReceiver is implemented by a backend the payment provider pushes
// events at. Only the direct providers do: PayGate receives their webhooks on
// the stack's side and Sitebin learns of a change by resolving a tier.
//
// The interface deliberately stops at verification. Mounting routes and
// applying an Update to an account is the ee provider's job, so the billing
// package never reaches into accounts.
type WebhookReceiver interface {
	// WebhookPath is the path segment the provider posts to, which is the one
	// place a provider's name legitimately appears in a URL: the provider
	// dictates where it delivers.
	WebhookPath() string
	// SignatureHeader names the header carrying the signature to verify.
	SignatureHeader() string
	VerifyWebhook(sigHeader string, body []byte, now time.Time) (Update, error)
}

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

// Compile-time proof that each backend is what it claims to be, and only that.
// The split matters: asserting PayGate as a WebhookReceiver here would be the
// first step back towards Sitebin knowing which processor the stack uses.
var (
	_ Backend = (*Stripe)(nil)
	_ Backend = (*Paddle)(nil)
	_ Backend = (*PayGate)(nil)

	_ TierSource = (*PayGate)(nil)

	_ WebhookReceiver = (*Stripe)(nil)
	_ WebhookReceiver = (*Paddle)(nil)
)
