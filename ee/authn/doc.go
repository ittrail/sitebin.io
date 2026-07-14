// Package authn is the Sitebin Enterprise Edition local-authentication logic:
// signup, login, and password change over the account store, using the core
// Argon2id helpers. OAuth (Google/Microsoft) lives in a sibling file added in
// Phase 3.
//
// Real implementation is under the `ee` build tag; doc.go keeps the community
// build compiling an empty package. Licensed under ee/LICENSE.
package authn
