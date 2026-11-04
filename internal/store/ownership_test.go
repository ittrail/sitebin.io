package store

import (
	"errors"
	"testing"
	"time"

	"github.com/ittrail/sitebin.io/internal/auth"
)

func ownSite(t *testing.T, s *Store, owner string) *Site {
	t.Helper()
	site, _, err := s.Create()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Update(site, func(m *Meta) error { m.OwnerAccountID = owner; return nil }); err != nil {
		t.Fatal(err)
	}
	return site
}

func TestTransferOwnerMovesEverything(t *testing.T) {
	s, site := redirectSite(t, "a.example", "b.example", "c.example")
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	oldPW, err := s.SetEditPassword(site)
	must(err)
	must(s.Update(site, func(m *Meta) error {
		m.OwnerAccountID = "a"
		m.Tags = []string{"x"}
		m.Admins = []SiteAdmin{{Account: "z"}}
		return nil
	}))
	must(s.SetDomainRedirect(site, "b.example", "a.example", 301))
	must(s.SetViewAddress(site, ViewOff, "", 0))
	must(s.SetTrusted(site, true))

	stamped := false
	pw, err := s.TransferOwner(site, "a", "b", []string{"a.example"}, func(m *Meta, now time.Time) bool {
		stamped = true
		m.QuotaFiles = 7
		return false
	})
	must(err)
	got, err := s.ByViewID(site.ViewID)
	must(err)
	m := got.Meta
	if m.OwnerAccountID != "b" || len(m.Tags) != 0 || len(m.Admins) != 0 {
		t.Errorf("owner/tags/admins = %q %v %v", m.OwnerAccountID, m.Tags, m.Admins)
	}
	if !stamped || m.QuotaFiles != 7 {
		t.Errorf("stamp not applied: %v %d", stamped, m.QuotaFiles)
	}
	if s.Trusted(got) {
		t.Error("trust marker should follow the stamp's answer (false)")
	}
	if len(m.CustomDomains) != 2 || m.CustomDomains[0] == "a.example" || m.CustomDomains[1] == "a.example" {
		t.Errorf("a.example not detached: %v", m.CustomDomains)
	}
	if _, err := s.ByDomain("a.example"); err == nil {
		t.Error("a.example still indexed")
	}
	if _, err := s.ByDomain("b.example"); err != nil {
		t.Errorf("b.example lost its index link: %v", err)
	}
	if len(m.DomainRedirects) != 0 {
		t.Errorf("redirects to the detached domain survive: %v", m.DomainRedirects)
	}
	if m.ViewAddress == nil || m.ViewAddress.Mode != ViewOff {
		t.Errorf("off should hold while other domains serve: %+v", m.ViewAddress)
	}
	if !auth.VerifyPassword(m.EditPasswordHash, pw) || auth.VerifyPassword(m.EditPasswordHash, oldPW) {
		t.Error("new password must work and the old one fail")
	}
}

func TestTransferOwnerGeneratedAddressServesAgain(t *testing.T) {
	s, site := redirectSite(t, "a.example")
	if err := s.Update(site, func(m *Meta) error { m.OwnerAccountID = "a"; return nil }); err != nil {
		t.Fatal(err)
	}
	if err := s.SetViewAddress(site, ViewOff, "", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.TransferOwner(site, "a", "b", []string{"a.example"}, nil); err != nil {
		t.Fatal(err)
	}
	got, _ := s.ByViewID(site.ViewID)
	if got.Meta.ViewAddress != nil {
		t.Errorf("the generated address stays off with no domain: %+v", got.Meta.ViewAddress)
	}
}

func TestTransferOwnerRefusesWrongOwnerAndLock(t *testing.T) {
	s := newTestStore(t)
	site := ownSite(t, s, "a")
	if _, err := s.TransferOwner(site, "x", "b", nil, nil); !errors.Is(err, ErrNotOwner) {
		t.Fatalf("wrong owner: %v", err)
	}
	anon := ownSite(t, s, "")
	if _, err := s.TransferOwner(anon, "", "b", nil, nil); !errors.Is(err, ErrNotOwner) {
		t.Fatalf("anonymous site: %v", err)
	}
	if _, err := s.SetLock(site, &SiteLock{At: time.Now(), By: "operator"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.TransferOwner(site, "a", "b", nil, nil); !errors.Is(err, ErrLocked) {
		t.Fatalf("locked: %v", err)
	}
	got, _ := s.ByViewID(site.ViewID)
	if got.Meta.OwnerAccountID != "a" {
		t.Errorf("owner changed to %q", got.Meta.OwnerAccountID)
	}
}

func TestTransferOwnerCancelsRunningPush(t *testing.T) {
	s := newTestStore(t)
	site := ownSite(t, s, "a")
	p := startFakePush(t, s, site)
	if _, err := s.TransferOwner(site, "a", "b", nil, nil); err != nil {
		t.Fatal(err)
	}
	if !p.wasCancelled() {
		t.Fatal("the old owner's push was not cancelled")
	}
	p.wait(t)
}

func TestAdoptOwner(t *testing.T) {
	s := newTestStore(t)
	site := ownSite(t, s, "")
	if err := s.SetTrusted(site, false); err != nil {
		t.Fatal(err)
	}
	if err := s.AdoptOwner(site, "a", func(m *Meta, now time.Time) bool { m.QuotaFiles = 3; return true }); err != nil {
		t.Fatal(err)
	}
	got, _ := s.ByViewID(site.ViewID)
	if got.Meta.OwnerAccountID != "a" || got.Meta.QuotaFiles != 3 || !s.Trusted(got) {
		t.Errorf("not adopted and stamped: %q trusted=%v", got.Meta.OwnerAccountID, s.Trusted(got))
	}
	if err := s.AdoptOwner(site, "b", nil); !errors.Is(err, ErrNotAdoptable) {
		t.Errorf("owned: %v", err)
	}

	locked := ownSite(t, s, "")
	if _, err := s.SetLock(locked, &SiteLock{At: time.Now(), By: "operator"}); err != nil {
		t.Fatal(err)
	}
	if err := s.AdoptOwner(locked, "a", nil); !errors.Is(err, ErrNotAdoptable) {
		t.Errorf("locked: %v", err)
	}

	expired := ownSite(t, s, "")
	past := time.Now().Add(-time.Hour)
	if err := s.Update(expired, func(m *Meta) error { m.ExpiresAt = &past; return nil }); err != nil {
		t.Fatal(err)
	}
	if err := s.AdoptOwner(expired, "a", nil); !errors.Is(err, ErrNotAdoptable) {
		t.Errorf("expired: %v", err)
	}
	if g, _ := s.ByViewID(expired.ViewID); g.Meta.OwnerAccountID != "" {
		t.Error("an expired site was adopted")
	}
}

func TestUpdateAdmins(t *testing.T) {
	s := newTestStore(t)
	site := ownSite(t, s, "a")
	add := func(accts ...string) func([]SiteAdmin) ([]SiteAdmin, error) {
		return func(in []SiteAdmin) ([]SiteAdmin, error) {
			for _, a := range accts {
				in = append(in, SiteAdmin{Account: a})
			}
			return in, nil
		}
	}
	if err := s.UpdateAdmins(site, add("p", "q", "p")); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.ByViewID(site.ViewID); len(got.Meta.Admins) != 2 {
		t.Fatalf("dedupe: %v", got.Meta.Admins)
	}
	if err := s.UpdateAdmins(site, add("1", "2", "3", "4", "5", "6", "7", "8")); err != nil {
		t.Fatalf("10 admins: %v", err)
	}
	if err := s.UpdateAdmins(site, add("11")); !errors.Is(err, ErrTooManyAdmins) {
		t.Fatalf("11th: %v", err)
	}
	if got, _ := s.ByViewID(site.ViewID); len(got.Meta.Admins) != 10 {
		t.Errorf("a refused edit changed the admins: %d", len(got.Meta.Admins))
	}
	boom := errors.New("boom")
	if err := s.UpdateAdmins(site, func([]SiteAdmin) ([]SiteAdmin, error) { return nil, boom }); !errors.Is(err, boom) {
		t.Errorf("edit error: %v", err)
	}
	if _, err := s.SetLock(site, &SiteLock{At: time.Now(), By: "operator"}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateAdmins(site, add("z")); !errors.Is(err, ErrLocked) {
		t.Errorf("locked: %v", err)
	}
}
