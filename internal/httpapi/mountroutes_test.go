package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ittrail/sitebin.io/internal/auth"
	"github.com/ittrail/sitebin.io/internal/ext"
	"github.com/ittrail/sitebin.io/internal/store"
)

// Sitebin's own routes under a custom domain with a path: Caddy hands
// <mount>/_sitebin/* to the backend with the mount, and the backend resolves
// the site from host + mount.

func mountFormsEnv(t *testing.T) (*env, *recSender) {
	t.Helper()
	ext.Register(&fakeProvider{domainsOK: true})
	t.Cleanup(ext.Reset)
	return formsEnv(t, nil)
}

// confirmedForm adds a confirmed form to site.
func confirmedForm(t *testing.T, e *env, site *store.Site, recipient string) store.Form {
	t.Helper()
	f, err := e.st.AddForm(site, store.Form{Name: "Contact", Recipient: recipient}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if f, err = e.st.ConfirmForm(site, f.Key, f.Recipient, f.Seq); err != nil {
		t.Fatal(err)
	}
	return f
}

func mounted(mount string) map[string]string { return map[string]string{mountHeader: mount} }

func TestFormUnderMountReachesMountedSite(t *testing.T) {
	e, rs := mountFormsEnv(t)
	a := e.ownedSite(t, "u1", map[string]string{"index.html": "root"}, "m.at")
	b := e.ownedSite(t, "u1", map[string]string{"index.html": "k1"}, "m.at/demos/k1")
	confirmedForm(t, e, a, "root@example.com")
	fb := confirmedForm(t, e, b, "demo@example.com")

	w := submit(t, e, "m.at", fb.Key, "name=Anna&message=Hallo", mounted("/demos/k1"))
	if w.Code != 303 || w.Header().Get("Location") != "/demos/k1/_sitebin/forms/"+fb.Key+"/thanks" {
		t.Fatalf("submit under the path = %d Location %q: %s", w.Code, w.Header().Get("Location"), w.Body)
	}
	if m := rs.last(t); m.To != "demo@example.com" {
		t.Fatalf("mailed to %s, want the mounted site's recipient", m.To)
	}
	// Without the mount the key is looked up on the root site, which has no
	// such form.
	if w := submit(t, e, "m.at", fb.Key, "name=Anna&message=Hallo", nil); w.Code != 404 {
		t.Fatalf("the mounted site's key on the root = %d, want 404", w.Code)
	}
	// An owner's own thank-you path is the site's, so it lives under the mount.
	if err := e.st.Update(b, func(m *store.Meta) error { m.Forms[0].Redirect = "/danke/"; return nil }); err != nil {
		t.Fatal(err)
	}
	if w := submit(t, e, "m.at", fb.Key, "name=Anna&message=Hallo", mounted("/demos/k1")); w.Header().Get("Location") != "/demos/k1/danke/" {
		t.Fatalf("own thank-you path under the mount: %q", w.Header().Get("Location"))
	}
}

func TestForgedMountGainsNothing(t *testing.T) {
	e, _ := mountFormsEnv(t)
	b := e.ownedSite(t, "u1", map[string]string{"index.html": "k1"}, "m.at/demos/k1")
	fb := confirmedForm(t, e, b, "demo@example.com")
	for _, tc := range []struct{ host, mount string }{
		{"m.at", "/nope"},
		{"x.at", "/demos/k1"},
		{"m.at", "/demos/k1/../k1"},
		{"m.at", "demos/k1"},
		{b.ViewID + ".sitebin.example", "/demos/k1"},
	} {
		if w := submit(t, e, tc.host, fb.Key, "name=A&message=B", mounted(tc.mount)); w.Code != 404 {
			t.Errorf("host %s mount %q = %d, want 404", tc.host, tc.mount, w.Code)
		}
	}
}

func TestUnlockUnderMountScopesCookie(t *testing.T) {
	e := mountEnv(t)
	b := e.ownedSite(t, "u1", map[string]string{"index.html": "k1"}, "m.at/demos/k1")
	if err := e.st.Update(b, func(m *store.Meta) error {
		m.ViewPasswordProtected, m.ViewPasswordHash = true, auth.HashPassword("pw")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	w := post(t, e, "m.at", "/_sitebin/unlock", urlenc, strings.NewReader("password=pw&redirect=/demos/k1/x"), mounted("/demos/k1"))
	if w.Code != 303 || w.Header().Get("Location") != "/demos/k1/x" {
		t.Fatalf("unlock = %d %q: %s", w.Code, w.Header().Get("Location"), w.Body)
	}
	c := w.Result().Cookies()
	if len(c) != 1 || c[0].Path != "/demos/k1/" {
		t.Fatalf("cookie = %+v, want one scoped to /demos/k1/", c)
	}
	// A wrong password shows the gate again, posting under the path.
	w = post(t, e, "m.at", "/_sitebin/unlock", urlenc, strings.NewReader("password=no&redirect=/demos/k1/"), mounted("/demos/k1"))
	if w.Code != 401 || !strings.Contains(w.Body.String(), `action="/demos/k1/_sitebin/unlock"`) {
		t.Fatalf("wrong password = %d:\n%s", w.Code, w.Body)
	}
}

func TestViewAccessWithTwoCookies(t *testing.T) {
	e := mountEnv(t)
	a := e.ownedSite(t, "u1", nil, "m.at")
	b := e.ownedSite(t, "u1", nil, "m.at/demos/k1")
	tok := func(s *store.Site) string { return e.api.signer.Sign(tokenSubject(s), time.Now(), time.Hour) }
	req := httptest.NewRequest("GET", "/", nil)
	// The browser sends the longer path's cookie first, then the root's.
	req.AddCookie(&http.Cookie{Name: viewCookieName, Value: tok(b)})
	req.AddCookie(&http.Cookie{Name: viewCookieName, Value: tok(a)})
	if !e.api.hasViewAccess(req, b) || !e.api.hasViewAccess(req, a) {
		t.Fatal("two unlocked sites on one host must both stay unlocked")
	}
	other := e.ownedSite(t, "u1", nil, "m.at/other")
	if e.api.hasViewAccess(req, other) {
		t.Fatal("a cookie of another site must not unlock this one")
	}
}

func TestCSPReportAttributedByDocumentPath(t *testing.T) {
	e := mountEnv(t)
	a := e.ownedSite(t, "u1", map[string]string{"index.html": "root"}, "m.at")
	b := e.ownedSite(t, "u1", map[string]string{"index.html": "k1"}, "m.at/demos/k1")
	e.api.csp.now = func() time.Time { return time.Now() }
	cspReportFor(t, e, "m.at", "https://api.emailjs.com/x", "https://m.at/demos/k1/page.html")
	e.api.csp.now = func() time.Time { return time.Now().Add(2 * time.Hour) }
	cspReportFor(t, e, "m.at", "https://api.emailjs.com/x", "https://m.at/demos/k1/page.html")
	if st := e.st.Stats(b); st.CSPViolations == 0 {
		t.Fatal("the report about a page under the path was not attributed to the mounted site")
	}
	if st := e.st.Stats(a); st.CSPViolations != 0 {
		t.Fatal("the root site was charged with the mounted site's report")
	}
}

func TestSiteByURLWithPath(t *testing.T) {
	e := mountEnv(t)
	e.ownedSite(t, "u1", nil, "m.at")
	b := e.ownedSite(t, "u1", nil, "m.at/demos/k1")
	if got := e.api.siteByURL("https://m.at/demos/k1/x"); got == nil || got.ViewID != b.ViewID {
		t.Fatalf("siteByURL = %v, want the mounted site", got)
	}
}
