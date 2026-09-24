package httpapi

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http/httptest"
	"net/mail"
	"net/textproto"
	"net/url"
	"strings"
	"testing"
	"time"

	altcha "github.com/altcha-org/altcha-lib-go/v2"

	"github.com/ittrail/sitebin.io/internal/store"
)

const urlenc = "application/x-www-form-urlencoded"

// activeForm creates a site with one form whose recipient has confirmed.
func activeForm(t *testing.T, e *env, spec store.Form) (*store.Site, store.Form) {
	t.Helper()
	c := e.createSite(t, nil, map[string]string{"index.html": "<h1>hi</h1>"})
	site, _ := e.st.ByViewID(c.ID)
	if spec.Name == "" {
		spec.Name = "Contact"
	}
	if spec.Recipient == "" {
		spec.Recipient = "office@example.com"
	}
	f, err := e.st.AddForm(site, spec, 10)
	if err != nil {
		t.Fatal(err)
	}
	if f, err = e.st.ConfirmForm(site, f.Key, f.Recipient, f.Seq); err != nil {
		t.Fatal(err)
	}
	return site, f
}

func viewHost(site *store.Site) string { return site.ViewID + ".sitebin.example" }

func post(t *testing.T, e *env, host, target, contentType string, body io.Reader, hdr map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", target, body)
	req.Host = host
	req.Header.Set("Content-Type", contentType)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	return e.public(t, req)
}

func submit(t *testing.T, e *env, host, key, form string, hdr map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	return post(t, e, host, "/_sitebin/forms/"+key, urlenc, strings.NewReader(form), hdr)
}

func get(t *testing.T, e *env, host, target string, hdr map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("GET", target, nil)
	req.Host = host
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	return e.public(t, req)
}

func TestSubmitMailsTheRecipient(t *testing.T) {
	e, rs := formsEnv(t, nil)
	site, f := activeForm(t, e, store.Form{})
	w := submit(t, e, viewHost(site), f.Key, "name=Anna&email=anna%40example.com&message=Hallo", nil)
	if w.Code != 303 || w.Header().Get("Location") != "/_sitebin/forms/"+f.Key+"/thanks" {
		t.Fatalf("submit = %d Location %q: %s", w.Code, w.Header().Get("Location"), w.Body)
	}
	m := rs.last(t)
	if m.To != "office@example.com" || !strings.Contains(mailText(t, m), "message: Hallo") {
		t.Fatalf("mail to %s:\n%s", m.To, mailText(t, m))
	}
	msg, _ := mail.ReadMessage(bytes.NewReader(m.Data))
	u, _ := url.Parse(strings.Trim(msg.Header.Get("List-Unsubscribe"), "<>"))
	c, ok := e.api.forms.links.ParseStop(u.Query().Get("t"), time.Now())
	if !ok || c.ViewID != site.ViewID || c.Key != f.Key || c.Recipient != f.Recipient || u.Path != "/forms/stop" {
		t.Errorf("stop link %q = %+v %v", u, c, ok)
	}
}

func TestSubmitAnswersJSON(t *testing.T) {
	e, _ := formsEnv(t, nil)
	site, f := activeForm(t, e, store.Form{})
	w := submit(t, e, viewHost(site), f.Key, "message=hi", map[string]string{"Accept": "application/json"})
	if w.Code != 200 || strings.TrimSpace(w.Body.String()) != `{"ok":true}` {
		t.Fatalf("JSON submit = %d %s", w.Code, w.Body)
	}
	w = submit(t, e, viewHost(site), "nosuchkey", "message=hi", map[string]string{"Accept": "application/json"})
	if w.Code != 404 || !strings.Contains(w.Body.String(), `"error"`) {
		t.Fatalf("JSON refusal = %d %s", w.Code, w.Body)
	}
}

func TestSubmitRefusals(t *testing.T) {
	e, rs := formsEnv(t, nil)
	site, f := activeForm(t, e, store.Form{})
	pending, _ := e.st.AddForm(site, store.Form{Name: "P", Recipient: "p@example.com"}, 10)
	stopped, _ := e.st.AddForm(site, store.Form{Name: "S", Recipient: "s@example.com"}, 10)
	e.st.ConfirmForm(site, stopped.Key, "s@example.com", 0)
	e.st.StopForm(site, stopped.Key, "s@example.com")
	for _, c := range []struct {
		name, key, body, ct string
		want                int
	}{
		{"unknown key", "nosuchkey", "a=1", urlenc, 404},
		{"pending", pending.Key, "a=1", urlenc, 403},
		{"stopped", stopped.Key, "a=1", urlenc, 403},
		{"empty", f.Key, "name=&message=+", urlenc, 400},
		{"json body", f.Key, `{"a":1}`, "application/json", 415},
	} {
		if w := post(t, e, viewHost(site), "/_sitebin/forms/"+c.key, c.ct, strings.NewReader(c.body), nil); w.Code != c.want {
			t.Errorf("%s: %d, want %d", c.name, w.Code, c.want)
		}
	}
	if rs.count() != 0 {
		t.Error("a refused submission sent mail")
	}
}

func TestSubmitPausedAndExpired(t *testing.T) {
	e, _ := formsEnv(t, nil)
	site, f := activeForm(t, e, store.Form{})
	e.st.Update(site, func(m *store.Meta) error { m.QuotaForms = intp(0); return nil })
	if w := submit(t, e, viewHost(site), f.Key, "message=hi", nil); w.Code != 403 || !strings.Contains(w.Body.String(), "paused") {
		t.Errorf("paused form = %d", w.Code)
	}
	past := time.Now().Add(-time.Hour)
	e.st.Update(site, func(m *store.Meta) error { m.QuotaForms = nil; m.ExpiresAt = &past; return nil })
	if w := submit(t, e, viewHost(site), f.Key, "message=hi", nil); w.Code != 410 {
		t.Errorf("expired site = %d, want 410", w.Code)
	}
}

// A status this binary does not know (written by a newer one, or by hand) is
// refused rather than treated as active.
func TestSubmitRefusesAnUnknownStatus(t *testing.T) {
	e, rs := formsEnv(t, nil)
	site, f := activeForm(t, e, store.Form{})
	if err := e.st.Update(site, func(m *store.Meta) error { m.Forms[0].Status = "archived"; return nil }); err != nil {
		t.Fatal(err)
	}
	w := submit(t, e, viewHost(site), f.Key, "message=hi", nil)
	if w.Code != 403 || !strings.Contains(w.Body.String(), "This form is not active.") || rs.count() != 0 {
		t.Fatalf("unknown status = %d (mails %d): %s", w.Code, rs.count(), w.Body)
	}
}

func TestSubmitWithFormsOff(t *testing.T) {
	e := newEnv(t, nil)
	c := e.createSite(t, nil, map[string]string{"index.html": "hi"})
	site, _ := e.st.ByViewID(c.ID)
	if w := submit(t, e, viewHost(site), "anykey", "message=hi", nil); w.Code != 404 {
		t.Fatalf("forms off = %d, want 404", w.Code)
	}
}

func TestSubmitHoneypotSendsNothing(t *testing.T) {
	e, rs := formsEnv(t, nil)
	site, f := activeForm(t, e, store.Form{})
	w := submit(t, e, viewHost(site), f.Key, "message=buy+now&_gotcha=http%3A%2F%2Fspam.example", nil)
	if w.Code != 303 || rs.count() != 0 {
		t.Fatalf("honeypot = %d mails=%d, want a normal-looking 303 and nothing sent", w.Code, rs.count())
	}
}

func TestSubmitRateLimits(t *testing.T) {
	e, _ := formsEnv(t, map[string]string{"SITEBIN_FORMS_PER_IP_HOUR": "2"})
	site, f := activeForm(t, e, store.Form{})
	for i := 0; i < 2; i++ {
		submit(t, e, viewHost(site), f.Key, "message=hi", nil)
	}
	if w := submit(t, e, viewHost(site), f.Key, "message=hi", nil); w.Code != 429 {
		t.Fatalf("3rd submission = %d, want 429", w.Code)
	}
}

// solvedAltcha fetches a challenge for f and solves it, returning the value of
// the altcha field, URL-encoded.
func solvedAltcha(t *testing.T, e *env, site *store.Site, f store.Form) string {
	t.Helper()
	w := get(t, e, viewHost(site), "/_sitebin/forms/"+f.Key+"/challenge", nil)
	if w.Code != 200 {
		t.Fatalf("challenge = %d", w.Code)
	}
	var ch altcha.Challenge
	if err := json.Unmarshal(w.Body.Bytes(), &ch); err != nil {
		t.Fatal(err)
	}
	sol, err := altcha.SolveChallenge(altcha.SolveChallengeOptions{Challenge: ch, DeriveKey: altcha.DeriveKeyPBKDF2()})
	if err != nil || sol == nil {
		t.Fatalf("solve: %v", err)
	}
	payload, _ := json.Marshal(map[string]any{"challenge": map[string]any{"parameters": ch.Parameters, "signature": ch.Signature}, "solution": sol})
	return url.QueryEscape(base64.StdEncoding.EncodeToString(payload))
}

// Bots must not drain a form's budget: honeypot hits, failed captchas and
// empty posts, from as many addresses as a botnet likes, are answered before
// the per-form bucket is charged. Only a message that is really mailed counts.
func TestSubmitPerFormBudgetIsSpentOnlyOnRealMessages(t *testing.T) {
	e, rs := formsEnv(t, map[string]string{"SITEBIN_FORMS_PER_FORM_HOUR": "2"})
	site, f := activeForm(t, e, store.Form{Captcha: true})
	ip := 0
	next := func() map[string]string {
		ip++
		return map[string]string{"X-Forwarded-For": fmt.Sprintf("198.51.100.%d", ip)}
	}
	for i := 0; i < 3; i++ {
		if w := submit(t, e, viewHost(site), f.Key, "message=buy+now&_gotcha=x", next()); w.Code != 303 {
			t.Fatalf("honeypot %d = %d, want the success look-alike", i+1, w.Code)
		}
		if w := submit(t, e, viewHost(site), f.Key, "message=buy+now", next()); w.Code != 403 {
			t.Fatalf("no captcha %d = %d, want 403", i+1, w.Code)
		}
	}
	if w := submit(t, e, viewHost(site), f.Key, "message=+&altcha="+solvedAltcha(t, e, site, f), next()); w.Code != 400 {
		t.Fatalf("empty post = %d, want 400", w.Code)
	}
	for i := 0; i < 2; i++ {
		if w := submit(t, e, viewHost(site), f.Key, "message=hi&altcha="+solvedAltcha(t, e, site, f), next()); w.Code != 303 {
			t.Fatalf("real message %d = %d, want 303: the bots drained the form", i+1, w.Code)
		}
	}
	if w := submit(t, e, viewHost(site), f.Key, "message=hi&altcha="+solvedAltcha(t, e, site, f), next()); w.Code != 429 {
		t.Fatalf("3rd real message = %d, want 429 from the per-form bucket", w.Code)
	}
	if rs.count() != 2 {
		t.Errorf("mails = %d, want 2", rs.count())
	}
}

// The per-IP bucket stays first, and a honeypot hit still spends it.
func TestSubmitHoneypotStillSpendsThePerIPBudget(t *testing.T) {
	e, rs := formsEnv(t, map[string]string{"SITEBIN_FORMS_PER_IP_HOUR": "2"})
	site, f := activeForm(t, e, store.Form{})
	for i := 0; i < 2; i++ {
		submit(t, e, viewHost(site), f.Key, "message=buy+now&_gotcha=x", nil)
	}
	if w := submit(t, e, viewHost(site), f.Key, "message=hi", nil); w.Code != 429 || rs.count() != 0 {
		t.Fatalf("after 2 honeypot hits from one IP = %d (mails %d), want 429", w.Code, rs.count())
	}
}

// Review Focus 3.
func TestSubmitOnCustomDomain(t *testing.T) {
	e, rs := formsEnv(t, nil)
	site, f := activeForm(t, e, store.Form{})
	if err := e.st.AddDomain(site, "www.kunde.example"); err != nil {
		t.Fatal(err)
	}
	if w := submit(t, e, "www.kunde.example", f.Key, "message=hi", nil); w.Code != 303 {
		t.Fatalf("submit on the custom domain = %d %s", w.Code, w.Body)
	}
	if !strings.Contains(mailText(t, rs.last(t)), "on www.kunde.example") {
		t.Error("the mail does not name the host the form was submitted on")
	}
	other, _ := activeForm(t, e, store.Form{Name: "Other"})
	if w := submit(t, e, viewHost(other), f.Key, "message=hi", nil); w.Code != 404 {
		t.Fatalf("a key used on another site's host = %d, want 404", w.Code)
	}
}

// Review Focus 5.
func TestSubmitRedirectsToConfiguredPath(t *testing.T) {
	e, _ := formsEnv(t, nil)
	site, f := activeForm(t, e, store.Form{Redirect: "/danke.html?sent=1#top"})
	w := submit(t, e, viewHost(site), f.Key, "message=hi", nil)
	if w.Code != 303 || w.Header().Get("Location") != "/danke.html?sent=1#top" {
		t.Fatalf("Location = %q", w.Header().Get("Location"))
	}
}

func TestSubmitThroughPathViews(t *testing.T) {
	e, _ := formsEnv(t, map[string]string{"SITEBIN_VIEW_ACCESS": "both"})
	site, f := activeForm(t, e, store.Form{})
	w := post(t, e, "sitebin.example", "/_sitebin/forms/"+f.Key+"?_site="+site.ViewID, urlenc, strings.NewReader("message=hi"), nil)
	if w.Code != 303 || w.Header().Get("Location") != "/_sitebin/forms/"+f.Key+"/thanks?_site="+site.ViewID {
		t.Fatalf("path view = %d Location %q", w.Code, w.Header().Get("Location"))
	}
	site2, g := activeForm(t, e, store.Form{Redirect: "/danke.html"})
	w = post(t, e, "sitebin.example", "/_sitebin/forms/"+g.Key+"?_site="+site2.ViewID, urlenc, strings.NewReader("message=hi"), nil)
	if w.Header().Get("Location") != "/v/"+site2.ViewID+"/danke.html" {
		t.Fatalf("path-view redirect = %q", w.Header().Get("Location"))
	}
	// _site is ignored on a site's own host.
	w = post(t, e, viewHost(site2), "/_sitebin/forms/"+f.Key+"?_site="+site.ViewID, urlenc, strings.NewReader("message=hi"), nil)
	if w.Code != 404 {
		t.Fatalf("_site on a site host = %d, want 404", w.Code)
	}
}

func multipartBody(t *testing.T, fields map[string]string, field, filename, content string) (io.Reader, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for k, v := range fields {
		mw.WriteField(k, v)
	}
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", fmt.Sprintf(`form-data; name=%q; filename=%q`, field, filename))
	p, _ := mw.CreatePart(h)
	p.Write([]byte(content))
	mw.Close()
	return &buf, mw.FormDataContentType()
}

func TestSubmitWithAttachment(t *testing.T) {
	e, rs := formsEnv(t, nil)
	site, f := activeForm(t, e, store.Form{Files: true})
	body, ct := multipartBody(t, map[string]string{"message": "see attached"}, "cv", "cv.pdf", "%PDF-1.4")
	if w := post(t, e, viewHost(site), "/_sitebin/forms/"+f.Key, ct, body, nil); w.Code != 303 {
		t.Fatalf("with attachment = %d %s", w.Code, w.Body)
	}
	if !strings.Contains(string(rs.last(t).Data), "cv.pdf") {
		t.Error("the attachment is missing from the mail")
	}
	site2, g := activeForm(t, e, store.Form{})
	body, ct = multipartBody(t, map[string]string{"message": "see attached"}, "cv", "cv.pdf", "%PDF-1.4")
	if w := post(t, e, viewHost(site2), "/_sitebin/forms/"+g.Key, ct, body, nil); w.Code != 400 || rs.count() != 1 {
		t.Fatalf("attachment on a form without files = %d mails=%d", w.Code, rs.count())
	}
}

func TestSubmitCaptcha(t *testing.T) {
	e, rs := formsEnv(t, nil)
	site, f := activeForm(t, e, store.Form{Captcha: true})
	w := get(t, e, viewHost(site), "/_sitebin/forms/"+f.Key+"/challenge", nil)
	if w.Code != 200 || !strings.HasPrefix(w.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("challenge = %d %q", w.Code, w.Header().Get("Content-Type"))
	}
	var ch altcha.Challenge
	if err := json.Unmarshal(w.Body.Bytes(), &ch); err != nil {
		t.Fatal(err)
	}
	sol, err := altcha.SolveChallenge(altcha.SolveChallengeOptions{Challenge: ch, DeriveKey: altcha.DeriveKeyPBKDF2()})
	if err != nil || sol == nil {
		t.Fatalf("solve: %v", err)
	}
	payload, _ := json.Marshal(map[string]any{"challenge": map[string]any{"parameters": ch.Parameters, "signature": ch.Signature}, "solution": sol})
	field := url.QueryEscape(base64.StdEncoding.EncodeToString(payload))

	if w := submit(t, e, viewHost(site), f.Key, "message=hi", nil); w.Code != 403 {
		t.Fatalf("without the captcha = %d, want 403", w.Code)
	}
	if w := submit(t, e, viewHost(site), f.Key, "message=hi&altcha="+field, nil); w.Code != 303 || rs.count() != 1 {
		t.Fatalf("with a solved captcha = %d mails=%d", w.Code, rs.count())
	}
	if w := submit(t, e, viewHost(site), f.Key, "message=hi&altcha="+field, nil); w.Code != 403 {
		t.Fatalf("a replayed solution = %d, want 403", w.Code)
	}
	plainSite, g := activeForm(t, e, store.Form{})
	if w := get(t, e, viewHost(plainSite), "/_sitebin/forms/"+g.Key+"/challenge", nil); w.Code != 404 {
		t.Fatalf("challenge for a form without captcha = %d, want 404", w.Code)
	}
}

// A captcha solution is only spent once it actually carries a message to the
// recipient. Every later refusal releases it, so the visitor's browser can
// re-post the same solved field without a fresh challenge.
func TestSubmitCaptchaReleasedOnEmptyForm(t *testing.T) {
	e, rs := formsEnv(t, nil)
	site, f := activeForm(t, e, store.Form{Captcha: true})
	field := solvedAltcha(t, e, site, f)
	if w := submit(t, e, viewHost(site), f.Key, "message=+&altcha="+field, nil); w.Code != 400 {
		t.Fatalf("empty form with a solved captcha = %d, want 400", w.Code)
	}
	if w := submit(t, e, viewHost(site), f.Key, "message=hi&altcha="+field, nil); w.Code != 303 || rs.count() != 1 {
		t.Fatalf("retry with the same solution and real content = %d mails=%d, want 303 and mailed", w.Code, rs.count())
	}
}

func TestSubmitCaptchaReleasedOnSMTPFailure(t *testing.T) {
	e, rs := formsEnv(t, nil)
	site, f := activeForm(t, e, store.Form{Captcha: true})
	field := solvedAltcha(t, e, site, f)
	rs.err = fmt.Errorf("550 5.1.1 <office@example.com>: Recipient address rejected")
	if w := submit(t, e, viewHost(site), f.Key, "message=hi&altcha="+field, nil); w.Code != 502 {
		t.Fatalf("SMTP failure = %d, want 502", w.Code)
	}
	rs.err = nil
	if w := submit(t, e, viewHost(site), f.Key, "message=hi&altcha="+field, nil); w.Code != 303 || rs.count() != 1 {
		t.Fatalf("retry once SMTP recovers = %d mails=%d, want 303 and mailed", w.Code, rs.count())
	}
}

// The per-form hourly bucket is a real, separate refusal (429), not a captcha
// one, so its solution must be released too. The bucket itself cannot be
// un-spent inside a test, so this asserts the release directly against the
// captcha: Verify on the same solution succeeds again once the 429 has run.
func TestSubmitCaptchaReleasedOnPerFormLimit(t *testing.T) {
	e, rs := formsEnv(t, map[string]string{"SITEBIN_FORMS_PER_FORM_HOUR": "1"})
	site, f := activeForm(t, e, store.Form{Captcha: true})
	field1 := solvedAltcha(t, e, site, f)
	if w := submit(t, e, viewHost(site), f.Key, "message=hi&altcha="+field1, nil); w.Code != 303 || rs.count() != 1 {
		t.Fatalf("first real submission = %d mails=%d, want 303", w.Code, rs.count())
	}
	field2 := solvedAltcha(t, e, site, f)
	if w := submit(t, e, viewHost(site), f.Key, "message=hi&altcha="+field2, nil); w.Code != 429 {
		t.Fatalf("second submission, over the per-form limit, = %d, want 429", w.Code)
	}
	raw, err := url.QueryUnescape(field2)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.api.forms.captcha.Verify(raw, site.ViewID, f.Key); err != nil {
		t.Errorf("the second solution was not released on the 429: %v", err)
	}
}

// A successful send is the one refusal-free path, and it is the only one
// that must keep the solution spent: replaying it must still be refused.
func TestSubmitCaptchaSuccessKeepsTheSolutionSpent(t *testing.T) {
	e, rs := formsEnv(t, nil)
	site, f := activeForm(t, e, store.Form{Captcha: true})
	field := solvedAltcha(t, e, site, f)
	if w := submit(t, e, viewHost(site), f.Key, "message=hi&altcha="+field, nil); w.Code != 303 || rs.count() != 1 {
		t.Fatalf("submit = %d mails=%d, want 303", w.Code, rs.count())
	}
	if w := submit(t, e, viewHost(site), f.Key, "message=hi&altcha="+field, nil); w.Code != 403 {
		t.Fatalf("replay after a successful send = %d, want 403: a spent solution must stay spent", w.Code)
	}
}

func TestSubmitSMTPFailureIs502(t *testing.T) {
	e, rs := formsEnv(t, nil)
	var logs bytes.Buffer
	e.api.log = slog.New(slog.NewTextHandler(&logs, nil))
	rs.err = fmt.Errorf("550 5.1.1 <office@example.com>: Recipient address rejected")
	site, f := activeForm(t, e, store.Form{})
	if w := submit(t, e, viewHost(site), f.Key, "message=unmistakable-value-4711", nil); w.Code != 502 {
		t.Fatalf("SMTP failure = %d, want 502", w.Code)
	}
	if !strings.Contains(logs.String(), "form mail not sent") {
		t.Fatalf("the failure was not logged:\n%s", &logs)
	}
	// Neither the recipient (the SMTP server quoted it) nor what was typed.
	for _, secret := range []string{"office@example.com", "unmistakable-value-4711"} {
		if strings.Contains(logs.String(), secret) {
			t.Errorf("the log holds %q:\n%s", secret, &logs)
		}
	}
}

func TestThanksPageGoesBackToTheForm(t *testing.T) {
	e, _ := formsEnv(t, nil)
	site, f := activeForm(t, e, store.Form{})
	w := get(t, e, viewHost(site), "/_sitebin/forms/"+f.Key+"/thanks", map[string]string{"Referer": "http://" + viewHost(site) + "/kontakt.html"})
	if w.Code != 200 || !strings.Contains(w.Body.String(), "Thank you") || !strings.Contains(w.Body.String(), `href="/kontakt.html"`) {
		t.Fatalf("thanks = %d %s", w.Code, w.Body)
	}
	w = get(t, e, viewHost(site), "/_sitebin/forms/"+f.Key+"/thanks", map[string]string{"Referer": "https://evil.example/x"})
	if !strings.Contains(w.Body.String(), `href="/"`) {
		t.Fatalf("a foreign Referer must not become the Back link: %s", w.Body)
	}
}

func TestAltchaScriptServed(t *testing.T) {
	e, _ := formsEnv(t, nil)
	site, _ := activeForm(t, e, store.Form{})
	w := get(t, e, viewHost(site), "/_sitebin/altcha.js", nil)
	if w.Code != 200 || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/javascript") || w.Body.String() != "// altcha" {
		t.Fatalf("altcha.js = %d %q %q", w.Code, w.Header().Get("Content-Type"), w.Body)
	}
}
