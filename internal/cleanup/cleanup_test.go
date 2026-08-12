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
