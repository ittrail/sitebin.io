# Tier Lifetimes & API Gating Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Anonymous drops live 24 hours and cannot be driven from a script; account-owned free sites live 7 days counted from their last content change; paid sites live until deleted.

**Architecture:** No new tier fields and no new meta fields. `max_expiry_days` already expresses all three lifetimes (`1` / `7` / `0`), and `Meta.OwnerAccountID` already distinguishes an anonymous drop from an owned site — the two features are derived from data that exists. Sliding renewal lives in the store, next to the file writes it reacts to. The API gate lives in one new file in `internal/httpapi` and is called from exactly two places: the create handler and the edit-auth middleware.

**Tech Stack:** Go 1.25, stdlib `net/http` with `ServeMux` patterns, `go test`. Static HTML/CSS/JS for both web surfaces (no build step). Website repo is separate: `C:\Projects\Sitebin-Website`.

## Global Constraints

- Greenfield: no migration, no grandfathering, no back-compat shims. Any site that exists at ship time is deleted.
- The community build (no `ext.Provider` registered) must stay fully open: every gate added here is inert unless a provider reports `AccountsEnabled()`.
- The API gate is a **plan boundary, not a security boundary** — say so in the code comment where it is defined. `Sec-Fetch-Site` and `Origin` are forgeable outside a browser.
- Anonymous drops never renew their expiry. Only sites with a non-empty `Meta.OwnerAccountID` slide.
- Renewal is triggered by **content** changes (file writes, deletions, replace-all, zip extraction, WebDAV writes), not by settings changes. A settings PUT may carry an explicit `expires_at`; renewing on top of it would silently override the user's choice.
- FTP writes do not renew the expiry (the `internal/ftp` package holds no `*store.Store`). FTP is globally disabled on the hosted instance; document the gap, do not close it here.
- Run `go build ./... && go test ./...` before every commit. The `ee` package needs its build tag: `go test -tags ee ./ee/...`.
- End every commit message with:
  `Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>`
- The working tree already carries unrelated, uncommitted CSS/UI changes in `internal/httpapi/gate.go`, `internal/viewer/viewer.go`, `web/static/app.css`, `web/static/edit.html`, `web/static/embed.js`, `web/static/index.html`, `web/viewer/viewer.css`. **Stage only the files each task names** — never `git add -A`.

## File Structure

**Created:**
- `internal/store/expiry.go` — sliding-renewal policy: when a site's expiry moves and by how much.
- `internal/store/expiry_test.go` — its tests.
- `internal/httpapi/apigate.go` — "does this caller get the API" policy: browser-fetch detection, embed-origin allowlist check, per-site decision.
- `internal/httpapi/apigate_test.go` — its tests.

**Modified:**
- `internal/store/files.go` — call the renewal from `saveFileLocked`, `DeleteFile`, `ClearFiles`.
- `internal/httpapi/webdav.go` — call the renewal after a mutating WebDAV request.
- `internal/httpapi/sites.go` — `expiryCap` helper, block clearing a capped expiry, gate anonymous creation, drop the allowlist loop now living in `apigate.go`.
- `internal/httpapi/server.go` — gate the site-scoped API routes in `withEditAuth`.
- `internal/httpapi/httpapi_test.go` — the `createSite` helper becomes browser-shaped.
- `web/static/app.js`, `web/static/embed.js`, `web/static/edit.js`, `web/static/index.html` — lifetime copy.
- `README.md`, `deploy/docker-compose.example.yml` — tier semantics and the new numbers.
- `C:\Projects\Sitebin-Website\public\pricing\index.html` and the doc pages repeating the old promises.

---

### Task 1: Sliding expiry renewal in the store

**Files:**
- Create: `internal/store/expiry.go`
- Create: `internal/store/expiry_test.go`
- Modify: `internal/store/files.go` (`saveFileLocked` ~line 166, `DeleteFile` ~line 197, `ClearFiles` ~line 219)

**Interfaces:**
- Consumes: `Store.lockSite`, `readMeta`, `writeMeta`, `Site.Meta`, `Site.dir` (all existing, package-private).
- Produces: `func (s *Store) RenewExpiry(site *Site) error` — public, takes the site lock itself, for callers that write files outside the store's own methods. `func (s *Store) renewExpiryLocked(site *Site) error` — package-private, requires the caller to already hold the site lock.

- [ ] **Step 1: Write the failing tests**

Create `internal/store/expiry_test.go`:

```go
package store

import (
	"strings"
	"testing"
	"time"
)

// expiringSite returns a site stamped like a tier-capped account site: owned,
// capped at days, and already carrying an expiry `in` from now.
func expiringSite(t *testing.T, s *Store, owner string, days int, in time.Duration) *Site {
	t.Helper()
	site, _, err := s.Create()
	if err != nil {
		t.Fatal(err)
	}
	exp := time.Now().Add(in).UTC()
	if err := s.Update(site, func(m *Meta) error {
		m.OwnerAccountID = owner
		m.QuotaExpiryDays = days
		m.ExpiresAt = &exp
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return site
}

func newExpiryStore(t *testing.T) *Store {
	t.Helper()
	s, err := New(t.TempDir(), "sitebin.example", 1<<20, 100)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestRenewExpiryOnWriteForOwnedSite(t *testing.T) {
	s := newExpiryStore(t)
	site := expiringSite(t, s, "acct-1", 7, 2*time.Hour)

	if err := s.SaveFile(site, "index.html", strings.NewReader("<h1>hi</h1>")); err != nil {
		t.Fatal(err)
	}

	got, err := s.ByViewID(site.ViewID)
	if err != nil {
		t.Fatal(err)
	}
	want := time.Now().Add(7 * 24 * time.Hour)
	if d := got.Meta.ExpiresAt.Sub(want); d > time.Minute || d < -time.Minute {
		t.Fatalf("expiry = %v, want ~%v", got.Meta.ExpiresAt, want)
	}
}

func TestNoRenewForAnonymousSite(t *testing.T) {
	s := newExpiryStore(t)
	site := expiringSite(t, s, "", 1, 2*time.Hour)
	before := *site.Meta.ExpiresAt

	if err := s.SaveFile(site, "index.html", strings.NewReader("x")); err != nil {
		t.Fatal(err)
	}

	got, _ := s.ByViewID(site.ViewID)
	if !got.Meta.ExpiresAt.Equal(before) {
		t.Fatalf("anonymous expiry moved: %v → %v", before, got.Meta.ExpiresAt)
	}
}

func TestNoRenewWithoutCapOrExpiry(t *testing.T) {
	s := newExpiryStore(t)
	site, _, err := s.Create()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Update(site, func(m *Meta) error { m.OwnerAccountID = "acct-1"; return nil }); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveFile(site, "index.html", strings.NewReader("x")); err != nil {
		t.Fatal(err)
	}
	got, _ := s.ByViewID(site.ViewID)
	if got.Meta.ExpiresAt != nil {
		t.Fatalf("uncapped site gained an expiry: %v", got.Meta.ExpiresAt)
	}
}

func TestRenewSkipsRedundantWrite(t *testing.T) {
	s := newExpiryStore(t)
	site := expiringSite(t, s, "acct-1", 7, 2*time.Hour)

	if err := s.SaveFile(site, "a.html", strings.NewReader("a")); err != nil {
		t.Fatal(err)
	}
	first, _ := s.ByViewID(site.ViewID)
	stamp := first.Meta.UpdatedAt

	// A second write moments later recomputes the same expiry; meta must not
	// be rewritten (this is what keeps a 500-file upload to one meta write).
	if err := s.SaveFile(site, "b.html", strings.NewReader("b")); err != nil {
		t.Fatal(err)
	}
	second, _ := s.ByViewID(site.ViewID)
	if !second.Meta.UpdatedAt.Equal(stamp) {
		t.Fatalf("meta rewritten on redundant renewal: %v → %v", stamp, second.Meta.UpdatedAt)
	}
}

func TestRenewExpiryOnDelete(t *testing.T) {
	s := newExpiryStore(t)
	site := expiringSite(t, s, "acct-1", 7, 2*time.Hour)
	if err := s.SaveFile(site, "gone.html", strings.NewReader("x")); err != nil {
		t.Fatal(err)
	}
	// push the expiry back so the delete has something to move
	past := time.Now().Add(2 * time.Hour).UTC()
	if err := s.Update(site, func(m *Meta) error { m.ExpiresAt = &past; return nil }); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteFile(site, "gone.html"); err != nil {
		t.Fatal(err)
	}
	got, _ := s.ByViewID(site.ViewID)
	want := time.Now().Add(7 * 24 * time.Hour)
	if d := got.Meta.ExpiresAt.Sub(want); d > time.Minute || d < -time.Minute {
		t.Fatalf("expiry after delete = %v, want ~%v", got.Meta.ExpiresAt, want)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/store/ -run 'Renew|NoRenew' -v`
Expected: FAIL — the renewal never happens, so `TestRenewExpiryOnWriteForOwnedSite` reports the untouched 2-hour expiry.

- [ ] **Step 3: Write the renewal policy**

Create `internal/store/expiry.go`:

```go
package store

import "time"

// renewGrace is how close a recomputed expiry must be to the stored one for a
// renewal to be skipped. It keeps a multi-file upload from rewriting meta.json
// once per file: the first file moves the expiry, the rest land inside the
// grace window and write nothing.
const renewGrace = time.Minute

// renewExpiryLocked slides an owned, capped site's expiry to now + cap. The
// caller must already hold the site lock.
//
// Anonymous sites are excluded deliberately: a drop must not be able to keep
// itself alive by rewriting one file before it expires. Their lifetime is
// fixed at creation.
func (s *Store) renewExpiryLocked(site *Site) error {
	m := site.Meta
	if m.OwnerAccountID == "" || m.QuotaExpiryDays <= 0 || m.ExpiresAt == nil {
		return nil
	}
	want := time.Now().Add(time.Duration(m.QuotaExpiryDays) * 24 * time.Hour).UTC()
	if want.Sub(*m.ExpiresAt).Abs() < renewGrace {
		return nil
	}
	meta, err := readMeta(site.dir)
	if err != nil {
		return err
	}
	meta.ExpiresAt = &want
	meta.UpdatedAt = time.Now().UTC()
	if err := writeMeta(site.dir, meta); err != nil {
		return err
	}
	site.Meta = meta
	return nil
}

// RenewExpiry is renewExpiryLocked for callers that mutate a site's files
// outside the store's own methods — the WebDAV handler, which writes through
// its own filesystem. It must not be called while holding the site lock.
func (s *Store) RenewExpiry(site *Site) error {
	l := s.lockSite(site.ViewID)
	l.Lock()
	defer l.Unlock()
	return s.renewExpiryLocked(site)
}
```

- [ ] **Step 4: Hook the renewal into the three write paths**

In `internal/store/files.go`, `saveFileLocked` currently ends with:

```go
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("commit file: %w", err)
	}
	return nil
}
```

Replace that trailing `return nil` with:

```go
	return s.renewExpiryLocked(site)
}
```

In `DeleteFile`, the function ends with the parent-pruning loop followed by `return nil`:

```go
	for d := filepath.Dir(dst); d != root && strings.HasPrefix(d, root); d = filepath.Dir(d) {
		if os.Remove(d) != nil { // fails when non-empty — that's the stop signal
			break
		}
	}
	return nil
}
```

Replace that `return nil` with `return s.renewExpiryLocked(site)`.

In `ClearFiles`, the function ends with the removal loop followed by `return nil`:

```go
	for _, e := range entries {
		if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
			return err
		}
	}
	return nil
}
```

Replace that `return nil` with `return s.renewExpiryLocked(site)`.

`ExtractZip` needs no change: it calls `saveFileLocked` per entry and inherits the hook (plus the grace window, so a zip writes meta once).

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./internal/store/ -v`
Expected: PASS, including the pre-existing store tests.

- [ ] **Step 6: Commit**

```bash
git add internal/store/expiry.go internal/store/expiry_test.go internal/store/files.go
git commit -m "feat: slide an owned site's expiry forward on every content change

Anonymous drops keep the fixed lifetime stamped at creation; a phishing
page must not be able to renew itself.

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 2: WebDAV writes renew the expiry

**Files:**
- Modify: `internal/httpapi/webdav.go:107-118`
- Test: `internal/httpapi/httpapi_test.go` (append near the other WebDAV tests, after `TestWebDAVGloballyDisabled` ~line 967)

**Interfaces:**
- Consumes: `Store.RenewExpiry` from Task 1; the existing `davMutating` map and the `davReq` / `editIDFrom` test helpers.
- Produces: nothing new.

- [ ] **Step 1: Write the failing test**

Append to `internal/httpapi/httpapi_test.go`:

```go
func TestWebDAVWriteRenewsExpiry(t *testing.T) {
	ext.Register(&fakeProvider{enabled: true, owner: "acct-1", grant: ext.CreateGrant{MaxExpiryDays: 7}})
	defer ext.Reset()
	e := newEnv(t, nil)
	c := e.createSite(t, map[string]string{"webdav": "true"}, map[string]string{"index.html": "x"})
	edit := editIDFrom(t, c.EditURL)

	// wind the expiry back so a renewal is visible
	site, _ := e.st.ByViewID(c.ID)
	soon := time.Now().Add(2 * time.Hour).UTC()
	if err := e.st.Update(site, func(m *store.Meta) error { m.ExpiresAt = &soon; return nil }); err != nil {
		t.Fatal(err)
	}

	if w := davReq(t, e, "PUT", "/dav/"+edit+"/new.html", c.EditPassword, strings.NewReader("hi")); w.Code != 201 {
		t.Fatalf("PUT: %d %s", w.Code, w.Body)
	}

	got, _ := e.st.ByViewID(c.ID)
	want := time.Now().Add(7 * 24 * time.Hour)
	if got.Meta.ExpiresAt == nil {
		t.Fatal("expiry cleared")
	}
	if d := got.Meta.ExpiresAt.Sub(want); d > time.Minute || d < -time.Minute {
		t.Fatalf("expiry = %v, want ~%v", got.Meta.ExpiresAt, want)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/httpapi/ -run TestWebDAVWriteRenewsExpiry -v`
Expected: FAIL — the expiry is still the 2-hour one.

- [ ] **Step 3: Call the renewal after a mutating WebDAV request**

In `internal/httpapi/webdav.go`, the handler currently ends:

```go
	if davMutating[r.Method] {
		// serialize with API writes / mode switches on the same site
		a.st.WithLock(site.ViewID, func() error { h.ServeHTTP(w, r); return nil })
	} else {
		h.ServeHTTP(w, r)
	}

	if davMutating[r.Method] && site.Meta.Mode == store.ModeViewer {
		if err := a.syncViewerLayout(site); err != nil {
			a.log.Error("viewer regen after webdav", "id", site.ViewID, "err", err)
		}
	}
}
```

Insert the renewal between the two blocks — after the lock is released, because
`RenewExpiry` takes that same lock:

```go
	if davMutating[r.Method] {
		if err := a.st.RenewExpiry(site); err != nil {
			a.log.Error("renew expiry after webdav", "id", site.ViewID, "err", err)
		}
	}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/httpapi/ -run TestWebDAV -v`
Expected: PASS for all WebDAV tests.

- [ ] **Step 5: Commit**

```bash
git add internal/httpapi/webdav.go internal/httpapi/httpapi_test.go
git commit -m "feat: WebDAV writes renew a capped site's expiry

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 3: A capped expiry cannot be cleared

**Files:**
- Modify: `internal/httpapi/sites.go` (`applySettings`, the `raw == "null"` branch ~line 72, and the cap check ~lines 84-94)
- Test: `internal/httpapi/httpapi_test.go` (append after the existing `TestExpiryCapKeepsExplicitExpiry` ~line 1140)

**Interfaces:**
- Consumes: `store.Site`, `a.cfg.MaxExpiryDays`, the existing `apiError` type.
- Produces: `func (a *API) expiryCap(site *store.Site) int` — effective lifetime cap in days, `0` = unlimited. Task 8 exposes its value in the site payload.

- [ ] **Step 1: Write the failing test**

Append to `internal/httpapi/httpapi_test.go`:

```go
func TestCappedExpiryCannotBeCleared(t *testing.T) {
	ext.Register(&fakeProvider{enabled: true, grant: ext.CreateGrant{MaxExpiryDays: 1}})
	defer ext.Reset()
	e := newEnv(t, nil)
	c := e.createSite(t, nil, map[string]string{"index.html": "x"})
	edit := editIDFrom(t, c.EditURL)

	req := authed(httptest.NewRequest("PUT", "/api/sites/"+edit, strings.NewReader(`{"expires_at":null}`)), c.EditPassword)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	if w := e.public(t, req); w.Code != 400 {
		t.Fatalf("clearing a capped expiry should be 400, got %d %s", w.Code, w.Body)
	}
	site, _ := e.st.ByViewID(c.ID)
	if site.Meta.ExpiresAt == nil {
		t.Fatal("expiry was cleared anyway")
	}
}

func TestUncappedExpiryCanBeCleared(t *testing.T) {
	e := newEnv(t, nil) // no provider: no cap stamped, no instance global
	c := e.createSite(t, map[string]string{"expires_at": time.Now().Add(48 * time.Hour).UTC().Format(time.RFC3339)}, map[string]string{"index.html": "x"})
	edit := editIDFrom(t, c.EditURL)

	req := authed(httptest.NewRequest("PUT", "/api/sites/"+edit, strings.NewReader(`{"expires_at":null}`)), c.EditPassword)
	req.Header.Set("Content-Type", "application/json")
	if w := e.public(t, req); w.Code != 200 {
		t.Fatalf("PUT: %d %s", w.Code, w.Body)
	}
	site, _ := e.st.ByViewID(c.ID)
	if site.Meta.ExpiresAt != nil {
		t.Fatalf("expiry not cleared: %v", site.Meta.ExpiresAt)
	}
}
```

- [ ] **Step 2: Run the tests to verify the first fails**

Run: `go test ./internal/httpapi/ -run 'ExpiryCanBeCleared|ExpiryCannotBeCleared' -v`
Expected: `TestCappedExpiryCannotBeCleared` FAILs with `got 200`; `TestUncappedExpiryCanBeCleared` already passes.

- [ ] **Step 3: Add the cap helper and block the clear**

In `internal/httpapi/sites.go`, add above `applySettings`:

```go
// expiryCap returns the effective maximum lifetime in days for a site: the
// per-site tier stamp when present, else the instance global. 0 = unlimited.
func (a *API) expiryCap(site *store.Site) int {
	if site.Meta.QuotaExpiryDays > 0 {
		return site.Meta.QuotaExpiryDays
	}
	return a.cfg.MaxExpiryDays
}
```

In `applySettings`, replace the clearing branch:

```go
		if raw == "null" || raw == `""` {
			var cleared *time.Time
			expires = &cleared
		} else {
```

with:

```go
		if raw == "null" || raw == `""` {
			// A capped site has a lifetime, not an optional one: letting the
			// holder clear it would turn a 24-hour drop into a permanent site.
			if cap := a.expiryCap(site); cap > 0 {
				return &apiError{400, fmt.Sprintf("this site's plan limits it to %d day(s); its expiry cannot be removed", cap)}
			}
			var cleared *time.Time
			expires = &cleared
		} else {
```

Then collapse the duplicated cap lookup a few lines below — replace:

```go
			// Per-site tier cap (if stamped) overrides the instance global.
			expiryCap := a.cfg.MaxExpiryDays
			if site.Meta.QuotaExpiryDays > 0 {
				expiryCap = site.Meta.QuotaExpiryDays
			}
			if expiryCap > 0 {
```

with:

```go
			// Per-site tier cap (if stamped) overrides the instance global.
			expiryCap := a.expiryCap(site)
			if expiryCap > 0 {
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/httpapi/ -v`
Expected: PASS. Note `httpapi_test.go:306` carries a comment claiming `expires_at:null` clears — that test site is uncapped, so it still holds; leave it.

- [ ] **Step 5: Commit**

```bash
git add internal/httpapi/sites.go internal/httpapi/httpapi_test.go
git commit -m "fix: a tier-capped site cannot clear its own expiry

An anonymous drop could PUT expires_at:null from its own edit page and
become permanent.

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 4: Browser-fetch detection (the API gate's foundation)

**Files:**
- Create: `internal/httpapi/apigate.go`
- Create: `internal/httpapi/apigate_test.go`
- Modify: `internal/httpapi/sites.go` (`createCORS` ~lines 345-362)

**Interfaces:**
- Consumes: `a.cfg.EmbedOrigins`, `a.cfg.SiteURL`, `a.cfg.BaseDomain`, `ext.Get()`.
- Produces:
  - `func (a *API) embedOriginAllowed(origin string) bool` — origin already lowercased by the caller.
  - `func (a *API) fromOwnBrowser(r *http.Request) bool`
  - `func (a *API) apiAllowedFor(site *store.Site, r *http.Request) bool`
  - `func (a *API) apiAccountHint() string` — the `/account` URL used in both refusal messages.
  Tasks 5 and 6 call `apiAllowedFor` and `fromOwnBrowser` respectively; both use `apiAccountHint`.

- [ ] **Step 1: Write the failing tests**

Create `internal/httpapi/apigate_test.go`:

```go
package httpapi

import (
	"net/http/httptest"
	"testing"

	"github.com/ittrail/sitebin.io/internal/ext"
)

func TestFromOwnBrowser(t *testing.T) {
	e := newEnv(t, map[string]string{"SITEBIN_EMBED_ORIGINS": "https://sitebin.io"})
	ext.Register(&fakeProvider{enabled: true, embedOK: true})
	defer ext.Reset()

	cases := []struct {
		name    string
		headers map[string]string
		want    bool
	}{
		{"bare script", nil, false},
		{"origin without fetch metadata", map[string]string{"Origin": "http://sitebin.example"}, false},
		{"same-origin fetch", map[string]string{"Sec-Fetch-Site": "same-origin"}, true},
		{"same-origin fetch with origin", map[string]string{
			"Sec-Fetch-Site": "same-origin", "Origin": "http://sitebin.example",
		}, true},
		{"allowlisted embed", map[string]string{
			"Sec-Fetch-Site": "cross-site", "Origin": "https://sitebin.io",
		}, true},
		{"foreign origin", map[string]string{
			"Sec-Fetch-Site": "cross-site", "Origin": "https://evil.example",
		}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", "/api/sites", nil)
			req.Host = "sitebin.example"
			for k, v := range tc.headers {
				req.Header.Set(k, v)
			}
			if got := e.api.fromOwnBrowser(req); got != tc.want {
				t.Fatalf("fromOwnBrowser = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestAPIAllowedFor(t *testing.T) {
	e := newEnv(t, nil)

	script := func() *http.Request { return httptest.NewRequest("GET", "/api/sites/x", nil) }
	browser := func() *http.Request {
		r := script()
		r.Header.Set("Sec-Fetch-Site", "same-origin")
		return r
	}

	site, _, err := e.st.Create()
	if err != nil {
		t.Fatal(err)
	}

	// community build: no provider, everything open
	if !e.api.apiAllowedFor(site, script()) {
		t.Error("community build must allow the API")
	}

	ext.Register(&fakeProvider{enabled: true})
	defer ext.Reset()

	if e.api.apiAllowedFor(site, script()) {
		t.Error("anonymous site must refuse a scripted call")
	}
	if !e.api.apiAllowedFor(site, browser()) {
		t.Error("anonymous site must allow its own edit page")
	}

	if err := e.st.Update(site, func(m *store.Meta) error { m.OwnerAccountID = "acct-1"; return nil }); err != nil {
		t.Fatal(err)
	}
	if !e.api.apiAllowedFor(site, script()) {
		t.Error("owned site must allow a scripted call")
	}
}
```

Add the imports this file needs at the top: `net/http`, `github.com/ittrail/sitebin.io/internal/store`.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/httpapi/ -run 'FromOwnBrowser|APIAllowedFor' -v`
Expected: FAIL to compile — `fromOwnBrowser` and `apiAllowedFor` are undefined.

- [ ] **Step 3: Write the gate**

Create `internal/httpapi/apigate.go`:

```go
package httpapi

import (
	"net/http"
	"strings"

	"github.com/ittrail/sitebin.io/internal/ext"
	"github.com/ittrail/sitebin.io/internal/store"
)

// embedOriginAllowed reports whether origin (already lowercased) is
// allowlisted for cross-origin use of the create flow. Cross-origin embedding
// is an enterprise capability, so it also requires an active provider.
func (a *API) embedOriginAllowed(origin string) bool {
	if origin == "" || len(a.cfg.EmbedOrigins) == 0 {
		return false
	}
	if p, ok := ext.Get(); !ok || !p.EmbedOriginsAllowed() {
		return false
	}
	for _, allowed := range a.cfg.EmbedOrigins {
		if allowed == "*" || allowed == origin {
			return true
		}
	}
	return false
}

// fromOwnBrowser reports whether the request carries the fetch metadata a
// browser sends when Sitebin's own pages — or an allowlisted embed — call the
// API. Scripts, CI jobs and agents send neither header.
//
// This is a PLAN boundary, not a security boundary. Sec-Fetch-Site and Origin
// are trivially forgeable outside a browser; the point is to keep automation
// on accounts, not to make anonymous automation impossible. Never use this to
// protect anything that matters.
func (a *API) fromOwnBrowser(r *http.Request) bool {
	fetchSite := r.Header.Get("Sec-Fetch-Site")
	if fetchSite == "" {
		return false
	}
	origin := strings.ToLower(r.Header.Get("Origin"))
	if origin == "" {
		// Same-origin GETs and some same-origin fetches omit Origin entirely.
		return fetchSite == "same-origin"
	}
	if origin == strings.ToLower(a.cfg.SiteURL(a.cfg.BaseDomain)) {
		return true
	}
	return a.embedOriginAllowed(origin)
}

// apiAllowedFor reports whether the caller may drive site through the JSON
// API. Sites created without an account have no API: their claim ticket
// manages them through the edit page only.
//
// The rule applies only where a provider gates creation at all, so the
// community build — which has no provider — stays fully open.
func (a *API) apiAllowedFor(site *store.Site, r *http.Request) bool {
	p, ok := ext.Get()
	if !ok || !p.AccountsEnabled() {
		return true
	}
	if site.Meta.OwnerAccountID != "" {
		return true
	}
	return a.fromOwnBrowser(r)
}

// apiAccountHint is the upgrade path shown when the gate refuses a caller.
func (a *API) apiAccountHint() string {
	return a.cfg.SiteURL(a.cfg.BaseDomain) + "/account"
}
```

- [ ] **Step 4: Reuse the allowlist check in createCORS**

In `internal/httpapi/sites.go`, replace the body of `createCORS` below the `Vary` header so the allowlist lives in one place:

```go
func (a *API) createCORS(w http.ResponseWriter, r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" || len(a.cfg.EmbedOrigins) == 0 {
		return false
	}
	if p, ok := ext.Get(); !ok || !p.EmbedOriginsAllowed() {
		return false
	}
	w.Header().Add("Vary", "Origin")
	if !a.embedOriginAllowed(strings.ToLower(origin)) {
		return false
	}
	w.Header().Set("Access-Control-Allow-Origin", origin)
	return true
}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./internal/httpapi/ -v`
Expected: PASS, including the existing CORS tests around line 1017-1090.

- [ ] **Step 6: Commit**

```bash
git add internal/httpapi/apigate.go internal/httpapi/apigate_test.go internal/httpapi/sites.go
git commit -m "feat: browser-fetch detection for the account-only API gate

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 5: The site API needs an account

**Files:**
- Modify: `internal/httpapi/server.go:231-238` (`withEditAuth`, the `verifyOK` branch)
- Modify: `internal/httpapi/httpapi_test.go:93-118` (the `createSite` helper)
- Test: `internal/httpapi/apigate_test.go` (append)

**Interfaces:**
- Consumes: `apiAllowedFor`, `apiAccountHint` from Task 4.
- Produces: nothing new.

- [ ] **Step 1: Write the failing test**

Append to `internal/httpapi/apigate_test.go`:

```go
func TestAnonymousSiteAPIRefusesScripts(t *testing.T) {
	ext.Register(&fakeProvider{enabled: true})
	defer ext.Reset()
	e := newEnv(t, nil)
	c := e.createSite(t, nil, map[string]string{"index.html": "x"})
	edit := editIDFrom(t, c.EditURL)

	// script-shaped: correct password, no browser fetch metadata
	req := authed(httptest.NewRequest("GET", "/api/sites/"+edit, nil), c.EditPassword)
	if w := e.public(t, req); w.Code != 403 {
		t.Fatalf("scripted call on an anonymous site: %d %s", w.Code, w.Body)
	}

	// the edit page's own fetch
	req = authed(httptest.NewRequest("GET", "/api/sites/"+edit, nil), c.EditPassword)
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	if w := e.public(t, req); w.Code != 200 {
		t.Fatalf("edit page call: %d %s", w.Code, w.Body)
	}
}

func TestOwnedSiteAPIAllowsScripts(t *testing.T) {
	ext.Register(&fakeProvider{enabled: true, owner: "acct-1"})
	defer ext.Reset()
	e := newEnv(t, nil)
	c := e.createSite(t, nil, map[string]string{"index.html": "x"})
	edit := editIDFrom(t, c.EditURL)

	req := authed(httptest.NewRequest("GET", "/api/sites/"+edit, nil), c.EditPassword)
	if w := e.public(t, req); w.Code != 200 {
		t.Fatalf("scripted call on an owned site: %d %s", w.Code, w.Body)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/httpapi/ -run TestAnonymousSiteAPIRefusesScripts -v`
Expected: FAIL — the scripted call returns 200.

- [ ] **Step 3: Gate the edit-auth middleware**

In `internal/httpapi/server.go`, replace the `verifyOK` branch of `withEditAuth`:

```go
		switch a.verifyEdit(r, site, pw) {
		case verifyOK:
			next(w, r, site)
```

with:

```go
		switch a.verifyEdit(r, site, pw) {
		case verifyOK:
			// The API is an account feature. An anonymous site keeps its claim
			// ticket and its edit page; it just cannot be scripted.
			if !a.apiAllowedFor(site, r) {
				writeError(w, 403, "this site was created without an account, so it has no API — create it while signed in at "+a.apiAccountHint()+" to script it")
				return
			}
			next(w, r, site)
```

- [ ] **Step 4: Make the test helper browser-shaped**

The `createSite` helper in `internal/httpapi/httpapi_test.go` speaks for the web UI in almost every test, so it should look like the web UI. After the request is built (`req := httptest.NewRequest("POST", "/api/sites", &buf)`), add:

```go
	req.Header.Set("Sec-Fetch-Site", "same-origin")
```

Any test that wants a script-shaped create builds its own request (Task 6 does).

- [ ] **Step 5: Run the whole suite**

Run: `go test ./... && go test -tags ee ./ee/...`
Expected: PASS. If `TestExpiryCapDefaultsLifetime` or `TestExpiryCapKeepsExplicitExpiry` (`httpapi_test.go` ~1104-1140) fail with 401, the helper edit in Step 4 was missed.

- [ ] **Step 6: Commit**

```bash
git add internal/httpapi/server.go internal/httpapi/apigate_test.go internal/httpapi/httpapi_test.go
git commit -m "feat: the site API refuses scripts on account-less sites

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 6: Creation via the API needs an account

**Files:**
- Modify: `internal/httpapi/sites.go:406` (`createSite`, right after `owner := grant.OwnerAccountID`)
- Test: `internal/httpapi/apigate_test.go` (append)

**Interfaces:**
- Consumes: `fromOwnBrowser`, `apiAccountHint` from Task 4.
- Produces: nothing new.

- [ ] **Step 1: Write the failing test**

Append to `internal/httpapi/apigate_test.go`:

```go
// scriptCreate posts a minimal multipart create with no browser headers.
func scriptCreate(t *testing.T, e *env, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", `form-data; name="files"; filename="index.html"`)
	p, _ := mw.CreatePart(h)
	p.Write([]byte("<h1>hi</h1>"))
	mw.Close()
	req := httptest.NewRequest("POST", "/api/sites", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return e.public(t, req)
}

func TestAnonymousCreateRefusesScripts(t *testing.T) {
	ext.Register(&fakeProvider{enabled: true})
	defer ext.Reset()
	e := newEnv(t, nil)

	if w := scriptCreate(t, e, nil); w.Code != 401 {
		t.Fatalf("scripted anonymous create: %d %s", w.Code, w.Body)
	}
	if w := scriptCreate(t, e, map[string]string{"Sec-Fetch-Site": "same-origin"}); w.Code != 201 {
		t.Fatalf("browser anonymous create: %d %s", w.Code, w.Body)
	}
}

func TestAnonymousCreateAllowsAllowlistedEmbed(t *testing.T) {
	ext.Register(&fakeProvider{enabled: true, embedOK: true})
	defer ext.Reset()
	e := newEnv(t, map[string]string{"SITEBIN_EMBED_ORIGINS": "https://sitebin.io"})

	w := scriptCreate(t, e, map[string]string{
		"Sec-Fetch-Site": "cross-site",
		"Origin":         "https://sitebin.io",
	})
	if w.Code != 201 {
		t.Fatalf("embed create: %d %s", w.Code, w.Body)
	}
}

func TestOwnedCreateAllowsScripts(t *testing.T) {
	ext.Register(&fakeProvider{enabled: true, owner: "acct-1"})
	defer ext.Reset()
	e := newEnv(t, nil)

	if w := scriptCreate(t, e, nil); w.Code != 201 {
		t.Fatalf("owned scripted create: %d %s", w.Code, w.Body)
	}
}
```

Add the imports this file now needs: `bytes`, `mime/multipart`, `net/textproto`.

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/httpapi/ -run TestAnonymousCreateRefusesScripts -v`
Expected: FAIL — the scripted create returns 201.

- [ ] **Step 3: Gate anonymous creation**

In `internal/httpapi/sites.go`, `createSite` currently reads:

```go
	owner := grant.OwnerAccountID

	r.Body = http.MaxBytesReader(w, r.Body, a.cfg.MaxSiteBytes+(10<<20))
```

Insert the gate between them:

```go
	owner := grant.OwnerAccountID

	// The API is an account feature: an anonymous drop is something you make
	// on Sitebin's own pages (or an allowlisted embed), not from a script.
	if gated && owner == "" && !a.fromOwnBrowser(r) {
		writeError(w, 401, "sign in to create sites from the API: "+a.apiAccountHint())
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, a.cfg.MaxSiteBytes+(10<<20))
```

- [ ] **Step 4: Run the whole suite**

Run: `go test ./... && go test -tags ee ./ee/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/httpapi/sites.go internal/httpapi/apigate_test.go
git commit -m "feat: creating a site from a script needs an account

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 7: Tier numbers, docs and the deployment example

**Files:**
- Modify: `README.md` (the config table row for `SITEBIN_MAX_EXPIRY_DAYS` ~line 108; the "API (for scripts and agents)" section ~line 143)
- Modify: `deploy/docker-compose.example.yml` (the `SITEBIN_TIERS` example ~lines 156-161)
- Modify: `e2e/.work/tiers.json` — leave alone; it is generated test scaffolding.

**Interfaces:**
- Consumes: the behaviour built in Tasks 1-6.
- Produces: the tier JSON the hosted instance will copy (documented, not deployed here).

- [ ] **Step 1: Document the API rule in the README**

In `README.md`, the API section opens with:

```markdown
### API (for scripts and agents)

The only credential is the edit id (in the URL) + the edit password
(`X-Edit-Password` header). No accounts, no OAuth.
```

Replace that second paragraph with:

```markdown
The only credential is the edit id (in the URL) + the edit password
(`X-Edit-Password` header). No accounts, no OAuth.

On an instance that runs with accounts (`SITEBIN_ACCOUNT_MODE=accounts|tiers`),
the API is an account feature: a site created anonymously answers `403` to
scripted calls and is managed through its edit page instead, and creating a
site from a script needs an account. Requests carrying browser fetch metadata
(`Sec-Fetch-Site`, plus an `Origin` that is the instance or an allowlisted
`SITEBIN_EMBED_ORIGINS` entry) are treated as coming from Sitebin's own pages.
This is a plan boundary, not a security boundary — the headers are forgeable.
A community instance has no provider and its API stays fully open.
```

- [ ] **Step 2: Document the lifetime semantics**

In the same file, replace the `SITEBIN_MAX_EXPIRY_DAYS` table row:

```markdown
| `SITEBIN_MAX_EXPIRY_DAYS` | `0` (off) | Optional cap on how far in the future expiry may be set. |
```

with:

```markdown
| `SITEBIN_MAX_EXPIRY_DAYS` | `0` (off) | Cap on how far in the future expiry may be set. A cap is also the default lifetime of a site created without an explicit expiry, and it cannot be cleared from the API. |
```

And add this paragraph directly under the config table (before the `---` on line 121):

```markdown
A tier's `max_expiry_days` works the same way per site and adds one rule:
while a site **owned by an account** stays under a cap, every content change
(upload, delete, replace, WebDAV write) slides its expiry to `now + cap`. An
anonymous site keeps the expiry stamped at creation — a drop cannot renew
itself. FTP writes do not slide the expiry.
```

- [ ] **Step 3: Update the deployment example**

In `deploy/docker-compose.example.yml`, replace the commented `SITEBIN_TIERS` example (~line 156) with the hosted shape:

```yaml
      # SITEBIN_TIERS: '[
      #   {"id":"drop","label":"Drop","max_sites":0,"max_site_bytes":26214400,"max_files":200,"webdav":false,"custom_domains":0,"max_expiry_days":1},
      #   {"id":"free","label":"Free","max_sites":10,"max_site_bytes":104857600,"max_files":500,"webdav":true,"custom_domains":0,"max_expiry_days":7},
      #   {"id":"pro","label":"Pro","max_sites":100,"max_site_bytes":1073741824,"max_files":5000,"webdav":true,"custom_domains":5,"max_expiry_days":0},
      #   {"id":"studio","label":"Studio","max_sites":500,"max_site_bytes":5368709120,"max_files":10000,"webdav":true,"custom_domains":25,"max_expiry_days":0}
      # ]'
      # SITEBIN_ANON_TIER: "drop"   # anonymous drops: 24 h, no API
      # SITEBIN_DEFAULT_TIER: "free"
```

Keep any surrounding comment lines that already explain `SITEBIN_TIERS_FILE`.

- [ ] **Step 4: Verify the example parses**

The example is commented-out YAML, so no test reads it. Validate the JSON by hand — paste the array (without the leading `# ` on each line) into PowerShell:

```powershell
'[{"id":"drop","label":"Drop","max_sites":0,"max_site_bytes":26214400,"max_files":200,"webdav":false,"custom_domains":0,"max_expiry_days":1},{"id":"free","label":"Free","max_sites":10,"max_site_bytes":104857600,"max_files":500,"webdav":true,"custom_domains":0,"max_expiry_days":7},{"id":"pro","label":"Pro","max_sites":100,"max_site_bytes":1073741824,"max_files":5000,"webdav":true,"custom_domains":5,"max_expiry_days":0},{"id":"studio","label":"Studio","max_sites":500,"max_site_bytes":5368709120,"max_files":10000,"webdav":true,"custom_domains":25,"max_expiry_days":0}]' | ConvertFrom-Json | Format-Table id, max_expiry_days, max_sites
```

Expected: four rows, `max_expiry_days` reading 1, 7, 0, 0. Then run `go test -tags ee ./ee/...` to confirm nothing else broke.

- [ ] **Step 5: Commit**

```bash
git add README.md deploy/docker-compose.example.yml
git commit -m "docs: tier lifetimes, sliding renewal and the account-only API

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 8: Product copy

**Files:**
- Modify: `web/static/app.js` (`showTicket` ~lines 51-72)
- Modify: `web/static/embed.js` (the ticket sub-line ~line 404 and the publish handler ~line 711)
- Modify: `web/static/edit.js` (expiry rendering ~lines 183-190, clear button ~line 269)
- Modify: `web/static/index.html` (the `api-hint` line ~line 82)

**Interfaces:**
- Consumes: the create response, which already carries `expires_at` (see `sitePayload`, `internal/httpapi/sites.go:317`).
- Produces: nothing other code depends on.

**Note:** `web/static/embed.js` and `web/static/index.html` already hold unrelated uncommitted changes. Edit around them; stage these four files only.

- [ ] **Step 1: Show the lifetime on the claim ticket**

In `web/static/app.js`, `showTicket(body)` sets the ticket rows. Add a lifetime line at the end of the function body, before the `sessionStorage.setItem` call:

```js
  const life = $("t-life");
  if (life) {
    if (body.expires_at) {
      const d = new Date(body.expires_at);
      const hours = Math.round((d - Date.now()) / 3600000);
      life.textContent = hours <= 48
        ? "This site disappears in " + hours + " hours. Sign in before then to keep it."
        : "This site expires " + d.toLocaleString() + " — every change you make pushes that back.";
    } else {
      life.textContent = "No expiry — this site stays up until you delete it.";
    }
  }
```

Then add the element it writes into. In `web/static/index.html`, inside the ticket block next to the existing `t-pass` row, add:

```html
      <p class="sub" id="t-life"></p>
```

- [ ] **Step 2: Same for the embeddable ticket**

In `web/static/embed.js`, the publish handler sets `this.$("t-pass").textContent = body.edit_password;`. Directly after that line add:

```js
      const life = this.$("t-life");
      if (life && body.expires_at) {
        const d = new Date(body.expires_at);
        const hours = Math.round((d - Date.now()) / 3600000);
        life.textContent = hours <= 48
          ? "Disappears in " + hours + " hours — sign in to keep it."
          : "Expires " + d.toLocaleString() + "; every change pushes that back.";
      }
```

And in the component's ticket template (~line 404, the `<p class="sub" id="ticket-sub">` line), add below it:

```html
    <p class="sub" id="t-life"></p>
```

- [ ] **Step 3: Reflect the cap on the edit page**

In `web/static/edit.js`, the expiry block reads:

```js
  if (site.expires_at) {
    const d = new Date(site.expires_at);
    $("e-expires").value = new Date(d.getTime() - d.getTimezoneOffset() * 60000).toISOString().slice(0, 16);
    $("expiry-status").textContent = "Expires " + d.toLocaleString();
  } else {
    $("e-expires").value = "";
    $("expiry-status").textContent = "No expiry — the site stays up.";
  }
```

Replace it with a version that hides "Clear" when the server would refuse it:

```js
  const capped = site.expiry_cap_days > 0;
  if (site.expires_at) {
    const d = new Date(site.expires_at);
    $("e-expires").value = new Date(d.getTime() - d.getTimezoneOffset() * 60000).toISOString().slice(0, 16);
    $("expiry-status").textContent = capped
      ? "Expires " + d.toLocaleString() + " — this plan's sites always have an expiry."
      : "Expires " + d.toLocaleString();
  } else {
    $("e-expires").value = "";
    $("expiry-status").textContent = "No expiry — the site stays up.";
  }
  $("clear-expiry").hidden = capped;
```

This needs the API to publish the cap it enforces, so the button disappears exactly when the server would refuse it (Task 3). In `internal/httpapi/sites.go`, `sitePayload` builds the response map; add next to the `"expires_at"` entry:

```go
		"expiry_cap_days":         a.expiryCap(site),
```

The signature is `func (a *API) sitePayload(site *store.Site) map[string]any`, so `a.expiryCap(site)` is already in scope.

- [ ] **Step 4: Mark the curl hint as account-only**

In `web/static/index.html`, replace:

```html
    <span>for robots: <code id="api-hint">curl -F "files=@index.html" https://…/api/sites</code></span>
```

with:

```html
    <span>for robots <span class="dim">(account required)</span>: <code id="api-hint">curl -F "files=@index.html" https://…/api/sites</code></span>
```

- [ ] **Step 5: Verify in the browser**

Run: `go run ./cmd/sitebin` with `SITEBIN_BASE_DOMAIN=localhost:8080 SITEBIN_HTTP_ONLY=true SITEBIN_VIEW_ACCESS=path SITEBIN_DATA_DIR=./.devdata`
Open `http://localhost:8080`, drop a file, confirm the ticket shows a lifetime line, then open the edit URL and confirm the expiry row reads correctly.
Expected: ticket shows "No expiry — this site stays up until you delete it." on a community instance (no tiers configured), and the Clear button is visible.

- [ ] **Step 6: Commit**

```bash
git add web/static/app.js web/static/embed.js web/static/edit.js web/static/index.html internal/httpapi/sites.go
git commit -m "feat: claim ticket and edit page state the site's lifetime

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 9: Website — pricing page and doc sweep

**Files (repo `C:\Projects\Sitebin-Website`):**
- Modify: `public/pricing/index.html` (plan cards ~67-97, footnote ~138-140, comparison table ~156-167, FAQ ~207-230)
- Modify: `public/index.html`, `public/docs/api/index.html`, `public/docs/using/index.html`, `public/docs/embed/index.html` — sweep

**Interfaces:**
- Consumes: nothing from the product repo; the numbers come from this plan.
- Produces: the public promise the product now keeps.

- [ ] **Step 1: Rewrite the Drop card**

In `public/pricing/index.html`, replace the Drop card's list:

```html
          <ul>
            <li>unlimited drops <span class="dim">(rate-limited)</span></li>
            <li><strong>25 MB</strong> per site · 200 files</li>
            <li>sites live for <strong>7 days</strong></li>
            <li>full API + claim ticket</li>
            <li class="no">custom domains</li>
            <li class="no">WebDAV / FTP</li>
          </ul>
```

with:

```html
          <ul>
            <li>unlimited drops <span class="dim">(rate-limited)</span></li>
            <li><strong>25 MB</strong> per site · 200 files</li>
            <li>sites live for <strong>24 hours</strong></li>
            <li>claim ticket to edit and delete</li>
            <li class="no">API &amp; CI deploys</li>
            <li class="no">custom domains</li>
          </ul>
```

- [ ] **Step 2: Rewrite the Free card**

Replace the Free card's blurb and list:

```html
          <p class="blurb">Sign in and your sites stay up for good.</p>
          <ul>
            <li><strong>10 sites</strong></li>
            <li><strong>100 MB</strong> per site · 500 files</li>
            <li>sites <strong>never expire</strong></li>
            <li>WebDAV network drive</li>
            <li>dashboard for all your sites</li>
            <li class="no">custom domains</li>
          </ul>
```

with:

```html
          <p class="blurb">Sign in and your sites stay as long as you use them.</p>
          <ul>
            <li><strong>10 sites</strong></li>
            <li><strong>100 MB</strong> per site · 500 files</li>
            <li>sites live <strong>7 days</strong> <span class="dim">— reset by every change</span></li>
            <li>full API + CI deploys</li>
            <li>WebDAV network drive</li>
            <li>dashboard for all your sites</li>
          </ul>
```

- [ ] **Step 3: Fix the footnote under the cards**

Replace:

```html
      <p class="dim" style="margin-top:18px;font-size:13px">Prices exclude VAT. Yearly
        billing is ten months' price — two months free. Anonymous sites never lock you
        out: the edit URL and password keep working even without an account.</p>
```

with:

```html
      <p class="dim" style="margin-top:18px;font-size:13px">Prices exclude VAT. Yearly
        billing is ten months' price — two months free. Paid plans keep every site
        until you delete it.</p>
```

- [ ] **Step 4: Fix the comparison table**

Replace these three rows:

```html
            <tr><th scope="row">Site lifetime</th><td>7 days</td><td>permanent</td><td>permanent</td><td>permanent</td></tr>
```
```html
            <tr><th scope="row">API + CI deploy</th><td>✓</td><td>✓</td><td>✓</td><td>✓</td></tr>
```

with:

```html
            <tr><th scope="row">Site lifetime</th><td>24 hours</td><td>7 days*</td><td>until deleted</td><td>until deleted</td></tr>
```
```html
            <tr><th scope="row">API + CI deploy</th><td class="na">—</td><td>✓</td><td>✓</td><td>✓</td></tr>
```

And extend the footnote below the table:

```html
      <p class="dim" style="margin-top:12px;font-size:13px">*Anonymous creation is
        rate-limited per IP to keep the free lane fast for everyone.</p>
```

becomes:

```html
      <p class="dim" style="margin-top:12px;font-size:13px">*A Free site's seven days
        restart with every change you publish to it — a site you keep working on stays
        online. Anonymous creation is rate-limited per IP to keep the free lane fast
        for everyone.</p>
```

- [ ] **Step 5: Rewrite the two stale FAQ answers**

Replace:

```html
          <p>After 7 days it answers "410 Gone" and its files are deleted 24 hours
            later. Want it permanent? Create a free account before it expires and the
            site is yours to keep.</p>
```

with:

```html
          <p>After 24 hours it answers "410 Gone" and its files are deleted shortly
            after. Want it to last? Create a free account and publish it there: Free
            sites live seven days and the clock restarts every time you change them,
            and paid plans keep sites until you delete them.</p>
```

Replace:

```html
          <p>No. The claim ticket (edit URL + password) manages an anonymous site
            forever — accounts just add a dashboard, permanence past 7 days, and quota
```

(and the rest of that answer) with:

```html
          <p>The claim ticket (edit URL + password) manages an anonymous site from its
            edit page for as long as it lives — but that is 24 hours, and scripted API
            access needs an account. Sign in and the same site type gets seven days
            that reset on every change, a dashboard, and the API.</p>
```

Read the surrounding markup first: the second answer runs past line 229 in the current file, so replace it to its closing `</p>`.

- [ ] **Step 6: Sweep the remaining pages**

Run in `C:\Projects\Sitebin-Website`:

```bash
grep -rn "7 days\|never expire\|full API\|no signup" public --include=*.html
```

Fix every hit that states a lifetime or an API promise, using the wording above. Expected locations: `public/index.html` (hero and feature copy), `public/docs/api/index.html` (the "no accounts" claim), `public/docs/using/index.html` (claim-ticket section), `public/docs/embed/index.html` (what an embed visitor gets). Leave `public/docs/quickstart/`, `configuration/`, `operations/`, `enterprise/` alone unless a hit appears — those describe self-hosting, where none of this applies.

- [ ] **Step 7: Check the pages render**

Run: `python -m http.server 4000 --directory public` (or any static server) and open `http://localhost:4000/pricing/`.
Expected: four plan cards, the table's lifetime row reading 24 hours / 7 days* / until deleted / until deleted, no leftover "never expire" anywhere.

- [ ] **Step 8: Commit (website repo)**

```bash
cd C:/Projects/Sitebin-Website
git add public
git commit -m "content: 24h anonymous drops, 7-day sliding Free, API needs an account

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

## Deployment (not part of any task)

The hosted instance at `app.sitebin.io` (`/opt/sitebin/`) carries its own tier
configuration. Before the website copy goes live, its `tiers.json` must match
§1 of the spec — `drop` at `max_expiry_days: 1`, `free` at `7`, `pro` and
`studio` at `0` — and the container must be restarted. Shipping the website
first would advertise limits the instance does not yet enforce.
