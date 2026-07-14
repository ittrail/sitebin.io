// Package licensing verifies Ed25519-signed Sitebin Enterprise license keys.
// A key is "<base64url(json payload)>.<base64url(signature)>"; the vendor
// signs with a private key held off-repo, and the binary embeds the matching
// public key. Verification is offline (no license server).
//
// (Directory is "licensing" rather than "license" to avoid colliding with the
// ee/LICENSE file on case-insensitive filesystems.)
//
// Real implementation is under the `ee` build tag; doc.go keeps the community
// build compiling an empty package. Licensed under ee/LICENSE.
package licensing
