package store

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// acctSite creates a site owned by owner, stamped with the account cap,
// holding one file of n bytes.
func acctSite(t *testing.T, s *Store, owner string, accountCap int64, n int) *Site {
	t.Helper()
	site, _, err := s.Create()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Update(site, func(m *Meta) error {
		m.OwnerAccountID = owner
		m.QuotaAccountBytes = accountCap
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if n > 0 {
		if err := s.SaveFile(site, "f.bin", bytes.NewReader(bytes.Repeat([]byte("x"), n))); err != nil {
			t.Fatal(err)
		}
	}
	return site
}

// grow adds n bytes to the site behind the store's back, as a container's
// database does.
func grow(t *testing.T, site *Site, name string, n int) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(site.ContentDir(), name), bytes.Repeat([]byte("c"), n), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestAccountUsageCountsTheOwnersOtherSites(t *testing.T) {
	s := newTestStore(t)
	a1 := acctSite(t, s, "acct-a", 1000, 100)
	a2 := acctSite(t, s, "acct-a", 1000, 50)
	writeRepoFile(t, a2, "objects/pack/p.pack", 20)
	acctSite(t, s, "acct-b", 1000, 500)
	anon, _, _ := s.Create()
	s.SaveFile(anon, "big.bin", strings.NewReader(strings.Repeat("z", 900)))

	if err := s.MeasureAccounts(); err != nil {
		t.Fatal(err)
	}
	if cap, others := s.AccountUsage(a1); cap != 1000 || others != 70 {
		t.Fatalf("a1: cap %d, others %d; want 1000 and 70 (a2's files and repository)", cap, others)
	}
	if _, others := s.AccountUsage(a2); others != 100 {
		t.Fatalf("a2: others %d, want 100", others)
	}
}

func TestAccountUsageWithoutAStampIsNone(t *testing.T) {
	s := newTestStore(t)
	a1 := acctSite(t, s, "acct-a", 0, 10)
	acctSite(t, s, "acct-a", 0, 10)
	s.MeasureAccounts()
	if cap, others := s.AccountUsage(a1); cap != 0 || others != 0 {
		t.Fatalf("no account cap stamped: cap %d, others %d; want 0, 0", cap, others)
	}
	anon, _, _ := s.Create()
	s.Update(anon, func(m *Meta) error { m.QuotaAccountBytes = 1000; return nil })
	if cap, _ := s.AccountUsage(anon); cap != 0 {
		t.Fatalf("an anonymous site has no account: cap %d", cap)
	}
}

// Every measure of a site — every write's check, every report — keeps the
// index current; no full measure is needed for a site in use.
func TestSiteUsageNotesTheSite(t *testing.T) {
	s := newTestStore(t)
	a1 := acctSite(t, s, "acct-a", 1<<40, 10) // far from the cap: no re-measure
	a2 := acctSite(t, s, "acct-a", 1<<40, 10)
	grow(t, a2, "db.bin", 300)
	if _, others := s.AccountUsage(a1); others != 10 {
		t.Fatalf("others %d before a2 is measured again, want the 10 its write noted", others)
	}
	if _, err := s.SiteUsage(a2); err != nil {
		t.Fatal(err)
	}
	if _, others := s.AccountUsage(a1); others != 310 {
		t.Fatalf("others %d after a2's measure, want 310", others)
	}
}

func TestAccountUsageForgetsADeletedSite(t *testing.T) {
	s := newTestStore(t)
	a1 := acctSite(t, s, "acct-a", 1<<20, 10)
	a2 := acctSite(t, s, "acct-a", 1<<20, 40)
	if err := s.Delete(a2); err != nil {
		t.Fatal(err)
	}
	if _, others := s.AccountUsage(a1); others != 0 {
		t.Fatalf("others %d after a2's delete, want 0", others)
	}
}

// A locked site is the operator's evidence: its owner can neither change nor
// delete it, so it must not hold their storage hostage.
func TestAccountUsageLeavesOutALockedSite(t *testing.T) {
	s := newTestStore(t)
	a1 := acctSite(t, s, "acct-a", 1<<20, 10)
	a2 := acctSite(t, s, "acct-a", 1<<20, 40)
	if _, err := s.SetLock(a2, &SiteLock{At: time.Now(), Reason: "phishing", By: LockByAdmin}); err != nil {
		t.Fatal(err)
	}
	s.MeasureAccounts()
	if _, others := s.AccountUsage(a1); others != 0 {
		t.Fatalf("others %d with a2 locked, want 0", others)
	}
}

func TestAccountUsageFollowsATransfer(t *testing.T) {
	s := newTestStore(t)
	a1 := acctSite(t, s, "acct-a", 1<<20, 10)
	a2 := acctSite(t, s, "acct-a", 1<<20, 40)
	b1 := acctSite(t, s, "acct-b", 1<<20, 5)
	if _, err := s.TransferOwner(a2, "acct-a", "acct-b", nil, func(*Meta, time.Time) bool { return true }); err != nil {
		t.Fatal(err)
	}
	if _, others := s.AccountUsage(a1); others != 0 {
		t.Fatalf("acct-a others %d after the transfer, want 0", others)
	}
	if _, others := s.AccountUsage(b1); others != 40 {
		t.Fatalf("acct-b others %d after the transfer, want a2's 40", others)
	}
}

// MeasureAccounts rebuilds the figures from disk: a site removed behind the
// store's back is forgotten, and growth nobody measured is found.
func TestMeasureAccountsRebuildsFromDisk(t *testing.T) {
	s := newTestStore(t)
	a1 := acctSite(t, s, "acct-a", 1<<20, 10)
	a2 := acctSite(t, s, "acct-a", 1<<20, 40)
	a3 := acctSite(t, s, "acct-a", 1<<20, 7)
	grow(t, a2, "db.bin", 60)
	if err := os.RemoveAll(a3.Dir()); err != nil {
		t.Fatal(err)
	}
	if err := s.MeasureAccounts(); err != nil {
		t.Fatal(err)
	}
	if _, others := s.AccountUsage(a1); others != 100 {
		t.Fatalf("others %d after a full measure, want a2's 100 and nothing of a3", others)
	}
}

// Near the cap a stale figure could refuse a write that deleting files
// elsewhere made room for, so the owner's other sites are measured again —
// at most every accountRemeasure. Far from the cap nothing is walked.
func TestAccountUsageRemeasuresNearTheCap(t *testing.T) {
	s := newTestStore(t)
	now := time.Now()
	s.acct.now = func() time.Time { return now }
	a1 := acctSite(t, s, "acct-a", 100, 10) // site cap: the instance's 1 MiB
	a2 := acctSite(t, s, "acct-a", 100, 60)
	if err := os.Remove(filepath.Join(a2.ContentDir(), "f.bin")); err != nil {
		t.Fatal(err)
	}
	// 100 - 60 = 40 is below the site cap: the account binds, and the
	// figure is older than accountRemeasure — so it is measured again, and
	// a2 is empty.
	now = now.Add(accountRemeasure + time.Second)
	if _, others := s.AccountUsage(a1); others != 0 {
		t.Fatalf("others %d, want 0 after the re-measure", others)
	}
	grow(t, a2, "db.bin", 30)
	now = now.Add(accountRemeasure - time.Second)
	if _, others := s.AccountUsage(a1); others != 0 {
		t.Fatalf("others %d within %v of the last re-measure, want the figure kept", others, accountRemeasure)
	}
	now = now.Add(2 * time.Second)
	if _, others := s.AccountUsage(a1); others != 30 {
		t.Fatalf("others %d past %v, want 30 re-measured", others, accountRemeasure)
	}
}

func TestAccountUsageFarFromTheCapWalksNothing(t *testing.T) {
	s := newTestStore(t)
	a1 := acctSite(t, s, "acct-a", 1<<40, 10)
	a2 := acctSite(t, s, "acct-a", 1<<40, 60)
	grow(t, a2, "db.bin", 30)
	if _, others := s.AccountUsage(a1); others != 60 {
		t.Fatalf("others %d, want the noted 60: the account cannot bind, so nothing is walked", others)
	}
}

func TestByteCap(t *testing.T) {
	s := newTestStore(t) // instance cap 1 MiB per site
	mib := int64(1 << 20)

	free := acctSite(t, s, "acct-a", 4*mib, 10)
	if max, account := s.ByteCap(free); max != mib || account {
		t.Fatalf("roomy account: ByteCap = %d, %v; want the site's %d, false", max, account, mib)
	}

	tight := acctSite(t, s, "acct-b", 1000, 10)
	acctSite(t, s, "acct-b", 1000, 400)
	if max, account := s.ByteCap(tight); max != 600 || !account {
		t.Fatalf("tight account: ByteCap = %d, %v; want 600, true", max, account)
	}

	over := acctSite(t, s, "acct-c", 100, 10)
	big := acctSite(t, s, "acct-c", 100, 0)
	grow(t, big, "db.bin", 300) // a downgrade, or a container's database
	s.SiteUsage(big)
	if max, account := s.ByteCap(over); max != -200 || !account {
		t.Fatalf("account over its cap: ByteCap = %d, %v; want -200, true", max, account)
	}

	none := acctSite(t, s, "acct-d", 0, 10)
	acctSite(t, s, "acct-d", 0, 900)
	if max, account := s.ByteCap(none); max != mib || account {
		t.Fatalf("no account cap: ByteCap = %d, %v; want %d, false", max, account, mib)
	}
}

func TestErrAccountFullIsTooLarge(t *testing.T) {
	if !errors.Is(ErrAccountFull, ErrTooLarge) {
		t.Fatal("ErrAccountFull must also be ErrTooLarge, so every existing size refusal applies")
	}
	if errors.Is(ErrTooLarge, ErrAccountFull) {
		t.Fatal("a site's own size refusal is not the account's")
	}
}

// The store's own funnels refuse a write past the account's room with
// ErrAccountFull, and keep the figure current after a write.
func TestStoreFunnelsCountTheAccount(t *testing.T) {
	s := newTestStore(t)
	a1 := acctSite(t, s, "acct-a", 200, 0)
	a2 := acctSite(t, s, "acct-a", 200, 150)

	if err := s.SaveFile(a1, "big.bin", strings.NewReader(strings.Repeat("a", 60))); !errors.Is(err, ErrAccountFull) {
		t.Fatalf("SaveFile past the account's 50 left: %v, want ErrAccountFull", err)
	}
	if err := s.SaveFile(a1, "ok.bin", strings.NewReader(strings.Repeat("a", 40))); err != nil {
		t.Fatalf("SaveFile within the account's room: %v", err)
	}
	if _, others := s.AccountUsage(a2); others != 40 {
		t.Fatalf("the write was not noted: others %d, want 40", others)
	}

	zipped := makeZip(t, map[string]string{"z.bin": strings.Repeat("z", 20)}, false)
	if err := s.ExtractZip(a1, bytes.NewReader(zipped), int64(len(zipped))); !errors.Is(err, ErrAccountFull) {
		t.Fatalf("ExtractZip past the account's 10 left: %v, want ErrAccountFull", err)
	}

	rep, err := s.BeginReplace(a1)
	if err != nil {
		t.Fatal(err)
	}
	defer rep.Abort()
	// a1's own 40 bytes are replaced, so 50 are left for the new content.
	if err := rep.SaveFile("new.bin", strings.NewReader(strings.Repeat("n", 51))); !errors.Is(err, ErrAccountFull) {
		t.Fatalf("Replacement past the account's 50: %v, want ErrAccountFull", err)
	}
}
