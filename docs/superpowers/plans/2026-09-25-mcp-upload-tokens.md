# Upload Tokens for Large MCP Uploads — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A new MCP tool, `open_upload`, hands an agent a short-lived single-site token so it can upload large files with its own HTTP client (WebDAV or the JSON API's file upload) instead of writing them out as tool arguments.

**Architecture:** An in-memory registry in `internal/httpapi` holds `sha256(secret)` → site, issue time, last use and in-flight count. `open_upload` (MCP, authorized exactly like `write_files`) issues a token; the WebDAV handler and a new `withUploadAuth` wrapper on `POST /api/sites/{editID}/files` accept it; every other edit route refuses any `sbu_` credential before password work. All core (MIT), no `ext` seam change.

**Tech Stack:** Go (stdlib `net/http`, `crypto/sha256`), `golang.org/x/net/webdav`, `github.com/modelcontextprotocol/go-sdk/mcp`, PowerShell 5.1 E2E with `curl.exe` and Docker.

**Spec:** `docs/superpowers/specs/2026-09-25-mcp-upload-tokens-design.md`

## Global Constraints

- Work on branch `feat/mcp-upload-tokens` off `main` in `C:\Projects\Sitebin-Project\Sitebin`.
- All code is core: `internal/ids`, `internal/config`, `internal/httpapi`, `internal/mcp`. Nothing in `ee/`, no new `ext` method.
- Token format: `sbu_` + 40 base62 characters (~238 bits), from `ids.NewUploadToken`.
- Lifetime: idle **5 minutes measured from the end of the last request**; absolute **60 minutes** from issuance; a request in flight never idles out; a request running at the cap completes, the next is refused.
- Caps: **5** live tokens per site (the sixth evicts the oldest), **10,000** instance-wide (then "too many open uploads on this instance, try again later").
- Durations and caps are named constants, not configuration.
- Stored as `sha256(secret)` in memory only; the secret is never logged (log the first 10 characters at most).
- Accepted only on `/dav/{editID}/…` (all methods) and `POST /api/sites/{editID}/files`; read from `Authorization: Bearer`, the Basic-auth password, or `X-Edit-Password`; never from the URL.
- A `sbu_` credential is answered only as an upload token — never tried as an edit password or account token.
- Refusal texts, verbatim: `upload token unknown or expired — call open_upload for a new one` (401) and `an upload token can only upload files — use the edit password or an account API token for anything else` (403).
- WebDAV with a token ignores the per-site `webdav_enabled` toggle and the plan's `quota_webdav`, but respects `SITEBIN_WEBDAV_ENABLED=false` (route stays 404, no `webdav_url` offered).
- Tool name `open_upload` is a contract; it needs scope `sitebin:sites:write`. `create_site`/`write_files` behave exactly as before.
- Revoked on edit-password rotation and site deletion (the paths that already call `verifyCache.Drop`), and by restart.
- `e2e/*.ps1` stay pure ASCII (write `--`, never an em dash).
- Run both `go test ./...` and `go test -tags ee ./...`, plus `go vet ./...`.
- Commits: lowercase conventional prefix, subject is a sentence about behaviour, trailer `Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>`. The website repo is committed but **never pushed** by this plan (ship order).

## Review Focus

1. `Authorization: bearer sbu_…` in lower case must be recognised as an upload token, not fall through to the password path — test in Task 2.
2. A request carrying a bad upload token *and* a correct edit password must be refused (401), never admitted on the password — test in Task 3.
3. An agent that opens a sixth upload while its first is still uploading: the running request completes without panicking, and the evicted token refuses the next request — test in Task 1.
4. A token used after its site was deleted must get a clean `404`, not a `500` — test in Task 4.
5. The token must work for WebDAV's non-PUT methods an agent uses to clean up (`PROPFIND` listing, `DELETE`) — test in Task 2.

---

### Task 1: Upload-token registry

**Files:**
- Modify: `internal/ids/ids.go` (after `APITokenPrefix`, ~line 37)
- Modify: `internal/ids/ids_test.go` (append)
- Create: `internal/httpapi/uploadtokens.go`
- Create: `internal/httpapi/uploadtokens_test.go`
- Modify: `internal/httpapi/server.go` (`API` struct ~line 36-54, `New` ~line 58-80)

**Interfaces:**
- Consumes: nothing new.
- Produces:
  - `ids.UploadTokenPrefix = "sbu_"`, `ids.NewUploadToken() string`
  - constants `uploadTokenIdle`, `uploadTokenMaxAge`, `uploadTokensPerSite`, `uploadTokensMax`; `var errTooManyUploads error`
  - `type uploadTokens struct { mu sync.Mutex; now func() time.Time; perSite, max int; m map[string]*uploadToken }`
  - `type uploadToken struct { viewID, editID string; issuedAt, lastSeen time.Time; inflight int }`
  - `func newUploadTokens(now func() time.Time) *uploadTokens`
  - `func (u *uploadTokens) issue(viewID, editID string) (secret string, expiresAt time.Time, err error)`
  - `func (u *uploadTokens) begin(secret, editID string) (end func(), ok bool)`
  - `func (u *uploadTokens) revokeSite(viewID string)`
  - `func hashUploadToken(secret string) string`
  - field `API.uploads *uploadTokens`, set in `New` to `newUploadTokens(time.Now)`
  - test helpers `type uploadClock struct{ t time.Time }` with `now()`/`advance(d)`, `func newTestUploads() (*uploadTokens, *uploadClock)`

- [ ] **Step 1: Write the failing ids test**

Append to `internal/ids/ids_test.go`:

```go
func TestNewUploadToken(t *testing.T) {
	a, b := NewUploadToken(), NewUploadToken()
	if !strings.HasPrefix(a, UploadTokenPrefix) || len(a) != len(UploadTokenPrefix)+40 {
		t.Fatalf("bad upload token %q", a)
	}
	if a == b {
		t.Fatal("two upload tokens collided")
	}
	if UploadTokenPrefix == APITokenPrefix {
		t.Fatal("upload and API tokens must be told apart by prefix")
	}
	// Every credential check recognises an upload token by its prefix, so no
	// edit password may ever carry it.
	for i := 0; i < 500; i++ {
		if strings.HasPrefix(NewEditPassword(), UploadTokenPrefix) {
			t.Fatal("an edit password carries the upload-token prefix")
		}
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./internal/ids/ -run TestNewUploadToken`
Expected: FAIL — `undefined: NewUploadToken`.

- [ ] **Step 3: Implement the generator**

In `internal/ids/ids.go`, after `const APITokenPrefix = "sbp_"`:

```go
// NewUploadToken returns a short-lived upload token: the prefix "sbu_" and a
// 40-char base62 secret (~238 bits), the strength of an account API token.
// The prefix is how every credential check recognises one before doing
// anything else, and how a secret scanner recognises a leaked one.
func NewUploadToken() string { return UploadTokenPrefix + randomString(base62, 40) }

// UploadTokenPrefix marks a string as a Sitebin upload token. No edit password
// can start with it: those are base62, which has no underscore.
const UploadTokenPrefix = "sbu_"
```

- [ ] **Step 4: Run it to verify it passes**

Run: `go test ./internal/ids/`
Expected: PASS.

- [ ] **Step 5: Write the failing registry tests**

Create `internal/httpapi/uploadtokens_test.go`:

```go
package httpapi

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ittrail/sitebin.io/internal/ids"
)

type uploadClock struct{ t time.Time }

func (c *uploadClock) now() time.Time          { return c.t }
func (c *uploadClock) advance(d time.Duration) { c.t = c.t.Add(d) }

func newTestUploads() (*uploadTokens, *uploadClock) {
	c := &uploadClock{t: time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)}
	return newUploadTokens(c.now), c
}

func TestUploadTokenOpensItsSiteOnly(t *testing.T) {
	u, _ := newTestUploads()
	secret, exp, err := u.issue("view1", "edit1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(secret, ids.UploadTokenPrefix) {
		t.Fatalf("secret = %q", secret)
	}
	if want := time.Date(2026, 9, 25, 13, 0, 0, 0, time.UTC); !exp.Equal(want) {
		t.Errorf("expires = %v, want %v", exp, want)
	}
	end, ok := u.begin(secret, "edit1")
	if !ok {
		t.Fatal("a fresh token was refused")
	}
	end()
	end() // a second call is harmless
	if _, ok := u.begin(secret, "edit2"); ok {
		t.Fatal("the token opened another site")
	}
	if _, ok := u.begin("sbu_nottherealone", "edit1"); ok {
		t.Fatal("an unknown token was accepted")
	}
	if _, ok := u.begin("", "edit1"); ok {
		t.Fatal("an empty credential was accepted")
	}
}

func TestUploadTokenHoldsOnlyTheHash(t *testing.T) {
	u, _ := newTestUploads()
	secret, _, _ := u.issue("view1", "edit1")
	for k := range u.m {
		if strings.Contains(k, secret[len(ids.UploadTokenPrefix):]) {
			t.Fatal("the secret is held in the clear")
		}
		if k != hashUploadToken(secret) {
			t.Fatalf("key %q is not the secret's hash", k)
		}
	}
}

func TestUploadTokenUnusedDiesAfterFiveMinutes(t *testing.T) {
	u, c := newTestUploads()
	secret, _, _ := u.issue("view1", "edit1")
	c.advance(5 * time.Minute)
	if _, ok := u.begin(secret, "edit1"); ok {
		t.Fatal("an unused token outlived its first five minutes")
	}
}

func TestUploadTokenIdleCountsFromTheEndOfTheLastRequest(t *testing.T) {
	u, c := newTestUploads()
	secret, _, _ := u.issue("view1", "edit1")
	c.advance(4 * time.Minute)
	end, ok := u.begin(secret, "edit1")
	if !ok {
		t.Fatal("refused within the idle window")
	}
	c.advance(20 * time.Minute) // a long upload is still running
	end2, ok := u.begin(secret, "edit1")
	if !ok {
		t.Fatal("a token with a request in flight idled out")
	}
	end2()
	end()
	c.advance(4*time.Minute + 59*time.Second)
	end, ok = u.begin(secret, "edit1")
	if !ok {
		t.Fatal("the idle window did not restart when the requests ended")
	}
	end()
	c.advance(5 * time.Minute)
	if _, ok := u.begin(secret, "edit1"); ok {
		t.Fatal("the token outlived five idle minutes")
	}
}

func TestUploadTokenAbsoluteCap(t *testing.T) {
	u, c := newTestUploads()
	secret, _, _ := u.issue("view1", "edit1")
	for minute := 4; minute <= 56; minute += 4 {
		c.advance(4 * time.Minute)
		end, ok := u.begin(secret, "edit1")
		if !ok {
			t.Fatalf("refused at minute %d although it was kept busy", minute)
		}
		end()
	}
	c.advance(3 * time.Minute) // minute 59
	running, ok := u.begin(secret, "edit1")
	if !ok {
		t.Fatal("refused at minute 59")
	}
	c.advance(time.Minute) // minute 60
	if _, ok := u.begin(secret, "edit1"); ok {
		t.Fatal("a new request started at the 60-minute cap")
	}
	running() // the request already running completes
	if _, ok := u.begin(secret, "edit1"); ok {
		t.Fatal("the token outlived its cap")
	}
}

func TestUploadTokenPerSiteCapEvictsTheOldest(t *testing.T) {
	u, c := newTestUploads()
	other, _, _ := u.issue("view2", "edit2")
	var secrets []string
	for i := 0; i < uploadTokensPerSite+1; i++ {
		c.advance(time.Second) // distinct issue times, so "oldest" is well defined
		s, _, err := u.issue("view1", "edit1")
		if err != nil {
			t.Fatal(err)
		}
		secrets = append(secrets, s)
	}
	if _, ok := u.begin(secrets[0], "edit1"); ok {
		t.Error("the oldest token survived a sixth")
	}
	for _, s := range secrets[1:] {
		if end, ok := u.begin(s, "edit1"); !ok {
			t.Error("a newer token was evicted")
		} else {
			end()
		}
	}
	if end, ok := u.begin(other, "edit2"); !ok {
		t.Error("another site's token was evicted")
	} else {
		end()
	}
}

// Review focus 3: an agent that opens more uploads while its first is still
// running must not crash anything, and the evicted token refuses afterwards.
func TestUploadTokenEvictedWhileInFlight(t *testing.T) {
	u, c := newTestUploads()
	first, _, _ := u.issue("view1", "edit1")
	running, ok := u.begin(first, "edit1")
	if !ok {
		t.Fatal("fresh token refused")
	}
	for i := 0; i < uploadTokensPerSite; i++ {
		c.advance(time.Second)
		if _, _, err := u.issue("view1", "edit1"); err != nil {
			t.Fatal(err)
		}
	}
	running() // must not panic on an evicted record
	if _, ok := u.begin(first, "edit1"); ok {
		t.Fatal("an evicted token still opens the site")
	}
}

func TestUploadTokenGlobalBackstop(t *testing.T) {
	u, c := newTestUploads()
	u.max = 2
	u.issue("a", "ea")
	u.issue("b", "eb")
	if _, _, err := u.issue("c", "ec"); !errors.Is(err, errTooManyUploads) {
		t.Fatalf("err = %v, want errTooManyUploads", err)
	}
	c.advance(5 * time.Minute) // both expire unused and are pruned on the next issue
	if _, _, err := u.issue("c", "ec"); err != nil {
		t.Fatalf("expired tokens still counted: %v", err)
	}
}

func TestUploadTokenRevokeSite(t *testing.T) {
	u, _ := newTestUploads()
	a, _, _ := u.issue("view1", "edit1")
	b, _, _ := u.issue("view2", "edit2")
	u.revokeSite("view1")
	if _, ok := u.begin(a, "edit1"); ok {
		t.Error("a revoked token still works")
	}
	if end, ok := u.begin(b, "edit2"); !ok {
		t.Error("revoking one site revoked another")
	} else {
		end()
	}
}
```

- [ ] **Step 6: Run them to verify they fail**

Run: `go test ./internal/httpapi/ -run TestUploadToken`
Expected: FAIL to compile — `undefined: newUploadTokens`.

- [ ] **Step 7: Implement the registry**

Create `internal/httpapi/uploadtokens.go`:

```go
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
```

In `internal/httpapi/server.go`, add to the `API` struct after `davLockSystems *davLocks`:

```go
	uploads        *uploadTokens
```

and in `New`, after `davLockSystems: newDavLocks(),`:

```go
		uploads:        newUploadTokens(time.Now),
```

- [ ] **Step 8: Run the tests to verify they pass**

Run: `go test ./internal/httpapi/ -run TestUploadToken -v`
Expected: PASS, all nine tests.

- [ ] **Step 9: Commit**

```bash
git add internal/ids/ids.go internal/ids/ids_test.go internal/httpapi/uploadtokens.go internal/httpapi/uploadtokens_test.go internal/httpapi/server.go
git commit -m "feat: an in-memory registry issues single-site upload tokens that idle out five minutes after their last request

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>"
```

---

### Task 2: WebDAV accepts upload tokens

**Files:**
- Create: `internal/httpapi/uploadauth.go`
- Create: `internal/httpapi/uploadauth_test.go`
- Modify: `internal/httpapi/webdav.go:46-81` (the auth block of `webdav`)

**Interfaces:**
- Consumes: `API.uploads`, `(*uploadTokens).issue/begin`, `uploadTokenIdle`, `uploadClock` (Task 1); existing test helpers `newEnv`, `env.createSite`, `env.public`, `editIDFrom`, `davReq`, `createResp`, `fakeProvider` (all in package `httpapi` tests).
- Produces:
  - `func uploadCredential(r *http.Request) string`
  - `const msgUploadTokenRefused`, `const msgUploadTokenOnlyUploads` (texts from Global Constraints)
  - test helpers `func uploadTokenFor(t *testing.T, e *env, c createResp) string` and `func bearer(req *http.Request, token string) *http.Request`

- [ ] **Step 1: Write the failing tests**

Create `internal/httpapi/uploadauth_test.go`:

```go
package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ittrail/sitebin.io/internal/ext"
)

// uploadTokenFor issues an upload token for a site made by env.createSite,
// the way open_upload does.
func uploadTokenFor(t *testing.T, e *env, c createResp) string {
	t.Helper()
	secret, _, err := e.api.uploads.issue(c.ID, editIDFrom(t, c.EditURL))
	if err != nil {
		t.Fatal(err)
	}
	return secret
}

func bearer(req *http.Request, token string) *http.Request {
	req.Header.Set("Authorization", "Bearer "+token)
	return req
}

func TestUploadTokenOpensWebDAVWithTheToggleOff(t *testing.T) {
	e := newEnv(t, nil)
	c := e.createSite(t, nil, map[string]string{"index.html": "x"}) // webdav off
	edit := editIDFrom(t, c.EditURL)
	tok := uploadTokenFor(t, e, c)

	req := bearer(httptest.NewRequest("PUT", "/dav/"+edit+"/big.bin", strings.NewReader("payload")), tok)
	if w := e.public(t, req); w.Code != 201 {
		t.Fatalf("PUT with an upload token: %d %s", w.Code, w.Body)
	}
	site, _ := e.st.ByViewID(c.ID)
	if b, err := e.st.ReadContentFile(site, "big.bin"); err != nil || string(b) != "payload" {
		t.Fatalf("big.bin = %q, %v", b, err)
	}
	if site.Meta.WebDAVEnabled {
		t.Error("the upload token switched the site's WebDAV toggle on")
	}
	// The toggle still governs the edit password.
	if w := davReq(t, e, "GET", "/dav/"+edit+"/big.bin", c.EditPassword, nil); w.Code != 404 {
		t.Errorf("edit password over WebDAV with the toggle off: %d", w.Code)
	}
}

// Review focus 5: an agent lists and cleans up with the same token.
func TestUploadTokenListsAndDeletesOverWebDAV(t *testing.T) {
	e := newEnv(t, nil)
	c := e.createSite(t, nil, map[string]string{"index.html": "x", "old.txt": "o"})
	edit := editIDFrom(t, c.EditURL)
	tok := uploadTokenFor(t, e, c)

	req := bearer(httptest.NewRequest("PROPFIND", "/dav/"+edit+"/", nil), tok)
	req.Header.Set("Depth", "1")
	if w := e.public(t, req); w.Code != 207 || !strings.Contains(w.Body.String(), "old.txt") {
		t.Fatalf("PROPFIND with a token: %d %s", w.Code, w.Body)
	}
	if w := e.public(t, bearer(httptest.NewRequest("DELETE", "/dav/"+edit+"/old.txt", nil), tok)); w.Code != 204 {
		t.Fatalf("DELETE with a token: %d %s", w.Code, w.Body)
	}
}

func TestUploadTokenWorksAsTheBasicAuthPassword(t *testing.T) {
	e := newEnv(t, nil)
	c := e.createSite(t, nil, map[string]string{"index.html": "x"})
	tok := uploadTokenFor(t, e, c)
	if w := davReq(t, e, "PUT", "/dav/"+editIDFrom(t, c.EditURL)+"/a.txt", tok, strings.NewReader("a")); w.Code != 201 {
		t.Fatalf("PUT with the token as Basic password: %d %s", w.Code, w.Body)
	}
}

// Review focus 1: "bearer" is case-insensitive in HTTP, and an agent's client
// may send it in lower case.
func TestUploadTokenLowerCaseBearer(t *testing.T) {
	e := newEnv(t, nil)
	c := e.createSite(t, nil, map[string]string{"index.html": "x"})
	tok := uploadTokenFor(t, e, c)
	req := httptest.NewRequest("PUT", "/dav/"+editIDFrom(t, c.EditURL)+"/a.txt", strings.NewReader("a"))
	req.Header.Set("Authorization", "bearer "+tok)
	if w := e.public(t, req); w.Code != 201 {
		t.Fatalf("lower-case bearer: %d %s", w.Code, w.Body)
	}
}

func TestUploadTokenRespectsTheInstanceWebDAVSwitch(t *testing.T) {
	e := newEnv(t, map[string]string{"SITEBIN_WEBDAV_ENABLED": "false"})
	c := e.createSite(t, nil, map[string]string{"index.html": "x"})
	tok := uploadTokenFor(t, e, c)
	req := bearer(httptest.NewRequest("PUT", "/dav/"+editIDFrom(t, c.EditURL)+"/a.txt", strings.NewReader("a")), tok)
	if w := e.public(t, req); w.Code != 404 {
		t.Fatalf("WebDAV off instance-wide, upload token: %d", w.Code)
	}
}

func TestUploadTokenIsBoundToOneSiteOverWebDAV(t *testing.T) {
	e := newEnv(t, nil)
	a := e.createSite(t, nil, map[string]string{"index.html": "a"})
	b := e.createSite(t, map[string]string{"webdav": "true"}, map[string]string{"index.html": "b"})
	tok := uploadTokenFor(t, e, a)
	req := bearer(httptest.NewRequest("PUT", "/dav/"+editIDFrom(t, b.EditURL)+"/x.txt", strings.NewReader("x")), tok)
	if w := e.public(t, req); w.Code != 401 {
		t.Fatalf("site A's token on site B: %d", w.Code)
	}
}

func TestExpiredUploadTokenRefusedOverWebDAV(t *testing.T) {
	e := newEnv(t, nil)
	clock := &uploadClock{t: time.Now()}
	e.api.uploads.now = clock.now
	c := e.createSite(t, nil, map[string]string{"index.html": "x"})
	tok := uploadTokenFor(t, e, c)
	clock.advance(uploadTokenIdle)
	req := bearer(httptest.NewRequest("PUT", "/dav/"+editIDFrom(t, c.EditURL)+"/x.txt", strings.NewReader("x")), tok)
	w := e.public(t, req)
	if w.Code != 401 || !strings.Contains(w.Body.String(), "open_upload") {
		t.Fatalf("expired token: %d %s", w.Code, w.Body)
	}
}

func TestUploadTokenCannotOpenAnAnonymousSiteOnAGatedInstance(t *testing.T) {
	e := newEnv(t, nil)
	site, _, err := e.st.Create()
	if err != nil {
		t.Fatal(err)
	}
	ext.Register(&fakeProvider{enabled: true})
	defer ext.Reset()
	tok, _, _ := e.api.uploads.issue(site.ViewID, site.EditID)
	req := bearer(httptest.NewRequest("PUT", "/dav/"+site.EditID+"/x.txt", strings.NewReader("x")), tok)
	if w := e.public(t, req); w.Code != 403 {
		t.Fatalf("anonymous site on a gated instance: %d %s", w.Code, w.Body)
	}
}

func TestUploadTokenKeepsTheSiteQuota(t *testing.T) {
	ext.Register(&fakeProvider{enabled: true, owner: "acct-1", grant: ext.CreateGrant{MaxSiteBytes: 20}})
	defer ext.Reset()
	e := newEnv(t, nil)
	c := e.createSite(t, nil, map[string]string{"index.html": "x"})
	tok := uploadTokenFor(t, e, c)
	req := bearer(httptest.NewRequest("PUT", "/dav/"+editIDFrom(t, c.EditURL)+"/big.txt", strings.NewReader(strings.Repeat("a", 30))), tok)
	if w := e.public(t, req); w.Code != http.StatusInsufficientStorage {
		t.Fatalf("over the tier byte quota: %d %s", w.Code, w.Body)
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/httpapi/ -run "UploadToken|ExpiredUploadToken"`
Expected: FAIL — the WebDAV tests get `404` (toggle off) or `401` instead of `201`.

- [ ] **Step 3: Implement `uploadCredential` and the texts**

Create `internal/httpapi/uploadauth.go`:

```go
package httpapi

import (
	"net/http"
	"strings"

	"github.com/ittrail/sitebin.io/internal/ids"
)

const (
	msgUploadTokenRefused     = "upload token unknown or expired — call open_upload for a new one"
	msgUploadTokenOnlyUploads = "an upload token can only upload files — use the edit password or an account API token for anything else"
)

// uploadCredential returns the upload token a request presents, or "" when it
// presents none. A token is recognised by its prefix wherever a password can
// travel — Authorization: Bearer, the password of Basic auth, X-Edit-Password
// — so that it is answered only as an upload token and never tried as
// anything else. No edit password can carry the prefix (they are base62, which
// has no underscore), and account API tokens have their own.
func uploadCredential(r *http.Request) string {
	const scheme = "Bearer "
	if h := r.Header.Get("Authorization"); len(h) > len(scheme) && strings.EqualFold(h[:len(scheme)], scheme) {
		if s := strings.TrimSpace(h[len(scheme):]); strings.HasPrefix(s, ids.UploadTokenPrefix) {
			return s
		}
	}
	if _, pw, ok := r.BasicAuth(); ok && strings.HasPrefix(pw, ids.UploadTokenPrefix) {
		return pw
	}
	if pw := r.Header.Get("X-Edit-Password"); strings.HasPrefix(pw, ids.UploadTokenPrefix) {
		return pw
	}
	return ""
}
```

- [ ] **Step 4: Accept the token in the WebDAV handler**

In `internal/httpapi/webdav.go`, replace the block from `site, err := a.st.ByEditID(editID)` down to and including the closing `}` of the `switch a.verifyEdit(r, site, pw)` (currently lines 53-73) with:

```go
	site, err := a.st.ByEditID(editID)
	if err != nil {
		writeError(w, 404, "not found")
		return
	}

	// An upload token opens the tree whatever the site's WebDAV toggle says:
	// it is an agent's upload channel, not the site's network drive, and it
	// grants nothing write_files does not. It is answered only as a token — a
	// bad one is a 401, never a fall-through to the edit password.
	if secret := uploadCredential(r); secret != "" {
		end, ok := a.uploads.begin(secret, editID)
		if !ok {
			w.Header().Set("WWW-Authenticate", `Basic realm="Sitebin WebDAV (password = edit password)"`)
			writeError(w, 401, msgUploadTokenRefused)
			return
		}
		defer end()
	} else {
		if !site.Meta.WebDAVEnabled {
			writeError(w, 404, "not found")
			return
		}
		_, pw, ok := r.BasicAuth()
		if !ok || pw == "" {
			w.Header().Set("WWW-Authenticate", `Basic realm="Sitebin WebDAV (password = edit password)"`)
			writeError(w, 401, "authentication required")
			return
		}
		switch a.verifyEdit(r, site, pw) {
		case verifyThrottled:
			writeError(w, 429, "too many password attempts")
			return
		case verifyFailed:
			w.Header().Set("WWW-Authenticate", `Basic realm="Sitebin WebDAV (password = edit password)"`)
			writeError(w, 401, "wrong edit password")
			return
		}
	}
```

Everything after it (the `gatedAnonymous` check onwards) stays unchanged. Also update the handler's doc comment to:

```go
// webdav serves /dav/{editID}/... — a network-drive view of the site's own
// files, gated by the edit password over HTTP Basic auth, or by an upload
// token from open_upload. Write access equals full edit rights, exactly like
// the API.
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./internal/httpapi/ -run "WebDAV|UploadToken|ExpiredUploadToken"`
Expected: PASS — the new tests and every existing `TestWebDAV*` test.

- [ ] **Step 6: Commit**

```bash
git add internal/httpapi/uploadauth.go internal/httpapi/uploadauth_test.go internal/httpapi/webdav.go
git commit -m "feat: an upload token opens its site's WebDAV tree whatever the site's WebDAV toggle says

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>"
```

---

### Task 3: The JSON API's file upload accepts upload tokens, nothing else does

**Files:**
- Modify: `internal/httpapi/uploadauth.go` (append `withUploadAuth`)
- Modify: `internal/httpapi/server.go:98` (route), `server.go:267-270` (`withEditAuth` start)
- Modify: `internal/httpapi/sites.go:641-643` (`createSite` start)
- Modify: `internal/httpapi/uploadauth_test.go` (append)

**Interfaces:**
- Consumes: `uploadCredential`, `msgUploadTokenRefused`, `msgUploadTokenOnlyUploads`, `uploadTokenFor`, `bearer` (Task 2); `API.uploads` (Task 1); existing `authed`, `storeError`, `writeError`, `a.gatedAnonymous`, `a.apiAccountHint`.
- Produces:
  - `func (a *API) withUploadAuth(next func(http.ResponseWriter, *http.Request, *store.Site)) http.HandlerFunc`
  - test helper `func uploadBody(t *testing.T, zipFiles, files map[string]string) (*bytes.Buffer, string)` — returns the multipart body and its Content-Type; a non-nil `zipFiles` becomes one `zip` part, each `files` entry one `files` part named by its path.

- [ ] **Step 1: Write the failing tests**

Add to the import block of `internal/httpapi/uploadauth_test.go`: `"archive/zip"`, `"bytes"`, `"fmt"`, `"mime/multipart"`, `"net/textproto"`. Append:

```go
// uploadBody builds a multipart upload: zipFiles (when non-nil) become one
// "zip" part holding an archive of them, and each files entry becomes a
// "files" part whose filename is its path.
func uploadBody(t *testing.T, zipFiles, files map[string]string) (*bytes.Buffer, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if zipFiles != nil {
		var zb bytes.Buffer
		zw := zip.NewWriter(&zb)
		for name, content := range zipFiles {
			f, err := zw.Create(name)
			if err != nil {
				t.Fatal(err)
			}
			f.Write([]byte(content))
		}
		if err := zw.Close(); err != nil {
			t.Fatal(err)
		}
		h := textproto.MIMEHeader{}
		h.Set("Content-Disposition", `form-data; name="zip"; filename="site.zip"`)
		p, _ := mw.CreatePart(h)
		p.Write(zb.Bytes())
	}
	for name, content := range files {
		h := textproto.MIMEHeader{}
		h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="files"; filename="%s"`, name))
		p, _ := mw.CreatePart(h)
		p.Write([]byte(content))
	}
	mw.Close()
	return &buf, mw.FormDataContentType()
}

func TestUploadTokenReplacesASiteWithAZip(t *testing.T) {
	e := newEnv(t, nil)
	c := e.createSite(t, nil, map[string]string{"index.html": "old", "old.txt": "x"})
	tok := uploadTokenFor(t, e, c)
	body, ct := uploadBody(t, map[string]string{"index.html": "new", "assets/app.js": "js"}, nil)
	req := bearer(httptest.NewRequest("POST", "/api/sites/"+editIDFrom(t, c.EditURL)+"/files?replace=true", body), tok)
	req.Header.Set("Content-Type", ct)
	if w := e.public(t, req); w.Code != 200 {
		t.Fatalf("zip upload with a token: %d %s", w.Code, w.Body)
	}
	site, _ := e.st.ByViewID(c.ID)
	files, _ := e.st.ListFiles(site)
	got := map[string]bool{}
	for _, f := range files {
		got[f.Path] = true
	}
	if len(files) != 2 || !got["index.html"] || !got["assets/app.js"] {
		t.Fatalf("after replace: %+v", files)
	}
}

func TestUploadTokenStoresAFileAtANestedPath(t *testing.T) {
	e := newEnv(t, nil)
	c := e.createSite(t, nil, map[string]string{"index.html": "x"})
	tok := uploadTokenFor(t, e, c)
	body, ct := uploadBody(t, nil, map[string]string{"media/video.mp4": "frames"})
	req := bearer(httptest.NewRequest("POST", "/api/sites/"+editIDFrom(t, c.EditURL)+"/files", body), tok)
	req.Header.Set("Content-Type", ct)
	if w := e.public(t, req); w.Code != 200 {
		t.Fatalf("nested upload: %d %s", w.Code, w.Body)
	}
	site, _ := e.st.ByViewID(c.ID)
	if b, err := e.st.ReadContentFile(site, "media/video.mp4"); err != nil || string(b) != "frames" {
		t.Fatalf("media/video.mp4 = %q, %v", b, err)
	}
}

// Review focus 2: a credential presented as an upload token either works as
// one or fails. A correct edit password riding along does not rescue it.
func TestBadUploadTokenNeverFallsBackToThePassword(t *testing.T) {
	e := newEnv(t, nil)
	c := e.createSite(t, nil, map[string]string{"index.html": "x"})
	body, ct := uploadBody(t, nil, map[string]string{"a.txt": "a"})
	req := authed(httptest.NewRequest("POST", "/api/sites/"+editIDFrom(t, c.EditURL)+"/files", body), c.EditPassword)
	req = bearer(req, "sbu_notarealtoken")
	req.Header.Set("Content-Type", ct)
	if w := e.public(t, req); w.Code != 401 {
		t.Fatalf("bad token with a good password alongside: %d %s", w.Code, w.Body)
	}
}

func TestUploadTokenIsBoundToOneSiteOverTheAPI(t *testing.T) {
	e := newEnv(t, nil)
	a := e.createSite(t, nil, map[string]string{"index.html": "a"})
	b := e.createSite(t, nil, map[string]string{"index.html": "b"})
	tok := uploadTokenFor(t, e, a)
	body, ct := uploadBody(t, nil, map[string]string{"x.txt": "x"})
	req := bearer(httptest.NewRequest("POST", "/api/sites/"+editIDFrom(t, b.EditURL)+"/files", body), tok)
	req.Header.Set("Content-Type", ct)
	if w := e.public(t, req); w.Code != 401 {
		t.Fatalf("site A's token on site B's upload: %d", w.Code)
	}
}

func TestUploadTokenRefusedEverywhereElse(t *testing.T) {
	e := newEnv(t, map[string]string{"SITEBIN_RATE_AUTH_PER_5MIN": "1"})
	c := e.createSite(t, nil, map[string]string{"index.html": "x"})
	edit := editIDFrom(t, c.EditURL)
	tok := uploadTokenFor(t, e, c)
	for _, rt := range []struct{ method, path, body string }{
		{"GET", "/api/sites/" + edit, ""},
		{"PUT", "/api/sites/" + edit, `{"view_password":"x"}`},
		{"DELETE", "/api/sites/" + edit, ""},
		{"GET", "/api/sites/" + edit + "/download", ""},
		{"GET", "/api/sites/" + edit + "/content/index.html", ""},
		{"DELETE", "/api/sites/" + edit + "/files/index.html", ""},
		{"POST", "/api/sites/" + edit + "/domains", `{"domain":"d.example.com"}`},
		{"GET", "/api/sites/" + edit + "/forms", ""},
		{"POST", "/api/sites", ""},
	} {
		req := bearer(httptest.NewRequest(rt.method, rt.path, strings.NewReader(rt.body)), tok)
		w := e.public(t, req)
		if w.Code != 403 || !strings.Contains(w.Body.String(), "upload token") {
			t.Errorf("%s %s with an upload token: %d %s", rt.method, rt.path, w.Code, w.Body)
		}
	}
	// With a rate limit of one attempt, the edit password still verifies: none
	// of the refusals above reached the rate-limited password path.
	if w := e.public(t, authed(httptest.NewRequest("GET", "/api/sites/"+edit, nil), c.EditPassword)); w.Code != 200 {
		t.Fatalf("edit password after the refusals: %d %s", w.Code, w.Body)
	}
	site, err := e.st.ByViewID(c.ID)
	if err != nil {
		t.Fatal("the site was deleted through an upload token")
	}
	if site.Meta.ViewPasswordProtected {
		t.Error("an upload token changed a setting")
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/httpapi/ -run "UploadToken|BadUploadToken"`
Expected: FAIL — the zip/nested uploads get `401` ("edit password required"), `TestUploadTokenRefusedEverywhereElse` gets `401`s instead of `403`s.

- [ ] **Step 3: Implement `withUploadAuth`**

Append to `internal/httpapi/uploadauth.go` (add `"github.com/ittrail/sitebin.io/internal/store"` to its imports):

```go
// withUploadAuth guards the one JSON API route an upload token may use,
// POST /api/sites/{editID}/files. A request presenting a token is answered
// from the token alone — this site's and live, or a 401 — and anything else
// goes through withEditAuth unchanged.
func (a *API) withUploadAuth(next func(http.ResponseWriter, *http.Request, *store.Site)) http.HandlerFunc {
	editAuth := a.withEditAuth(next)
	return func(w http.ResponseWriter, r *http.Request) {
		secret := uploadCredential(r)
		if secret == "" {
			editAuth(w, r)
			return
		}
		editID := r.PathValue("editID")
		site, err := a.st.ByEditID(editID)
		if err != nil {
			storeError(w, err)
			return
		}
		end, ok := a.uploads.begin(secret, editID)
		if !ok {
			writeError(w, 401, msgUploadTokenRefused)
			return
		}
		defer end()
		// Tokens are issued only for sites MCP could open, but the rule is
		// cheap and belongs on every entry: an account-less site on a gated
		// instance cannot be scripted.
		if a.gatedAnonymous(site) {
			writeError(w, 403, "this site was created without an account, so it has no API — create it while signed in at "+a.apiAccountHint()+" to script it")
			return
		}
		next(w, r, site)
	}
}
```

- [ ] **Step 4: Route the upload through it, and refuse the token everywhere else**

In `internal/httpapi/server.go`, change the files route:

```go
	mux.HandleFunc("POST /api/sites/{editID}/files", a.withUploadAuth(a.uploadFiles))
```

In `withEditAuth`, make the returned function start with:

```go
	return func(w http.ResponseWriter, r *http.Request) {
		// An upload token opens exactly one route, which withUploadAuth
		// guards. Everywhere else it is refused before any password work, so
		// it neither burns the rate limit nor reads as a wrong password.
		if uploadCredential(r) != "" {
			writeError(w, 403, msgUploadTokenOnlyUploads)
			return
		}
		editID := r.PathValue("editID")
```

In `internal/httpapi/sites.go`, `createSite`, directly after `a.createCORS(w, r)`:

```go
	// An upload token belongs to one existing site; it creates nothing.
	if uploadCredential(r) != "" {
		writeError(w, 403, msgUploadTokenOnlyUploads)
		return
	}
```

- [ ] **Step 5: Run the httpapi suite to verify it passes**

Run: `go test ./internal/httpapi/`
Expected: PASS — the new tests and the whole existing suite (`TestUploadAndDeleteFiles`, `TestReplaceAllUpload` prove the password path through `withUploadAuth` still works).

- [ ] **Step 6: Commit**

```bash
git add internal/httpapi/uploadauth.go internal/httpapi/uploadauth_test.go internal/httpapi/server.go internal/httpapi/sites.go
git commit -m "feat: an upload token uploads files through the JSON API and is refused on every other route

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>"
```

---

### Task 4: Revoke a site's upload tokens on password rotation and deletion

**Files:**
- Modify: `internal/httpapi/siteservice.go:110-132` (`RotateEditPassword`, `Delete`)
- Modify: `internal/httpapi/sites.go:730-738` (`deleteSite`)
- Modify: `internal/httpapi/mcpops.go:357-368` (`DeleteSite`)
- Modify: `internal/httpapi/uploadauth_test.go` (append)

**Interfaces:**
- Consumes: `(*uploadTokens).revokeSite`, `uploadTokenFor`, `bearer` (Tasks 1-2); `siteService{a: e.api}`.
- Produces: nothing new.

- [ ] **Step 1: Write the failing tests**

Append to `internal/httpapi/uploadauth_test.go`:

```go
func TestRotatingTheEditPasswordRevokesUploadTokens(t *testing.T) {
	e := newEnv(t, nil)
	c := e.createSite(t, nil, map[string]string{"index.html": "x"})
	tok := uploadTokenFor(t, e, c)
	if _, err := (siteService{a: e.api}).RotateEditPassword(c.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.api.uploads.begin(tok, editIDFrom(t, c.EditURL)); ok {
		t.Fatal("an upload token survived a password rotation")
	}
}

func TestDeletingASiteRevokesItsUploadTokens(t *testing.T) {
	e := newEnv(t, nil)
	deletes := map[string]func(c createResp){
		"api": func(c createResp) {
			e.public(t, authed(httptest.NewRequest("DELETE", "/api/sites/"+editIDFrom(t, c.EditURL), nil), c.EditPassword))
		},
		"site service": func(c createResp) {
			if err := (siteService{a: e.api}).Delete(c.ID); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, del := range deletes {
		c := e.createSite(t, nil, map[string]string{"index.html": "x"})
		uploadTokenFor(t, e, c)
		del(c)
		for _, tk := range e.api.uploads.m {
			if tk.viewID == c.ID {
				t.Errorf("%s delete left an upload token behind", name)
			}
		}
	}
}

// Review focus 4: a token whose site is gone gets a clean 404, not a 500.
func TestUploadTokenForADeletedSiteIsANotFound(t *testing.T) {
	e := newEnv(t, nil)
	c := e.createSite(t, nil, map[string]string{"index.html": "x"})
	edit := editIDFrom(t, c.EditURL)
	tok := uploadTokenFor(t, e, c)
	site, _ := e.st.ByViewID(c.ID)
	if err := e.st.Delete(site); err != nil { // the cleanup sweep's path: no revocation
		t.Fatal(err)
	}
	if w := e.public(t, bearer(httptest.NewRequest("PUT", "/dav/"+edit+"/x.txt", strings.NewReader("x")), tok)); w.Code != 404 {
		t.Errorf("WebDAV on a deleted site: %d", w.Code)
	}
	body, ct := uploadBody(t, nil, map[string]string{"x.txt": "x"})
	req := bearer(httptest.NewRequest("POST", "/api/sites/"+edit+"/files", body), tok)
	req.Header.Set("Content-Type", ct)
	if w := e.public(t, req); w.Code != 404 {
		t.Errorf("API upload on a deleted site: %d %s", w.Code, w.Body)
	}
}
```

The MCP delete path is covered in Task 5 (`TestMCPDeleteSiteRevokesUploadTokens`), where `open_upload` exists.

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/httpapi/ -run "Revokes|DeletedSite"`
Expected: FAIL — the token survives rotation and deletion (`TestUploadTokenForADeletedSiteIsANotFound` already passes; it pins the behaviour).

- [ ] **Step 3: Revoke next to every `verifyCache.Drop`**

`internal/httpapi/siteservice.go`, in `RotateEditPassword` after `s.a.verifyCache.Drop(site.EditID + ":")`:

```go
	// and every upload token issued under it: rotating is how an owner cuts
	// off whoever held access
	s.a.uploads.revokeSite(site.ViewID)
```

In `Delete`, after `s.a.verifyCache.Drop(site.EditID + ":")`:

```go
	s.a.uploads.revokeSite(site.ViewID)
```

`internal/httpapi/sites.go`, in `deleteSite` after `a.verifyCache.Drop(site.EditID + ":")`:

```go
	a.uploads.revokeSite(site.ViewID)
```

`internal/httpapi/mcpops.go`, in `DeleteSite` after `o.a.verifyCache.Drop(site.EditID + ":")`:

```go
	o.a.uploads.revokeSite(site.ViewID)
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/httpapi/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/httpapi/siteservice.go internal/httpapi/sites.go internal/httpapi/mcpops.go internal/httpapi/uploadauth_test.go
git commit -m "feat: rotating a site's edit password or deleting the site revokes its upload tokens

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>"
```

---

### Task 5: The `open_upload` MCP tool

**Files:**
- Modify: `internal/config/config.go` (after `DAVURL`, ~line 502) and `internal/config/config_test.go` (append)
- Modify: `internal/mcp/ops.go` (new `UploadResult` type near `FormsResult`; `Ops` interface; `DecodeFiles` message ~line 280)
- Modify: `internal/mcp/server.go` (`Instructions`, `write_files` description, new tool after `download_site`)
- Modify: `internal/mcp/server_test.go` (`fakeOps`, `TestToolCatalog`, `TestInitializeReportsInstructions`, `TestOversizedCallGetsTheHelpfulError`, `TestEveryToolIsScoped`, new test)
- Modify: `internal/mcp/ops_test.go` (`TestDecodeFilesCapIsCumulative` wanted words)
- Modify: `internal/httpapi/mcpops.go` (new `OpenUpload`)
- Modify: `internal/httpapi/mcpops_test.go` (append)

**Interfaces:**
- Consumes: `API.uploads.issue`, `uploadTokenIdle`, `uploadBody`, `bearer` (Tasks 1-3); `mcpOps.openSite`, `mcpClient`, `mcpCreate`, `mcpCall`, `mcpText`, `fakeProvider` (existing).
- Produces:
  - `func (c Config) FilesURL(editID string) string` → `<scheme>://<base><port>/api/sites/<editID>/files`
  - `type mcp.UploadResult struct { EditID, ViewURL, Token, UploadURL, WebDAVURL string; IdleTimeoutSeconds int; ExpiresAt time.Time; Examples []string }` with JSON names `edit_id, view_url, token, upload_url, webdav_url (omitempty), idle_timeout_seconds, expires_at, examples`
  - `Ops.OpenUpload(ctx context.Context, a Auth, ref SiteRef) (*UploadResult, error)`
  - MCP tool `open_upload` (args: `siteArgs`), scope `ScopeWrite`

- [ ] **Step 1: Write the failing config test**

Append to `internal/config/config_test.go`:

```go
func TestFilesURLIsTheUploadRouteOnTheMainDomain(t *testing.T) {
	cfg, err := Load(env(map[string]string{
		"SITEBIN_BASE_DOMAIN": "app.sitebin.io",
		"SITEBIN_VIEW_DOMAIN": "sitebin.app",
		"SITEBIN_HTTP_ONLY":   "true",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.FilesURL("e1"); !strings.HasSuffix(got, "://app.sitebin.io/api/sites/e1/files") {
		t.Errorf("FilesURL = %q", got)
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./internal/config/ -run TestFilesURL`
Expected: FAIL — `cfg.FilesURL undefined`.

- [ ] **Step 3: Implement `FilesURL`**

In `internal/config/config.go`, after `DAVURL`:

```go
// FilesURL returns the JSON API's file-upload URL for an edit id — the route
// an upload token from open_upload may POST to.
func (c Config) FilesURL(editID string) string {
	return c.scheme() + "://" + c.BaseDomain + c.portSuffix() + "/api/sites/" + editID + "/files"
}
```

Run: `go test ./internal/config/` — Expected: PASS.

- [ ] **Step 4: Write the failing MCP-package tests**

In `internal/mcp/server_test.go`:

Add to `fakeOps` (after `ResendFormConfirmation`):

```go
func (f *fakeOps) OpenUpload(_ context.Context, a Auth, ref SiteRef) (*UploadResult, error) {
	f.note("open_upload", a, ref)
	return &UploadResult{EditID: ref.EditID, Token: "sbu_test", UploadURL: "https://x.example/api/sites/e1/files", Examples: []string{}}, f.err
}
```

In `TestToolCatalog`'s `want` map add `"open_upload": true`.

In `TestInitializeReportsInstructions` change the loop list to:

```go
	for _, want := range []string{"edit_id", "edit_password", "account API token", "open_upload"} {
```

In `TestOversizedCallGetsTheHelpfulError` change the final check to:

```go
	if !strings.Contains(resultText(res), "open_upload") {
		t.Errorf("the caller was not told what to do instead: %s", resultText(res))
	}
```

In `TestEveryToolIsScoped`'s `argsFor` add:

```go
		"open_upload":              {"edit_id": "e1"},
```

Append:

```go
func TestOpenUploadReachesOps(t *testing.T) {
	ops := &fakeOps{}
	cs := connect(t, ops, nil)
	res := call(t, cs, "open_upload", map[string]any{"edit_id": "e1", "edit_password": "pw"})
	if res.IsError {
		t.Fatalf("open_upload: %s", resultText(res))
	}
	if ops.gotRef.EditID != "e1" || ops.gotRef.EditPassword != "pw" {
		t.Errorf("ref = %+v", ops.gotRef)
	}
	if !strings.Contains(resultText(res), "sbu_test") {
		t.Errorf("result does not carry the token: %s", resultText(res))
	}
}
```

In `internal/mcp/ops_test.go`, `TestDecodeFilesCapIsCumulative`, change the wanted words to:

```go
	for _, want := range []string{"write_files", "open_upload", "zip"} {
```

- [ ] **Step 5: Run them to verify they fail**

Run: `go test ./internal/mcp/`
Expected: FAIL to compile — `undefined: UploadResult`.

- [ ] **Step 6: Implement the type, the interface method, the tool and the texts**

In `internal/mcp/ops.go`, after `FormsResult`:

```go
// UploadResult is what open_upload returns: a short-lived credential for one
// site and where to use it. The examples are the useful part — an agent runs
// a working command more reliably than it assembles one from a schema.
type UploadResult struct {
	EditID             string    `json:"edit_id"`
	ViewURL            string    `json:"view_url" jsonschema:"the public URL of the site"`
	Token              string    `json:"token" jsonschema:"send as Authorization: Bearer <token>, or as the password of HTTP Basic auth; never put it in a URL"`
	UploadURL          string    `json:"upload_url" jsonschema:"POST multipart/form-data here: a zip part is extracted, each files part is stored at the path in its filename; add ?replace=true to make the upload the whole site"`
	WebDAVURL          string    `json:"webdav_url,omitempty" jsonschema:"the site's WebDAV tree: PUT a file to its path (MKCOL a folder first), PROPFIND to list, DELETE to remove; absent when WebDAV is off on this instance"`
	IdleTimeoutSeconds int       `json:"idle_timeout_seconds" jsonschema:"the token expires this many seconds after its last request ends"`
	ExpiresAt          time.Time `json:"expires_at" jsonschema:"the token expires at this time however it is used"`
	Examples           []string  `json:"examples" jsonschema:"ready-to-run curl commands"`
}
```

In the `Ops` interface, after `DownloadSite`:

```go
	// OpenUpload issues a short-lived upload token for one site, authorized
	// exactly like WriteFiles.
	OpenUpload(ctx context.Context, a Auth, ref SiteRef) (*UploadResult, error)
```

In `DecodeFiles`, replace the over-size `fmt.Errorf` with:

```go
			return nil, fmt.Errorf("%w: this call carries more than %d MiB of file content. Split it across several write_files calls, or call open_upload and send the files with your own HTTP client — a zip for a whole site", ErrTooLarge, MaxContentBytes>>20)
```

In `internal/mcp/server.go`, replace the first paragraph of `Instructions` with:

```go
const Instructions = `Sitebin publishes files as a live website. Drop files, get a URL.

Call create_site with your files to publish a site; the result carries the
public view_url, an edit_id, and — once, and never again — an edit_password.
Every other tool addresses a site by its edit_id.

Files travel as tool arguments, which suits pages, styles and scripts. For
anything larger than a few hundred KB — images, video, a whole build — call
open_upload and send the files with your own HTTP client, if you can make HTTP
requests (curl, or code execution with network access).
```

(the Authentication and "Sites are public" paragraphs follow unchanged).

Change the `write_files` description to:

```go
		Description: "Add or overwrite files in a site. With replace set, every existing " +
			"file is removed first, so the site ends up containing exactly the files you pass. " +
			"For files larger than a few hundred KB, use open_upload instead.",
```

After the `download_site` tool, add:

```go
	sdk.AddTool(s, &sdk.Tool{
		Name: "open_upload",
		Description: "Get a short-lived token and URLs to upload files with your own HTTP client, for files " +
			"too large to pass to write_files (anything beyond a few hundred KB). Only useful if you can run " +
			"curl or make HTTP requests; otherwise use write_files. The token expires 5 minutes after its " +
			"last use, and after an hour at the latest; call open_upload again for a new one.",
	}, func(ctx context.Context, _ *sdk.CallToolRequest, in siteArgs) (*sdk.CallToolResult, *UploadResult, error) {
		if err := authorize(auth, ScopeWrite); err != nil {
			return nil, nil, err
		}
		r, err := ops.OpenUpload(ctx, auth, in.ref())
		if err != nil {
			return nil, nil, err
		}
		return nil, r, nil
	})
```

Also update the transport comment in `NewHandler` that says "the message that tells them to use WebDAV, FTP or a zip upload" to "the message that tells them to call open_upload".

- [ ] **Step 7: Run the MCP package tests**

Run: `go test ./internal/mcp/`
Expected: PASS. (`go build ./...` still fails until Step 9: `mcpOps` does not implement `OpenUpload`.)

- [ ] **Step 8: Write the failing adapter tests**

Append to `internal/httpapi/mcpops_test.go`:

```go
func TestMCPOpenUploadThenUploadOverHTTP(t *testing.T) {
	e := newEnv(t, nil)
	cs := mcpClient(t, e, nil)
	editID, pw := mcpCreate(t, cs, "<h1>small</h1>")

	res := mcpCall(t, cs, "open_upload", map[string]any{"edit_id": editID, "edit_password": pw})
	if res.IsError {
		t.Fatalf("open_upload: %s", mcpText(res))
	}
	var up mcp.UploadResult
	raw, _ := json.Marshal(res.StructuredContent)
	if err := json.Unmarshal(raw, &up); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(up.Token, "sbu_") {
		t.Fatalf("token = %q", up.Token)
	}
	if !strings.HasSuffix(up.UploadURL, "/api/sites/"+editID+"/files") {
		t.Errorf("upload_url = %q", up.UploadURL)
	}
	if !strings.HasSuffix(up.WebDAVURL, "/dav/"+editID+"/") {
		t.Errorf("webdav_url = %q", up.WebDAVURL)
	}
	if up.IdleTimeoutSeconds != int(uploadTokenIdle/time.Second) {
		t.Errorf("idle_timeout_seconds = %d", up.IdleTimeoutSeconds)
	}
	if d := time.Until(up.ExpiresAt); d < 59*time.Minute || d > 61*time.Minute {
		t.Errorf("expires_at = %v", up.ExpiresAt)
	}
	if len(up.Examples) != 3 {
		t.Fatalf("examples = %v", up.Examples)
	}
	for _, ex := range up.Examples {
		if !strings.HasPrefix(ex, "curl ") || !strings.Contains(ex, up.Token) {
			t.Errorf("example is not ready to run: %q", ex)
		}
	}

	// The token works with a plain HTTP client, for a file larger than any
	// tool call may carry.
	big := strings.Repeat("x", mcp.MaxContentBytes+1)
	body, ct := uploadBody(t, nil, map[string]string{"big.txt": big})
	req := bearer(httptest.NewRequest("POST", "/api/sites/"+editID+"/files", body), up.Token)
	req.Header.Set("Content-Type", ct)
	if w := e.public(t, req); w.Code != 200 {
		t.Fatalf("upload with the issued token: %d %s", w.Code, w.Body)
	}
	site, _ := e.st.ByEditID(editID)
	if b, err := e.st.ReadContentFile(site, "big.txt"); err != nil || len(b) != len(big) {
		t.Fatalf("big.txt: %d bytes, %v", len(b), err)
	}
}

func TestMCPOpenUploadWithoutWebDAVOffersOnlyTheAPI(t *testing.T) {
	e := newEnv(t, map[string]string{"SITEBIN_WEBDAV_ENABLED": "false"})
	cs := mcpClient(t, e, nil)
	editID, pw := mcpCreate(t, cs, "x")
	res := mcpCall(t, cs, "open_upload", map[string]any{"edit_id": editID, "edit_password": pw})
	m, _ := res.StructuredContent.(map[string]any)
	if res.IsError || m == nil {
		t.Fatalf("open_upload: %s", mcpText(res))
	}
	if v, ok := m["webdav_url"]; ok {
		t.Errorf("webdav_url offered with WebDAV off: %v", v)
	}
	if ex, _ := m["examples"].([]any); len(ex) != 2 {
		t.Errorf("examples = %v", m["examples"])
	}
}

func TestMCPOpenUploadNeedsTheSitesAuthority(t *testing.T) {
	e := newEnv(t, nil)
	cs := mcpClient(t, e, nil)
	editID, _ := mcpCreate(t, cs, "x")
	res := mcpCall(t, cs, "open_upload", map[string]any{"edit_id": editID, "edit_password": "wrong"})
	if !res.IsError {
		t.Fatal("open_upload with a wrong password issued a token")
	}
	if n := len(e.api.uploads.m); n != 0 {
		t.Fatalf("%d tokens issued", n)
	}
}

func TestMCPOpenUploadWithAnOwningAccountToken(t *testing.T) {
	e := newEnv(t, nil)
	ext.Register(&fakeProvider{
		enabled: true,
		owner:   "acct-1",
		bearer:  map[string]string{"sbp_a": "acct-1", "sbp_b": "acct-2"},
	})
	defer ext.Reset()

	owner := mcpClient(t, e, http.Header{"Authorization": {"Bearer sbp_a"}})
	editID, _ := mcpCreate(t, owner, "x")
	if res := mcpCall(t, owner, "open_upload", map[string]any{"edit_id": editID}); res.IsError {
		t.Fatalf("owning token: %s", mcpText(res))
	}
	other := mcpClient(t, e, http.Header{"Authorization": {"Bearer sbp_b"}})
	if res := mcpCall(t, other, "open_upload", map[string]any{"edit_id": editID}); !res.IsError {
		t.Fatal("another account's token opened an upload")
	}
}

func TestMCPDeleteSiteRevokesUploadTokens(t *testing.T) {
	e := newEnv(t, nil)
	cs := mcpClient(t, e, nil)
	editID, pw := mcpCreate(t, cs, "x")
	if res := mcpCall(t, cs, "open_upload", map[string]any{"edit_id": editID, "edit_password": pw}); res.IsError {
		t.Fatalf("open_upload: %s", mcpText(res))
	}
	if res := mcpCall(t, cs, "delete_site", map[string]any{"edit_id": editID, "edit_password": pw}); res.IsError {
		t.Fatalf("delete_site: %s", mcpText(res))
	}
	if n := len(e.api.uploads.m); n != 0 {
		t.Fatalf("%d upload tokens outlived their site", n)
	}
}
```

- [ ] **Step 9: Run them to verify they fail**

Run: `go test ./internal/httpapi/ -run TestMCPOpenUpload`
Expected: FAIL to compile — `mcpOps does not implement mcp.Ops (missing method OpenUpload)`.

- [ ] **Step 10: Implement the adapter**

In `internal/httpapi/mcpops.go`, after `DownloadSite`:

```go
// OpenUpload issues an upload token for a site the caller may write to. The
// authority check is openSite — the one write_files uses — so whoever may
// write files through MCP may open an upload, and nobody else.
func (o mcpOps) OpenUpload(_ context.Context, auth mcp.Auth, ref mcp.SiteRef) (*mcp.UploadResult, error) {
	site, err := o.openSite(auth, ref)
	if err != nil {
		return nil, err
	}
	secret, expires, err := o.a.uploads.issue(site.ViewID, site.EditID)
	if err != nil {
		return nil, err // errTooManyUploads says what to do
	}
	o.a.log.Info("upload token issued", "id", site.ViewID, "token", secret[:10])

	res := &mcp.UploadResult{
		EditID:             site.EditID,
		ViewURL:            o.a.cfg.ViewURL(site.Meta.ID),
		Token:              secret,
		UploadURL:          o.a.cfg.FilesURL(site.EditID),
		IdleTimeoutSeconds: int(uploadTokenIdle / time.Second),
		ExpiresAt:          expires,
	}
	h := "-H 'Authorization: Bearer " + secret + "'"
	res.Examples = []string{
		"curl " + h + " -F 'zip=@dist.zip' '" + res.UploadURL + "?replace=true'",
		"curl " + h + " -F 'files=@video.mp4;filename=media/video.mp4' '" + res.UploadURL + "'",
	}
	// With WebDAV off instance-wide the route is a 404, so it is not offered.
	if o.a.cfg.WebDAVAllowed {
		res.WebDAVURL = o.a.cfg.DAVURL(site.EditID)
		res.Examples = append(res.Examples, "curl "+h+" -T big.bin '"+res.WebDAVURL+"big.bin'")
	}
	return res, nil
}
```

Add `"time"` to the imports of `mcpops.go`.

- [ ] **Step 11: Run everything to verify it passes**

Run: `go build ./... && go build -tags ee ./... && go test ./internal/... && go vet ./...`
Expected: all PASS.

- [ ] **Step 12: Commit**

```bash
git add internal/config/config.go internal/config/config_test.go internal/mcp/ops.go internal/mcp/server.go internal/mcp/server_test.go internal/mcp/ops_test.go internal/httpapi/mcpops.go internal/httpapi/mcpops_test.go
git commit -m "feat: open_upload hands an agent a short-lived token and curl commands for files too large for a tool call

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>"
```

---

### Task 6: Documentation in both repos

**Files:**
- Modify: `Sitebin/README.md` (MCP section, ~lines 322-376)
- Modify: `Sitebin/CLAUDE.md` ("The MCP server" section)
- Modify: `Sitebin-Website/public/docs/mcp/index.html` (tool table ~line 131-152, files paragraph ~line 167-170)

**Interfaces:**
- Consumes: the finished behaviour from Tasks 1-5 (names, limits, texts).
- Produces: nothing code-facing.

- [ ] **Step 1: README — tool table, scopes, the files paragraph**

In the **Tools** table, after the `download_site` row:

```markdown
| `open_upload` | A short-lived token and URLs to upload large files with your own HTTP client (WebDAV or the zip/files upload), with ready-to-run `curl` commands |
```

In the scope table, append `, `open_upload`` to the `sitebin:sites:write` row.

Replace the paragraph starting "Files travel as JSON" with:

```markdown
Files travel as JSON: `{"path": "index.html", "text": "…"}`, or `"base64"` for
binary. One call carries at most 8 MiB, but a model writes every byte of a tool
argument out as output tokens, so anything beyond a few hundred KB is
impractical long before that. For those, **`open_upload`** returns an upload
token for one site and the URLs to use it on:

- `POST <upload_url>` — the JSON API's file upload: a `zip` part is extracted,
  each `files` part is stored at the path in its filename, `?replace=true`
  makes the upload the whole site;
- `<webdav_url>` — the site's WebDAV tree, whatever the site's own WebDAV
  toggle says (omitted when `SITEBIN_WEBDAV_ENABLED=false`).

Send the token as `Authorization: Bearer sbu_…` or as the Basic-auth password.
It opens nothing else — every other route answers `403` — and it expires five
minutes after its last request ends, and an hour after it was issued at the
latest. Rotating the edit password or deleting the site revokes it; so does a
restart, since tokens are held in memory only. At most five per site are live
at once. Design:
[`docs/superpowers/specs/2026-09-25-mcp-upload-tokens-design.md`](docs/superpowers/specs/2026-09-25-mcp-upload-tokens-design.md).

Sites created through MCP are recorded in `meta.json` as `"origin": "mcp"`;
that is provenance for the admin console and changes nothing about how the
site is served.
```

- [ ] **Step 2: CLAUDE.md — one bullet in "The MCP server"**

Append to the bullet list of `## The MCP server`:

```markdown
- **Upload tokens (`sbu_`) are memory-only and single-site.** `open_upload`
  issues them (`internal/httpapi/uploadtokens.go`); only `/dav/{editID}/` and
  `POST /api/sites/{editID}/files` accept them, and `withEditAuth` refuses any
  `sbu_` credential before password work. A `sbu_` credential is never tried
  as an edit password or account token — keep `uploadCredential` the single
  place that recognises one. Idle 5 min from the END of the last request,
  60 min absolute. Read `docs/superpowers/specs/2026-09-25-mcp-upload-tokens-design.md`.
```

- [ ] **Step 3: Commit the product repo**

```bash
git add README.md CLAUDE.md
git commit -m "docs: open_upload and the upload token's rules in the README and the repo guide

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>"
```

- [ ] **Step 4: Website — tool table row and files paragraph**

In `C:\Projects\Sitebin-Project\Sitebin-Website\public\docs\mcp\index.html`, after the `download_site` row:

```html
            <tr><th scope="row"><code>open_upload</code></th><td>A short-lived token and URLs for files too large to pass as arguments. The agent uploads with its own HTTP client — a zip for a whole site, or single files, or over WebDAV — using the <code>curl</code> commands in the result. The token opens nothing else and expires five minutes after its last use.</td></tr>
```

Replace the sentence `One call carries up to 8&nbsp;MiB; a bigger site belongs on WebDAV, FTP or the API's zip upload.` with:

```html
One call carries up to 8&nbsp;MiB, but a model writes every byte of an
        argument out itself, so for anything beyond a few hundred&nbsp;KB the
        agent calls <code>open_upload</code> and uploads with <code>curl</code>
        instead — if it can make HTTP requests of its own.
```

Run the link check: `powershell -File scripts/check-links.ps1` (from `Sitebin-Website`). Expected: no broken links.

- [ ] **Step 5: Commit the website repo — do NOT push**

```bash
cd /c/Projects/Sitebin-Project/Sitebin-Website
git add public/docs/mcp/index.html
git commit -m "content: agents upload large files through open_upload with a short-lived token

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>"
```

Pushing publishes; per the ship order it waits until the product is deployed and verified on app.sitebin.io.

---

### Task 7: End-to-end proof and full verification

**Files:**
- Modify: `e2e/mcp.ps1` (catalog list ~line 142-146, new section after the "writing" section ~line 214, `$ExpectedAssertions` line 28)

**Interfaces:**
- Consumes: the running community image built from this branch; `Req`, `CallTool`, `ToolStruct`, `ToolIsError`, `ToolText`, `Assert`, `$origin`, `$viewURL`, `$editID`, `$pw`, `$work` from the script.
- Produces: nothing.

- [ ] **Step 1: Extend the E2E script (ASCII only)**

In the catalog loop, add `"open_upload"` to the list (after `"download_site"`).

After the `Assert "replace emptied the site first" …` line, insert:

```powershell
Write-Host "== large files through open_upload" -ForegroundColor Cyan
$r = CallTool "open_upload" @{ edit_id = $editID; edit_password = $pw }
Assert "open_upload succeeded" (-not (ToolIsError $r)) (ToolText $r)
$up = ToolStruct $r
$tok = ""; if ($null -ne $up) { $tok = $up.token }
Assert "the token is an upload token" ($tok -match '^sbu_[0-9A-Za-z]{40}$') "$tok"
Assert "upload and WebDAV URLs returned" ($null -ne $up -and $up.upload_url -match "/api/sites/$editID/files$" -and $up.webdav_url -match "/dav/$editID/$") "$($up.upload_url) $($up.webdav_url)"

# 9 MiB: above the 8 MiB a tool call may carry.
$big = Join-Path $work "big.bin"
[IO.File]::WriteAllBytes($big, (New-Object byte[] (9MB)))
$r2 = Req "POST" "$origin/api/sites/$editID/files" @("-H", "Authorization: Bearer $tok", "-F", "files=@$big;filename=media/big.bin")
Assert "a 9 MiB file uploads with the token" ($r2.code -eq 200) "$($r2.code) $($r2.body)"
$bigURL = ($viewURL.TrimEnd("/")) + "/media/big.bin"
$got = & curl.exe -s -o NUL -w "%{size_download}" $bigURL
Assert "the big file serves in full" ([int64]$got -eq 9MB) "$got"

# The site's own WebDAV toggle is off (create_site never set it).
$small = Join-Path $work "dav.txt"
[IO.File]::WriteAllText($small, "dav-ok")
$r2 = Req "PUT" "$origin/dav/$editID/dav.txt" @("-H", "Authorization: Bearer $tok", "--data-binary", "@$small")
Assert "WebDAV PUT with the token and the toggle off" ($r2.code -eq 201) "$($r2.code) $($r2.body)"
Assert "the WebDAV file serves" ((Req "GET" (($viewURL.TrimEnd("/")) + "/dav.txt")).body -match "dav-ok")

$r2 = Req "GET" "$origin/api/sites/$editID" @("-H", "Authorization: Bearer $tok")
Assert "the token is refused on the settings route" ($r2.code -eq 403) "$($r2.code) $($r2.body)"
```

Change `$ExpectedAssertions = 42` to `$ExpectedAssertions = 51` (one catalog entry plus eight new assertions).

- [ ] **Step 2: Check the script is pure ASCII**

Run: `grep -nP '[^\x00-\x7F]' e2e/mcp.ps1`
Expected: no output.

- [ ] **Step 3: Run the full Go verification**

Run:
```bash
go build ./... && go build -tags ee ./... && go vet ./... && go test ./... && go test -tags ee ./...
```
Expected: every package `ok`.

- [ ] **Step 4: Build the community image and run the E2E suites that touch changed code**

```powershell
docker build -t sitebin:latest .
powershell -File e2e/mcp.ps1
powershell -File e2e/e2e.ps1
```
Expected: `== MCP E2E: 51 passed, 0 failed` and the core E2E all green (it covers WebDAV and the file upload route, both changed).

- [ ] **Step 5: Commit**

```bash
git add e2e/mcp.ps1
git commit -m "test: the MCP e2e uploads a 9 MiB file and a WebDAV file through open_upload's token

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>"
```

- [ ] **Step 6: Mark the spec implemented**

In `docs/superpowers/specs/2026-09-25-mcp-upload-tokens-design.md` change `**Status:** draft — awaiting review` to `**Status:** implemented`, and add a "Corrections (post-implementation)" block at the end if review changed any rule. Commit:

```bash
git add docs/superpowers/specs/2026-09-25-mcp-upload-tokens-design.md
git commit -m "docs: the upload-token design is implemented

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>"
```

**Not part of this plan (needs the user's go):** merging to `main` and pushing, deploying to app.sitebin.io, verifying `open_upload` live, and only then pushing the website repo.
