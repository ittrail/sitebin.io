package cleanup

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ittrail/sitebin.io/internal/ext"
	"github.com/ittrail/sitebin.io/internal/store"
)

func TestSweep(t *testing.T) {
	st, err := store.New(t.TempDir(), "sitebin.example", 1<<20, 100)
	if err != nil {
		t.Fatal(err)
	}
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

	removed, err := Sweep(st, now)
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
	if _, err := Sweep(st, time.Now()); err != nil {
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
func (p *stubProvider) CustomDomainsAllowed() bool            { return true }
func (p *stubProvider) EmbedOriginsAllowed() bool             { return true }
func (p *stubProvider) AuthorizeCreate(*http.Request) (ext.CreateGrant, error) {
	return ext.CreateGrant{}, nil
}
func (p *stubProvider) BearerAccount(*http.Request) (string, bool) { return "", false }
func (p *stubProvider) OnSiteCreated(string, string) error         { return nil }
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
	site := expiredOwnedSite(t, st, now, "acct-1", 7, true)

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
	site := expiredOwnedSite(t, st, now, "acct-1", 7, true)

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
	expiredOwnedSite(t, st, now, "acct-1", 7, true)

	if removed, err := Sweep(st, now); err != nil || removed != 1 {
		t.Fatalf("removed = %d, err = %v, want 1, nil", removed, err)
	}
}

func TestSweepDeletesUserChosenExpiryOnUnlimitedTier(t *testing.T) {
	ext.Register(&stubProvider{grant: ext.CreateGrant{MaxExpiryDays: 0}, ok: true})
	defer ext.Reset()
	st, _ := store.New(t.TempDir(), "sitebin.example", 1<<20, 100)
	now := time.Now().UTC()
	expiredOwnedSite(t, st, now, "acct-1", 0, false) // the owner asked for this date

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

	removed, err := Sweep(st, now)
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

	if removed, err := Sweep(st, now); err != nil || removed != 1 {
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

	removed, err := Sweep(st, now)
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

	if _, err := Sweep(st, time.Now()); err != nil {
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
