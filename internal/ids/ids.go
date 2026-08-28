// Package ids generates the random identifiers and secrets Sitebin relies on
// for access control: view ids (subdomain labels), edit ids, and edit
// passwords. All are drawn from crypto/rand.
package ids

import (
	"crypto/rand"
	"strings"
)

// base32Lower is subdomain- and path-safe. 26 chars * 5 bits = 130 bits.
const base32Lower = "abcdefghijklmnopqrstuvwxyz234567"

const base62 = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

const idLen = 26

// New returns a 26-char lowercase base32 id (~130 bits). It backs the view and
// edit ids and is used for any other unguessable identifier (e.g. accounts).
func New() string { return randomString(base32Lower, idLen) }

// NewViewID returns a 26-char lowercase base32 id used as the site's
// subdomain label and folder name.
func NewViewID() string { return New() }

// NewEditID returns a 26-char lowercase base32 id used as the edit-URL path
// token.
func NewEditID() string { return New() }

// NewAPIToken returns an account API token: the fixed prefix "sbp_" followed
// by a 40-char base62 secret (~238 bits). The prefix exists so the string is
// recognisable in logs and by secret scanners, and so a leaked token can be
// identified as Sitebin's without trying it.
func NewAPIToken() string { return APITokenPrefix + randomString(base62, 40) }

// APITokenPrefix marks a string as a Sitebin account API token.
const APITokenPrefix = "sbp_"

// NewEditPassword returns a 22-char base62 secret (~131 bits).
func NewEditPassword() string { return randomString(base62, 22) }

// ValidID reports whether s is a well-formed view/edit id.
func ValidID(s string) bool {
	if len(s) != idLen {
		return false
	}
	for _, c := range s {
		if !strings.ContainsRune(base32Lower, c) {
			return false
		}
	}
	return true
}

// randomString samples uniformly from alphabet via rejection sampling.
func randomString(alphabet string, n int) string {
	limit := 256 - 256%len(alphabet) // rejection threshold for uniformity
	out := make([]byte, 0, n)
	buf := make([]byte, n*2)
	for len(out) < n {
		if _, err := rand.Read(buf); err != nil {
			panic("ids: crypto/rand failed: " + err.Error())
		}
		for _, b := range buf {
			if int(b) < limit {
				out = append(out, alphabet[int(b)%len(alphabet)])
				if len(out) == n {
					break
				}
			}
		}
	}
	return string(out)
}
