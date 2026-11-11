package cleanup

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ittrail/sitebin.io/internal/ext"
	"github.com/ittrail/sitebin.io/internal/gitrepo"
	"github.com/ittrail/sitebin.io/internal/store"
)

func TestSweep(t *testing.T) {
	st, err := store.New(t.TempDir(), "sitebin.example", 1<<20, 100)
	if err != nil {
		t.Fatal(err)
	}
	st.SetDomainVerifier(store.TrustingVerifier{}, "sitebin.example")
	now := time.Now().UTC()

	healthy, _, _ := st.Create()
	expiredRecent, _, _ := st.Create() // inside grace period → kept
	expiredOld, _, _ := st.Create()    // past grace → deleted
	if err := st.AddDomain(expiredOld, "old.example.org"); err != nil {
		t.Fatal(err)
	}

	set := func(s *store.Site, when time.Time) {
		if err := st.Update(s, func(m *store.Meta) error { m.ExpiresAt = &when; return nil }); err != nil {
			t.Fatal(err)
		}
	}
	set(expiredRecent, now.Add(-1*time.Hour))
	set(expiredOld, now.Add(-25*time.Hour))

	removed, err := Sweep(st, now, Options{})
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if removed != 1 {
		t.Errorf("removed = %d, want 1", removed)
	}
	if _, err := st.ByViewID(healthy.ViewID); err != nil {
		t.Errorf("healthy site removed: %v", err)
	}
	if _, err := st.ByViewID(expiredRecent.ViewID); err != nil {
		t.Errorf("in-grace site removed: %v", err)
	}
	if _, err := st.ByViewID(expiredOld.ViewID); err == nil {
		t.Error("expired site survived")
	}
	if _, err := st.ByDomain("old.example.org"); err == nil {
		t.Error("expired site's domain link survived")
	}
	if _, err := st.ByEditID(expiredOld.EditID); err == nil {
		t.Error("expired site's edit link survived")
	}
}

func TestSweepPrunesDanglingLinks(t *testing.T) {
	dir := t.TempDir()
	st, err := store.New(dir, "sitebin.example", 1<<20, 100)
	if err != nil {
		t.Fatal(err)
	}
	site, _, _ := st.Create()
	// simulate a crash that removed the site dir but left index links behind
	if err := os.RemoveAll(site.Dir()); err != nil {
		t.Fatal(err)
	}
	if _, err := Sweep(st, time.Now(), Options{}); err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(dir, "edit-index", site.EditID)); !os.IsNotExist(err) {
		t.Error("dangling edit link not pruned")
	}
}

// stubProvider is the minimum ext.Provider the sweep consults.
type stubProvider struct {
	grant ext.CreateGrant
	ok    bool
	err   error
	calls []string
	// beforeQuota, if set, runs inside QuotaFor before it returns — a hook for
	// simulating a concurrent mutation (e.g. an upload renewing the site's
	// expiry) that lands mid-sweep, between the sweep reading its stale
	// snapshot and ApplyQuota re-reading meta.json from disk.
	beforeQuota func()
}

func (p *stubProvider) Name() string                          { return "stub" }
func (p *stubProvider) Version() string                       { return "0" }
func (p *stubProvider) Init(ext.Host) error                   { return nil }
func (p *stubProvider) PublicRoutes() map[string]http.Handler { return nil }
func (p *stubProvider) AccountsEnabled() bool                 { return true }
func (p *stubProvider) CustomDomainsAllowed() error           { return nil }
func (p *stubProvider) EmbedOriginsAllowed() bool             { return true }
func (p *stubProvider) AuthorizeCreate(*http.Request) (ext.CreateGrant, error) {
	return ext.CreateGrant{}, nil
}
func (p *stubProvider) BearerCredential(*http.Request) (ext.Credential, bool) {
	return ext.Credential{}, false
}
func (p *stubProvider) AccountSiteIDs(string) ([]string, bool) { return nil, false }
func (p *stubProvider) OnSiteCreated(string, string) error     { return nil }
func (p *stubProvider) QuotaFor(owner string) (ext.CreateGrant, bool, error) {
	p.calls = append(p.calls, owner)
	if p.beforeQuota != nil {
		p.beforeQuota()
	}
	return p.grant, p.ok, p.err
}

// expiredOwnedSite creates a site owned by owner, capped, and expired past the
// grace window.
func expiredOwnedSite(t *testing.T, st *store.Store, now time.Time, owner string, days int, fromTier bool) *store.Site {
	t.Helper()
	site, _, err := st.Create()
	if err != nil {
		t.Fatal(err)
	}
	when := now.Add(-25 * time.Hour)
	if err := st.Update(site, func(m *store.Meta) error {
		m.OwnerAccountID = owner
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
	site := expiredOwnedSite(t, st, now, "acct-1", 7, true)

	removed, err := Sweep(st, now, Options{})
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
	site := expiredOwnedSite(t, st, now, "acct-1", 7, true)

	if removed, err := Sweep(st, now, Options{}); err != nil || removed != 1 {
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
	site := expiredOwnedSite(t, st, now, "acct-1", 7, true)

	removed, err := Sweep(st, now, Options{})
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
	expiredOwnedSite(t, st, now, "acct-1", 7, true)

	if removed, err := Sweep(st, now, Options{}); err != nil || removed != 1 {
		t.Fatalf("removed = %d, err = %v, want 1, nil", removed, err)
	}
}

func TestSweepDeletesUserChosenExpiryOnUnlimitedTier(t *testing.T) {
	ext.Register(&stubProvider{grant: ext.CreateGrant{MaxExpiryDays: 0}, ok: true})
	defer ext.Reset()
	st, _ := store.New(t.TempDir(), "sitebin.example", 1<<20, 100)
	now := time.Now().UTC()
	expiredOwnedSite(t, st, now, "acct-1", 0, false) // the owner asked for this date

	if removed, err := Sweep(st, now, Options{}); err != nil || removed != 1 {
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

	if removed, err := Sweep(st, now, Options{}); err != nil || removed != 1 {
		t.Fatalf("removed = %d, err = %v, want 1, nil", removed, err)
	}
	if len(p.calls) != 0 {
		t.Fatalf("provider consulted for an anonymous site: %v", p.calls)
	}
}

// TestSweepKeepsSiteRenewedDuringSweep guards against a race: the sweep reads
// its list of expired sites once up front, but a per-site QuotaFor round trip
// can take long enough for a concurrent upload to renew that same site's
// expiry (via the store's own sliding renewal) before this site's turn comes
// up. ApplyQuota re-reads meta.json under the lock and refreshes site.Meta in
// place, so the renewed expiry is what reconcile must judge — not whatever
// was true when Sweep first listed the site.
func TestSweepKeepsSiteRenewedDuringSweep(t *testing.T) {
	st, err := store.New(t.TempDir(), "sitebin.example", 1<<20, 100)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	site := expiredOwnedSite(t, st, now, "acct-1", 7, true)

	renewed := now.Add(48 * time.Hour)
	p := &stubProvider{grant: ext.CreateGrant{MaxExpiryDays: 7}, ok: true}
	p.beforeQuota = func() {
		// simulate an upload landing mid-sweep, between the sweep's stale
		// snapshot and ApplyQuota's fresh read.
		if err := st.Update(site, func(m *store.Meta) error { m.ExpiresAt = &renewed; return nil }); err != nil {
			t.Fatal(err)
		}
	}
	ext.Register(p)
	defer ext.Reset()

	removed, err := Sweep(st, now, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if removed != 0 {
		t.Fatalf("removed = %d, want 0 — a concurrent upload renewed the site mid-sweep", removed)
	}
	got, err := st.ByViewID(site.ViewID)
	if err != nil {
		t.Fatalf("site deleted despite being renewed mid-sweep: %v", err)
	}
	if got.Meta.ExpiresAt == nil || !got.Meta.ExpiresAt.Equal(renewed) {
		t.Fatalf("renewed expiry not preserved: %v", got.Meta.ExpiresAt)
	}
}

// TestSweepDeletesOwnedSiteInCommunityBuild covers the ext.Get() ok=false
// branch of reconcile with an OWNED site — the branch a data directory
// written by an ee-tagged binary and later run under the community binary
// still exercises, since owner_account_id survives in meta.json regardless
// of which binary reads it. TestSweep alone does not reach this branch: all
// of its sites are anonymous, so they return at the OwnerAccountID=="" check
// before ext.Get() is ever consulted.
func TestSweepDeletesOwnedSiteInCommunityBuild(t *testing.T) {
	// deliberately no ext.Register: this is the community build
	st, err := store.New(t.TempDir(), "sitebin.example", 1<<20, 100)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	site := expiredOwnedSite(t, st, now, "acct-1", 7, true)

	if removed, err := Sweep(st, now, Options{}); err != nil || removed != 1 {
		t.Fatalf("removed = %d, err = %v, want 1, nil — an owned expired site must still be deleted with no provider registered", removed, err)
	}
	if _, err := st.ByViewID(site.ViewID); err == nil {
		t.Fatal("owned site survived in the community build")
	}
}

// routedProvider answers QuotaFor per owner, so a single sweep can exercise
// several owners with different outcomes in one pass. It embeds stubProvider
// for the rest of the ext.Provider surface and for the shared calls log.
type routedProvider struct {
	stubProvider
	responses map[string]struct {
		grant ext.CreateGrant
		ok    bool
		err   error
	}
}

func (p *routedProvider) QuotaFor(owner string) (ext.CreateGrant, bool, error) {
	p.calls = append(p.calls, owner)
	r := p.responses[owner]
	return r.grant, r.ok, r.err
}

// TestSweepHandlesMultipleSitesIndependently guards against a loop bug (e.g.
// continue mistakenly written as break) that every other test in this file
// would miss, since each of them sweeps exactly one site: a break on the
// first decision would still leave that single-site sweep looking correct.
// Here four sites in four different states go through one Sweep call, and
// each must land exactly where its own state says it should.
func TestSweepHandlesMultipleSitesIndependently(t *testing.T) {
	p := &routedProvider{responses: map[string]struct {
		grant ext.CreateGrant
		ok    bool
		err   error
	}{
		"acct-upgraded":     {grant: ext.CreateGrant{MaxExpiryDays: 0}, ok: true},
		"acct-still-capped": {grant: ext.CreateGrant{MaxExpiryDays: 7}, ok: true},
		"acct-lookup-down":  {err: errors.New("paygate down")},
	}}
	ext.Register(p)
	defer ext.Reset()

	st, err := store.New(t.TempDir(), "sitebin.example", 1<<20, 100)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()

	upgraded := expiredOwnedSite(t, st, now, "acct-upgraded", 7, true)
	stillCapped := expiredOwnedSite(t, st, now, "acct-still-capped", 7, true)
	lookupDown := expiredOwnedSite(t, st, now, "acct-lookup-down", 7, true)
	anon, _, _ := st.Create()
	when := now.Add(-25 * time.Hour)
	if err := st.Update(anon, func(m *store.Meta) error { m.ExpiresAt = &when; return nil }); err != nil {
		t.Fatal(err)
	}

	removed, err := Sweep(st, now, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if removed != 2 {
		t.Fatalf("removed = %d, want 2 (the still-capped site and the anonymous one)", removed)
	}
	if _, err := st.ByViewID(upgraded.ViewID); err != nil {
		t.Errorf("upgraded owner's site deleted: %v", err)
	}
	if _, err := st.ByViewID(stillCapped.ViewID); err == nil {
		t.Error("still-capped site survived")
	}
	if _, err := st.ByViewID(lookupDown.ViewID); err != nil {
		t.Errorf("site with a failed lookup deleted: %v", err)
	}
	if _, err := st.ByViewID(anon.ViewID); err == nil {
		t.Error("anonymous site survived")
	}
}

// The sweep is the safety net the marker's fail-safe polarity relies on: a site
// created before the marker existed, or one whose tier changed without a
// restamp, has to converge on the right answer.
func TestSweepReconcilesTheTrustMarker(t *testing.T) {
	ext.Register(&trustProvider{trusted: map[string]bool{"trusted-acct": true}})
	defer ext.Reset()
	st, err := store.New(t.TempDir(), "sitebin.example", 1<<20, 100)
	if err != nil {
		t.Fatal(err)
	}
	own := func(owner string) *store.Site {
		site, _, _ := st.Create()
		if owner != "" {
			if err := st.Update(site, func(m *store.Meta) error { m.OwnerAccountID = owner; return nil }); err != nil {
				t.Fatal(err)
			}
		}
		return site
	}
	trusted := own("trusted-acct")
	untrusted := own("plain-acct")
	anon := own("")
	// an anonymous site wrongly carrying the marker: its account was deleted
	// and the exemption outlived the owner
	if err := st.SetTrusted(anon, true); err != nil {
		t.Fatal(err)
	}

	if _, err := Sweep(st, time.Now(), Options{}); err != nil {
		t.Fatal(err)
	}

	if !st.Trusted(trusted) {
		t.Error("a site owned on a trusted tier must gain the marker")
	}
	if st.Trusted(untrusted) {
		t.Error("a site owned on an untrusted tier must not carry the marker")
	}
	if st.Trusted(anon) {
		t.Error("an anonymous site must never keep the marker")
	}
}

// trustProvider answers only QuotaFor; the sweep needs nothing else from it.
type trustProvider struct {
	stubProvider
	trusted map[string]bool
}

func (p *trustProvider) QuotaFor(owner string) (ext.CreateGrant, bool, error) {
	return ext.CreateGrant{OwnerAccountID: owner, Trusted: p.trusted[owner]}, true, nil
}

// sweepVerifier flips per domain.
type sweepVerifier struct{ ok map[string]bool }

func (v *sweepVerifier) Verify(_ context.Context, domain, _, _ string) (bool, error) {
	return v.ok[domain], nil
}

// The sweep is the second half of domain verification: a claim whose record
// appeared after the owner clicked "add" is attached without them coming back.
func TestSweepAttachesAPendingDomainOnceProven(t *testing.T) {
	st, err := store.New(t.TempDir(), "sitebin.example", 1<<20, 100)
	if err != nil {
		t.Fatal(err)
	}
	v := &sweepVerifier{ok: map[string]bool{}}
	st.SetDomainVerifier(v, "sitebin.example")
	site, _, _ := st.Create()
	if err := st.AddDomain(site, "later.example.org"); !errors.Is(err, store.ErrDomainPending) {
		t.Fatalf("AddDomain = %v", err)
	}
	if _, err := Sweep(st, time.Now(), Options{}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ByDomain("later.example.org"); err == nil {
		t.Fatal("attached without proof")
	}
	v.ok["later.example.org"] = true
	if _, err := Sweep(st, time.Now(), Options{}); err != nil {
		t.Fatal(err)
	}
	if got, err := st.ByDomain("later.example.org"); err != nil || got.ViewID != site.ViewID {
		t.Fatalf("the sweep did not attach the proven domain: %v", err)
	}
}

func TestSweepReconcileKeepsTheFormsCap(t *testing.T) {
	ten := 10
	p := &stubProvider{grant: ext.CreateGrant{MaxExpiryDays: 0, MaxForms: &ten}, ok: true}
	ext.Register(p)
	defer ext.Reset()
	st, err := store.New(t.TempDir(), "sitebin.example", 1<<20, 100)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	site := expiredOwnedSite(t, st, now, "acct-1", 7, true)
	if _, err := Sweep(st, now, Options{}); err != nil {
		t.Fatal(err)
	}
	got, err := st.ByViewID(site.ViewID)
	if err != nil {
		t.Fatalf("site deleted: %v", err)
	}
	if got.Meta.QuotaForms == nil || *got.Meta.QuotaForms != 10 {
		t.Fatalf("QuotaForms = %v after reconcile, want the plan's 10", got.Meta.QuotaForms)
	}
}

func TestSweepPurgesReportsOlderThanRetention(t *testing.T) {
	st, _ := store.New(t.TempDir(), "sitebin.example", 1<<20, 100)
	now := time.Now()
	st.AddReport(store.Report{Target: "old", Reason: "x", Time: now.Add(-store.ReportRetention - time.Hour)})
	st.AddReport(store.Report{Target: "new", Reason: "x", Time: now})
	if _, err := Sweep(st, now, Options{}); err != nil {
		t.Fatal(err)
	}
	reports, _ := st.ListReports()
	if len(reports) != 1 || reports[0].Target != "new" {
		t.Fatalf("reports after sweep = %+v", reports)
	}
}

// A locked site is the operator's evidence hold: the sweep must neither
// delete it, however long ago it expired, nor restamp it from its owner's
// tier, nor touch its trust marker or domains. Unlocked, the same site is
// swept as usual.
func TestSweepLeavesALockedSiteAlone(t *testing.T) {
	p := &stubProvider{grant: ext.CreateGrant{MaxExpiryDays: 7, MaxSiteBytes: 1 << 30}, ok: true}
	ext.Register(p)
	defer ext.Reset()
	st, err := store.New(t.TempDir(), "sitebin.example", 1<<20, 100)
	if err != nil {
		t.Fatal(err)
	}
	st.SetDomainVerifier(store.TrustingVerifier{}, "sitebin.example")
	now := time.Now().UTC()
	site := expiredOwnedSite(t, st, now, "acct-1", 7, true)
	longAgo := now.Add(-400 * 24 * time.Hour)
	st.Update(site, func(m *store.Meta) error { m.ExpiresAt = &longAgo; return nil })
	if _, err := st.SetLock(site, &store.SiteLock{Reason: "phishing", By: store.LockByAdmin}); err != nil {
		t.Fatal(err)
	}

	removed, err := Sweep(st, now, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if removed != 0 {
		t.Fatalf("removed = %d, want 0 — the site is locked", removed)
	}
	got, err := st.ByViewID(site.ViewID)
	if err != nil {
		t.Fatalf("locked site deleted by the sweep: %v", err)
	}
	if got.Meta.QuotaBytes != 0 {
		t.Errorf("locked site restamped from the owner's tier: %+v", got.Meta)
	}
	if len(p.calls) != 0 {
		t.Errorf("the sweep asked the owner's tier for a locked site: %v", p.calls)
	}

	// Lifted, the hold is gone and the expiry applies again.
	if _, err := st.SetLock(got, nil); err != nil {
		t.Fatal(err)
	}
	if removed, _ := Sweep(st, now, Options{}); removed != 1 {
		t.Fatalf("removed = %d after unlock, want 1", removed)
	}
}

// The account-lock kind is held exactly like the operator's.
func TestSweepLeavesAnAccountLockedAnonymousSiteAlone(t *testing.T) {
	st, err := store.New(t.TempDir(), "sitebin.example", 1<<20, 100)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	site, _, _ := st.Create()
	when := now.Add(-48 * time.Hour)
	st.Update(site, func(m *store.Meta) error { m.ExpiresAt = &when; return nil })
	st.SetLock(site, &store.SiteLock{By: store.LockByAccount})
	if removed, _ := Sweep(st, now, Options{}); removed != 0 {
		t.Fatalf("removed = %d, want 0", removed)
	}
	if _, err := st.ByViewID(site.ViewID); err != nil {
		t.Fatalf("locked site gone: %v", err)
	}
}

func gitOwnedSite(t *testing.T, st *store.Store) *store.Site {
	t.Helper()
	site, _, err := st.Create()
	if err != nil {
		t.Fatal(err)
	}
	exp := time.Now().Add(48 * time.Hour).UTC()
	if err := st.Update(site, func(m *store.Meta) error {
		m.OwnerAccountID = "acct-1"
		m.ExpiresAt = &exp
		m.QuotaBytes = 5
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return site
}

func TestReconcileGitBackfillsWithoutTouchingExpiry(t *testing.T) {
	yes := true
	ext.Register(&stubProvider{ok: true, grant: ext.CreateGrant{OwnerAccountID: "acct-1", Git: &yes, MaxSiteBytes: 999, MaxFiles: 77, MaxExpiryDays: 1}})
	defer ext.Reset()
	st, err := store.New(t.TempDir(), "sitebin.example", 1<<20, 100)
	if err != nil {
		t.Fatal(err)
	}
	site := gitOwnedSite(t, st)
	before := *site.Meta.ExpiresAt

	reconcileGit(st, site)

	got, _ := st.ByViewID(site.ViewID)
	if got.Meta.QuotaGit == nil || !*got.Meta.QuotaGit {
		t.Fatalf("QuotaGit = %v, want true", got.Meta.QuotaGit)
	}
	if got.Meta.ExpiresAt == nil || !got.Meta.ExpiresAt.Equal(before) || got.Meta.QuotaBytes != 5 {
		t.Fatalf("reconcileGit touched other fields: expires %v (was %v), bytes %d", got.Meta.ExpiresAt, before, got.Meta.QuotaBytes)
	}
}

func TestReconcileGitLookupErrorChangesNothing(t *testing.T) {
	yes := true
	for name, p := range map[string]*stubProvider{
		"error":   {ok: true, err: errors.New("outage"), grant: ext.CreateGrant{Git: &yes}},
		"unknown": {ok: false, grant: ext.CreateGrant{Git: &yes}},
	} {
		t.Run(name, func(t *testing.T) {
			ext.Register(p)
			defer ext.Reset()
			st, _ := store.New(t.TempDir(), "sitebin.example", 1<<20, 100)
			site := gitOwnedSite(t, st)
			reconcileGit(st, site)
			got, _ := st.ByViewID(site.ViewID)
			if got.Meta.QuotaGit != nil {
				t.Fatalf("QuotaGit = %v, want nil", *got.Meta.QuotaGit)
			}
		})
	}
}

func TestReconcileGitCommunityBuildDoesNothing(t *testing.T) {
	ext.Reset()
	st, _ := store.New(t.TempDir(), "sitebin.example", 1<<20, 100)
	site := gitOwnedSite(t, st)
	reconcileGit(st, site)
	got, _ := st.ByViewID(site.ViewID)
	if got.Meta.QuotaGit != nil {
		t.Fatal("community build must not stamp git")
	}
}

// gitSite creates a site with an initialised, pushed-to repository.
func gitSite(t *testing.T, st *store.Store, r *gitrepo.Runner) *store.Site {
	t.Helper()
	site, _, err := st.Create()
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Init(site.GitDir()); err != nil {
		t.Fatal(err)
	}
	if err := gitrepo.MarkPushed(site.GitDir()); err != nil {
		t.Fatal(err)
	}
	return site
}

func collectFixture(t *testing.T) (*store.Store, *gitrepo.Runner) {
	t.Helper()
	bin := gitrepo.NeedGit(t)
	r, err := gitrepo.New(gitrepo.Options{Bin: bin, StateDir: filepath.Join(t.TempDir(), "gitstate")})
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.New(t.TempDir(), "sitebin.example", 1<<20, 100)
	if err != nil {
		t.Fatal(err)
	}
	return st, r
}

func gcMarker(site *store.Site) string { return filepath.Join(site.GitDir(), "sitebin-gc") }

func TestCollectGitRunsOnChanged(t *testing.T) {
	st, r := collectFixture(t)
	site := gitSite(t, st, r)
	CollectGit(context.Background(), st, r)
	if _, err := os.Stat(gcMarker(site)); err != nil {
		t.Fatalf("pushed repository was not collected: %v", err)
	}
}

func TestCollectGitSkipsLocked(t *testing.T) {
	st, r := collectFixture(t)
	site := gitSite(t, st, r)
	if _, err := st.SetLock(site, &store.SiteLock{Reason: "phishing", By: store.LockByAdmin}); err != nil {
		t.Fatal(err)
	}
	CollectGit(context.Background(), st, r)
	if _, err := os.Stat(gcMarker(site)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("locked site's repository was collected (stat err: %v)", err)
	}
}

func TestCollectGitSkipsARepositoryWithARunningWrite(t *testing.T) {
	st, r := collectFixture(t)
	busy := gitSite(t, st, r)
	free := gitSite(t, st, r)
	end, err := st.BeginGitWrite(busy, func() {})
	if err != nil {
		t.Fatal(err)
	}
	defer end()
	CollectGit(context.Background(), st, r)
	if _, err := os.Stat(gcMarker(busy)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("repository with a running push was collected (stat err: %v)", err)
	}
	if _, err := os.Stat(gcMarker(free)); err != nil {
		t.Fatalf("the other repository was not collected: %v", err)
	}
}

func TestSweepReconcileKeepsTheAccountCap(t *testing.T) {
	p := &stubProvider{grant: ext.CreateGrant{MaxExpiryDays: 0, MaxAccountBytes: 9000}, ok: true}
	ext.Register(p)
	defer ext.Reset()
	st, err := store.New(t.TempDir(), "sitebin.example", 1<<20, 100)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	site := expiredOwnedSite(t, st, now, "acct-1", 7, true)
	if _, err := Sweep(st, now, Options{}); err != nil {
		t.Fatal(err)
	}
	got, err := st.ByViewID(site.ViewID)
	if err != nil {
		t.Fatalf("site deleted: %v", err)
	}
	if got.Meta.QuotaAccountBytes != 9000 {
		t.Fatalf("QuotaAccountBytes = %d after reconcile, want the plan's 9000", got.Meta.QuotaAccountBytes)
	}
}

// The account cap is backfilled and follows a tiers.json edit (which changes
// no tier id, so nothing restamps), writing only that field.
func TestReconcileAccountBytesBackfillsAndFollows(t *testing.T) {
	p := &stubProvider{ok: true, grant: ext.CreateGrant{OwnerAccountID: "acct-1", MaxAccountBytes: 4000, MaxSiteBytes: 999, MaxExpiryDays: 1}}
	ext.Register(p)
	defer ext.Reset()
	st, _ := store.New(t.TempDir(), "sitebin.example", 1<<20, 100)
	site := gitOwnedSite(t, st)
	before := *site.Meta.ExpiresAt

	reconcileAccountBytes(st, site)
	got, _ := st.ByViewID(site.ViewID)
	if got.Meta.QuotaAccountBytes != 4000 {
		t.Fatalf("QuotaAccountBytes = %d, want 4000", got.Meta.QuotaAccountBytes)
	}
	if got.Meta.ExpiresAt == nil || !got.Meta.ExpiresAt.Equal(before) || got.Meta.QuotaBytes != 5 {
		t.Fatalf("reconcileAccountBytes touched other fields: expires %v (was %v), bytes %d", got.Meta.ExpiresAt, before, got.Meta.QuotaBytes)
	}
	p.grant.MaxAccountBytes = 0 // the operator removed the field
	reconcileAccountBytes(st, got)
	got, _ = st.ByViewID(site.ViewID)
	if got.Meta.QuotaAccountBytes != 0 {
		t.Fatalf("QuotaAccountBytes = %d after the plan dropped the cap, want 0", got.Meta.QuotaAccountBytes)
	}
}

func TestReconcileAccountBytesLookupErrorChangesNothing(t *testing.T) {
	for name, p := range map[string]*stubProvider{
		"error":   {ok: true, err: errors.New("outage"), grant: ext.CreateGrant{MaxAccountBytes: 4000}},
		"unknown": {ok: false, grant: ext.CreateGrant{MaxAccountBytes: 4000}},
	} {
		t.Run(name, func(t *testing.T) {
			ext.Register(p)
			defer ext.Reset()
			st, _ := store.New(t.TempDir(), "sitebin.example", 1<<20, 100)
			site := gitOwnedSite(t, st)
			st.Update(site, func(m *store.Meta) error { m.QuotaAccountBytes = 123; return nil })
			reconcileAccountBytes(st, site)
			got, _ := st.ByViewID(site.ViewID)
			if got.Meta.QuotaAccountBytes != 123 {
				t.Fatalf("QuotaAccountBytes = %d, want the stamp left at 123", got.Meta.QuotaAccountBytes)
			}
		})
	}
}

func TestSweepBackfillsTheAccountCap(t *testing.T) {
	ext.Register(&stubProvider{ok: true, grant: ext.CreateGrant{OwnerAccountID: "acct-1", MaxAccountBytes: 4000}})
	defer ext.Reset()
	st, _ := store.New(t.TempDir(), "sitebin.example", 1<<20, 100)
	site := gitOwnedSite(t, st)
	if _, err := Sweep(st, time.Now(), Options{}); err != nil {
		t.Fatal(err)
	}
	got, _ := st.ByViewID(site.ViewID)
	if got.Meta.QuotaAccountBytes != 4000 {
		t.Fatalf("QuotaAccountBytes = %d after a sweep, want 4000", got.Meta.QuotaAccountBytes)
	}
}

// Every sweep rebuilds the account figures from disk: growth nobody measured
// (a container's database) is found.
func TestSweepMeasuresAccounts(t *testing.T) {
	st, _ := store.New(t.TempDir(), "sitebin.example", 1<<20, 100)
	mk := func() *store.Site {
		site, _, _ := st.Create()
		st.Update(site, func(m *store.Meta) error { m.OwnerAccountID = "acct-1"; m.QuotaAccountBytes = 1 << 40; return nil })
		return site
	}
	a1, a2 := mk(), mk()
	if err := os.WriteFile(filepath.Join(a2.ContentDir(), "db.bin"), make([]byte, 77), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, others := st.AccountUsage(a1); others != 0 {
		t.Fatalf("others %d before a sweep, want 0 (nothing measured a2)", others)
	}
	if _, err := Sweep(st, time.Now(), Options{}); err != nil {
		t.Fatal(err)
	}
	if _, others := st.AccountUsage(a1); others != 77 {
		t.Fatalf("others %d after a sweep, want 77", others)
	}
}
