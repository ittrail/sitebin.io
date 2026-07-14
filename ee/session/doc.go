// Package session issues and validates Sitebin Enterprise Edition account
// sessions as stateless signed cookies (reusing the core HMAC token signer).
// The cookie encodes the account id and its token version; bumping the
// account's token_version invalidates all its existing sessions.
//
// Real implementation is under the `ee` build tag; doc.go keeps the community
// build compiling an empty package. Licensed under ee/LICENSE.
package session
