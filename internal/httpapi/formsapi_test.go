package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/ittrail/sitebin.io/internal/ext"
	"github.com/ittrail/sitebin.io/internal/store"
)

type formsResp struct {
	Enabled  bool     `json:"enabled"`
	Limit    int      `json:"limit"`
	Used     int      `json:"used"`
	MaxFiles int      `json:"max_files"`
	Warnings []string `json:"warnings"`
	Forms    []struct {
		Key, Name, Recipient, Redirect, Status, Snippet string
		Captcha, Files                                  bool
	} `json:"forms"`
}

func (e *env) formsCall(t *testing.T, method, editID, pw, path string, body any) (*httptest.ResponseRecorder, formsResp) {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, "/api/sites/"+editID+"/forms"+path, rdr)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Sec-Fetch-Site", "same-origin") // the edit page's own fetch
	w := e.public(t, authed(req, pw))
	var out formsResp
	json.Unmarshal(w.Body.Bytes(), &out)
	return w, out
}

func newFormSite(t *testing.T, e *env) (editID, pw, viewID string) {
	t.Helper()
	c := e.createSite(t, nil, map[string]string{"index.html": "<h1>hi</h1>"})
	return editIDFrom(t, c.EditURL), c.EditPassword, c.ID
}

func TestFormsOffOnTheInstance(t *testing.T) {
	e := newEnv(t, nil)
	id, pw, _ := newFormSite(t, e)
	w, out := e.formsCall(t, "GET", id, pw, "", nil)
	if w.Code != 200 || out.Enabled {
		t.Fatalf("GET = %d enabled=%v, want 200 and enabled=false", w.Code, out.Enabled)
	}
	w, _ = e.formsCall(t, "POST", id, pw, "", map[string]any{"name": "Contact", "recipient": "a@example.com"})
	if w.Code != 409 {
		t.Fatalf("POST with forms off = %d, want 409", w.Code)
	}
}

// TestFormsOffRefusesEveryWrite: every write route — not just create — must
// answer 409 while the instance has no forms, including DELETE. A form that
// predates forms being switched off (or was added directly in the store, as
// here) must still be refused, and left untouched, by every one of them.
func TestFormsOffRefusesEveryWrite(t *testing.T) {
	e := newEnv(t, nil)
	id, pw, viewID := newFormSite(t, e)
	site, err := e.st.ByViewID(viewID)
	if err != nil {
		t.Fatal(err)
	}
	f, err := e.st.AddForm(site, store.Form{Name: "A", Recipient: "a@example.com"}, 10)
	if err != nil {
		t.Fatal(err)
	}

	if w, _ := e.formsCall(t, "DELETE", id, pw, "/"+f.Key, nil); w.Code != 409 {
		t.Errorf("DELETE with forms off = %d, want 409", w.Code)
	}
	if w, _ := e.formsCall(t, "PUT", id, pw, "/"+f.Key, map[string]any{"name": "Renamed"}); w.Code != 409 {
		t.Errorf("PUT with forms off = %d, want 409", w.Code)
	}
	if w, _ := e.formsCall(t, "POST", id, pw, "/"+f.Key+"/confirmation", nil); w.Code != 409 {
		t.Errorf("resend confirmation with forms off = %d, want 409", w.Code)
	}

	site, _ = e.st.ByViewID(viewID)
	if _, _, ok := store.FindForm(site.Meta, f.Key); !ok {
		t.Error("a refused write deleted the form")
	}
}

func TestAddFormSendsAConfirmation(t *testing.T) {
	e, rs := formsEnv(t, nil)
	id, pw, viewID := newFormSite(t, e)
	w, out := e.formsCall(t, "POST", id, pw, "", map[string]any{"name": "Contact", "recipient": "office@example.com", "captcha": true, "redirect": "/danke.html"})
	if w.Code != 201 {
		t.Fatalf("POST = %d %s", w.Code, w.Body)
	}
	if len(out.Forms) != 1 || out.Forms[0].Status != "pending" || !out.Forms[0].Captcha || out.Forms[0].Redirect != "/danke.html" {
		t.Fatalf("forms = %+v", out.Forms)
	}
	key := out.Forms[0].Key
	if !strings.Contains(out.Forms[0].Snippet, `action="/_sitebin/forms/`+key+`"`) || !strings.Contains(out.Forms[0].Snippet, "altcha-widget") {
		t.Errorf("snippet = %s", out.Forms[0].Snippet)
	}
	m := rs.last(t)
	if m.To != "office@example.com" || m.From != "forms@sitebin.example" {
		t.Errorf("confirmation envelope %s -> %s", m.From, m.To)
	}
	tok, _ := url.QueryUnescape(confirmToken(t, m))
	c, ok := e.api.forms.links.ParseConfirm(tok, time.Now())
	if !ok || c.ViewID != viewID || c.Key != key || c.Recipient != "office@example.com" || c.Seq != 0 {
		t.Errorf("confirmation token = %+v %v", c, ok)
	}
}

func TestAddFormValidates(t *testing.T) {
	e, rs := formsEnv(t, nil)
	id, pw, _ := newFormSite(t, e)
	for _, body := range []map[string]any{
		{"recipient": "a@example.com"},
		{"name": "Contact"},
		{"name": "Contact\r\nBcc: x", "recipient": "a@example.com"},
		{"name": "Contact", "recipient": "Office <a@example.com>"},
		{"name": "Contact", "recipient": "a@example.com", "redirect": "https://evil.example/"},
	} {
		if w, _ := e.formsCall(t, "POST", id, pw, "", body); w.Code != 400 {
			t.Errorf("%v: %d, want 400", body, w.Code)
		}
	}
	if rs.count() != 0 {
		t.Error("an invalid form mailed someone")
	}
}

func TestFormsCapCommunityDefault(t *testing.T) {
	e, _ := formsEnv(t, nil)
	id, pw, _ := newFormSite(t, e)
	for i := 0; i < 10; i++ {
		if w, _ := e.formsCall(t, "POST", id, pw, "", map[string]any{"name": "F", "recipient": fmt.Sprintf("r%d@example.com", i)}); w.Code != 201 {
			t.Fatalf("form %d: %d %s", i+1, w.Code, w.Body)
		}
	}
	w, _ := e.formsCall(t, "POST", id, pw, "", map[string]any{"name": "F", "recipient": "r10@example.com"})
	if w.Code != 403 || !strings.Contains(w.Body.String(), "10 form") {
		t.Fatalf("11th form = %d %s, want 403 naming the cap of 10", w.Code, w.Body)
	}
}

func TestFormsCapFromTheEnvironment(t *testing.T) {
	e, _ := formsEnv(t, map[string]string{"SITEBIN_FORMS_MAX_PER_SITE": "1"})
	id, pw, _ := newFormSite(t, e)
	e.formsCall(t, "POST", id, pw, "", map[string]any{"name": "A", "recipient": "a@example.com"})
	if w, _ := e.formsCall(t, "POST", id, pw, "", map[string]any{"name": "B", "recipient": "b@example.com"}); w.Code != 403 {
		t.Fatalf("second form with SITEBIN_FORMS_MAX_PER_SITE=1 = %d", w.Code)
	}
}

// With a provider, a site without a stamped cap has none. Without this rule
// every Drop and Free site on the hosted instance would gain 10 forms the day
// the feature ships.
func TestFormsCapIsZeroForAnUnstampedSiteWithAProvider(t *testing.T) {
	e, _ := formsEnv(t, nil)
	// Trusted, so the 0 comes from the missing stamp and not from the trust rule.
	ext.Register(&fakeProvider{enabled: true, grant: ext.CreateGrant{Trusted: true}})
	defer ext.Reset()
	id, pw, _ := newFormSite(t, e)
	w, out := e.formsCall(t, "GET", id, pw, "", nil)
	if w.Code != 200 || out.Limit != 0 {
		t.Fatalf("GET = %d limit=%d, want limit 0", w.Code, out.Limit)
	}
	if w, _ := e.formsCall(t, "POST", id, pw, "", map[string]any{"name": "A", "recipient": "a@example.com"}); w.Code != 403 {
		t.Fatalf("POST = %d, want 403", w.Code)
	}
}

// An untrusted site is served with form-action 'none' and connect-src 'self':
// a plain HTML form on it cannot post, and the only thing a form would still
// serve is a phishing drop's own fetch. With accounts enabled, a site without
// the trust marker has no forms, whatever its stamp says.
func TestFormsNeedATrustedSiteWithAccounts(t *testing.T) {
	e, rs := formsEnv(t, nil)
	ext.Register(&fakeProvider{enabled: true, owner: "acct-1", grant: ext.CreateGrant{MaxForms: intp(1), Trusted: false}})
	defer ext.Reset()
	id, pw, viewID := newFormSite(t, e)
	site, _ := e.st.ByViewID(viewID)
	if e.st.Trusted(site) || site.Meta.QuotaForms == nil || *site.Meta.QuotaForms != 1 {
		t.Fatal("precondition: an owned site stamped with 1 form and no trust marker")
	}
	w, _ := e.formsCall(t, "POST", id, pw, "", map[string]any{"name": "A", "recipient": "a@example.com"})
	if w.Code != 403 || !strings.Contains(w.Body.String(), "includes no forms") {
		t.Fatalf("add on an untrusted site = %d %s, want 403 naming no forms", w.Code, w.Body)
	}
	if _, out := e.formsCall(t, "GET", id, pw, "", nil); out.Limit != 0 {
		t.Errorf("listing limit = %d, want 0 on an untrusted site", out.Limit)
	}

	// A form that got in anyway (through the store here; a tier that lost its
	// trust in real life) is paused, for a plain post and a script's alike.
	f, err := e.st.AddForm(site, store.Form{Name: "A", Recipient: "a@example.com", Captcha: true}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if f, err = e.st.ConfirmForm(site, f.Key, f.Recipient, f.Seq); err != nil {
		t.Fatal(err)
	}
	if w := submit(t, e, viewHost(site), f.Key, "message=hi", nil); w.Code != 403 || !strings.Contains(w.Body.String(), "paused") {
		t.Errorf("submit on an untrusted site = %d, want 403 paused", w.Code)
	}
	if w := submit(t, e, viewHost(site), f.Key, "message=hi", map[string]string{"Accept": "application/json"}); w.Code != 403 {
		t.Errorf("JSON submit on an untrusted site = %d, want 403", w.Code)
	}
	if w := get(t, e, viewHost(site), "/_sitebin/forms/"+f.Key+"/challenge", nil); w.Code != 404 {
		t.Errorf("challenge on an untrusted site = %d, want 404", w.Code)
	}
	if rs.count() != 0 {
		t.Errorf("an untrusted site sent %d mails", rs.count())
	}
}

func TestFormsOnATrustedSiteWithAccounts(t *testing.T) {
	e, _ := formsEnv(t, nil)
	ext.Register(&fakeProvider{enabled: true, owner: "acct-1", grant: ext.CreateGrant{MaxForms: intp(1), Trusted: true}})
	defer ext.Reset()
	id, pw, _ := newFormSite(t, e)
	if w, _ := e.formsCall(t, "POST", id, pw, "", map[string]any{"name": "A", "recipient": "a@example.com"}); w.Code != 201 {
		t.Fatalf("add on a trusted site = %d %s, want 201", w.Code, w.Body)
	}
}

// A Pro site created before forms existed has no stamp; its plan is asked once.
func TestFormsCapIsStampedFromThePlanWhenFirstNeeded(t *testing.T) {
	e, _ := formsEnv(t, nil)
	fp := &fakeProvider{enabled: true, owner: "acct-1", grant: ext.CreateGrant{Trusted: true}, quota: ext.CreateGrant{MaxForms: intp(1)}, quotaOK: true}
	ext.Register(fp)
	defer ext.Reset()
	id, pw, viewID := newFormSite(t, e)
	site, _ := e.st.ByViewID(viewID)
	if site.Meta.QuotaForms != nil {
		t.Fatal("precondition: the grant carried no MaxForms, so nothing is stamped at creation")
	}
	_, out := e.formsCall(t, "GET", id, pw, "", nil)
	if out.Limit != 1 {
		t.Fatalf("listing limit = %d, want the plan's 1", out.Limit)
	}
	site, _ = e.st.ByViewID(viewID)
	if site.Meta.QuotaForms == nil || *site.Meta.QuotaForms != 1 {
		t.Fatalf("QuotaForms = %v after the first listing, want 1 stamped", site.Meta.QuotaForms)
	}
	e.formsCall(t, "POST", id, pw, "", map[string]any{"name": "A", "recipient": "a@example.com"})
	if w, _ := e.formsCall(t, "POST", id, pw, "", map[string]any{"name": "B", "recipient": "b@example.com"}); w.Code != 403 {
		t.Fatalf("second form on a 1-form plan = %d", w.Code)
	}
}

func TestFormsPlanLookupErrorRefusesTheAdd(t *testing.T) {
	e, rs := formsEnv(t, nil)
	ext.Register(&fakeProvider{enabled: true, owner: "acct-1", grant: ext.CreateGrant{Trusted: true}, quotaErr: errors.New("paygate down")})
	defer ext.Reset()
	id, pw, viewID := newFormSite(t, e)
	if w, _ := e.formsCall(t, "POST", id, pw, "", map[string]any{"name": "A", "recipient": "a@example.com"}); w.Code != 503 {
		t.Fatalf("POST = %d, want 503 while the plan is unknown", w.Code)
	}
	site, _ := e.st.ByViewID(viewID)
	if site.Meta.QuotaForms != nil || len(site.Meta.Forms) != 0 || rs.count() != 0 {
		t.Error("a failed plan lookup changed something")
	}
}

func TestUpdateFormRecipientConfirmsAgain(t *testing.T) {
	e, rs := formsEnv(t, nil)
	id, pw, viewID := newFormSite(t, e)
	_, out := e.formsCall(t, "POST", id, pw, "", map[string]any{"name": "A", "recipient": "a@example.com"})
	key := out.Forms[0].Key
	site, _ := e.st.ByViewID(viewID)
	e.st.ConfirmForm(site, key, "a@example.com", 0)

	w, out := e.formsCall(t, "PUT", id, pw, "/"+key, map[string]any{"name": "Renamed"})
	if w.Code != 200 || out.Forms[0].Status != "active" || out.Forms[0].Name != "Renamed" || rs.count() != 1 {
		t.Fatalf("rename: %d %+v mails=%d", w.Code, out.Forms, rs.count())
	}
	w, out = e.formsCall(t, "PUT", id, pw, "/"+key, map[string]any{"recipient": "b@example.com"})
	if w.Code != 200 || out.Forms[0].Status != "pending" || rs.count() != 2 || rs.last(t).To != "b@example.com" {
		t.Fatalf("new recipient: %d %+v mails=%d", w.Code, out.Forms, rs.count())
	}
}

func TestDeleteForm(t *testing.T) {
	e, _ := formsEnv(t, nil)
	id, pw, _ := newFormSite(t, e)
	_, out := e.formsCall(t, "POST", id, pw, "", map[string]any{"name": "A", "recipient": "a@example.com"})
	if w, _ := e.formsCall(t, "DELETE", id, pw, "/"+out.Forms[0].Key, nil); w.Code != 204 {
		t.Fatalf("DELETE = %d", w.Code)
	}
	if _, out = e.formsCall(t, "GET", id, pw, "", nil); len(out.Forms) != 0 {
		t.Errorf("forms after delete = %+v", out.Forms)
	}
	if w, _ := e.formsCall(t, "DELETE", id, pw, "/nosuchkey", nil); w.Code != 404 {
		t.Errorf("DELETE unknown = %d", w.Code)
	}
}

func TestResendConfirmation(t *testing.T) {
	e, rs := formsEnv(t, nil)
	id, pw, viewID := newFormSite(t, e)
	_, out := e.formsCall(t, "POST", id, pw, "", map[string]any{"name": "A", "recipient": "a@example.com"})
	key := out.Forms[0].Key
	if w, _ := e.formsCall(t, "POST", id, pw, "/"+key+"/confirmation", nil); w.Code != 202 || rs.count() != 2 {
		t.Fatalf("resend pending = %d mails=%d", w.Code, rs.count())
	}
	site, _ := e.st.ByViewID(viewID)
	e.st.ConfirmForm(site, key, "a@example.com", 0)
	if w, _ := e.formsCall(t, "POST", id, pw, "/"+key+"/confirmation", nil); w.Code != 409 {
		t.Fatalf("resend active = %d, want 409", w.Code)
	}
}

func TestConfirmationMailsAreThrottledPerAddress(t *testing.T) {
	e, rs := formsEnv(t, nil)
	id, pw, _ := newFormSite(t, e)
	for i := 0; i < 3; i++ {
		if w, _ := e.formsCall(t, "POST", id, pw, "", map[string]any{"name": "F", "recipient": "same@example.com"}); w.Code != 201 {
			t.Fatalf("form %d = %d", i+1, w.Code)
		}
	}
	w, _ := e.formsCall(t, "POST", id, pw, "", map[string]any{"name": "F", "recipient": "same@example.com"})
	if w.Code != 429 || rs.count() != 3 {
		t.Fatalf("4th mail to one address today = %d (mails %d), want 429 and nothing sent", w.Code, rs.count())
	}
	_, out := e.formsCall(t, "GET", id, pw, "", nil)
	if len(out.Forms) != 3 {
		t.Errorf("a throttled add still created a form: %d forms", len(out.Forms))
	}
}

func TestConfirmationMailFailureIsAWarning(t *testing.T) {
	e, rs := formsEnv(t, nil)
	rs.err = errors.New("dial tcp: connection refused")
	id, pw, _ := newFormSite(t, e)
	w, out := e.formsCall(t, "POST", id, pw, "", map[string]any{"name": "A", "recipient": "a@example.com"})
	if w.Code != 201 || len(out.Warnings) == 0 || out.Forms[0].Status != "pending" {
		t.Fatalf("POST = %d warnings=%v forms=%+v", w.Code, out.Warnings, out.Forms)
	}
}

func TestFormsAttachmentsNeedTheInstanceToAllowThem(t *testing.T) {
	e, _ := formsEnv(t, map[string]string{"SITEBIN_FORMS_MAX_FILES": "0"})
	id, pw, _ := newFormSite(t, e)
	if w, _ := e.formsCall(t, "POST", id, pw, "", map[string]any{"name": "A", "recipient": "a@example.com", "files": true}); w.Code != 400 {
		t.Fatalf("files on an instance without attachments = %d, want 400", w.Code)
	}
}

func TestFormsBeyondTheCapArePaused(t *testing.T) {
	e, _ := formsEnv(t, nil)
	id, pw, viewID := newFormSite(t, e)
	e.formsCall(t, "POST", id, pw, "", map[string]any{"name": "A", "recipient": "a@example.com"})
	e.formsCall(t, "POST", id, pw, "", map[string]any{"name": "B", "recipient": "b@example.com"})
	site, _ := e.st.ByViewID(viewID)
	e.st.Update(site, func(m *store.Meta) error { m.QuotaForms = intp(1); return nil }) // a downgrade
	_, out := e.formsCall(t, "GET", id, pw, "", nil)
	if out.Forms[0].Status != "pending" || out.Forms[1].Status != "paused" || out.Limit != 1 || out.Used != 2 {
		t.Fatalf("after a downgrade: %+v limit %d used %d", out.Forms, out.Limit, out.Used)
	}
}

func TestFormsSnippetOnPathViews(t *testing.T) {
	e, _ := formsEnv(t, map[string]string{"SITEBIN_VIEW_ACCESS": "path"})
	id, pw, viewID := newFormSite(t, e)
	_, out := e.formsCall(t, "POST", id, pw, "", map[string]any{"name": "A", "recipient": "a@example.com"})
	if !strings.Contains(out.Forms[0].Snippet, "?_site="+viewID) {
		t.Errorf("path-view snippet lacks the site: %s", out.Forms[0].Snippet)
	}
}
