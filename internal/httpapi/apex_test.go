package httpapi

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ittrail/sitebin.io/internal/ext"
	"github.com/ittrail/sitebin.io/internal/ids"
	"github.com/ittrail/sitebin.io/internal/store"
)

func apexEnv(t *testing.T, over map[string]string) *env {
	t.Helper()
	vars := map[string]string{
		"SITEBIN_BASE_DOMAIN":      "app.sitebin.example",
		"SITEBIN_VIEW_DOMAIN":      "sitebin-user.example",
		"SITEBIN_ABUSE_CONTACT":    "abuse@sitebin.example",
		"SITEBIN_ABUSE_REPORT_URL": "https://app.sitebin.example/report",
		"SITEBIN_HOME_URL":         "https://sitebin.example",
	}
	for k, v := range over {
		vars[k] = v
	}
	return newEnv(t, vars)
}

func onHost(e *env, t *testing.T, method, host, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	req.Host = host
	w := httptest.NewRecorder()
	e.api.Public().ServeHTTP(w, req)
	return w
}

// The view domain's apex and www say whose domain it is and how to report a
// site on it — static, noindex, nothing loaded from anywhere.
func TestViewApexInfoPage(t *testing.T) {
	e := apexEnv(t, nil)
	for _, host := range []string{"sitebin-user.example", "www.sitebin-user.example", "SITEBIN-USER.example:443"} {
		w := onHost(e, t, "GET", host, "/")
		if w.Code != 200 {
			t.Fatalf("%s: GET / = %d %s", host, w.Code, w.Body)
		}
		body := w.Body.String()
		for _, want := range []string{
			"app.sitebin.example",
			`href="mailto:abuse@sitebin.example"`,
			`href="https://app.sitebin.example/report"`,
			`href="https://sitebin.example"`,
			`<meta name="robots" content="noindex">`,
			"third part",
		} {
			if !strings.Contains(body, want) {
				t.Errorf("%s: info page lacks %q", host, want)
			}
		}
		for _, bad := range []string{"<script", "<link", "src=", "@import", "url("} {
			if strings.Contains(body, bad) {
				t.Errorf("%s: info page loads something (%q)", host, bad)
			}
		}
		if got := w.Header().Get("X-Robots-Tag"); got != "noindex" {
			t.Errorf("X-Robots-Tag = %q", got)
		}
		if csp := w.Header().Get("Content-Security-Policy"); !strings.HasPrefix(csp, "default-src 'none'") {
			t.Errorf("CSP = %q", csp)
		}
	}
}

// The app never answers on the view domain: its routes are the base
// domain's, and nothing but the info page and security.txt lives here.
func TestViewApexAnswersNothingElse(t *testing.T) {
	ext.Reset()
	t.Cleanup(ext.Reset)
	ext.Register(&fakeProvider{enabled: true})
	e := apexEnv(t, nil)
	for _, c := range []struct{ method, path string }{
		{"GET", "/account"},
		{"POST", "/api/sites"},
		{"GET", "/e/" + ids.NewEditID()},
		{"POST", "/mcp"},
		{"GET", "/.well-known/oauth-protected-resource/mcp"},
		{"GET", "/_sitebin/assets/static/app.css"},
		{"GET", "/dav/x/"},
		{"GET", "/index.html"},
	} {
		if w := onHost(e, t, c.method, "sitebin-user.example", c.path); w.Code != 404 {
			t.Errorf("%s %s on the view apex = %d, want 404", c.method, c.path, w.Code)
		}
	}
	// The base domain is untouched.
	if w := onHost(e, t, "GET", "app.sitebin.example", "/"); w.Code != 200 || !strings.Contains(w.Body.String(), "landing") {
		t.Fatalf("base landing page = %d %s", w.Code, w.Body)
	}
	if w := onHost(e, t, "GET", "app.sitebin.example", "/account"); w.Code != 200 {
		t.Fatalf("the app's own routes on the base domain = %d", w.Code)
	}
}

// www can never be a site: view ids are 26 base32 characters. And a real
// site's own host is not taken for the apex.
func TestViewApexShadowsNoSite(t *testing.T) {
	if ids.ValidID("www") {
		t.Fatal("www is a valid view id")
	}
	e := apexEnv(t, nil)
	c := e.createSite(t, nil, map[string]string{"index.html": "x"})
	if _, err := e.api.siteByHost("www.sitebin-user.example"); err == nil {
		t.Fatal("www resolved to a site")
	}
	site, err := e.api.siteByHost(c.ID + ".sitebin-user.example")
	if err != nil || site.ViewID != c.ID {
		t.Fatalf("a site's own host no longer resolves: %v", err)
	}
	// The site's /_sitebin/ routes still reach the backend on its own host.
	if w := onHost(e, t, "GET", c.ID+".sitebin-user.example", "/_sitebin/assets/static/app.css"); w.Code != 200 {
		t.Fatalf("site asset = %d", w.Code)
	}
	req := httptest.NewRequest("GET", "/internal/authz", nil)
	req.Header.Set("X-Forwarded-Host", c.ID+".sitebin-user.example")
	if w := e.internal(t, req); w.Code != 200 {
		t.Fatalf("authz for the site = %d", w.Code)
	}
}

func TestSecurityTxt(t *testing.T) {
	e := apexEnv(t, nil)
	for _, host := range []string{"app.sitebin.example", "sitebin-user.example", "www.sitebin-user.example"} {
		w := onHost(e, t, "GET", host, "/.well-known/security.txt")
		if w.Code != 200 {
			t.Fatalf("%s: security.txt = %d", host, w.Code)
		}
		if ct := w.Header().Get("Content-Type"); ct != "text/plain; charset=utf-8" {
			t.Errorf("Content-Type = %q", ct)
		}
		body := w.Body.String()
		for _, want := range []string{
			"Contact: mailto:abuse@sitebin.example\n",
			"Contact: https://app.sitebin.example/report\n",
			"Preferred-Languages: en, de\n",
			"Canonical: http://app.sitebin.example/.well-known/security.txt\n",
			"Canonical: http://sitebin-user.example/.well-known/security.txt\n",
			"Canonical: http://www.sitebin-user.example/.well-known/security.txt\n",
			"Policy: https://app.sitebin.example/report\n",
		} {
			if !strings.Contains(body, want) {
				t.Errorf("%s: security.txt lacks %q:\n%s", host, want, body)
			}
		}
		var expires time.Time
		for _, line := range strings.Split(body, "\n") {
			if v, ok := strings.CutPrefix(line, "Expires: "); ok {
				var err error
				if expires, err = time.Parse(time.RFC3339, v); err != nil {
					t.Fatalf("Expires %q: %v", v, err)
				}
			}
		}
		if d := time.Until(expires); d < 170*24*time.Hour || d > 190*24*time.Hour {
			t.Errorf("Expires %v is not about 180 days ahead", expires)
		}
	}
}

// Without a mailbox the report page is the one contact; with neither there is
// no valid file to serve.
func TestSecurityTxtContacts(t *testing.T) {
	e := apexEnv(t, map[string]string{"SITEBIN_ABUSE_CONTACT": ""})
	body := onHost(e, t, "GET", "app.sitebin.example", "/.well-known/security.txt").Body.String()
	if strings.Contains(body, "mailto:") || !strings.Contains(body, "Contact: https://app.sitebin.example/report") {
		t.Fatalf("security.txt without a mailbox:\n%s", body)
	}
	e = apexEnv(t, map[string]string{"SITEBIN_ABUSE_CONTACT": "", "SITEBIN_ABUSE_REPORT_URL": "none"})
	if w := onHost(e, t, "GET", "app.sitebin.example", "/.well-known/security.txt"); w.Code != 404 {
		t.Fatalf("security.txt with no contact at all = %d", w.Code)
	}
	if w := onHost(e, t, "GET", "sitebin-user.example", "/"); w.Code != 200 || strings.Contains(w.Body.String(), "mailto:") {
		t.Fatalf("info page without contacts = %d", w.Code)
	}
}

// One domain for everything: the apex is the app, as before.
func TestSingleDomainHasNoApexPage(t *testing.T) {
	e := newEnv(t, nil)
	if w := onHost(e, t, "GET", "sitebin.example", "/"); !strings.Contains(w.Body.String(), "landing") {
		t.Fatalf("landing page = %d %s", w.Code, w.Body)
	}
	if w := onHost(e, t, "GET", "www.sitebin.example", "/"); strings.Contains(w.Body.String(), "third part") {
		t.Fatal("the info page answered on a single-domain install")
	}
	w := onHost(e, t, "GET", "sitebin.example", "/.well-known/security.txt")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "Contact: http://sitebin.example/report") {
		t.Fatalf("security.txt on a single domain = %d %s", w.Code, w.Body)
	}
}

// A visitor of a suspended site learns where to report it.
func TestSuspendedPageNamesTheAbuseContact(t *testing.T) {
	e := apexEnv(t, nil)
	c := e.createSite(t, nil, map[string]string{"index.html": "x"})
	site, _ := e.st.ByViewID(c.ID)
	if _, err := e.st.SetLock(site, &store.SiteLock{At: time.Now(), By: store.LockByAdmin, Reason: "phishing"}); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", "/internal/authz", nil)
	req.Header.Set("X-Forwarded-Host", c.ID+".sitebin-user.example")
	w := e.internal(t, req)
	if w.Code != 410 {
		t.Fatalf("authz = %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "Site suspended") || !strings.Contains(body, `href="mailto:abuse@sitebin.example"`) || !strings.Contains(body, `href="https://app.sitebin.example/report"`) {
		t.Fatalf("suspended page:\n%s", body)
	}
	if strings.Contains(body, "phishing") {
		t.Fatal("the lock's reason reached a visitor")
	}
}
