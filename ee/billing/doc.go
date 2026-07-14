// Package billing integrates Stripe and Paddle for paid tiers: it creates
// checkout sessions and verifies + interprets provider webhooks into a
// provider-agnostic Update applied to an account's tier and billing state.
//
// Webhook signatures are HMAC-SHA256 (both providers); verification is
// hand-rolled to avoid heavy SDK dependencies. Real implementation is under
// the `ee` build tag; doc.go keeps the community build compiling an empty
// package. Licensed under ee/LICENSE.
package billing
