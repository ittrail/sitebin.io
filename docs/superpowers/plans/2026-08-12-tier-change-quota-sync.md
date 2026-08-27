# Tier Changes & Quota Sync Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A site's stamped quotas follow its owner's current tier, so an upgrade never loses a paying customer's sites and a downgrade cannot buy permanent hosting.

**Architecture:** Quotas stay stamped in `meta.json` — that stamp is the boundary that keeps the MIT core ignorant of accounts. The fix corrects *when* they are stamped again. One reconciliation function in the store owns the whole transition table; two callers drive it. The cleanup sweep asks the extension for the owner's current caps before deleting an expired site (the upgrade direction, which must be right even for a customer who never visits), and the enterprise provider restamps an account's sites whenever it notices the resolved tier differs from the stored one (the downgrade direction, where a delay favours the customer).

**Tech Stack:** Go 1.25, stdlib only. The enterprise extension lives behind the `ee` build tag and is reached through the `internal/ext` seam.

## Global Constraints

- The community build registers no provider. Every behaviour added here must be inert there: cleanup deletes exactly as it does today, and no new refusal or restamp fires.
- **A failed tier lookup must never delete data.** If the owner's current tier cannot be determined, the site is kept and retried on the next sweep. A site kept too long is recoverable; a deleted one is not.
- `effectiveTier` deliberately fails *open* (a PayGate outage falls back to the stored tier) because blocking site creation on a billing outage is wrong. The deletion guard needs the opposite, so it uses a separate strict variant that surfaces the error.
- The downgrade grace is **30 days**, a named constant — not the new tier's own cap. Dropping a hundred permanent sites to seven days would be a deletion in all but name.
- Greenfield: no migration, no backfill, no compatibility shims.
- `go build ./... && go test ./...` plus `go test -tags ee ./ee/...` must pass before every commit. The `ee` package only compiles under its build tag.
- End every commit message with:
  `Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>`
- Stage only the files each task names; never `git add -A`.

## File Structure

**Modified:**
- `internal/store/meta.go` — one new field recording where an expiry came from.
- `internal/store/expiry.go` — gains the quota-reconciliation function that owns the transition table; renewal marks the expiry as tier-imposed.
- `internal/httpapi/sites.go` — the creation stamp marks its expiry as tier-imposed; an explicit `expires_at` marks it as the user's.
- `internal/ext/ext.go` — two seam methods.
- `internal/httpapi/siteservice.go` — implements the new `SiteService` method.
- `internal/cleanup/cleanup.go` — the deletion guard.
- `ee/provider.go` — the strict tier resolver, `QuotaFor`, and the sync-on-notice.
- `ee/dashboard.go`, `ee/billingh.go` — call the sync at the moments a tier changes.
- Test doubles: `internal/httpapi/gate_test.go` (`fakeProvider`), `ee/provider_test.go` (`fakeSites`).
- `C:\Projects\Sitebin-Website\public\pricing\index.html` — the cancellation FAQ.

**No new files.** Each change lands next to the code it belongs to; the transition table lives in `internal/store/expiry.go`, which already owns expiry policy.

---

### Task 1: Record where an expiry came from

**Files:**
- Modify: `internal/store/meta.go` (the `Meta` struct)
- Modify: `internal/store/expiry.go` (`renewExpiryLocked`)
- Modify: `internal/httpapi/sites.go` (`applySettings` explicit-timestamp branch; the creation default-lifetime stamp in `createSite`)
- Test: `internal/store/expiry_test.go`, `internal/httpapi/httpapi_test.go`

**Interfaces:**
- Consumes: nothing from other tasks.
- Produces: `Meta.ExpiryFromTier bool` (JSON `expiry_from_tier,omitempty`) — true when the current `ExpiresAt` was imposed by the tier, false when a caller chose it. Tasks 2-4 branch on it.

- [ ] **Step 1: Write the failing tests**

Append to `internal/store/expiry_test.go`:

```go
func TestRenewMarksExpiryAsTierImposed(t *testing.T) {
	s := newExpiryStore(t)
	site := expiringSite(t, s, "acct-1", 7, 2*time.Hour)
	// the helper sets the expiry directly, so the flag starts false
	if err := s.SaveFile(site, "index.html", strings.NewReader("x")); err != nil {
		t.Fatal(err)
	}
	got, _ := s.ByViewID(site.ViewID)
	if !got.Meta.ExpiryFromTier {
		t.Fatal("renewal did not mark the expiry as tier-imposed")
	}
}
```

Append to `internal/httpapi/httpapi_test.go`:

```go
func TestCreationStampMarksExpiryAsTierImposed(t *testing.T) {
	ext.Register(&fakeProvider{enabled: true, owner: "acct-1", grant: ext.CreateGrant{MaxExpiryDays: 7}})
	defer ext.Reset()
	e := newEnv(t, nil)
	c := e.createSite(t, nil, map[string]string{"index.html": "x"})

	site, _ := e.st.ByViewID(c.ID)
	if site.Meta.ExpiresAt == nil {
		t.Fatal("no expiry stamped")
	}
	if !site.Meta.ExpiryFromTier {
		t.Fatal("the tier's default lifetime should be marked as tier-imposed")
	}
}

func TestExplicitExpiryIsNotTierImposed(t *testing.T) {
	ext.Register(&fakeProvider{enabled: true, owner: "acct-1", grant: ext.CreateGrant{MaxExpiryDays: 7}})
	defer ext.Reset()
	e := newEnv(t, nil)
	c := e.createSite(t, nil, map[string]string{"index.html": "x"})
	edit := editIDFrom(t, c.EditURL)

	when := time.Now().Add(48 * time.Hour).UTC().Format(time.RFC3339)
	req := authed(httptest.NewRequest("PUT", "/api/sites/"+edit, strings.NewReader(`{"expires_at":"`+when+`"}`)), c.EditPassword)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	if w := e.public(t, req); w.Code != 200 {
		t.Fatalf("PUT: %d %s", w.Code, w.Body)
	}
	site, _ := e.st.ByViewID(c.ID)
	if site.Meta.ExpiryFromTier {
		t.Fatal("a caller-chosen expiry must not be marked as tier-imposed")
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/store/ -run TestRenewMarks -v` and `go test ./internal/httpapi/ -run 'TierImposed' -v`
Expected: FAIL to compile — `ExpiryFromTier` is not a field of `Meta`.

- [ ] **Step 3: Add the field**

In `internal/store/meta.go`, add to the `Meta` struct, directly under `ExpiresAt`:

```go
	// ExpiryFromTier records whether ExpiresAt was imposed by the owner's tier
	// (the creation default, sliding renewal, or a downgrade grace) rather than
	// chosen by a caller. A tier change may lift an imposed expiry; it must
	// never silently discard one the owner asked for.
	ExpiryFromTier bool `json:"expiry_from_tier,omitempty"`
```

- [ ] **Step 4: Set it in the three places that impose an expiry**

In `internal/store/expiry.go`, `renewExpiryLocked` currently sets `meta.ExpiresAt = &want`. Add directly after it:

```go
	meta.ExpiryFromTier = true
```

In `internal/httpapi/sites.go`, `createSite`'s default-lifetime block currently reads:

```go
	if site.Meta.QuotaExpiryDays > 0 && site.Meta.ExpiresAt == nil {
		exp := time.Now().Add(time.Duration(site.Meta.QuotaExpiryDays) * 24 * time.Hour).UTC()
		if err := a.st.Update(site, func(m *store.Meta) error { m.ExpiresAt = &exp; return nil }); err != nil {
```

Change the mutation to also set the flag:

```go
		if err := a.st.Update(site, func(m *store.Meta) error { m.ExpiresAt = &exp; m.ExpiryFromTier = true; return nil }); err != nil {
```

In the same file, `applySettings` writes the caller's expiry through the `expires` pointer. Find the block that applies it inside `a.st.Update` and set `m.ExpiryFromTier = false` wherever `m.ExpiresAt = *expires` is assigned — a caller-supplied value, including a clear, is never tier-imposed. Read the surrounding code before editing; the assignment is inside the same `Update` closure that applies the other settings.

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go build ./... && go test ./internal/store/ ./internal/httpapi/`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/store/meta.go internal/store/expiry.go internal/store/expiry_test.go internal/httpapi/sites.go internal/httpapi/httpapi_test.go
git commit -m "feat: record whether a site's expiry came from its tier

A tier change may lift an expiry the plan imposed; it must never discard
one the owner chose.

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 2: The reconciliation function

**Files:**
- Modify: `internal/store/expiry.go`
- Test: `internal/store/expiry_test.go`

**Interfaces:**
- Consumes: `Meta.ExpiryFromTier` from Task 1.
- Produces:
  - `type Quota struct { Bytes int64; Files int; ExpiryDays int; Domains *int; WebDAV *bool }`
  - `const DowngradeGrace = 30 * 24 * time.Hour`
  - `func (s *Store) ApplyQuota(site *Site, q Quota, grace time.Duration) error`
  Tasks 3 and 4 both call `ApplyQuota`; Task 4 passes `DowngradeGrace`, Task 3 passes `0`.

This is the whole transition table in one place, so the two callers cannot drift apart.

- [ ] **Step 1: Write the failing tests**

Append to `internal/store/expiry_test.go`:

```go
func quotaSite(t *testing.T, s *Store, days int, expiresIn time.Duration, fromTier bool) *Site {
	t.Helper()
	site, _, err := s.Create()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Update(site, func(m *Meta) error {
		m.OwnerAccountID = "acct-1"
		m.QuotaExpiryDays = days
		if expiresIn != 0 {
			exp := time.Now().Add(expiresIn).UTC()
			m.ExpiresAt = &exp
			m.ExpiryFromTier = fromTier
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return site
}

func TestApplyQuotaUpgradeClearsTierExpiry(t *testing.T) {
	s := newExpiryStore(t)
	site := quotaSite(t, s, 7, 3*24*time.Hour, true)

	if err := s.ApplyQuota(site, Quota{Bytes: 1 << 30, Files: 5000, ExpiryDays: 0}, DowngradeGrace); err != nil {
		t.Fatal(err)
	}
	got, _ := s.ByViewID(site.ViewID)
	if got.Meta.ExpiresAt != nil {
		t.Fatalf("tier expiry survived an upgrade to unlimited: %v", got.Meta.ExpiresAt)
	}
	if got.Meta.QuotaBytes != 1<<30 || got.Meta.QuotaFiles != 5000 || got.Meta.QuotaExpiryDays != 0 {
		t.Fatalf("quotas not restamped: %+v", got.Meta)
	}
}

func TestApplyQuotaUpgradeKeepsUserExpiry(t *testing.T) {
	s := newExpiryStore(t)
	site := quotaSite(t, s, 7, 3*24*time.Hour, false) // the owner chose this date
	before := *site.Meta.ExpiresAt

	if err := s.ApplyQuota(site, Quota{ExpiryDays: 0}, DowngradeGrace); err != nil {
		t.Fatal(err)
	}
	got, _ := s.ByViewID(site.ViewID)
	if got.Meta.ExpiresAt == nil || !got.Meta.ExpiresAt.Equal(before) {
		t.Fatalf("a caller-chosen expiry was discarded: %v", got.Meta.ExpiresAt)
	}
}

func TestApplyQuotaDowngradeStampsGrace(t *testing.T) {
	s := newExpiryStore(t)
	site := quotaSite(t, s, 0, 0, false) // paid site: no expiry at all

	if err := s.ApplyQuota(site, Quota{ExpiryDays: 7}, DowngradeGrace); err != nil {
		t.Fatal(err)
	}
	got, _ := s.ByViewID(site.ViewID)
	if got.Meta.ExpiresAt == nil {
		t.Fatal("downgrade did not stamp a grace expiry")
	}
	want := time.Now().Add(DowngradeGrace)
	if d := got.Meta.ExpiresAt.Sub(want); d > time.Minute || d < -time.Minute {
		t.Fatalf("grace expiry = %v, want ~%v", got.Meta.ExpiresAt, want)
	}
	if !got.Meta.ExpiryFromTier {
		t.Fatal("the grace expiry must be marked as tier-imposed")
	}
}

func TestApplyQuotaClampsExpiryBeyondNewCap(t *testing.T) {
	s := newExpiryStore(t)
	site := quotaSite(t, s, 0, 90*24*time.Hour, false) // far-future date the owner chose

	if err := s.ApplyQuota(site, Quota{ExpiryDays: 7}, DowngradeGrace); err != nil {
		t.Fatal(err)
	}
	got, _ := s.ByViewID(site.ViewID)
	want := time.Now().Add(7 * 24 * time.Hour)
	if d := got.Meta.ExpiresAt.Sub(want); d > time.Minute || d < -time.Minute {
		t.Fatalf("expiry beyond the new cap = %v, want clamped to ~%v", got.Meta.ExpiresAt, want)
	}
}

func TestApplyQuotaLeavesExpiryWithinNewCap(t *testing.T) {
	s := newExpiryStore(t)
	site := quotaSite(t, s, 7, 2*24*time.Hour, true)
	before := *site.Meta.ExpiresAt

	if err := s.ApplyQuota(site, Quota{ExpiryDays: 7}, DowngradeGrace); err != nil {
		t.Fatal(err)
	}
	got, _ := s.ByViewID(site.ViewID)
	if !got.Meta.ExpiresAt.Equal(before) {
		t.Fatalf("expiry within the cap was moved: %v -> %v", before, got.Meta.ExpiresAt)
	}
}

func TestApplyQuotaWithZeroGraceDoesNotStampAnExpiry(t *testing.T) {
	s := newExpiryStore(t)
	site := quotaSite(t, s, 0, 0, false)

	if err := s.ApplyQuota(site, Quota{ExpiryDays: 7}, 0); err != nil {
		t.Fatal(err)
	}
	got, _ := s.ByViewID(site.ViewID)
	if got.Meta.ExpiresAt != nil {
		t.Fatalf("grace 0 must not impose an expiry, got %v", got.Meta.ExpiresAt)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/store/ -run ApplyQuota -v`
Expected: FAIL to compile — `Quota`, `DowngradeGrace` and `ApplyQuota` are undefined.

- [ ] **Step 3: Implement the reconciliation**

Add to `internal/store/expiry.go`:

```go
// DowngradeGrace is how long a site that had no expiry keeps living after its
// owner moves to a tier that caps lifetimes. It is deliberately longer than any
// tier's own cap: dropping a permanent site to its new tier's lifetime would be
// a deletion in all but name.
const DowngradeGrace = 30 * 24 * time.Hour

// Quota is the set of per-site caps a tier grants. It mirrors the Quota* fields
// of Meta; nil pointers mean "inherit the instance global".
type Quota struct {
	Bytes      int64
	Files      int
	ExpiryDays int
	Domains    *int
	WebDAV     *bool
}

// ApplyQuota restamps a site's per-site caps and reconciles its expiry with the
// new lifetime cap. It is the single place the tier-change transition table
// lives, so the cleanup sweep and the enterprise tier sync cannot drift apart.
//
// grace is how long a site that currently has NO expiry gets before the new cap
// applies. Callers that must not create an expiry (the cleanup sweep, which is
// only ever looking at sites that already have one) pass 0.
//
// An expiry the owner chose is never lifted, only clamped when it exceeds the
// new cap.
func (s *Store) ApplyQuota(site *Site, q Quota, grace time.Duration) error {
	return s.Update(site, func(m *Meta) error {
		m.QuotaBytes = q.Bytes
		m.QuotaFiles = q.Files
		m.QuotaExpiryDays = q.ExpiryDays
		m.QuotaDomains = q.Domains
		m.QuotaWebDAV = q.WebDAV

		now := time.Now()
		if q.ExpiryDays <= 0 {
			// the tier no longer expires sites; lift only what the tier imposed
			if m.ExpiryFromTier {
				m.ExpiresAt = nil
				m.ExpiryFromTier = false
			}
			return nil
		}
		limit := now.Add(time.Duration(q.ExpiryDays) * 24 * time.Hour).UTC()
		switch {
		case m.ExpiresAt == nil:
			if grace <= 0 {
				return nil
			}
			until := now.Add(grace).UTC()
			m.ExpiresAt = &until
			m.ExpiryFromTier = true
		case m.ExpiresAt.After(limit):
			m.ExpiresAt = &limit
			m.ExpiryFromTier = true
		}
		return nil
	})
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/store/ -v`
Expected: PASS, including the pre-existing renewal tests.

- [ ] **Step 5: Commit**

```bash
git add internal/store/expiry.go internal/store/expiry_test.go
git commit -m "feat: one function reconciles a site's quotas with a new tier

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 3: The seam — asking for an owner's current caps

**Files:**
- Modify: `internal/ext/ext.go` (the `Provider` and `SiteService` interfaces)
- Modify: `internal/httpapi/siteservice.go`
- Modify: `internal/httpapi/gate_test.go` (`fakeProvider`)
- Modify: `ee/provider.go`
- Modify: `ee/provider_test.go` (`fakeSites`)
- Test: `internal/httpapi/apigate_test.go`

**Interfaces:**
- Consumes: `store.Quota`, `store.ApplyQuota`, `store.DowngradeGrace` from Task 2.
- Produces:
  - `ext.Provider.QuotaFor(ownerAccountID string) (CreateGrant, bool, error)`
  - `ext.SiteService.ApplyQuota(viewID string, g CreateGrant) error`
  - `(*provider).effectiveTierStrict(acc *account.Account) (eeconfig.Tier, error)` in `ee/`
  Task 4 calls `ApplyQuota` through the seam and `effectiveTierStrict` directly; Task 5 calls `QuotaFor`.

Adding methods to these interfaces breaks every implementer at compile time, which is the point — this task is done when everything compiles again.

- [ ] **Step 1: Add the two seam methods**

In `internal/ext/ext.go`, add to the `Provider` interface, after `AuthorizeCreate`:

```go
	// QuotaFor returns the caps the owner's CURRENT tier grants, so the core can
	// restamp a site whose stamped quotas predate a tier change. ok=false means
	// the account is unknown (deleted, or accounts are not enabled). A non-nil
	// error means the tier could not be determined — callers MUST NOT act
	// destructively on an error; a site kept too long is recoverable, a deleted
	// one is not.
	QuotaFor(ownerAccountID string) (CreateGrant, bool, error)
```

And to the `SiteService` interface:

```go
	// ApplyQuota restamps a site's per-site caps from a grant and reconciles its
	// expiry with the new lifetime cap.
	ApplyQuota(viewID string, g CreateGrant) error
```

- [ ] **Step 2: Implement it in the core's SiteService**

In `internal/httpapi/siteservice.go`:

```go
func (s siteService) ApplyQuota(viewID string, g ext.CreateGrant) error {
	site, err := s.a.st.ByViewID(viewID)
	if err != nil {
		return err
	}
	return s.a.st.ApplyQuota(site, quotaFromGrant(g), store.DowngradeGrace)
}

// quotaFromGrant maps the extension's grant onto the store's cap set.
func quotaFromGrant(g ext.CreateGrant) store.Quota {
	return store.Quota{
		Bytes:      g.MaxSiteBytes,
		Files:      g.MaxFiles,
		ExpiryDays: g.MaxExpiryDays,
		Domains:    g.MaxCustomDomain,
		WebDAV:     g.WebDAV,
	}
}
```

Add the `internal/store` import to that file.

- [ ] **Step 3: Implement it in the enterprise provider**

In `ee/provider.go`, add a strict tier resolver next to `effectiveTier`. `effectiveTier` deliberately falls back to the stored tier when PayGate fails, because refusing to create a site during a billing outage is worse than using a stale tier. The deletion guard needs the opposite:

```go
// effectiveTierStrict resolves an account's tier like effectiveTier, but
// surfaces a PayGate failure instead of falling back to the stored tier.
// Callers that would destroy data on a wrong answer use this one.
func (p *provider) effectiveTierStrict(acc *account.Account) (eeconfig.Tier, error) {
	if p.paygate != nil && acc.Provider == account.OIDCProv && acc.OAuthSubject != "" {
		id, ok, err := p.paygate.TierFor(context.Background(), acc.OAuthSubject)
		if err != nil {
			return eeconfig.Tier{}, fmt.Errorf("paygate tier lookup for %s: %w", acc.ID, err)
		}
		if ok {
			if t, found := p.cfg.Tier(id); found {
				return t, nil
			}
			slog.Warn("paygate returned a tier missing from tiers config; using stored tier", "tier", id)
		}
	}
	if t, ok := p.cfg.Tier(acc.Tier); ok {
		return t, nil
	}
	t, _ := p.cfg.Tier(p.cfg.DefaultTier)
	return t, nil
}

// QuotaFor returns the caps the owner's current tier grants. See ext.Provider.
func (p *provider) QuotaFor(ownerAccountID string) (ext.CreateGrant, bool, error) {
	if !p.cfg.Enabled() || ownerAccountID == "" {
		return ext.CreateGrant{}, false, nil
	}
	acc, err := p.accounts.ByID(ownerAccountID)
	if err != nil {
		// an account that no longer exists is not an error: the site is
		// orphaned and its caller may proceed
		return ext.CreateGrant{}, false, nil
	}
	if p.cfg.Mode != eeconfig.ModeTiers {
		return ext.CreateGrant{OwnerAccountID: acc.ID}, true, nil
	}
	t, err := p.effectiveTierStrict(acc)
	if err != nil {
		return ext.CreateGrant{}, false, err
	}
	return grantFromTier(acc.ID, t), true, nil
}
```

Check what `p.accounts.ByID` returns for a missing account before writing the error branch — if it distinguishes "not found" from an I/O failure, return the I/O failure as an error rather than swallowing it, and only treat "not found" as `ok=false`.

- [ ] **Step 4: Update the two test doubles**

In `internal/httpapi/gate_test.go`, `fakeProvider` gains fields and a method:

```go
	quota    ext.CreateGrant
	quotaOK  bool
	quotaErr error
```

```go
func (f *fakeProvider) QuotaFor(string) (ext.CreateGrant, bool, error) {
	return f.quota, f.quotaOK, f.quotaErr
}
```

In `ee/provider_test.go`, `fakeSites` gains:

```go
	quotas map[string]ext.CreateGrant
```

```go
func (s *fakeSites) ApplyQuota(id string, g ext.CreateGrant) error {
	if s.quotas == nil {
		s.quotas = map[string]ext.CreateGrant{}
	}
	s.quotas[id] = g
	return nil
}
```

- [ ] **Step 5: Verify everything compiles and the suites still pass**

Run: `go build ./... && go test ./... && go test -tags ee ./ee/...`
Expected: PASS. A compile error naming an unimplemented interface method means an implementer was missed — find it and add the method.

- [ ] **Step 6: Commit**

```bash
git add internal/ext/ext.go internal/httpapi/siteservice.go internal/httpapi/gate_test.go ee/provider.go ee/provider_test.go
git commit -m "feat: seam methods for reading and restamping an owner's caps

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 4: The deletion guard

**Files:**
- Modify: `internal/cleanup/cleanup.go`
- Test: `internal/cleanup/cleanup_test.go`

**Interfaces:**
- Consumes: `ext.Get()`, `ext.Provider.QuotaFor`, `store.ApplyQuota`, `store.Quota` from Tasks 2 and 3.
- Produces: nothing later tasks depend on.

`internal/cleanup` does not import `internal/ext` today; it will now.

- [ ] **Step 1: Write the failing tests**

The cleanup package has no test double yet. Append to `internal/cleanup/cleanup_test.go`:

```go
// stubProvider is the minimum ext.Provider the sweep consults.
type stubProvider struct {
	grant ext.CreateGrant
	ok    bool
	err   error
	calls []string
}

func (p *stubProvider) Name() string                        { return "stub" }
func (p *stubProvider) Version() string                     { return "0" }
func (p *stubProvider) Init(ext.Host) error                 { return nil }
func (p *stubProvider) PublicRoutes() map[string]http.Handler { return nil }
func (p *stubProvider) AccountsEnabled() bool               { return true }
func (p *stubProvider) CustomDomainsAllowed() bool          { return true }
func (p *stubProvider) EmbedOriginsAllowed() bool           { return true }
func (p *stubProvider) AuthorizeCreate(*http.Request) (ext.CreateGrant, error) {
	return ext.CreateGrant{}, nil
}
func (p *stubProvider) OnSiteCreated(string, string) error { return nil }
func (p *stubProvider) QuotaFor(owner string) (ext.CreateGrant, bool, error) {
	p.calls = append(p.calls, owner)
	return p.grant, p.ok, p.err
}

// expiredOwnedSite creates a site owned by acct-1, capped, and expired past the
// grace window.
func expiredOwnedSite(t *testing.T, st *store.Store, now time.Time, days int, fromTier bool) *store.Site {
	t.Helper()
	site, _, err := st.Create()
	if err != nil {
		t.Fatal(err)
	}
	when := now.Add(-25 * time.Hour)
	if err := st.Update(site, func(m *store.Meta) error {
		m.OwnerAccountID = "acct-1"
		m.QuotaExpiryDays = days
		m.ExpiresAt = &when
		m.ExpiryFromTier = fromTier
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return site
}

func TestSweepKeepsSiteWhoseOwnerUpgraded(t *testing.T) {
	p := &stubProvider{grant: ext.CreateGrant{MaxExpiryDays: 0, MaxSiteBytes: 1 << 30}, ok: true}
	ext.Register(p)
	defer ext.Reset()
	st, err := store.New(t.TempDir(), "sitebin.example", 1<<20, 100)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	site := expiredOwnedSite(t, st, now, 7, true)

	removed, err := Sweep(st, now)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 0 {
		t.Fatalf("removed = %d, want 0 — the owner is now on an unlimited tier", removed)
	}
	got, err := st.ByViewID(site.ViewID)
	if err != nil {
		t.Fatalf("site deleted despite the upgrade: %v", err)
	}
	if got.Meta.ExpiresAt != nil {
		t.Fatalf("expiry not lifted: %v", got.Meta.ExpiresAt)
	}
	if got.Meta.QuotaBytes != 1<<30 {
		t.Fatalf("quotas not restamped: %+v", got.Meta)
	}
}

func TestSweepDeletesSiteStillCapped(t *testing.T) {
	ext.Register(&stubProvider{grant: ext.CreateGrant{MaxExpiryDays: 7}, ok: true})
	defer ext.Reset()
	st, _ := store.New(t.TempDir(), "sitebin.example", 1<<20, 100)
	now := time.Now().UTC()
	site := expiredOwnedSite(t, st, now, 7, true)

	if removed, err := Sweep(st, now); err != nil || removed != 1 {
		t.Fatalf("removed = %d, err = %v, want 1, nil", removed, err)
	}
	if _, err := st.ByViewID(site.ViewID); err == nil {
		t.Fatal("a site still capped by its tier survived")
	}
}

func TestSweepKeepsSiteWhenTierLookupFails(t *testing.T) {
	ext.Register(&stubProvider{err: errors.New("paygate down")})
	defer ext.Reset()
	st, _ := store.New(t.TempDir(), "sitebin.example", 1<<20, 100)
	now := time.Now().UTC()
	site := expiredOwnedSite(t, st, now, 7, true)

	removed, err := Sweep(st, now)
	if err != nil {
		t.Fatalf("a lookup failure must not fail the sweep: %v", err)
	}
	if removed != 0 {
		t.Fatalf("removed = %d, want 0 — a failed lookup must never delete", removed)
	}
	got, err := st.ByViewID(site.ViewID)
	if err != nil {
		t.Fatalf("site deleted after a failed lookup: %v", err)
	}
	if got.Meta.ExpiresAt == nil || got.Meta.QuotaExpiryDays != 7 {
		t.Fatalf("meta touched despite the failed lookup: %+v", got.Meta)
	}
}

func TestSweepDeletesSiteWithUnknownOwner(t *testing.T) {
	ext.Register(&stubProvider{ok: false})
	defer ext.Reset()
	st, _ := store.New(t.TempDir(), "sitebin.example", 1<<20, 100)
	now := time.Now().UTC()
	expiredOwnedSite(t, st, now, 7, true)

	if removed, err := Sweep(st, now); err != nil || removed != 1 {
		t.Fatalf("removed = %d, err = %v, want 1, nil", removed, err)
	}
}

func TestSweepDeletesUserChosenExpiryOnUnlimitedTier(t *testing.T) {
	ext.Register(&stubProvider{grant: ext.CreateGrant{MaxExpiryDays: 0}, ok: true})
	defer ext.Reset()
	st, _ := store.New(t.TempDir(), "sitebin.example", 1<<20, 100)
	now := time.Now().UTC()
	expiredOwnedSite(t, st, now, 0, false) // the owner asked for this date

	if removed, err := Sweep(st, now); err != nil || removed != 1 {
		t.Fatalf("removed = %d, err = %v, want 1, nil — the owner chose this expiry", removed, err)
	}
}

func TestSweepIgnoresProviderForAnonymousSites(t *testing.T) {
	p := &stubProvider{grant: ext.CreateGrant{MaxExpiryDays: 0}, ok: true}
	ext.Register(p)
	defer ext.Reset()
	st, _ := store.New(t.TempDir(), "sitebin.example", 1<<20, 100)
	now := time.Now().UTC()
	site, _, _ := st.Create()
	when := now.Add(-25 * time.Hour)
	if err := st.Update(site, func(m *store.Meta) error { m.ExpiresAt = &when; return nil }); err != nil {
		t.Fatal(err)
	}

	if removed, err := Sweep(st, now); err != nil || removed != 1 {
		t.Fatalf("removed = %d, err = %v, want 1, nil", removed, err)
	}
	if len(p.calls) != 0 {
		t.Fatalf("provider consulted for an anonymous site: %v", p.calls)
	}
}
```

Add the imports this file needs: `errors`, `net/http`, and `github.com/ittrail/sitebin.io/internal/ext`.

The existing `TestSweep` registers no provider and must keep passing unchanged — it is the community-build case.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/cleanup/ -v`
Expected: FAIL — `TestSweepKeepsSiteWhoseOwnerUpgraded` and `TestSweepKeepsSiteWhenTierLookupFails` report `removed = 1`, because the sweep deletes unconditionally.

- [ ] **Step 3: Add the guard**

In `internal/cleanup/cleanup.go`, replace the deletion branch of the loop:

```go
	for _, site := range sites {
		if site.Meta.ExpiresAt != nil && now.After(site.Meta.ExpiresAt.Add(grace)) {
			if err := st.Delete(site); err != nil {
```

with:

```go
	for _, site := range sites {
		if site.Meta.ExpiresAt == nil || !now.After(site.Meta.ExpiresAt.Add(grace)) {
			continue
		}
		if keep, err := reconcile(st, site); err != nil {
			slog.Error("cleanup: tier lookup failed, keeping site", "id", site.ViewID, "err", err)
			continue
		} else if keep {
			slog.Info("cleanup: kept expired site, its owner's tier no longer expires sites", "id", site.ViewID)
			continue
		}
		if err := st.Delete(site); err != nil {
```

and add the helper below `Sweep`:

```go
// reconcile restamps an owned site from its owner's CURRENT tier before the
// site is deleted, and reports whether the site should now be kept. A tier
// change between creation and expiry is invisible in the stamped meta, so an
// upgrade would otherwise arrive too late to save the site.
//
// An error means the owner's tier could not be determined; the caller must keep
// the site and retry on the next sweep.
func reconcile(st *store.Store, site *store.Site) (keep bool, err error) {
	if site.Meta.OwnerAccountID == "" {
		return false, nil // anonymous: nothing to look up
	}
	p, ok := ext.Get()
	if !ok {
		return false, nil // community build: no accounts, no tiers
	}
	grant, found, err := p.QuotaFor(site.Meta.OwnerAccountID)
	if err != nil {
		return false, err
	}
	if !found {
		return false, nil // the account is gone; the site is orphaned
	}
	// grace 0: this site already has an expiry, and a sweep must never invent one
	if err := st.ApplyQuota(site, store.Quota{
		Bytes:      grant.MaxSiteBytes,
		Files:      grant.MaxFiles,
		ExpiryDays: grant.MaxExpiryDays,
		Domains:    grant.MaxCustomDomain,
		WebDAV:     grant.WebDAV,
	}, 0); err != nil {
		return false, err
	}
	return site.Meta.ExpiresAt == nil, nil
}
```

`ApplyQuota` refreshes `site.Meta` in place, so the final check reads the reconciled value: the expiry is nil exactly when the new tier lifted it.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/cleanup/ -v && go test ./...`
Expected: PASS, including the unchanged `TestSweep`.

- [ ] **Step 5: Commit**

```bash
git add internal/cleanup/cleanup.go internal/cleanup/cleanup_test.go
git commit -m "fix: an upgrade saves a site even if it arrives at the last minute

The sweep now asks for the owner's current tier before deleting, and
keeps the site when the lookup fails.

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 5: Tier sync on notice

**Files:**
- Modify: `ee/provider.go`
- Modify: `ee/dashboard.go` (`handleSelectTier`, and the account page render)
- Modify: `ee/billingh.go` (`applyBillingUpdate`)
- Test: `ee/tiers_test.go`

**Interfaces:**
- Consumes: `effectiveTierStrict`, `grantFromTier`, `p.host.Sites().ApplyQuota` from Task 3.
- Produces: `func (p *provider) syncTier(acc *account.Account)` — persists a changed tier onto the account and restamps every site it owns. Fire-and-forget: it logs its failures rather than surfacing them, because it runs on request paths whose primary job is something else.

- [ ] **Step 1: Write the failing tests**

**Which path `syncTier` actually serves.** Without PayGate, the resolved tier is derived *from* `acc.Tier`, so it can never differ from it and `syncTier` is a no-op by construction — the dashboard, self-select and billing paths restamp directly in Step 4 instead. PayGate is the one source that changes a tier behind Sitebin's back, so the tests must use the PayGate setup. `ee/tiers_test.go` already has `pgStub(t, tier, status, code)` and `setupPayGate(t, srvURL)` for exactly this.

Append to `ee/tiers_test.go`:

```go
func TestSyncTierRestampsOwnedSitesFromPayGate(t *testing.T) {
	srv := pgStub(t, "pro", "active", 200)
	defer srv.Close()
	p := setupPayGate(t, srv.URL)
	acc, err := p.accounts.CreateOAuth(account.OIDCProv, "stack-user-sync", "sync@example.com", "free")
	if err != nil {
		t.Fatal(err)
	}
	if err := p.accounts.LinkSite(acc, "site-1"); err != nil {
		t.Fatal(err)
	}

	p.syncTier(acc)

	if acc.Tier != "pro" {
		t.Fatalf("stored tier = %q, want pro", acc.Tier)
	}
	sites := p.host.Sites().(*fakeSites)
	g, ok := sites.quotas["site-1"]
	if !ok {
		t.Fatal("owned site was not restamped")
	}
	if g.MaxSiteBytes != 5000000 || g.MaxExpiryDays != 0 {
		t.Fatalf("restamped with the wrong tier: %+v", g)
	}
}

func TestSyncTierIsANoOpWhenNothingChanged(t *testing.T) {
	srv := pgStub(t, "free", "active", 200)
	defer srv.Close()
	p := setupPayGate(t, srv.URL)
	acc, err := p.accounts.CreateOAuth(account.OIDCProv, "stack-user-same", "same@example.com", "free")
	if err != nil {
		t.Fatal(err)
	}
	if err := p.accounts.LinkSite(acc, "site-1"); err != nil {
		t.Fatal(err)
	}

	p.syncTier(acc)

	sites := p.host.Sites().(*fakeSites)
	if len(sites.quotas) != 0 {
		t.Fatalf("restamped despite no tier change: %v", sites.quotas)
	}
}

func TestSyncTierLeavesEverythingAloneOnLookupFailure(t *testing.T) {
	srv := pgStub(t, "", "", 500)
	defer srv.Close()
	p := setupPayGate(t, srv.URL)
	acc, err := p.accounts.CreateOAuth(account.OIDCProv, "stack-user-down", "down@example.com", "free")
	if err != nil {
		t.Fatal(err)
	}
	if err := p.accounts.LinkSite(acc, "site-1"); err != nil {
		t.Fatal(err)
	}

	p.syncTier(acc)

	if acc.Tier != "free" {
		t.Fatalf("stored tier changed during an outage: %q", acc.Tier)
	}
	sites := p.host.Sites().(*fakeSites)
	if len(sites.quotas) != 0 {
		t.Fatalf("restamped despite a failed lookup: %v", sites.quotas)
	}
}
```

Note: `setupPayGate` builds the provider with a `fakeHost` whose `sites` field is a `*fakeSites`. Read `ee/provider_test.go` to confirm how `fakeHost.Sites()` is declared before writing the type assertion, and adjust it to match.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test -tags ee ./ee/ -run SyncTier -v`
Expected: FAIL to compile — `syncTier` is undefined.

- [ ] **Step 3: Implement the sync**

In `ee/provider.go`:

```go
// syncTier reconciles an account's sites with its current tier. Tier changes
// arrive through several doors — the dashboard, a billing webhook, or PayGate,
// which has no webhook at all and is only ever polled — so rather than hooking
// each one, every path that already resolves a tier calls this and it does
// nothing when nothing changed.
//
// It logs its failures instead of returning them: it runs on request paths
// whose primary job is something else, and a stale stamp is not worth failing
// a page render over. The cleanup sweep is the backstop that keeps a missed
// sync from destroying data.
func (p *provider) syncTier(acc *account.Account) {
	if p.cfg.Mode != eeconfig.ModeTiers {
		return
	}
	t, err := p.effectiveTierStrict(acc)
	if err != nil {
		slog.Warn("tier sync: could not resolve tier", "account", acc.ID, "err", err)
		return
	}
	if t.ID == acc.Tier {
		return
	}
	if err := p.accounts.Update(acc, func(cur *account.Account) error { cur.Tier = t.ID; return nil }); err != nil {
		slog.Error("tier sync: could not persist tier", "account", acc.ID, "tier", t.ID, "err", err)
		return
	}
	ids, err := p.accounts.ListSiteIDs(acc)
	if err != nil {
		slog.Error("tier sync: could not list sites", "account", acc.ID, "err", err)
		return
	}
	grant := grantFromTier(acc.ID, t)
	for _, id := range ids {
		if err := p.host.Sites().ApplyQuota(id, grant); err != nil {
			slog.Error("tier sync: could not restamp site", "account", acc.ID, "site", id, "err", err)
		}
	}
	slog.Info("tier sync: restamped owned sites", "account", acc.ID, "tier", t.ID, "sites", len(ids))
}
```

- [ ] **Step 4: Call it where a tier changes**

Three call sites. In `ee/dashboard.go`, `handleSelectTier` currently ends its update with a redirect:

```go
	if err := p.accounts.Update(acc, func(cur *account.Account) error { cur.Tier = t.ID; return nil }); err != nil {
		http.Error(w, "could not change tier", http.StatusInternalServerError)
		return
	}
	p.redirect(w, r, "/account")
```

The account's stored tier is now `t.ID`, so `syncTier` would see no difference. Restamp directly instead — insert before the redirect:

```go
	ids, err := p.accounts.ListSiteIDs(acc)
	if err != nil {
		slog.Error("tier change: could not list sites", "account", acc.ID, "err", err)
	} else {
		grant := grantFromTier(acc.ID, t)
		for _, id := range ids {
			if err := p.host.Sites().ApplyQuota(id, grant); err != nil {
				slog.Error("tier change: could not restamp site", "account", acc.ID, "site", id, "err", err)
			}
		}
	}
	p.redirect(w, r, "/account")
```

In `ee/billingh.go`, `applyBillingUpdate` ends by returning `p.accounts.Update(...)`. Capture that result, and when it succeeded and the update carried a tier change, restamp the same way:

```go
	changed := u.Canceled || u.TierID != ""
	if err := p.accounts.Update(acc, func(cur *account.Account) error { /* unchanged body */ }); err != nil {
		return err
	}
	if changed {
		if ids, err := p.accounts.ListSiteIDs(acc); err == nil {
			if t, ok := p.cfg.Tier(acc.Tier); ok {
				grant := grantFromTier(acc.ID, t)
				for _, id := range ids {
					if err := p.host.Sites().ApplyQuota(id, grant); err != nil {
						slog.Error("billing: could not restamp site", "account", acc.ID, "site", id, "err", err)
					}
				}
			}
		}
	}
	return nil
```

Note `p.accounts.Update` refreshes `acc` in place, so `acc.Tier` is the new value by then. Read the function before editing and keep its existing closure body exactly as it is.

In `ee/dashboard.go`, the account page render calls `p.effectiveTier(acc)`. Add `p.syncTier(acc)` immediately before it — this is the PayGate path, where a plan change has no webhook and the dashboard visit is the first moment Sitebin can notice.

`AuthorizeCreate` needs no change: `grantForAccount` already stamps the new site from `effectiveTier`, so a newly created site is never stale.

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test -tags ee ./ee/... -v && go build ./... && go test ./...`
Expected: PASS, including the existing `TestSelfSelectTier` cases.

- [ ] **Step 6: Commit**

```bash
git add ee/provider.go ee/dashboard.go ee/billingh.go ee/tiers_test.go
git commit -m "fix: a tier change restamps the account's existing sites

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 6: The cancellation FAQ

**Files (repo `C:\Projects\Sitebin-Website`):**
- Modify: `public/pricing/index.html` (the "What happens if I cancel a paid plan?" answer)

**Interfaces:**
- Consumes: the 30-day grace from Task 2.
- Produces: nothing.

- [ ] **Step 1: Rewrite the answer**

The current text promises nothing is deleted, which a 30-day grace makes untrue:

```html
        <details>
          <summary>What happens if I cancel a paid plan?</summary>
          <p>Your account drops back to Free limits at the end of the billing period.
            Nothing is deleted: sites over the Free quota stay online but read-only for
            uploads until you're back under the caps (or upgrade again). Custom domains
            beyond the Free tier stop being served.</p>
        </details>
```

Replace the answer with:

```html
        <details>
          <summary>What happens if I cancel a paid plan?</summary>
          <p>Your account drops back to Free limits at the end of the billing period, and
            your existing sites get <strong>30 days</strong> — the date shows on each one
            in your dashboard. Upgrade again within that window and it disappears; the
            sites carry on as before. Sites over the Free storage quota stay online but
            stop accepting uploads until you're back under the caps, and custom domains
            beyond the Free tier stop being served.</p>
        </details>
```

Read the surrounding markup first and match the real indentation; the snippet above is from a reading at plan time.

- [ ] **Step 2: Check the page still renders**

Serve the directory (`python -m http.server 4000 --directory public`) and open `/pricing/`.
Expected: the FAQ entry opens and closes cleanly, no stray "Nothing is deleted" anywhere on the page.

- [ ] **Step 3: Sweep for the same promise elsewhere**

Run in `C:\Projects\Sitebin-Website`:

```bash
grep -rn "Nothing is deleted\|nothing is deleted\|never deleted" public --include=*.html
```

Fix any other hit that describes what happens on cancellation. Leave hits that describe something else (for example the MIT licence's durability on `/open-source/`).

- [ ] **Step 4: Commit (website repo)**

```bash
cd C:/Projects/Sitebin-Website
git add public
git commit -m "content: cancelling a paid plan gives existing sites a 30-day grace

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

## Deployment (not part of any task)

This is the change the hosted rollout was waiting on. Once it ships, the
instance at `app.sitebin.io` (`/opt/sitebin/`) can receive the new tier
configuration: `tiers.json` with Drop `max_expiry_days: 1`, Free `7`, Pro and
Studio `0`, **plus `SITEBIN_ANON_TIER: "drop"` in the environment** — `eeconfig`
refuses to start when the anonymous tier names a tier that is not in the file.
Restart the container, confirm a drop is stamped with a 24-hour expiry, and only
then push the website repo.
