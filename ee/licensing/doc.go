// Package licensing verifies Sitebin Enterprise license keys offline.
//
// A key is four dot-separated base64url segments — a certificate and the
// license it vouches for:
//
//	<certPayload>.<certSig>.<licPayload>.<licSig>
//
// The stack that sells the license is a small certificate authority: one root
// keypair per stack instance, one signing keypair per registered app, and a
// root-signed certificate binding the app id to that signing key. The
// certificate travels inside the license string, so verification is two
// Ed25519 checks and needs no network — there is no license server, and there
// never was.
//
// The trusted ROOTS are baked into the binary at build time (a list, so a root
// can be rotated without redistributing every binary) rather than fetched:
// anyone who controls where a public key comes from can mint their own and
// sign themselves a perpetual license.
//
// Nothing here ever fails startup. An absent, malformed or unverifiable key
// resolves to the unlicensed state, which behaves as licensed for a 90-day
// trial and thereafter blocks only the CREATION of new sites and drops.
//
// This package decides nothing and enforces nothing: it verifies a key and
// derives a Status, and the ee package acts on it in exactly two places —
// ee.provider.AuthorizeCreate for the state, and ee.provider.CustomDomainsAllowed
// for the entitlements. Neither is on the serving path.
//
// (Directory is "licensing" rather than "license" to avoid colliding with the
// ee/LICENSE file on case-insensitive filesystems.)
//
// Real implementation is under the `ee` build tag; doc.go keeps the community
// build compiling an empty package. Licensed under ee/LICENSE.
package licensing
