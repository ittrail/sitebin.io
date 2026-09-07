// Package authn is the Sitebin Enterprise Edition authentication logic:
// local signup, login and password change over the account store using the
// core Argon2id helpers, and sign-in through OpenID Connect providers (Google,
// Microsoft, any generic issuer).
//
// Real implementation is under the `ee` build tag; doc.go keeps the community
// build compiling an empty package. Licensed under ee/LICENSE.
package authn
