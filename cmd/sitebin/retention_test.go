package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/ittrail/sitebin.io/internal/store"
)

// `sitebin hold` / `sitebin unhold`: the evidence hold on a locked site,
// by any handle findSite takes. A site that is not locked is refused.
func TestCLIHoldAndUnhold(t *testing.T) {
	st := cliStore(t)
	st.SetLockRetention(180 * 24 * time.Hour)
	site, _, _ := st.Create()
	if err := st.AddDomain(site, "bank-login.example.org"); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

	err := holdSite(st, &bytes.Buffer{}, site.ViewID, true, now)
	if err == nil || !strings.Contains(err.Error(), "not locked") || !strings.Contains(err.Error(), "sitebin lock "+site.ViewID) {
		t.Fatalf("hold on an unlocked site = %v", err)
	}

	locked := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	st.SetLock(site, &store.SiteLock{At: locked, By: store.LockByScanner})
	var out bytes.Buffer
	if err := holdSite(st, &out, "bank-login.example.org", true, now); err != nil {
		t.Fatalf("hold: %v", err)
	}
	if !strings.Contains(out.String(), "evidence hold placed on site "+site.ViewID) || !strings.Contains(out.String(), "sitebin unhold "+site.ViewID) {
		t.Errorf("hold output = %q", out.String())
	}
	got, _ := st.ByViewID(site.ViewID)
	if h := got.Meta.Locked.Hold; h == nil || h.By != store.HoldByCLI || !h.At.Equal(now) {
		t.Fatalf("hold = %+v", h)
	}
	out.Reset()
	holdSite(st, &out, site.ViewID, true, now.Add(time.Hour))
	if !strings.Contains(out.String(), "already held") {
		t.Errorf("second hold output = %q", out.String())
	}

	// Releasing it, past the retention: the next sweep purges the site.
	out.Reset()
	if err := holdSite(st, &out, site.EditID, false, now); err != nil {
		t.Fatalf("unhold: %v", err)
	}
	if s := out.String(); !strings.Contains(s, "evidence hold released") || !strings.Contains(s, "purge due 2026-08-28") || !strings.Contains(s, "next sweep purges") {
		t.Errorf("unhold output = %q", s)
	}
	if got, _ := st.ByViewID(site.ViewID); got.Meta.Locked.Hold != nil {
		t.Fatal("still held")
	}
	out.Reset()
	holdSite(st, &out, site.ViewID, false, now)
	if !strings.Contains(out.String(), "had no evidence hold") {
		t.Errorf("unhold of an unheld site = %q", out.String())
	}
}

// `sitebin list` and `sitebin lock` say what the retention does with a
// lock: the purge date, the hold, or nothing with retention 0.
func TestCLIShowsTheRetention(t *testing.T) {
	st := cliStore(t)
	st.SetLockRetention(180 * 24 * time.Hour)
	due, _, _ := st.Create()
	held, _, _ := st.Create()
	at := time.Date(2026, 9, 29, 8, 0, 0, 0, time.UTC)
	st.SetLock(due, &store.SiteLock{At: at, By: store.LockByAdmin, Reason: "phishing"})
	st.SetLock(held, &store.SiteLock{At: at, By: store.LockByAdmin})
	st.SetHold(held, &store.LockHold{At: at.Add(24 * time.Hour), By: store.HoldByCLI})

	var out bytes.Buffer
	if err := listSites(st, &out); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	if !strings.Contains(text, "by admin: phishing · purge due 2027-03-28 08:00") {
		t.Errorf("list does not show the purge date: %s", text)
	}
	if !strings.Contains(text, "held (case open) since 2026-09-30 by cli") {
		t.Errorf("list does not show the hold: %s", text)
	}

	// Re-locking keeps the clock, and the output says since when.
	out.Reset()
	if err := lockSite(st, &out, due.ViewID, "phishing, reported"); err != nil {
		t.Fatal(err)
	}
	if s := out.String(); !strings.Contains(s, "locked since 2026-09-29 08:00") || !strings.Contains(s, "purge due 2027-03-28 08:00") {
		t.Errorf("re-lock output = %q", s)
	}

	st.SetLockRetention(0)
	out.Reset()
	listSites(st, &out)
	if !strings.Contains(out.String(), "kept until unlocked") {
		t.Errorf("retention 0 is not shown: %s", out.String())
	}
}
