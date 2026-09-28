//go:build ee

package ee

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ittrail/sitebin.io/ee/account"
	"github.com/ittrail/sitebin.io/internal/ext"
)

// The stack's suspension order: see suspend.go and
// docs/superpowers/specs/2026-09-28-site-lock-and-account-suspension.md.

const (
	suspendSubject = "11111111-1111-4111-8111-111111111111"
	siteB          = "bbbbbbbbbbbbbbbbbbbbbbbbbb"
)

func suspendOrderBody(subject string, suspended bool, reason string) string {
	b, _ := json.Marshal(map[string]any{"userId": subject, "email": "subject@example.com", "suspended": suspended, "reason": reason})
	return string(b)
}

func orderSuspend(mux http.Handler, body string) *httptest.ResponseRecorder {
	return serve(mux, stackOrder(gdprSuspendPath, testGDPRSecret, time.Now(), body))
}

func TestSuspendRouteAbsentWithoutASecret(t *testing.T) {
	_, _, mux := setupStackInstance(t, "")
	r := httptest.NewRequest("POST", gdprSuspendPath, strings.NewReader(suspendOrderBody(suspendSubject, true, "")))
	if w := serve(mux, r); w.Code != http.StatusNotFound {
		t.Fatalf("suspend without a secret = %d, want 404", w.Code)
	}
}

// The order is authenticated exactly like the GDPR orders, and a refused one
// changes nothing.
func TestSuspendSignatureVerification(t *testing.T) {
	p, host, mux := setupStackInstance(t, testGDPRSecret)
	acc, _ := stackUser(t, p, host, suspendSubject, "subject@example.com")
	body := suspendOrderBody(suspendSubject, true, "phishing")
	now := time.Now()

	// The headers of an order signed for another body, on this one.
	swapped := httptest.NewRequest("POST", gdprSuspendPath, strings.NewReader(body))
	swapped.Header = stackOrder(gdprSuspendPath, testGDPRSecret, now, suspendOrderBody(suspendSubject, false, "")).Header

	for name, r := range map[string]*http.Request{
		"unsigned":     httptest.NewRequest("POST", gdprSuspendPath, strings.NewReader(body)),
		"wrong secret": stackOrder(gdprSuspendPath, "not-the-secret-0123456789abcdef0123456789", now, body),
		"stale":        stackOrder(gdprSuspendPath, testGDPRSecret, now.Add(-gdprMaxSkew-time.Minute), body),
		"future":       stackOrder(gdprSuspendPath, testGDPRSecret, now.Add(gdprMaxSkew+time.Minute), body),
		"body swapped": swapped,
	} {
		if w := serve(mux, r); w.Code != http.StatusUnauthorized {
			t.Errorf("%s: %d, want 401", name, w.Code)
		}
	}
	got, _ := p.accounts.ByID(acc.ID)
	if got.Suspended() {
		t.Fatal("a refused order suspended the account")
	}
	for id, info := range host.sites.infos {
		if info.Locked != nil {
			t.Fatalf("a refused order locked %s", id)
		}
	}
}

// A verified order that does not say which way is refused, not guessed:
// reading it as "lift" would unlock a phishing operator's sites.
func TestSuspendNeedsAnExplicitDirection(t *testing.T) {
	p, host, mux := setupStackInstance(t, testGDPRSecret)
	stackUser(t, p, host, suspendSubject, "subject@example.com")
	for _, body := range []string{
		`{"userId":"` + suspendSubject + `"}`,
		`{"suspended":true}`,
		`not json`,
	} {
		if w := orderSuspend(mux, body); w.Code != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400", body, w.Code)
		}
	}
}

func TestSuspendOfAnUnknownUserIsA200(t *testing.T) {
	_, _, mux := setupStackInstance(t, testGDPRSecret)
	for _, s := range []bool{true, false} {
		w := orderSuspend(mux, suspendOrderBody("22222222-2222-4222-8222-222222222222", s, ""))
		if w.Code != 200 || !strings.Contains(w.Body.String(), `"found":false`) {
			t.Errorf("suspended=%v for an unknown user = %d %s", s, w.Code, w.Body)
		}
	}
}

func TestSuspendLocksEveryAccountSiteAndUnsuspendLiftsOnlyThose(t *testing.T) {
	p, host, mux := setupStackInstance(t, testGDPRSecret)
	acc, _ := stackUser(t, p, host, suspendSubject, "subject@example.com")
	// The operator locked site B by hand before the suspension.
	host.sites.SetLock(siteB, &ext.SiteLock{At: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), Reason: "evidence", By: ext.LockByAdmin})
	// And a stale ownership marker, for a site that no longer exists.
	p.accounts.LinkSite(acc, "zzzzzzzzzzzzzzzzzzzzzzzzzz")
	before := acc.TokenVersion

	w := orderSuspend(mux, suspendOrderBody(suspendSubject, true, "phishing"))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"sitesLocked":1`) {
		t.Fatalf("suspend = %d %s", w.Code, w.Body)
	}
	got, _ := p.accounts.ByID(acc.ID)
	if !got.Suspended() || got.SuspendedReason != "phishing" || got.TokenVersion != before+1 {
		t.Fatalf("account after suspend: at=%v reason=%q version=%d (was %d)", got.SuspendedAt, got.SuspendedReason, got.TokenVersion, before)
	}
	if l := host.sites.infos[siteA].Locked; l == nil || l.By != ext.LockByAccount || !strings.Contains(l.Reason, "phishing") {
		t.Errorf("site A lock = %+v", l)
	}
	if l := host.sites.infos[siteB].Locked; l == nil || l.By != ext.LockByAdmin || l.Reason != "evidence" {
		t.Errorf("the operator's lock on site B was replaced: %+v", l)
	}
	if ids, _ := p.accounts.ListSiteIDs(got); len(ids) != 2 {
		t.Errorf("the stale marker was not dropped: %v", ids)
	}

	// Repeated: nothing moves.
	firstAt := *got.SuspendedAt
	lockAt := host.sites.infos[siteA].Locked.At
	if w := orderSuspend(mux, suspendOrderBody(suspendSubject, true, "")); w.Code != 200 {
		t.Fatalf("repeat = %d", w.Code)
	}
	got, _ = p.accounts.ByID(acc.ID)
	if !got.SuspendedAt.Equal(firstAt) || got.SuspendedReason != "phishing" || !host.sites.infos[siteA].Locked.At.Equal(lockAt) {
		t.Errorf("a repeated order moved the suspension or the lock")
	}

	// Lifted: only the suspension's own lock goes.
	w = orderSuspend(mux, suspendOrderBody(suspendSubject, false, ""))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"sitesUnlocked":1`) {
		t.Fatalf("unsuspend = %d %s", w.Code, w.Body)
	}
	got, _ = p.accounts.ByID(acc.ID)
	if got.Suspended() || got.SuspendedReason != "" {
		t.Errorf("still suspended: %+v", got)
	}
	if host.sites.infos[siteA].Locked != nil {
		t.Error("the suspension's lock on site A survived the unsuspension")
	}
	if l := host.sites.infos[siteB].Locked; l == nil || l.By != ext.LockByAdmin {
		t.Error("the unsuspension lifted the operator's lock on site B")
	}
}

// A lock that fails is a 500 so the stack retries, and the account is
// suspended already: the refusals do not wait for the sites.
func TestSuspendReportsAFailedLockAndConvergesOnRetry(t *testing.T) {
	p, host, mux := setupStackInstance(t, testGDPRSecret)
	acc, _ := stackUser(t, p, host, suspendSubject, "subject@example.com")
	host.sites.lockErrs = map[string]error{siteB: errors.New("disk on fire")}

	if w := orderSuspend(mux, suspendOrderBody(suspendSubject, true, "")); w.Code != http.StatusInternalServerError {
		t.Fatalf("suspend with a failing lock = %d, want 500", w.Code)
	}
	if got, _ := p.accounts.ByID(acc.ID); !got.Suspended() {
		t.Fatal("the account was not suspended although the order was verified")
	}
	if host.sites.infos[siteA].Locked == nil {
		t.Error("one failing site kept the others from being locked")
	}
	host.sites.lockErrs = nil
	if w := orderSuspend(mux, suspendOrderBody(suspendSubject, true, "")); w.Code != 200 {
		t.Fatalf("retry = %d", w.Code)
	}
	if host.sites.infos[siteB].Locked == nil {
		t.Error("the retry did not lock the site that failed")
	}
}

// Every way a suspended account could come in is refused: its browser
// session (and a fresh one, as a sign-in would set), its API tokens.
func TestSuspendedAccountIsRefusedEverywhere(t *testing.T) {
	p, host, mux := setupStackInstance(t, testGDPRSecret)
	acc, secret := stackUser(t, p, host, suspendSubject, "subject@example.com")
	cookie := p.sessions.Cookie(acc.ID, acc.TokenVersion)
	if w := getAs(mux, "/account", cookie); w.Code != 200 {
		t.Fatalf("dashboard before = %d", w.Code)
	}
	if _, ok := p.BearerCredential(bearerRequest(secret, false)); !ok {
		t.Fatal("the account token did not work before")
	}

	orderSuspend(mux, suspendOrderBody(suspendSubject, true, ""))
	got, _ := p.accounts.ByID(acc.ID)

	if w := getAs(mux, "/account", cookie); w.Code != http.StatusSeeOther {
		t.Errorf("dashboard with the old session = %d, want a redirect to sign in", w.Code)
	}
	fresh := p.sessions.Cookie(got.ID, got.TokenVersion)
	if w := getAs(mux, "/account", fresh); w.Code != http.StatusSeeOther {
		t.Errorf("dashboard with a current-version session = %d, want a redirect", w.Code)
	}
	r := httptest.NewRequest("GET", "/", nil)
	r.AddCookie(fresh)
	if _, ok := p.SessionAccount(r); ok {
		t.Error("the owner-session edit still honours a suspended account")
	}
	for _, mcp := range []bool{false, true} {
		if _, ok := p.BearerCredential(bearerRequest(secret, mcp)); ok {
			t.Errorf("mcp=%v: an account token of a suspended account still works", mcp)
		}
		if _, ok := p.accountForAPI(bearerRequest(secret, mcp)); ok {
			t.Errorf("mcp=%v: accountForAPI honours a suspended account's token", mcp)
		}
	}
	if _, err := p.AuthorizeCreate(bearerRequest(secret, false)); err == nil {
		t.Error("a suspended account's token can still create sites")
	}

	// Lifted, the account works again — with a new session, the old one
	// stays revoked.
	orderSuspend(mux, suspendOrderBody(suspendSubject, false, ""))
	if _, ok := p.BearerCredential(bearerRequest(secret, false)); !ok {
		t.Error("the account token does not work after the unsuspension")
	}
	if w := getAs(mux, "/account", fresh); w.Code != 200 {
		t.Errorf("dashboard after unsuspension = %d", w.Code)
	}
}

// An MCP OAuth token of a suspended account is refused after it verifies —
// including through the verifier's first-use provisioning, which hands an
// existing account's id back.
func TestSuspendedAccountsOAuthTokenIsRefused(t *testing.T) {
	ti := newTestIssuer(t)
	p, _ := setupMCPOAuthInstance(t, ti, nil)
	acc, err := p.accounts.CreateOAuth(account.OIDCProv, testSubject, "agent-owner@example.com", true, "")
	if err != nil {
		t.Fatal(err)
	}
	tok := ti.sign(t, nil, nil)
	if _, ok := p.BearerCredential(bearerRequest(tok, true)); !ok {
		t.Fatal("the OAuth token did not work before")
	}
	now := time.Now()
	p.accounts.Update(acc, func(cur *account.Account) error { cur.SuspendedAt = &now; return nil })
	if _, ok := p.BearerCredential(bearerRequest(tok, true)); ok {
		t.Error("an OAuth token of a suspended account is honoured on /mcp")
	}
	if _, ok := p.accountForAPI(bearerRequest(tok, true)); ok {
		t.Error("accountForAPI honours a suspended account's OAuth token")
	}
}

func TestSuspendedLocalAccountCannotSignIn(t *testing.T) {
	p, _, mux := setupAccounts(t)
	acc, _ := localUser(t, p, "local@example.com")
	now := time.Now()
	p.accounts.Update(acc, func(cur *account.Account) error { cur.SuspendedAt = &now; return nil })
	r := form(map[string][]string{"email": {"local@example.com"}, "password": {"password123"}})
	r.URL.Path = "/account/login"
	w := serve(mux, r)
	if sessionCookieMaybe(w) != nil || !strings.Contains(w.Body.String(), "suspended") {
		t.Fatalf("a suspended local account signed in: %d %s", w.Code, w.Body)
	}
}

func TestRegisterBadgesASuspendedOwner(t *testing.T) {
	p, host, mux := setupAdmin(t, "boss@example.com")
	cookie, _ := adminUser(t, p, mux, "boss@example.com", "admin")
	owner, err := p.accounts.CreateLocal("abuser@example.com", "$argon2$notreal", "free")
	if err != nil {
		t.Fatal(err)
	}
	instance(t, host, owner.ID)
	if strings.Contains(getAs(mux, "/account/admin", cookie).Body.String(), `class="susp"`) {
		t.Fatal("an active owner carries the badge")
	}
	now := time.Now()
	p.accounts.Update(owner, func(cur *account.Account) error {
		cur.SuspendedAt = &now
		cur.SuspendedReason = "phishing"
		return nil
	})
	body := getAs(mux, "/account/admin", cookie).Body.String()
	if !strings.Contains(body, `class="susp"`) || !strings.Contains(body, "phishing") {
		t.Error("the register does not badge the suspended owner")
	}
}

// The export carries the suspension: it is data held about the subject.
func TestGDPRExportCarriesTheSuspension(t *testing.T) {
	p, host, mux := setupStackInstance(t, testGDPRSecret)
	stackUser(t, p, host, suspendSubject, "subject@example.com")
	orderSuspend(mux, suspendOrderBody(suspendSubject, true, "phishing"))
	w := serve(mux, stackOrder(gdprExportPath, testGDPRSecret, time.Now(), `{"userId":"`+suspendSubject+`"}`))
	if !strings.Contains(w.Body.String(), `"suspended_reason":"phishing"`) || !strings.Contains(w.Body.String(), `"suspended_at"`) {
		t.Errorf("export = %s", w.Body)
	}
}
