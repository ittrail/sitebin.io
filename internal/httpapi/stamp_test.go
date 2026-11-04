package httpapi

import (
	"testing"
	"time"

	"github.com/ittrail/sitebin.io/internal/ext"
	"github.com/ittrail/sitebin.io/internal/store"
)

func TestStampGrantMatchesCreation(t *testing.T) {
	yes := true
	g := ext.CreateGrant{OwnerAccountID: "acct-1", MaxSiteBytes: 1000, MaxFiles: 5, MaxExpiryDays: 7,
		MaxCustomDomain: intp(2), WebDAV: &yes, Git: &yes, MaxForms: intp(3), Trusted: true}
	e, _ := formsEnv(t, nil)
	ext.Register(&fakeProvider{enabled: true, grant: g})
	defer ext.Reset()
	_, _, viewID := newFormSite(t, e)
	created, _ := e.st.ByViewID(viewID)

	anon, _, err := e.st.Create()
	if err != nil {
		t.Fatal(err)
	}
	if err := e.st.AdoptOwner(anon, "acct-1", func(m *store.Meta, now time.Time) bool { return stampGrant(m, g, now) }); err != nil {
		t.Fatal(err)
	}
	adopted, _ := e.st.ByViewID(anon.ViewID)

	c, a := created.Meta, adopted.Meta
	if c.OwnerAccountID != a.OwnerAccountID || c.QuotaBytes != a.QuotaBytes || c.QuotaFiles != a.QuotaFiles ||
		c.QuotaExpiryDays != a.QuotaExpiryDays || *c.QuotaDomains != *a.QuotaDomains || *c.QuotaWebDAV != *a.QuotaWebDAV ||
		*c.QuotaGit != *a.QuotaGit || *c.QuotaForms != *a.QuotaForms || c.ExpiryFromTier != a.ExpiryFromTier {
		t.Errorf("stamps differ:\ncreated %+v\nadopted %+v", c, a)
	}
	if c.ExpiresAt == nil || a.ExpiresAt == nil || a.ExpiresAt.Sub(*c.ExpiresAt).Abs() > time.Minute {
		t.Errorf("expiry differs: %v vs %v", c.ExpiresAt, a.ExpiresAt)
	}
	if e.st.Trusted(created) != e.st.Trusted(adopted) || !e.st.Trusted(adopted) {
		t.Errorf("trust differs: created %v adopted %v", e.st.Trusted(created), e.st.Trusted(adopted))
	}
}

func TestStampGrantExpiry(t *testing.T) {
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	day := 24 * time.Hour
	at := func(d time.Duration) *time.Time { x := now.Add(d); return &x }
	g := ext.CreateGrant{MaxExpiryDays: 7}

	// No date: the tier's.
	m := store.Meta{}
	stampGrant(&m, g, now)
	if m.ExpiresAt == nil || !m.ExpiresAt.Equal(*at(7 * day)) || !m.ExpiryFromTier {
		t.Errorf("none -> %v %v", m.ExpiresAt, m.ExpiryFromTier)
	}
	// A tier date follows the new cap, even outward.
	m = store.Meta{ExpiresAt: at(2 * day), ExpiryFromTier: true}
	stampGrant(&m, g, now)
	if !m.ExpiresAt.Equal(*at(7 * day)) || !m.ExpiryFromTier {
		t.Errorf("tier date -> %v", m.ExpiresAt)
	}
	// An owner date inside the cap survives untouched.
	m = store.Meta{ExpiresAt: at(2 * day)}
	stampGrant(&m, g, now)
	if !m.ExpiresAt.Equal(*at(2 * day)) || m.ExpiryFromTier {
		t.Errorf("owner date inside -> %v %v", m.ExpiresAt, m.ExpiryFromTier)
	}
	// An owner date past the cap is clamped, not relabelled.
	m = store.Meta{ExpiresAt: at(30 * day)}
	stampGrant(&m, g, now)
	if !m.ExpiresAt.Equal(*at(7 * day)) || m.ExpiryFromTier {
		t.Errorf("owner date past -> %v %v", m.ExpiresAt, m.ExpiryFromTier)
	}
	// No cap: a tier date goes, an owner date stays.
	m = store.Meta{ExpiresAt: at(2 * day), ExpiryFromTier: true}
	stampGrant(&m, ext.CreateGrant{}, now)
	if m.ExpiresAt != nil || m.ExpiryFromTier {
		t.Errorf("uncapped tier date -> %v", m.ExpiresAt)
	}
	m = store.Meta{ExpiresAt: at(90 * day)}
	stampGrant(&m, ext.CreateGrant{}, now)
	if !m.ExpiresAt.Equal(*at(90 * day)) {
		t.Errorf("uncapped owner date -> %v", m.ExpiresAt)
	}
}
