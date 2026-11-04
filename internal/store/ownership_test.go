package store

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
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

// detaching is a detach callback that names fixed addresses.
func detaching(ds ...string) func(*Meta) ([]string, error) {
	return func(*Meta) ([]string, error) { return ds, nil }
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
	pw, err := s.TransferOwner(site, "a", "b", detaching("a.example"), func(m *Meta, now time.Time) bool {
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
	if _, err := s.TransferOwner(site, "a", "b", detaching("a.example"), nil); err != nil {
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
	if err := s.UpdateAdmins(site, "someone-else", func(in []SiteAdmin) ([]SiteAdmin, error) { return append(in, SiteAdmin{Account: "x"}), nil }); !errors.Is(err, ErrNotOwner) {
		t.Fatalf("wrong expected owner: %v", err)
	}
	if err := s.UpdateAdmins(site, "", func(in []SiteAdmin) ([]SiteAdmin, error) { return append(in, SiteAdmin{Account: "x"}), nil }); !errors.Is(err, ErrNotOwner) {
		t.Fatalf("empty expected owner: %v", err)
	}
	if got, _ := s.ByViewID(site.ViewID); len(got.Meta.Admins) != 0 {
		t.Fatalf("a refused edit wrote: %v", got.Meta.Admins)
	}
	add := func(accts ...string) func([]SiteAdmin) ([]SiteAdmin, error) {
		return func(in []SiteAdmin) ([]SiteAdmin, error) {
			for _, a := range accts {
				in = append(in, SiteAdmin{Account: a})
			}
			return in, nil
		}
	}
	if err := s.UpdateAdmins(site, "a", add("p", "q", "p")); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.ByViewID(site.ViewID); len(got.Meta.Admins) != 2 {
		t.Fatalf("dedupe: %v", got.Meta.Admins)
	}
	if err := s.UpdateAdmins(site, "a", add("1", "2", "3", "4", "5", "6", "7", "8")); err != nil {
		t.Fatalf("10 admins: %v", err)
	}
	if err := s.UpdateAdmins(site, "a", add("11")); !errors.Is(err, ErrTooManyAdmins) {
		t.Fatalf("11th: %v", err)
	}
	if got, _ := s.ByViewID(site.ViewID); len(got.Meta.Admins) != 10 {
		t.Errorf("a refused edit changed the admins: %d", len(got.Meta.Admins))
	}
	boom := errors.New("boom")
	if err := s.UpdateAdmins(site, "a", func([]SiteAdmin) ([]SiteAdmin, error) { return nil, boom }); !errors.Is(err, boom) {
		t.Errorf("edit error: %v", err)
	}
	if _, err := s.SetLock(site, &SiteLock{At: time.Now(), By: "operator"}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateAdmins(site, "a", add("z")); !errors.Is(err, ErrLocked) {
		t.Errorf("locked: %v", err)
	}
}

func TestTransferOwnerRefusesEmptyRecipient(t *testing.T) {
	s := newTestStore(t)
	site := ownSite(t, s, "a")
	if _, err := s.TransferOwner(site, "a", "", nil, nil); !errors.Is(err, ErrNotOwner) {
		t.Fatalf("transfer to nobody: %v", err)
	}
	if got, _ := s.ByViewID(site.ViewID); got.Meta.OwnerAccountID != "a" {
		t.Errorf("owner became %q", got.Meta.OwnerAccountID)
	}
	anon := ownSite(t, s, "")
	if err := s.AdoptOwner(anon, "", nil); !errors.Is(err, ErrNotAdoptable) {
		t.Fatalf("adopt for nobody: %v", err)
	}
}

func TestTransferOwnerBadDetachAddressDetachesNothing(t *testing.T) {
	s, site := redirectSite(t, "a.example", "b.example")
	if err := s.Update(site, func(m *Meta) error { m.OwnerAccountID = "a"; return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := s.TransferOwner(site, "a", "b", detaching("a.example", "not a domain!"), nil); err == nil {
		t.Fatal("a bad address did not refuse the call")
	}
	got, _ := s.ByViewID(site.ViewID)
	if got.Meta.OwnerAccountID != "a" || len(got.Meta.CustomDomains) != 2 {
		t.Fatalf("partly done: owner %q, domains %v", got.Meta.OwnerAccountID, got.Meta.CustomDomains)
	}
	if _, err := s.ByDomain("a.example"); err != nil {
		t.Errorf("a.example was detached: %v", err)
	}
}

func TestTransferOwnerMarkerFailureStillCompletes(t *testing.T) {
	s := newTestStore(t)
	site := ownSite(t, s, "a")
	p := startFakePush(t, s, site)
	// A directory where the trust marker belongs makes SetTrusted(true) fail.
	if err := os.MkdirAll(filepath.Join(site.FilesDir(), TrustedMarker), 0o755); err != nil {
		t.Fatal(err)
	}
	pw, err := s.TransferOwner(site, "a", "b", nil, func(*Meta, time.Time) bool { return true })
	if err != nil || pw == "" {
		t.Fatalf("a marker failure lost the password: %q, %v", pw, err)
	}
	got, _ := s.ByViewID(site.ViewID)
	if got.Meta.OwnerAccountID != "b" || !auth.VerifyPassword(got.Meta.EditPasswordHash, pw) {
		t.Fatal("meta does not hold the new owner and the returned password")
	}
	if !p.wasCancelled() {
		t.Fatal("the old owner's push survived")
	}
	p.wait(t)
}

// LiveAdmin is the meta half of an admin's standing: listed, with no end
// date or one still ahead, on a site that has an owner — never the owner
// itself, never an empty account.
func TestLiveAdmin(t *testing.T) {
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	ahead, past := now.Add(time.Hour), now.Add(-time.Hour)
	m := Meta{OwnerAccountID: "owner", Admins: []SiteAdmin{
		{Account: "open"}, {Account: "ahead", Until: &ahead}, {Account: "ended", Until: &past}, {Account: "edge", Until: &now},
	}}
	for account, want := range map[string]bool{
		"open": true, "ahead": true, "ended": false, "edge": false,
		"owner": false, "stranger": false, "": false,
	} {
		if got := m.LiveAdmin(account, now); got != want {
			t.Errorf("LiveAdmin(%q) = %v, want %v", account, got, want)
		}
	}
	anon := m
	anon.OwnerAccountID = ""
	if anon.LiveAdmin("open", now) {
		t.Error("an anonymous site has an admin")
	}
}

func TestDeleteIfOwnerSaysWhyItCannotRead(t *testing.T) {
	s := newTestStore(t)
	site := ownSite(t, s, "acct-1")
	// Damaged meta: the read error itself, and nothing deleted.
	if err := os.WriteFile(metaPath(site.dir), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteIfOwner(site, "acct-1"); err == nil || errors.Is(err, ErrNotOwner) || errors.Is(err, ErrNotFound) {
		t.Errorf("unreadable meta = %v, want the parse error", err)
	}
	if _, err := os.Stat(site.dir); err != nil {
		t.Errorf("the site was deleted: %v", err)
	}
	// Vanished meta: not found.
	os.Remove(metaPath(site.dir))
	if err := s.DeleteIfOwner(site, "acct-1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing meta = %v, want ErrNotFound", err)
	}
}

// The detach list is decided on the meta as it stands under the site lock,
// and a failure to decide it refuses the transfer with nothing written.
func TestTransferOwnerDecidesTheDetachListUnderTheLock(t *testing.T) {
	s, site := redirectSite(t, "a.example", "b.example")
	if err := s.Update(site, func(m *Meta) error { m.OwnerAccountID = "a"; return nil }); err != nil {
		t.Fatal(err)
	}
	stale := *site // loaded before the next write
	if err := s.Update(site, func(m *Meta) error { m.Name = "fresh"; return nil }); err != nil {
		t.Fatal(err)
	}
	boom := errors.New("zone unreadable")
	if _, err := s.TransferOwner(&stale, "a", "b", func(m *Meta) ([]string, error) {
		if m.Name != "fresh" || m.OwnerAccountID != "a" {
			t.Errorf("detach saw a stale meta: name %q owner %q", m.Name, m.OwnerAccountID)
		}
		return nil, boom
	}, nil); !errors.Is(err, boom) {
		t.Fatalf("a failing detach = %v, want its error", err)
	}
	got, _ := s.ByViewID(site.ViewID)
	if got.Meta.OwnerAccountID != "a" || len(got.Meta.CustomDomains) != 2 || got.Meta.EditPasswordHash != site.Meta.EditPasswordHash {
		t.Fatalf("a refused transfer wrote: owner %q, domains %v", got.Meta.OwnerAccountID, got.Meta.CustomDomains)
	}
	if _, err := s.ByDomain("a.example"); err != nil {
		t.Errorf("a.example lost its index link: %v", err)
	}
}

func TestAccountBoundAddresses(t *testing.T) {
	s, v := zoneStore(t)
	s.SetOperatorZones([]string{"ops.example"})
	s.SetOperatorCheck(func(id string) bool { return id == "a" })
	mustZone(t, s, v, "a", "kunde.example")
	mustZone(t, s, v, "b", "other.example")
	m := &Meta{
		OwnerAccountID: "a",
		CustomDomains:  []string{"shop.kunde.example", "kunde.example/blog", "x.ops.example", "via.example", "self.example", "shop.other.example"},
		DomainClaims:   []DomainClaim{{Domain: "via.example", ViaHost: true}},
	}
	got, err := s.AccountBoundAddresses(m, "a")
	want := []string{"shop.kunde.example", "kunde.example/blog", "x.ops.example", "via.example"}
	if err != nil || strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("AccountBoundAddresses = %v, %v; want %v", got, err, want)
	}
	// A zone file that exists but cannot be read is an error, not "no zone".
	if err := os.WriteFile(filepath.Join(s.zonesDir(), "kunde.example.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, err := s.AccountBoundAddresses(m, "a"); err == nil {
		t.Fatalf("an unreadable zone passed: %v", got)
	}
}
