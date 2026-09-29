package httpapi

import (
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ittrail/sitebin.io/internal/ext"
	"github.com/ittrail/sitebin.io/internal/store"
)

// The lock retention through the seam, the owner's view and the operator's
// mail. See the lock-retention addendum of
// docs/superpowers/specs/2026-09-28-site-lock-and-account-suspension.md.

const retention180 = 180 * 24 * time.Hour

// The register reads the purge date and the evidence hold from the core —
// one rule — and places and releases the hold through SetHold. A lock that
// replaces a lock keeps its date and its hold.
func TestSiteServiceRetentionSeam(t *testing.T) {
	e := newEnv(t, nil)
	e.st.SetLockRetention(retention180)
	c := e.createSite(t, nil, map[string]string{"index.html": "x"})
	svc := e.api.SiteService()

	if err := svc.SetHold(c.ID, &ext.LockHold{By: "acct-admin"}); !errors.Is(err, ext.ErrSiteNotLocked) {
		t.Fatalf("SetHold on an unlocked site = %v, want ErrSiteNotLocked", err)
	}
	at := time.Date(2026, 9, 29, 8, 0, 0, 0, time.UTC)
	if err := svc.SetLock(c.ID, &ext.SiteLock{At: at, By: ext.LockByScanner, Reason: "held for review"}); err != nil {
		t.Fatal(err)
	}
	info, _ := svc.Info(c.ID)
	if l := info.Locked; l == nil || l.PurgeAt == nil || !l.PurgeAt.Equal(at.Add(retention180)) || l.Hold != nil {
		t.Fatalf("Info.Locked = %+v", l)
	}

	// Keep: the operator's lock over the scanner's keeps the clock.
	if err := svc.SetLock(c.ID, &ext.SiteLock{At: time.Now(), By: ext.LockByAdmin, Reason: "phishing"}); err != nil {
		t.Fatal(err)
	}
	info, _ = svc.Info(c.ID)
	if !info.Locked.At.Equal(at) || info.Locked.By != ext.LockByAdmin {
		t.Fatalf("Keep moved the lock date: %+v", info.Locked)
	}

	holdAt := time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC)
	if err := svc.SetHold(c.ID, &ext.LockHold{At: holdAt, By: "acct-admin"}); err != nil {
		t.Fatal(err)
	}
	info, _ = svc.Info(c.ID)
	if l := info.Locked; l.Hold == nil || l.Hold.By != "acct-admin" || !l.Hold.At.Equal(holdAt) || l.PurgeAt == nil || !l.PurgeAt.Equal(at.Add(retention180)) {
		t.Fatalf("held lock = %+v (hold %+v)", l, l.Hold)
	}
	// A hold handed to SetLock is ignored; a re-lock keeps the one there.
	svc.SetLock(c.ID, &ext.SiteLock{By: ext.LockByAdmin, Reason: "again", Hold: nil})
	if info, _ := svc.Info(c.ID); info.Locked.Hold == nil {
		t.Fatal("a re-lock dropped the evidence hold")
	}

	// The owner sees the lock, never the hold or the purge date.
	w := e.public(t, authed(httptest.NewRequest("GET", "/api/sites/"+editIDFrom(t, c.EditURL), nil), c.EditPassword))
	if b := w.Body.String(); w.Code != 200 || strings.Contains(b, "hold") || strings.Contains(b, "purge") {
		t.Errorf("the owner's payload tells of the case: %d %s", w.Code, b)
	}

	if err := svc.SetHold(c.ID, nil); err != nil {
		t.Fatal(err)
	}
	if info, _ := svc.Info(c.ID); info.Locked.Hold != nil || info.Locked.PurgeAt == nil {
		t.Fatalf("released lock = %+v", info.Locked)
	}

	// Retention 0: no purge date.
	e.st.SetLockRetention(0)
	if info, _ := svc.Info(c.ID); info.Locked.PurgeAt != nil {
		t.Errorf("retention 0 reports a purge date: %v", info.Locked.PurgeAt)
	}

	svc.ForceDelete(c.ID)
	if err := svc.SetHold(c.ID, &ext.LockHold{}); !errors.Is(err, ext.ErrSiteGone) {
		t.Errorf("SetHold on a gone site = %v, want ErrSiteGone", err)
	}
}

// Every retention purge is one plain-text line in the operator's digest —
// never an immediate mail, and never at the cost of the hourly budget.
func TestLockPurgeGoesToTheDigest(t *testing.T) {
	e, _, rs := abuseEnv(t, false)
	e.st.SetLockRetention(24 * time.Hour)
	var digest func()
	e.api.alerts.after = func(_ time.Duration, f func()) { digest = f }
	id, _, _ := cleanSite(t, e, nil)
	site, _ := e.st.ByViewID(id)
	if err := e.st.AddDomain(site, "bank-login.example.org"); err != nil {
		t.Fatal(err)
	}
	at := time.Now().Add(-48 * time.Hour).UTC()
	e.st.SetLock(site, &store.SiteLock{At: at, By: store.LockByScanner, Reason: "held for review: telegram-bot-api in https://evil.example/x"})

	if purged, err := e.st.PurgeLocked(site, time.Now()); err != nil || !purged {
		t.Fatalf("purge = %v, %v", purged, err)
	}
	e.api.alerts.wg.Wait()
	rs.mu.Lock()
	immediate := len(rs.sent)
	rs.mu.Unlock()
	if immediate != 0 {
		t.Fatalf("a purge was mailed at once (%d mails)", immediate)
	}
	if digest == nil {
		t.Fatal("the purge was not queued for the digest")
	}
	digest()
	e.api.alerts.wg.Wait()
	m := rs.last(t)
	txt := mailText(t, m)
	for _, want := range []string{id + "  purged  locked " + at.Format("2006-01-02") + " by scanner, 1-day retention", "owner acct-1", "bank-login[.]example[.]org", "hxxps://evil[.]example/x", "retention purges"} {
		if !strings.Contains(txt, want) {
			t.Errorf("digest lacks %q:\n%s", want, txt)
		}
	}
	if strings.Contains(strings.ToLower(string(m.Data)), "text/html") {
		t.Error("the digest must be plain text")
	}
}

// A hold alert says when the site will be purged and how to keep it.
func TestHeldAlertNamesTheRetention(t *testing.T) {
	e, fp, rs := abuseEnv(t, false)
	e.st.SetLockRetention(retention180)
	body, ct := filesBody(t, nil, map[string]string{"index.html": kitHTML}, nil)
	req := httptest.NewRequest("POST", "/api/sites", body)
	req.Header.Set("Content-Type", ct)
	e.public(t, bearer(req, "sbp_tok"))
	id := createdID(t, fp)
	e.api.alerts.wg.Wait()
	txt := mailText(t, rs.last(t))
	for _, want := range []string{"Retention: purged after", "180 days after the lock", "sitebin hold " + id} {
		if !strings.Contains(txt, want) {
			t.Errorf("hold alert lacks %q:\n%s", want, txt)
		}
	}
}
