//go:build ee

package account

import (
	"errors"
	"sort"
	"sync"
	"testing"

	"github.com/ittrail/sitebin.io/internal/ids"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestCreateLocalAndLookup(t *testing.T) {
	s := newStore(t)
	a, err := s.CreateLocal("Alice@Example.com", "hash1", "free")
	if err != nil {
		t.Fatalf("CreateLocal: %v", err)
	}
	if a.Email != "alice@example.com" {
		t.Errorf("email not normalized: %q", a.Email)
	}
	if a.Provider != Local || a.Tier != "free" || a.PasswordHash != "hash1" {
		t.Errorf("account fields wrong: %+v", a)
	}
	got, err := s.ByEmail("alice@example.com")
	if err != nil || got.ID != a.ID {
		t.Fatalf("ByEmail: %v", err)
	}
	got, err = s.ByID(a.ID)
	if err != nil || got.Email != a.Email {
		t.Fatalf("ByID: %v", err)
	}
}

func TestCreateLocalDuplicateEmail(t *testing.T) {
	s := newStore(t)
	if _, err := s.CreateLocal("bob@example.com", "h", "free"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateLocal("BOB@example.com", "h2", "free"); !errors.Is(err, ErrEmailTaken) {
		t.Fatalf("expected ErrEmailTaken, got %v", err)
	}
}

func TestCreateLocalBadEmail(t *testing.T) {
	s := newStore(t)
	for _, bad := range []string{"", "no-at", "a@b", "a b@c.com", "@x.com"} {
		if _, err := s.CreateLocal(bad, "h", "free"); !errors.Is(err, ErrBadEmail) {
			t.Errorf("CreateLocal(%q) = %v, want ErrBadEmail", bad, err)
		}
	}
}

func TestCreateOAuth(t *testing.T) {
	s := newStore(t)
	a, err := s.CreateOAuth(Google, "sub-123", "carol@example.com", "free")
	if err != nil {
		t.Fatalf("CreateOAuth: %v", err)
	}
	if !a.EmailVerified {
		t.Error("oauth email should be verified")
	}
	got, err := s.ByOAuth(Google, "sub-123")
	if err != nil || got.ID != a.ID {
		t.Fatalf("ByOAuth: %v", err)
	}
	// same subject again → taken
	if _, err := s.CreateOAuth(Google, "sub-123", "carol@example.com", "free"); !errors.Is(err, ErrEmailTaken) {
		// email is claimed first on the retry path; either taken error is fine
		if !errors.Is(err, ErrOAuthTaken) {
			t.Fatalf("expected taken error, got %v", err)
		}
	}
	// different provider, same subject string → distinct identity
	if _, err := s.CreateOAuth(Microsoft, "sub-123", "dora@example.com", "free"); err != nil {
		t.Fatalf("cross-provider subject collision: %v", err)
	}
}

func TestByEmailAndOAuthNotFound(t *testing.T) {
	s := newStore(t)
	if _, err := s.ByEmail("ghost@example.com"); !errors.Is(err, ErrNotFound) {
		t.Errorf("ByEmail: %v", err)
	}
	if _, err := s.ByOAuth(Google, "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("ByOAuth: %v", err)
	}
	if _, err := s.ByID("not-a-valid-id"); !errors.Is(err, ErrNotFound) {
		t.Errorf("ByID: %v", err)
	}
}

func TestUpdate(t *testing.T) {
	s := newStore(t)
	a, _ := s.CreateLocal("ed@example.com", "h", "free")
	err := s.Update(a, func(cur *Account) error {
		cur.Tier = "pro"
		cur.EmailVerified = true
		cur.TokenVersion++
		return nil
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if a.Tier != "pro" || !a.EmailVerified || a.TokenVersion != 1 {
		t.Errorf("in-place account not refreshed: %+v", a)
	}
	reload, _ := s.ByID(a.ID)
	if reload.Tier != "pro" {
		t.Error("update not persisted")
	}
}

func TestSiteLinks(t *testing.T) {
	s := newStore(t)
	a, _ := s.CreateLocal("fi@example.com", "h", "free")
	v1, v2 := makeViewID(), makeViewID()
	if err := s.LinkSite(a, v1); err != nil {
		t.Fatal(err)
	}
	if err := s.LinkSite(a, v2); err != nil {
		t.Fatal(err)
	}
	if err := s.LinkSite(a, v1); err != nil { // idempotent
		t.Fatal(err)
	}
	ids, _ := s.ListSiteIDs(a)
	sort.Strings(ids)
	want := []string{v1, v2}
	sort.Strings(want)
	if len(ids) != 2 || ids[0] != want[0] || ids[1] != want[1] {
		t.Errorf("linked sites = %v, want %v", ids, want)
	}
	if err := s.UnlinkSite(a, v1); err != nil {
		t.Fatal(err)
	}
	ids, _ = s.ListSiteIDs(a)
	if len(ids) != 1 || ids[0] != v2 {
		t.Errorf("after unlink = %v", ids)
	}
}

func TestDeleteCascades(t *testing.T) {
	s := newStore(t)
	a, _ := s.CreateLocal("gwen@example.com", "h", "free")
	v1, v2 := makeViewID(), makeViewID()
	s.LinkSite(a, v1)
	s.LinkSite(a, v2)

	var deleted []string
	err := s.Delete(a, func(vid string) error { deleted = append(deleted, vid); return nil })
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if len(deleted) != 2 {
		t.Errorf("deleteSite called for %v, want 2 sites", deleted)
	}
	if _, err := s.ByID(a.ID); !errors.Is(err, ErrNotFound) {
		t.Error("account survives delete")
	}
	if _, err := s.ByEmail("gwen@example.com"); !errors.Is(err, ErrNotFound) {
		t.Error("email index survives delete")
	}
}

func TestConcurrentUpdate(t *testing.T) {
	s := newStore(t)
	a, _ := s.CreateLocal("hank@example.com", "h", "free")
	var wg sync.WaitGroup
	for i := 0; i < 30; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			h, _ := s.ByID(a.ID)
			s.Update(h, func(cur *Account) error { cur.TokenVersion++; return nil })
		}()
	}
	wg.Wait()
	reload, _ := s.ByID(a.ID)
	if reload.TokenVersion != 30 {
		t.Errorf("lost updates: token_version = %d, want 30", reload.TokenVersion)
	}
}

// makeViewID mints a valid 26-char id for tests.
func makeViewID() string { return ids.New() }
