//go:build ee

package ee

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/ittrail/sitebin.io/internal/ext"
)

// The register's half of abuse detection. See
// docs/superpowers/specs/2026-09-29-abuse-detection.md.

func TestReportsTabListsReportsWithTheirSites(t *testing.T) {
	p, host, mux := setupAdmin(t, "boss@example.com")
	cookie, accID := adminUser(t, p, mux, "boss@example.com", "admin")
	instance(t, host, accID)
	now := time.Now()
	host.sites.reports = []ext.AbuseReport{
		{Time: now, Target: "https://" + siteC + ".sitebin.app/login", ViewID: siteC, Reason: "Phishing or credential theft",
			Details: "a fake <b>bank</b>", Contact: "cert@bank.example", Source: "203.0.113.0/24", Via: "page"},
		{Time: now.Add(-time.Hour), Target: "https://elsewhere.example/", Reason: "Spam", Via: "api"},
		{Time: now.Add(-2 * time.Hour), Target: "gone", ViewID: "zzzzzzzzzzzzzzzzzzzzzzzzzz", Reason: "Other", Via: "api"},
	}
	body := getAs(mux, "/account/admin/reports", cookie).Body.String()
	for _, want := range []string{
		"Phishing or credential theft", "sitebin.app/login", "a fake &lt;b&gt;bank&lt;/b&gt;", `href="mailto:cert@bank.example"`,
		`href="/account/admin?q=` + siteC + `"`, `action="/account/admin/sites/` + siteC + `/lock?return=reports"`,
		`value="abuse report: Phishing or credential theft"`, "not resolved to a site", "no longer exists", "Reports (3)",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("reports tab lacks %q", want)
		}
	}
	if strings.Index(body, "Phishing or credential theft") > strings.Index(body, "Spam") {
		t.Error("reports are not newest first")
	}

	// one click locks the reported site and comes back to the tab
	acc, _ := p.accounts.ByEmail("boss@example.com")
	req := form(url.Values{"csrf": {p.csrf(acc)}, "reason": {"abuse report: Phishing or credential theft"}})
	req.URL.Path, req.URL.RawQuery = "/account/admin/sites/"+siteC+"/lock", "return=reports"
	req.AddCookie(cookie)
	w := serve(mux, req)
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/account/admin/reports?flash=locked" {
		t.Fatalf("lock from a report = %d %q", w.Code, w.Header().Get("Location"))
	}
	if l := host.sites.infos[siteC].Locked; l == nil || l.By != ext.LockByAdmin || !strings.Contains(l.Reason, "abuse report") {
		t.Fatalf("lock %+v", l)
	}
	body = getAs(mux, "/account/admin/reports?flash=locked", cookie).Body.String()
	if !strings.Contains(body, "LOCKED") || strings.Contains(body, `action="/account/admin/sites/`+siteC+`/lock`) {
		t.Error("a locked site still offers the lock")
	}
	// the sites view links the tab
	if body := getAs(mux, "/account/admin", cookie).Body.String(); !strings.Contains(body, `href="/account/admin/reports"`) {
		t.Error("the register does not link the Reports tab")
	}
}

func TestReportsTabIsAdminOnly(t *testing.T) {
	p, _, mux := setupAdmin(t, "boss@example.com")
	cookie, _ := adminUser(t, p, mux, "boss@example.com", "free")
	if w := getAs(mux, "/account/admin/reports", cookie); w.Code != 404 {
		t.Fatalf("non-admin reports tab = %d", w.Code)
	}
	if w := getAs(mux, "/account/admin/reports", nil); w.Code != 404 {
		t.Fatalf("anonymous reports tab = %d", w.Code)
	}
}

func TestRegisterShowsScannerFindingsAndDismisses(t *testing.T) {
	p, host, mux := setupAdmin(t, "boss@example.com")
	cookie, accID := adminUser(t, p, mux, "boss@example.com", "admin")
	instance(t, host, accID)
	acc, _ := p.accounts.ByEmail("boss@example.com")
	info := host.sites.infos[siteC]
	for i := 0; i < 5; i++ {
		info.Findings = append(info.Findings, ext.ScanFinding{Rule: "telegram-bot-api", Severity: "block", Path: "p" + string(rune('0'+i)) + ".html",
			Excerpt: "fetch(`https://api.telegram.org/bot${t}/sendmessage`)", Source: "upload", At: time.Now()})
	}
	info.Locked = &ext.SiteLock{At: time.Now(), By: ext.LockByScanner, Reason: "held for review: telegram-bot-api in p4.html"}
	host.sites.infos[siteC] = info

	body := getAs(mux, "/account/admin", cookie).Body.String()
	for _, want := range []string{"telegram-bot-api (block) · p4.html", "and 2 more", "by the scanner", "held for review",
		`action="/account/admin/sites/` + siteC + `/review"`, "?lock=" + siteC, `<span class="k">Scanner hits</span><span class="v">1</span>`} {
		if !strings.Contains(body, want) {
			t.Errorf("register lacks %q", want)
		}
	}
	if strings.Contains(body, "p0.html") {
		t.Error("the oldest findings are shown instead of the newest")
	}

	// the flagged filter takes scanner hits (and still CSP violations)
	csp := host.sites.infos[siteA]
	csp.Violations = 1
	host.sites.infos[siteA] = csp
	only := getAs(mux, "/account/admin?filter=flagged", cookie).Body.String()
	if !strings.Contains(only, siteC) || !strings.Contains(only, siteA) || strings.Contains(only, "dddddddddddddddddddddddddd") {
		t.Error("the flagged filter is not scanner-or-CSP")
	}

	// dismiss needs the CSRF token, then clears
	if w := postAs(mux, "/account/admin/sites/"+siteC+"/review", cookie, url.Values{}); w.Code != http.StatusForbidden {
		t.Errorf("dismiss without CSRF = %d", w.Code)
	}
	w := postAs(mux, "/account/admin/sites/"+siteC+"/review", cookie, url.Values{"csrf": {p.csrf(acc)}})
	if w.Code != http.StatusSeeOther || !strings.Contains(w.Header().Get("Location"), "flash=reviewed") {
		t.Fatalf("dismiss = %d %q", w.Code, w.Header().Get("Location"))
	}
	if len(host.sites.cleared) != 1 || len(host.sites.infos[siteC].Findings) != 0 {
		t.Errorf("cleared %v findings %v", host.sites.cleared, host.sites.infos[siteC].Findings)
	}
	if host.sites.infos[siteC].Locked == nil {
		t.Error("a dismissal unlocked the site")
	}
}

func TestAdminKeepsAScannerLock(t *testing.T) {
	p, host, mux := setupAdmin(t, "boss@example.com")
	cookie, accID := adminUser(t, p, mux, "boss@example.com", "admin")
	instance(t, host, accID)
	acc, _ := p.accounts.ByEmail("boss@example.com")
	host.sites.SetLock(siteC, &ext.SiteLock{By: ext.LockByScanner, Reason: "held for review"})
	postAs(mux, "/account/admin/sites/"+siteC+"/lock", cookie, url.Values{"csrf": {p.csrf(acc)}, "reason": {"confirmed phishing"}})
	if l := host.sites.infos[siteC].Locked; l == nil || l.By != ext.LockByAdmin || l.Reason != "confirmed phishing" {
		t.Fatalf("kept lock = %+v", l)
	}
}

func TestAccountEmailNamesTheOwner(t *testing.T) {
	p, _, mux := setupAdmin(t, "boss@example.com")
	_, accID := adminUser(t, p, mux, "owner@example.com", "free")
	if email, ok := p.AccountEmail(accID); !ok || email != "owner@example.com" {
		t.Errorf("AccountEmail = %q %v", email, ok)
	}
	if _, ok := p.AccountEmail("nosuchaccount"); ok {
		t.Error("an unknown account has an email")
	}
	var _ ext.AccountDirectory = p
}
