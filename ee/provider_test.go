//go:build ee

package ee

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/ittrail/sitebin.io/internal/ext"
)

// --- fakes ---

type fakeSites struct {
	infos   map[string]ext.SiteInfo
	deleted []string
	rotated []string
	quotas  map[string]ext.CreateGrant
	// applyErrs fails ApplyQuota for the listed site ids, so a test can make a
	// restamp succeed for some of an account's sites and fail for others.
	applyErrs map[string]error
}

func (s *fakeSites) Info(id string) (ext.SiteInfo, bool) { i, ok := s.infos[id]; return i, ok }
func (s *fakeSites) RotateEditPassword(id string) (string, error) {
	s.rotated = append(s.rotated, id)
	return "freshEditPw123456789012", nil
}
func (s *fakeSites) Delete(id string) error {
	s.deleted = append(s.deleted, id)
	delete(s.infos, id)
	return nil
}
func (s *fakeSites) ApplyQuota(id string, g ext.CreateGrant) error {
	if err := s.applyErrs[id]; err != nil {
		return err
	}
	if s.quotas == nil {
		s.quotas = map[string]ext.CreateGrant{}
	}
	s.quotas[id] = g
	return nil
}

type fakeHost struct {
	dir       string
	sites     *fakeSites
	pathViews bool
}

func (h *fakeHost) DataDir() string        { return h.dir }
func (h *fakeHost) BaseDomain() string     { return "sitebin.example" }
func (h *fakeHost) HTTPOnly() bool         { return true }
func (h *fakeHost) Secret() []byte         { return []byte("0123456789abcdef0123456789abcdef") }
func (h *fakeHost) PathViews() bool        { return h.pathViews }
func (h *fakeHost) Sites() ext.SiteService { return h.sites }

func setupAccounts(t *testing.T) (*provider, *fakeHost, http.Handler) {
	t.Helper()
	t.Setenv("SITEBIN_ACCOUNT_MODE", "accounts")
	p := newProvider()
	host := &fakeHost{dir: t.TempDir(), sites: &fakeSites{infos: map[string]ext.SiteInfo{}}}
	if err := p.Init(host); err != nil {
		t.Fatalf("Init: %v", err)
	}
	mux := http.NewServeMux()
	for pat, h := range p.PublicRoutes() {
		mux.Handle(pat, h)
	}
	return p, host, mux
}

func form(v url.Values) *http.Request {
	r := httptest.NewRequest("POST", "/", strings.NewReader(v.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return r
}

func TestPathViewsRefusedWithAccounts(t *testing.T) {
	t.Setenv("SITEBIN_ACCOUNT_MODE", "accounts")
	p := newProvider()
	host := &fakeHost{dir: t.TempDir(), sites: &fakeSites{infos: map[string]ext.SiteInfo{}}, pathViews: true}
	err := p.Init(host)
	if err == nil || !strings.Contains(err.Error(), "SITEBIN_VIEW_ACCESS") {
		t.Fatalf("expected path+accounts to be refused, got %v", err)
	}
}

func TestCustomDomainsAllowedInEnterprise(t *testing.T) {
	p, _, _ := setupAccounts(t) // enterprise provider
	if !p.CustomDomainsAllowed() {
		t.Error("enterprise should allow custom domains")
	}
}

func TestAuthorizeCreateRequiresLogin(t *testing.T) {
	p, _, _ := setupAccounts(t)
	_, err := p.AuthorizeCreate(httptest.NewRequest("POST", "/api/sites", nil))
	var ce *ext.CreateError
	if !errors.As(err, &ce) || ce.Status != 401 {
		t.Fatalf("expected 401 CreateError, got %v", err)
	}
}

func TestSignupLoginDashboardFlow(t *testing.T) {
	p, host, mux := setupAccounts(t)

	// signup
	req := form(url.Values{"email": {"user@example.com"}, "password": {"password123"}})
	req.URL.Path = "/account/signup"
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("signup = %d, want 303 (%s)", w.Code, w.Body)
	}
	cookie := sessionCookie(t, w)

	// authorized creation now returns the account id
	acc, _ := p.accounts.ByEmail("user@example.com")
	authReq := httptest.NewRequest("POST", "/api/sites", nil)
	authReq.AddCookie(cookie)
	grant, err := p.AuthorizeCreate(authReq)
	if err != nil || grant.OwnerAccountID != acc.ID {
		t.Fatalf("AuthorizeCreate after login = %+v, %v", grant, err)
	}

	// give the account a site and confirm the dashboard shows it
	viewID := "abcdefghijklmnopqrstuvwxyz"
	p.accounts.LinkSite(acc, viewID)
	host.sites.infos[viewID] = ext.SiteInfo{ViewID: viewID, Mode: "webserver", ViewURL: "http://" + viewID + ".sitebin.example", EditURL: "http://sitebin.example/e/x"}

	dash := httptest.NewRequest("GET", "/account", nil)
	dash.AddCookie(cookie)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, dash)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "user@example.com") || !strings.Contains(w.Body.String(), viewID) {
		t.Fatalf("dashboard = %d, body missing account/site", w.Code)
	}

	csrf := p.csrf(acc)

	// rotate edit password
	rot := form(url.Values{"csrf": {csrf}})
	rot.URL.Path = "/account/sites/" + viewID + "/rotate"
	rot.AddCookie(cookie)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, rot)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "freshEditPw") {
		t.Fatalf("rotate = %d, body %q", w.Code, w.Body)
	}
	if len(host.sites.rotated) != 1 {
		t.Errorf("rotate not called: %v", host.sites.rotated)
	}

	// rotate without CSRF is forbidden
	badRot := form(url.Values{})
	badRot.URL.Path = "/account/sites/" + viewID + "/rotate"
	badRot.AddCookie(cookie)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, badRot)
	if w.Code != http.StatusForbidden {
		t.Fatalf("rotate w/o csrf = %d, want 403", w.Code)
	}

	// delete the site
	del := form(url.Values{"csrf": {csrf}})
	del.URL.Path = "/account/sites/" + viewID + "/delete"
	del.AddCookie(cookie)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, del)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("delete site = %d", w.Code)
	}
	if len(host.sites.deleted) != 1 || host.sites.deleted[0] != viewID {
		t.Errorf("site not deleted: %v", host.sites.deleted)
	}
	if p.owns(acc, viewID) {
		t.Error("account still owns deleted site")
	}
}

func TestLoginWrongPasswordRerenders(t *testing.T) {
	p, _, mux := setupAccounts(t)
	p.local.Signup("a@example.com", "password123", "")

	req := form(url.Values{"email": {"a@example.com"}, "password": {"wrong"}})
	req.URL.Path = "/account/login"
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "Incorrect email or password") {
		t.Fatalf("login fail = %d, body missing error", w.Code)
	}
	if sessionCookieMaybe(w) != nil {
		t.Error("session set on failed login")
	}
}

func TestDeleteAccount(t *testing.T) {
	p, _, mux := setupAccounts(t)
	// signup
	req := form(url.Values{"email": {"z@example.com"}, "password": {"password123"}})
	req.URL.Path = "/account/signup"
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	cookie := sessionCookie(t, w)
	acc, _ := p.accounts.ByEmail("z@example.com")

	del := form(url.Values{"csrf": {p.csrf(acc)}})
	del.URL.Path = "/account/delete"
	del.AddCookie(cookie)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, del)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "Account deleted") {
		t.Fatalf("delete account = %d", w.Code)
	}
	if _, err := p.accounts.ByEmail("z@example.com"); err == nil {
		t.Error("account survived deletion")
	}
}

func sessionCookie(t *testing.T, w *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	c := sessionCookieMaybe(w)
	if c == nil {
		t.Fatal("no session cookie set")
	}
	return c
}

func sessionCookieMaybe(w *httptest.ResponseRecorder) *http.Cookie {
	for _, c := range w.Result().Cookies() {
		if c.Name == "sitebin_s" && c.Value != "" {
			return c
		}
	}
	return nil
}
