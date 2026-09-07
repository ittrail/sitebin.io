// Package billing sells paid tiers through exactly one of three backends:
// Stripe direct, Paddle direct, or the IT-Trail SaaS Stack's PayGate. The
// direct backends create checkout sessions, cancel subscriptions and verify
// and interpret provider webhooks into a provider-agnostic Update; PayGate
// sells by tier name and is polled for the tier, so Sitebin never learns
// which processor the stack uses.
//
// Webhook signatures are HMAC-SHA256 (both direct providers); verification is
// hand-rolled to avoid heavy SDK dependencies. Real implementation is under
// the `ee` build tag; doc.go keeps the community build compiling an empty
// package. Licensed under ee/LICENSE.
package billing
