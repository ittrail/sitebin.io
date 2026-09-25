package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/ittrail/sitebin.io/internal/ids"
)

// Upload tokens let an agent upload files with its own HTTP client instead of
// passing them to an MCP tool as arguments, which a model can only do by
// writing them out token by token. open_upload issues one; WebDAV and the
// JSON API's file upload accept it. Design:
// docs/superpowers/specs/2026-09-25-mcp-upload-tokens-design.md.
//
// They live in memory and nowhere else. A credential that lasts minutes has
// no business in the filesystem database, and a restart that invalidates all
// of them costs an agent one more open_upload call.
const (
	// uploadTokenIdle is how long a token survives after its last request
	// ENDS. Counting from the end is what keeps a long upload from losing its
	// token halfway.
	uploadTokenIdle = 5 * time.Minute
	// uploadTokenMaxAge caps a token however it is used. Without it a leaked
	// token — the result sits in an agent's transcript — could be kept alive
	// indefinitely by touching it every few minutes.
	uploadTokenMaxAge = 60 * time.Minute
	// uploadTokensPerSite bounds one site's live tokens; opening another
	// evicts the oldest.
	uploadTokensPerSite = 5
	// uploadTokensMax is the instance-wide backstop.
	uploadTokensMax = 10000
)

// errTooManyUploads is shown to the agent as-is.
var errTooManyUploads = errors.New("too many open uploads on this instance, try again later")

type uploadToken struct {
	viewID   string // the revocation handle
	editID   string // the one site the token opens
	issuedAt time.Time
	lastSeen time.Time
	inflight int
}

// live reports whether t may start a request at now.
func (t *uploadToken) live(now time.Time) bool {
	if !now.Before(t.issuedAt.Add(uploadTokenMaxAge)) {
		return false
	}
	return t.inflight > 0 || now.Before(t.lastSeen.Add(uploadTokenIdle))
}

// uploadTokens is the registry. It is keyed by sha256(secret) and never holds
// the secret itself.
type uploadTokens struct {
	mu      sync.Mutex
	now     func() time.Time
	perSite int
	max     int
	m       map[string]*uploadToken
}

func newUploadTokens(now func() time.Time) *uploadTokens {
	return &uploadTokens{
		now:     now,
		perSite: uploadTokensPerSite,
		max:     uploadTokensMax,
		m:       make(map[string]*uploadToken),
	}
}

func hashUploadToken(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

// issue mints a token for one site. It returns the secret — the only moment
// it exists outside the caller — and the time the token dies however it is
// used.
func (u *uploadTokens) issue(viewID, editID string) (string, time.Time, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	now := u.now()
	u.pruneLocked(now)

	count, oldestKey := 0, ""
	var oldest time.Time
	for k, t := range u.m {
		if t.viewID != viewID {
			continue
		}
		count++
		if oldestKey == "" || t.issuedAt.Before(oldest) {
			oldestKey, oldest = k, t.issuedAt
		}
	}
	if count >= u.perSite {
		delete(u.m, oldestKey)
	}
	if len(u.m) >= u.max {
		return "", time.Time{}, errTooManyUploads
	}
	secret := ids.NewUploadToken()
	u.m[hashUploadToken(secret)] = &uploadToken{viewID: viewID, editID: editID, issuedAt: now, lastSeen: now}
	return secret, now.Add(uploadTokenMaxAge), nil
}

// begin admits one request presenting secret for the site editID. ok=false is
// a refusal — unknown, expired, or another site's token. On success, end must
// be called when the request finishes; calling it more than once is harmless.
func (u *uploadTokens) begin(secret, editID string) (end func(), ok bool) {
	if !strings.HasPrefix(secret, ids.UploadTokenPrefix) {
		return nil, false
	}
	key := hashUploadToken(secret)
	u.mu.Lock()
	defer u.mu.Unlock()
	now := u.now()
	t, found := u.m[key]
	if !found {
		return nil, false
	}
	if !t.live(now) {
		if t.inflight == 0 {
			delete(u.m, key)
		}
		return nil, false
	}
	if t.editID != editID {
		return nil, false
	}
	t.inflight++
	t.lastSeen = now
	var once sync.Once
	return func() {
		once.Do(func() {
			u.mu.Lock()
			defer u.mu.Unlock()
			t.inflight--
			t.lastSeen = u.now()
		})
	}, true
}

// revokeSite drops every token for the site viewID. A request already running
// on one completes; the next is refused.
func (u *uploadTokens) revokeSite(viewID string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	for k, t := range u.m {
		if t.viewID == viewID {
			delete(u.m, k)
		}
	}
}

// pruneLocked forgets tokens that can never be used again.
func (u *uploadTokens) pruneLocked(now time.Time) {
	for k, t := range u.m {
		if t.inflight == 0 && !t.live(now) {
			delete(u.m, k)
		}
	}
}
