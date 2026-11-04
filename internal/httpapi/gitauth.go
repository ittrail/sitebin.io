package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/ittrail/sitebin.io/internal/ext"
	"github.com/ittrail/sitebin.io/internal/ids"
	"github.com/ittrail/sitebin.io/internal/mcp"
	"github.com/ittrail/sitebin.io/internal/provenance"
	"github.com/ittrail/sitebin.io/internal/store"
)

// The Git endpoint's credential gate. See
// docs/superpowers/specs/2026-11-03-site-git-design.md, "The gate" and "OAuth".
//
// Git sends HTTP Basic and nothing else, so the password's SHAPE says what
// the credential is, and each shape is judged by the rule that already holds
// for it elsewhere — the same extension lookup, the same accountStanding, the
// same verifyEdit. A credential that fails is refused: it is never tried again
// as a weaker one. A browser session never counts here, so the endpoint has no
// cookie path and no CSRF surface.

// gitBasicChallenge is the WWW-Authenticate value of the gate's 401. Basic is
// the only scheme Git answers by itself: this is what makes it ask its
// credential helper.
const gitBasicChallenge = `Basic realm="Sitebin"`

// msgGitConsent is the one refusal a person must act on in a browser. The
// placeholder is the instance's account page (apiAccountHint).
const msgGitConsent = "sign in once at %s and accept the updated terms, then push again"

// msgGitUnavailable answers a valid token the stack could not vouch for right
// now: the consent check failed closed, and the same token may work shortly.
const msgGitUnavailable = "the sign-in service cannot be reached right now; try again in a minute"

// msgGitNoWrite and msgGitNoRead answer an OAuth grant without the scope the
// request needs. A readable refusal, not a 403, which Git shows as a bare
// status: signing in again with the same grant would not help, approving
// the scope would.
const (
	msgGitNoWrite = "this sign-in was not granted write access; sign in again and approve writing"
	msgGitNoRead  = "this sign-in was not granted read access; sign in again and approve reading"
)

// gitDenial is how the Git handler answers a refused request:
//   - challenge: 401 with WWW-Authenticate: gitBasicChallenge — no credential,
//     or one that failed. Git asks its credential helper again.
//   - refusal: a readable refusal, gitrepo.WriteRefusal with msg, which Git
//     prints as "fatal: remote error: <msg>" (it answers 200: Git reads an
//     ERR line only from an advertisement).
//   - otherwise: status, with msg as a plain body.
type gitDenial struct {
	status    int
	msg       string
	challenge bool
	refusal   bool
}

func gitChallenge() *gitDenial {
	return &gitDenial{status: http.StatusUnauthorized, msg: "authentication required", challenge: true}
}

func gitRefusal(msg string) *gitDenial {
	return &gitDenial{status: http.StatusOK, msg: msg, refusal: true}
}

// gitAuth decides who a Git request's Basic credential is, for site, and
// whether it may do what the request asks (write: a push). The username is
// never checked. It returns the actor to record, or why not:
//
//  1. no Basic password: a challenge;
//  2. an upload token (anywhere on the request): 403 — it opens only its own
//     routes, and is never tried as a password;
//  3. an account token (sbp_): tokenAccount, the JSON API's own rule — its
//     account must stand on the site (its owner or a live admin), and an
//     OAuth credential never counts; anything else is a challenge;
//  4. a compact JWS (three base64url segments): an OAuth access token, asked
//     about with the Git marker — the only way the extension verifies one
//     here. Its account must stand on the site (accountStanding) and it must
//     hold the scope (read to fetch, write to push: a readable refusal
//     otherwise). A refusal the verifier notes — consent outstanding, or the
//     stack unavailable — is a readable refusal; any other is a challenge;
//  5. anything else: the site's edit password, through verifyEdit (its rate
//     limits and verify cache): 429 when throttled, a challenge when wrong.
//
// The actor's role is the standing the credential earned, from the very call
// that authenticated it (tokenAccount, accountStanding; rolePassword for the
// edit password), and never worked out again: the push's re-check after its
// registration (gitStandingHolds) compares it with meta.json as it is then.
//
// What follows authentication — a locked site, the plan, an anonymous site on
// an accounts instance — is the handler's, not the gate's.
func (a *API) gitAuth(r *http.Request, site *store.Site, write bool) (actor, *gitDenial) {
	_, pw, ok := r.BasicAuth()
	if !ok || pw == "" {
		return actor{}, gitChallenge()
	}
	// uploadCredential is the one place an upload token is recognised.
	if uploadCredential(r) != "" {
		return actor{}, &gitDenial{status: http.StatusForbidden, msg: msgUploadTokenOnlyUploads}
	}

	switch {
	case strings.HasPrefix(pw, ids.APITokenPrefix):
		account, st := a.tokenAccount(gitBearerRequest(r.Context(), r, pw), site)
		if st == standNone {
			return actor{}, gitChallenge()
		}
		return actor{surface: provenance.SurfaceGit, auth: provenance.AuthToken, account: account, role: st.role()}, nil

	case jwsShaped(pw):
		ctx := ext.WithRefusalNote(ext.WithGitCaller(r.Context()))
		cred, ok := bearerCredential(gitBearerRequest(ctx, r, pw))
		if !ok {
			switch ext.RefusalFrom(ctx) {
			case ext.RefusalConsent:
				return actor{}, gitRefusal(a.gitConsentMessage())
			case ext.RefusalUnavailable:
				return actor{}, gitRefusal(msgGitUnavailable)
			}
			return actor{}, gitChallenge()
		}
		if !cred.OAuth {
			return actor{}, gitChallenge()
		}
		st := a.accountStanding(site, cred.AccountID)
		if st == standNone {
			return actor{}, gitChallenge()
		}
		scope, missing := mcp.ScopeRead, msgGitNoRead
		if write {
			scope, missing = mcp.ScopeWrite, msgGitNoWrite
		}
		if !mcp.Allows(cred.Scopes, cred.OAuth, scope) {
			return actor{}, gitRefusal(missing)
		}
		return actor{surface: provenance.SurfaceGit, auth: provenance.AuthOAuth, account: cred.AccountID, role: st.role()}, nil
	}

	switch a.verifyEdit(r, site, pw) {
	case verifyOK:
		return actor{surface: provenance.SurfaceGit, auth: provenance.AuthPassword, role: rolePassword}, nil
	case verifyThrottled:
		return actor{}, &gitDenial{status: http.StatusTooManyRequests, msg: "too many password attempts, slow down"}
	default:
		return actor{}, gitChallenge()
	}
}

// gitBearerRequest is the copy of r the extension is asked about: it carries
// ctx, presents secret as its bearer and has no cookies, so whatever the
// extension does with it, a browser session is not part of it. The copy is
// the extension's alone — a marker in ctx never reaches the request the
// handler serves.
func gitBearerRequest(ctx context.Context, r *http.Request, secret string) *http.Request {
	r2 := r.Clone(ctx)
	r2.Header.Del("Cookie")
	r2.Header.Set("Authorization", "Bearer "+secret)
	return r2
}

// gitConsentMessage is the readable refusal for a token whose subject has a
// document outstanding: signing in once in a browser passes the consent gate,
// and the same refresh token works on the next push, because refusals are
// never cached.
func (a *API) gitConsentMessage() string {
	return fmt.Sprintf(msgGitConsent, a.apiAccountHint())
}

// jwsShaped reports whether s is a compact JWS — three non-empty base64url
// segments joined by dots — which is how an OAuth access token travels. An
// edit password never is one (22 base62 characters, no dot), and account and
// upload tokens are told apart by their prefixes before this is asked.
func jwsShaped(s string) bool {
	if strings.Count(s, ".") != 2 {
		return false
	}
	for _, seg := range strings.Split(s, ".") {
		if seg == "" {
			return false
		}
		for i := 0; i < len(seg); i++ {
			c := seg[i]
			if !('A' <= c && c <= 'Z' || 'a' <= c && c <= 'z' || '0' <= c && c <= '9' || c == '-' || c == '_') {
				return false
			}
		}
	}
	return true
}
