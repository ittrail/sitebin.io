// Package account is the Sitebin Enterprise Edition account store: filesystem-
// backed user accounts (local + OAuth), indexed by email and OAuth subject,
// with each account tracking the sites it owns. No database — same "filesystem
// is the database" model as the core site store.
//
// Real implementation is under the `ee` build tag; this doc.go keeps the
// community build compiling an empty package. Licensed under ee/LICENSE.
package account
