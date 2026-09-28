package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ittrail/sitebin.io/internal/store"
)

func cliStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.New(t.TempDir(), "sitebin.example", 1<<20, 100)
	if err != nil {
		t.Fatal(err)
	}
	st.SetDomainVerifier(store.TrustingVerifier{}, "sitebin.example")
	return st
}

func TestCLILockAndUnlock(t *testing.T) {
	st := cliStore(t)
	site, _, _ := st.Create()
	if err := st.AddDomain(site, "bank-login.example.org"); err != nil {
		t.Fatal(err)
	}

	// By domain, the handle an abuse report usually carries.
	var out bytes.Buffer
	if err := lockSite(st, &out, "bank-login.example.org", "phishing, reported by Hetzner"); err != nil {
		t.Fatalf("lock: %v", err)
	}
	if !strings.Contains(out.String(), "locked site "+site.ViewID+": phishing, reported by Hetzner") {
		t.Errorf("lock output = %q", out.String())
	}
	got, _ := st.ByViewID(site.ViewID)
	if l := got.Meta.Locked; l == nil || l.By != store.LockByAdmin || l.Reason != "phishing, reported by Hetzner" {
		t.Fatalf("lock = %+v", l)
	}

	// By edit id; the expiry, long past, is announced as what applies again.
	past := time.Now().Add(-72 * time.Hour)
	st.Update(got, func(m *store.Meta) error { m.ExpiresAt = &past; return nil })
	out.Reset()
	if err := unlockSite(st, &out, site.EditID); err != nil {
		t.Fatalf("unlock: %v", err)
	}
	if !strings.Contains(out.String(), "unlocked site "+site.ViewID) || !strings.Contains(out.String(), "next sweep deletes") {
		t.Errorf("unlock output = %q", out.String())
	}
	if got, _ := st.ByViewID(site.ViewID); got.Meta.IsLocked() {
		t.Fatal("still locked")
	}
	out.Reset()
	if err := unlockSite(st, &out, site.ViewID); err != nil || !strings.Contains(out.String(), "was not locked") {
		t.Errorf("unlock of an unlocked site = %v %q", err, out.String())
	}

	if err := lockSite(st, &out, "nosuch.example.org", ""); err == nil {
		t.Error("locking an unknown site succeeded")
	}
}

// Locking a site a suspension locked makes it the operator's own hold.
func TestCLILockTurnsAnAccountLockIntoTheOperators(t *testing.T) {
	st := cliStore(t)
	site, _, _ := st.Create()
	st.SetLock(site, &store.SiteLock{Reason: "suspended", By: store.LockByAccount})
	if err := lockSite(st, &bytes.Buffer{}, site.ViewID, ""); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.ByViewID(site.ViewID); got.Meta.Locked.By != store.LockByAdmin {
		t.Errorf("lock = %+v", got.Meta.Locked)
	}
}

// A lock is an evidence hold: the takedown command needs --force for it.
func TestCLIDeleteOfALockedSiteNeedsForce(t *testing.T) {
	st := cliStore(t)
	site, _, _ := st.Create()
	st.SetLock(site, &store.SiteLock{Reason: "evidence", By: store.LockByAdmin})

	err := deleteSite(st, &bytes.Buffer{}, site.ViewID, false)
	if err == nil || !strings.Contains(err.Error(), "--force") || !strings.Contains(err.Error(), "evidence") {
		t.Fatalf("delete without --force = %v", err)
	}
	if _, err := st.ByViewID(site.ViewID); err != nil {
		t.Fatal("the locked site was deleted without --force")
	}
	var out bytes.Buffer
	if err := deleteSite(st, &out, site.ViewID, true); err != nil {
		t.Fatalf("delete --force: %v", err)
	}
	if _, err := st.ByViewID(site.ViewID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("site after delete --force: %v", err)
	}

	// An unlocked site needs no flag, as before.
	open, _, _ := st.Create()
	if err := deleteSite(st, &bytes.Buffer{}, open.ViewID, false); err != nil {
		t.Fatalf("delete of an unlocked site: %v", err)
	}
}

func TestCLIForceFlagStandsAnywhere(t *testing.T) {
	for _, args := range [][]string{{"--force", "x"}, {"x", "--force"}, {"-f", "x"}} {
		force, rest := forceFlag(args)
		if !force || len(rest) != 1 || rest[0] != "x" {
			t.Errorf("forceFlag(%v) = %v %v", args, force, rest)
		}
	}
	if force, rest := forceFlag([]string{"x"}); force || len(rest) != 1 {
		t.Errorf("no flag: %v %v", force, rest)
	}
}

func TestCLIListShowsTheLock(t *testing.T) {
	st := cliStore(t)
	locked, _, _ := st.Create()
	open, _, _ := st.Create()
	st.SetLock(locked, &store.SiteLock{Reason: "phishing", By: store.LockByAdmin})

	var out bytes.Buffer
	if err := listSites(st, &out); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, line := range strings.Split(text, "\n") {
		switch {
		case strings.HasPrefix(line, locked.ViewID) && !strings.Contains(line, "LOCKED"):
			t.Errorf("the locked site's row does not say so: %q", line)
		case strings.HasPrefix(line, open.ViewID) && strings.Contains(line, "LOCKED"):
			t.Errorf("the open site's row says locked: %q", line)
		}
	}
	if !strings.Contains(text, "by admin: phishing") || !strings.Contains(text, "2 site(s), 1 locked.") {
		t.Errorf("list = %s", text)
	}
}
