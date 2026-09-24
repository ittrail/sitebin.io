package httpapi

import (
	"io"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/ittrail/sitebin.io/internal/forms"
	"github.com/ittrail/sitebin.io/internal/store"
)

func consent(t *testing.T, e *env, method, path, tok string, oneClick bool) *httptest.ResponseRecorder {
	t.Helper()
	target := path
	var body io.Reader
	switch {
	case method == "GET":
		target += "?t=" + url.QueryEscape(tok)
	case oneClick: // RFC 8058: the token is in the List-Unsubscribe URL
		target += "?t=" + url.QueryEscape(tok)
		body = strings.NewReader("List-Unsubscribe=One-Click")
	default:
		body = strings.NewReader("t=" + url.QueryEscape(tok))
	}
	req := httptest.NewRequest(method, target, body)
	if body != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	return e.public(t, req)
}

func pendingForm(t *testing.T, e *env) (*store.Site, store.Form) {
	t.Helper()
	c := e.createSite(t, nil, map[string]string{"index.html": "hi"})
	site, _ := e.st.ByViewID(c.ID)
	f, err := e.st.AddForm(site, store.Form{Name: "Contact", Recipient: "office@example.com"}, 10)
	if err != nil {
		t.Fatal(err)
	}
	return site, f
}

func confirmTok(e *env, site *store.Site, f store.Form) string {
	return e.api.forms.links.ConfirmToken(forms.ConfirmClaim{ViewID: site.ViewID, Key: f.Key, Recipient: f.Recipient, Seq: f.Seq}, time.Now())
}

func stopTok(e *env, site *store.Site, key, addr string) string {
	return e.api.forms.links.StopToken(forms.StopClaim{ViewID: site.ViewID, Key: key, Recipient: addr}, time.Now())
}

func formStatus(t *testing.T, e *env, site *store.Site, key string) store.Form {
	t.Helper()
	s, _ := e.st.ByViewID(site.ViewID)
	f, _, ok := store.FindForm(s.Meta, key)
	if !ok {
		t.Fatalf("form %s is gone", key)
	}
	return f
}

func TestConfirmFlow(t *testing.T) {
	e, _ := formsEnv(t, nil)
	site, f := pendingForm(t, e)
	tok := confirmTok(e, site, f)
	w := consent(t, e, "GET", "/forms/confirm", tok, false)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `action="/forms/confirm"`) {
		t.Fatalf("GET = %d %s", w.Code, w.Body)
	}
	if formStatus(t, e, site, f.Key).Status != store.FormPending {
		t.Fatal("GET confirmed the form: a mail scanner would do the same")
	}
	w = consent(t, e, "POST", "/forms/confirm", tok, false)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "Confirmed") || formStatus(t, e, site, f.Key).Status != store.FormActive {
		t.Fatalf("POST = %d %s", w.Code, w.Body)
	}
	if w = consent(t, e, "POST", "/forms/confirm", tok, false); w.Code != 200 {
		t.Fatalf("a second click = %d, want 200", w.Code)
	}
}

// Review Focus 4.
func TestConfirmAfterDeleteOrRecipientChange(t *testing.T) {
	e, _ := formsEnv(t, nil)
	site, f := pendingForm(t, e)
	tok := confirmTok(e, site, f)
	to := "new@example.com"
	e.st.UpdateForm(site, f.Key, store.FormPatch{Recipient: &to})
	w := consent(t, e, "POST", "/forms/confirm", tok, false)
	if w.Code != 410 || !strings.Contains(w.Body.String(), "no longer valid") {
		t.Fatalf("old link after a recipient change = %d %s", w.Code, w.Body)
	}
	if g := formStatus(t, e, site, f.Key); g.Status != store.FormPending || g.Recipient != to {
		t.Fatalf("the old link changed the form: %+v", g)
	}

	site2, g := pendingForm(t, e)
	tok2 := confirmTok(e, site2, g)
	e.st.DeleteForm(site2, g.Key)
	for _, m := range []string{"GET", "POST"} {
		if w := consent(t, e, m, "/forms/confirm", tok2, false); w.Code != 410 || !strings.Contains(w.Body.String(), "no longer exists") {
			t.Fatalf("%s after delete = %d %s", m, w.Code, w.Body)
		}
	}
}

func TestConfirmRefusesBadTokens(t *testing.T) {
	e, _ := formsEnv(t, nil)
	site, f := pendingForm(t, e)
	if w := consent(t, e, "GET", "/forms/confirm", "garbage", false); w.Code != 400 {
		t.Errorf("garbage = %d", w.Code)
	}
	if w := consent(t, e, "POST", "/forms/confirm", stopTok(e, site, f.Key, f.Recipient), false); w.Code != 400 {
		t.Errorf("a stop token used to confirm = %d", w.Code)
	}
}

func TestStopFlow(t *testing.T) {
	e, _ := formsEnv(t, nil)
	site, f := activeForm(t, e, store.Form{})
	tok := stopTok(e, site, f.Key, f.Recipient)
	w := consent(t, e, "GET", "/forms/stop", tok, false)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `action="/forms/stop"`) || formStatus(t, e, site, f.Key).Status != store.FormActive {
		t.Fatalf("GET = %d, and it must change nothing", w.Code)
	}
	w = consent(t, e, "POST", "/forms/stop", tok, false)
	if w.Code != 200 || formStatus(t, e, site, f.Key).Status != store.FormStopped {
		t.Fatalf("POST = %d %s", w.Code, w.Body)
	}
	if w := submit(t, e, viewHost(site), f.Key, "message=hi", nil); w.Code != 403 {
		t.Fatalf("submission after stop = %d, want 403", w.Code)
	}
}

func TestStopOneClick(t *testing.T) {
	e, _ := formsEnv(t, nil)
	site, f := activeForm(t, e, store.Form{})
	w := consent(t, e, "POST", "/forms/stop", stopTok(e, site, f.Key, f.Recipient), true)
	if w.Code != 200 || formStatus(t, e, site, f.Key).Status != store.FormStopped {
		t.Fatalf("one-click = %d %s", w.Code, w.Body)
	}
}

func TestStopLinkOfAFormerRecipientChangesNothing(t *testing.T) {
	e, _ := formsEnv(t, nil)
	site, f := activeForm(t, e, store.Form{})
	old := stopTok(e, site, f.Key, f.Recipient)
	to := "new@example.com"
	g, _, _ := e.st.UpdateForm(site, f.Key, store.FormPatch{Recipient: &to})
	e.st.ConfirmForm(site, f.Key, to, g.Seq)
	if w := consent(t, e, "POST", "/forms/stop", old, false); w.Code != 200 {
		t.Fatalf("old stop link = %d", w.Code)
	}
	if h := formStatus(t, e, site, f.Key); h.Status != store.FormActive || h.Recipient != to {
		t.Fatalf("a former recipient's link stopped the new one: %+v", h)
	}
}

// Fix round 1: a stop link for a form the owner already deleted must not read
// back an empty-quoted form name.
func TestStopAfterFormDeletedReadsCleanly(t *testing.T) {
	e, _ := formsEnv(t, nil)
	site, f := activeForm(t, e, store.Form{})
	tok := stopTok(e, site, f.Key, f.Recipient)
	if err := e.st.DeleteForm(site, f.Key); err != nil {
		t.Fatal(err)
	}
	w := consent(t, e, "POST", "/forms/stop", tok, false)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "Stopped") {
		t.Fatalf("stop after delete = %d %s", w.Code, w.Body)
	}
	if strings.Contains(w.Body.String(), "“”") {
		t.Errorf("body reads an empty-quoted form name: %s", w.Body)
	}
}

func TestConsentPagesNeedForms(t *testing.T) {
	e := newEnv(t, nil)
	if w := consent(t, e, "GET", "/forms/confirm", "x", false); w.Code != 404 {
		t.Fatalf("confirm page with forms off = %d", w.Code)
	}
}
