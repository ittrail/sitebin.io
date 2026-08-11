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

	domains := 10
	webdav := true
	if err := s.ApplyQuota(site, Quota{Bytes: 1 << 30, Files: 5000, ExpiryDays: 0, Domains: &domains, WebDAV: &webdav}, DowngradeGrace); err != nil {
		t.Fatal(err)
	}
	got, _ := s.ByViewID(site.ViewID)
	if got.Meta.ExpiresAt != nil {
		t.Fatalf("tier expiry survived an upgrade to unlimited: %v", got.Meta.ExpiresAt)
	}
	if got.Meta.QuotaBytes != 1<<30 || got.Meta.QuotaFiles != 5000 || got.Meta.QuotaExpiryDays != 0 {
		t.Fatalf("quotas not restamped: %+v", got.Meta)
	}
	if got.Meta.QuotaDomains == nil || *got.Meta.QuotaDomains != 10 {
		t.Fatalf("QuotaDomains not restamped: %+v", got.Meta.QuotaDomains)
	}
	if got.Meta.QuotaWebDAV == nil || *got.Meta.QuotaWebDAV != true {
		t.Fatalf("QuotaWebDAV not restamped: %+v", got.Meta.QuotaWebDAV)
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
	if !got.Meta.ExpiryFromTier {
		t.Fatal("a clamped expiry must be marked as tier-imposed, or a later upgrade will never lift it")
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
