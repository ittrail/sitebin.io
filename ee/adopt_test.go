//go:build ee

package ee

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/ittrail/sitebin.io/ee/account"
	"github.com/ittrail/sitebin.io/internal/ext"
	"github.com/ittrail/sitebin.io/internal/provenance"
)

// adoptFake is the part of the core's ext.OwnershipSites that adoption and
// the limits check use, over fakeSites' infos.
type adoptFake struct {
	passwords map[string]string // view id -> edit password
	bytes     map[string]int64
	files     map[string]int
	byteErr   map[string]error
	throttled map[string]bool
	notMove   map[string][]string
	forms     map[string][]string // view id -> form recipients
	adopted   []adoptCall
	adoptErr  error
	trusted   map[string]bool
}

type adoptCall struct {
	ViewID, To string
	Grant      ext.CreateGrant
	By         ext.ChangeBy
}

func (s *fakeSites) ad() *adoptFake {
	if s.adopt == nil {
		s.adopt = &adoptFake{
			passwords: map[string]string{}, bytes: map[string]int64{}, files: map[string]int{},
			byteErr: map[string]error{}, throttled: map[string]bool{}, notMove: map[string][]string{}, forms: map[string][]string{},
			trusted: map[string]bool{},
		}
	}
	return s.adopt
}

// ResolveSite accepts a view id, its view URL, or "edit-<view id>" (an edit
// id), as the core does for the same three shapes.
func (s *fakeSites) ResolveSite(in string) (string, bool) {
	in = strings.TrimSpace(in)
	for id := range s.infos {
		if in == id || in == "https://"+id+".sitebin.app" || in == "edit-"+id {
			return id, true
		}
	}
	return "", false
}

func (s *fakeSites) CheckEditPassword(id, pw, ip string) (bool, time.Duration) {
	if s.ad().throttled[id] {
		return false, time.Minute
	}
	want, ok := s.ad().passwords[id]
	return ok && want == pw, 0
}

func (s *fakeSites) AdoptSite(id, to string, g ext.CreateGrant, by ext.ChangeBy) error {
	if err := s.ad().adoptErr; err != nil {
		return err
	}
	i, ok := s.infos[id]
	if !ok {
		return fmt.Errorf("%w: %s", ext.ErrSiteGone, id)
	}
	if i.Locked != nil {
		return fmt.Errorf("%w: %s", ext.ErrSiteLocked, id)
	}
	if i.Owner != "" || s.linkOwner[id] != "" || (i.ExpiresAt != nil && !i.ExpiresAt.After(time.Now())) {
		return fmt.Errorf("%w: %s", ext.ErrNotAdoptable, id)
	}
	i.Owner = to
	if g.MaxExpiryDays > 0 {
		at := time.Now().Add(time.Duration(g.MaxExpiryDays) * 24 * time.Hour)
		i.ExpiresAt = &at
	}
	s.infos[id] = i
	s.ad().trusted[id] = g.Trusted
	s.ad().adopted = append(s.ad().adopted, adoptCall{id, to, g, by})
	return nil
}

func (s *fakeSites) SiteBytes(id string) (int64, int, error) {
	if err := s.ad().byteErr[id]; err != nil {
		return 0, 0, err
	}
	return s.ad().bytes[id], s.ad().files[id], nil
}

func (s *fakeSites) WillNotMove(id, sender string) []string { return s.ad().notMove[id] }

const adoptTiersJSON = `[
  {"id":"free","label":"Free","max_site_bytes":1000,"max_files":10,"max_sites":1,"custom_domains":0,"max_expiry_days":7},
  {"id":"pro","label":"Pro","max_site_bytes":1000,"max_account_bytes":1500,"max_files":10,"max_sites":3,"custom_domains":2,"max_expiry_days":30,"max_containers":2,"max_forms":1,"sharing":true,"trusted":true},
  {"id":"admin","label":"Admin","max_site_bytes":1000,"max_files":10,"max_sites":3,"max_expiry_days":30,"admin":true,"sharing":true}
]`

const (
	adoptA = "aaaaaaaaaaaaaaaaaaaaaaaaaa"
	adoptB = "bbbbbbbbbbbbbbbbbbbbbbbbbb"
	adoptC = "cccccccccccccccccccccccccc"
	adoptD = "dddddddddddddddddddddddddd"
)

type adoptEnv struct {
	t      *testing.T
	p      *provider
	mux    http.Handler
	fs     *fakeSites
	cookie *http.Cookie
	acc    *account.Account
}

func newAdoptEnv(t *testing.T, tier string) *adoptEnv {
	t.Helper()
	p, mux := sharingProvider(t, "tiers", adoptTiersJSON)
	cookie, id := adminUser(t, p, mux, tier+"@example.com", tier)
	acc, err := p.accounts.ByID(id)
	if err != nil {
		t.Fatal(err)
	}
	return &adoptEnv{t: t, p: p, mux: mux, fs: p.host.Sites().(*fakeSites), cookie: cookie, acc: acc}
}

// anon registers an anonymous drop with the given edit password.
func (e *adoptEnv) anon(id, pw string) {
	e.fs.infos[id] = ext.SiteInfo{ViewID: id, Mode: "webserver", ViewURL: "https://" + id + ".sitebin.app"}
	e.fs.ad().passwords[id] = pw
}

// owned registers a drop an account already owns, with a known password.
func (e *adoptEnv) owned(id, owner, pw string) {
	e.fs.infos[id] = ext.SiteInfo{ViewID: id, Mode: "webserver", Owner: owner, ViewURL: "https://" + id + ".sitebin.app"}
	e.fs.ad().passwords[id] = pw
}

func (e *adoptEnv) post(site, pw string, mods ...func(*http.Request)) *httptest.ResponseRecorder {
	return e.postWith(e.cookie, e.p.csrf(e.acc), site, pw, mods...)
}

func (e *adoptEnv) postWith(c *http.Cookie, csrf, site, pw string, mods ...func(*http.Request)) *httptest.ResponseRecorder {
	r := form(url.Values{"csrf": {csrf}, "site": {site}, "password": {pw}})
	r.URL.Path = "/account/adopt"
	if c != nil {
		r.AddCookie(c)
	}
	for _, m := range mods {
		m(r)
	}
	return serve(e.mux, r)
}

func fromIP(ip string) func(*http.Request) {
	return func(r *http.Request) { r.Header.Set("X-Forwarded-For", ip) }
}

const wrongText = "URL or password wrong."

func TestAdoptHappyPath(t *testing.T) {
	e := newAdoptEnv(t, "pro")
	e.anon(adoptA, "secretpw")

	w := e.post("https://"+adoptA+".sitebin.app", "secretpw", fromIP("203.0.113.9"))
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/account" {
		t.Fatalf("adopt = %d -> %q (%s)", w.Code, w.Header().Get("Location"), w.Body)
	}
	if got, _ := e.fs.SiteOwner(adoptA); got != e.acc.ID {
		t.Errorf("owner = %q, want the adopting account", got)
	}
	ids, _ := e.p.accounts.ListSiteIDs(e.acc)
	if len(ids) != 1 || ids[0] != adoptA {
		t.Errorf("the account's marker list = %v", ids)
	}
	if n := len(e.fs.ad().adopted); n != 1 {
		t.Fatalf("AdoptSite calls = %d", n)
	}
	c := e.fs.ad().adopted[0]
	if c.Grant.MaxSiteBytes != 1000 || c.Grant.MaxFiles != 10 || c.Grant.MaxExpiryDays != 30 || !c.Grant.Trusted || c.Grant.OwnerAccountID != e.acc.ID {
		t.Errorf("grant = %+v, want the pro tier's stamp", c.Grant)
	}
	if c.By.Account != e.acc.ID || c.By.IP != "203.0.113.9" {
		t.Errorf("ChangeBy = %+v, want the account and auth.ClientIP", c.By)
	}
	if info, _ := e.fs.Info(adoptA); info.ExpiresAt == nil {
		t.Error("the tier expiry was not stamped")
	}
	// The one-time notice names the site, and the site is listed.
	notice := cookieNamed(w, noticeCookie)
	if notice == nil {
		t.Fatal("no one-time notice cookie")
	}
	r := httptest.NewRequest("GET", "/account", nil)
	r.AddCookie(e.cookie)
	r.AddCookie(notice)
	dash := serve(e.mux, r)
	body := dash.Body.String()
	if !strings.Contains(body, "Adopted") || strings.Count(body, adoptA) < 2 {
		t.Errorf("the dashboard shows no notice naming the site / does not list it:\n%s", body)
	}
	if cleared := cookieNamed(dash, noticeCookie); cleared == nil || cleared.Value != "" {
		t.Error("the notice is not cleared once shown")
	}
	if strings.Contains(getAs(e.mux, "/account", e.cookie).Body.String(), "Adopted") {
		t.Error("the notice shows without its cookie")
	}
	// The edit password still works.
	if ok, _ := e.fs.CheckEditPassword(adoptA, "secretpw", "1.1.1.1"); !ok {
		t.Error("the edit password stopped working after adoption")
	}
	// The account log says so.
	es, _ := e.p.accounts.Provenance(e.acc.ID)
	var found bool
	for _, en := range es {
		if en.Action == provenance.ActionAdopt && en.Site == adoptA && en.IP == "203.0.113.9" {
			found = true
		}
	}
	if !found {
		t.Errorf("no adopt line in the account log: %+v", es)
	}
}

func cookieNamed(w *httptest.ResponseRecorder, name string) *http.Cookie {
	for _, c := range w.Result().Cookies() {
		if c.Name == name {
			return c
		}
	}
	return nil
}

func TestAdoptRefusalsAreIdentical(t *testing.T) {
	past := time.Now().Add(-time.Hour)
	cases := []struct {
		name string
		set  func(e *adoptEnv)
		site string
		pw   string
	}{
		{"wrong password", func(e *adoptEnv) { e.anon(adoptA, "secretpw") }, "https://" + adoptA + ".sitebin.app", "nope"},
		{"unknown url", func(e *adoptEnv) { e.anon(adoptA, "secretpw") }, "https://zzzzzzzzzzzzzzzzzzzzzzzzzz.sitebin.app", "secretpw"},
		{"owned by another", func(e *adoptEnv) { e.owned(adoptA, "someone-else", "secretpw") }, adoptA, "secretpw"},
		{"edit id of an owned site", func(e *adoptEnv) { e.owned(adoptA, "someone-else", "secretpw") }, "edit-" + adoptA, "secretpw"},
		{"owned by the caller", func(e *adoptEnv) { e.owned(adoptA, e.acc.ID, "secretpw") }, adoptA, "secretpw"},
		{"locked", func(e *adoptEnv) {
			e.anon(adoptA, "secretpw")
			i := e.fs.infos[adoptA]
			i.Locked = &ext.SiteLock{At: time.Now(), By: "operator"}
			e.fs.infos[adoptA] = i
		}, adoptA, "secretpw"},
		{"expired", func(e *adoptEnv) {
			e.anon(adoptA, "secretpw")
			i := e.fs.infos[adoptA]
			i.ExpiresAt = &past
			e.fs.infos[adoptA] = i
		}, adoptA, "secretpw"},
		{"throttled password check", func(e *adoptEnv) {
			e.anon(adoptA, "secretpw")
			e.fs.ad().throttled[adoptA] = true
		}, adoptA, "secretpw"},
		{"AdoptSite refuses", func(e *adoptEnv) {
			e.anon(adoptA, "secretpw")
			e.fs.ad().adoptErr = fmt.Errorf("%w: raced", ext.ErrNotAdoptable)
		}, adoptA, "secretpw"},
		{"AdoptSite fails", func(e *adoptEnv) {
			e.anon(adoptA, "secretpw")
			e.fs.ad().adoptErr = errors.New("disk on fire")
		}, adoptA, "secretpw"},
		{"junk", func(e *adoptEnv) { e.anon(adoptA, "secretpw") }, "%%%\x00<script>", "x"},
		{"oversized", func(e *adoptEnv) { e.anon(adoptA, "secretpw") }, strings.Repeat("a", 5000), "x"},
		{"empty", func(e *adoptEnv) { e.anon(adoptA, "secretpw") }, "", ""},
	}
	var want int
	for i, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newAdoptEnv(t, "pro")
			c.set(e)
			w := e.post(c.site, c.pw)
			if i == 0 {
				want = w.Code
			}
			if w.Code != want {
				t.Errorf("status = %d, want %d (the same as every other refusal)", w.Code, want)
			}
			body := w.Body.String()
			if !strings.Contains(body, wrongText) {
				t.Errorf("body lacks %q:\n%s", wrongText, body)
			}
			// The whole answer is the one alert and the form: no limit lines.
			alert := body[strings.Index(body, `role="alert"`):]
			alert = alert[strings.Index(alert, ">")+1 : strings.Index(alert, "</div>")]
			if alert != wrongText || strings.Contains(body, "<ul") {
				t.Errorf("answer = %q, want exactly %q and nothing else", alert, wrongText)
			}
			if len(c.pw) > 3 && strings.Contains(body, c.pw) {
				t.Error("the password is echoed back")
			}
			if n := len(e.fs.ad().adopted); n != 0 {
				t.Errorf("AdoptSite succeeded %d times", n)
			}
			if ids, _ := e.p.accounts.ListSiteIDs(e.acc); len(ids) != 0 {
				t.Errorf("a marker was linked: %v", ids)
			}
		})
	}
	if want != http.StatusBadRequest {
		t.Errorf("refusal status = %d, want 400", want)
	}
}

func TestAdoptRateLimited(t *testing.T) {
	t.Run("per account", func(t *testing.T) {
		e := newAdoptEnv(t, "pro")
		e.anon(adoptA, "secretpw")
		for i := 1; i <= 10; i++ {
			// a new address every time: only the account's bucket can refuse
			w := e.post(adoptA, "nope", fromIP(fmt.Sprintf("198.51.100.%d", i)))
			if w.Code != http.StatusBadRequest {
				t.Fatalf("attempt %d = %d", i, w.Code)
			}
		}
		w := e.post(adoptA, "secretpw", fromIP("198.51.100.99"))
		if w.Code != http.StatusTooManyRequests {
			t.Errorf("11th attempt = %d, want 429", w.Code)
		}
		if len(e.fs.ad().adopted) != 0 {
			t.Error("a throttled attempt adopted the site")
		}
	})
	t.Run("per address", func(t *testing.T) {
		e := newAdoptEnv(t, "pro")
		e.anon(adoptA, "secretpw")
		c2, id2 := adminUser(t, e.p, e.mux, "second@example.com", "pro")
		acc2, _ := e.p.accounts.ByID(id2)
		for i := 1; i <= 10; i++ {
			var w *httptest.ResponseRecorder
			if i%2 == 0 {
				w = e.postWith(c2, e.p.csrf(acc2), adoptA, "nope", fromIP("192.0.2.7"))
			} else {
				w = e.post(adoptA, "nope", fromIP("192.0.2.7"))
			}
			if w.Code != http.StatusBadRequest {
				t.Fatalf("attempt %d = %d", i, w.Code)
			}
		}
		w := e.postWith(c2, e.p.csrf(acc2), adoptA, "secretpw", fromIP("192.0.2.7"))
		if w.Code != http.StatusTooManyRequests {
			t.Errorf("11th attempt from one address = %d, want 429", w.Code)
		}
	})
}

func TestAdoptNeedsSharing(t *testing.T) {
	e := newAdoptEnv(t, "free")
	e.anon(adoptA, "secretpw")
	w := e.post(adoptA, "secretpw")
	body := w.Body.String()
	if w.Code != http.StatusForbidden || !strings.Contains(body, "Adopting needs Pro or Studio") || !strings.Contains(body, `href="/account#plan"`) {
		t.Errorf("free account = %d:\n%s", w.Code, body)
	}
	if strings.Contains(body, wrongText) {
		t.Error("the plan page must not judge the URL or password")
	}
	if len(e.fs.ad().adopted) != 0 {
		t.Error("a free account adopted a site")
	}
}

func TestAdoptSharingLookupErrorIsARetry(t *testing.T) {
	e := newAdoptEnv(t, "pro")
	e.anon(adoptA, "secretpw")
	acc, _ := e.p.accounts.ByID(e.acc.ID)
	e.p.accounts.Update(acc, func(a *account.Account) error { a.Tier = "retired"; return nil })
	w := e.post(adoptA, "secretpw")
	if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), "try again") {
		t.Errorf("unknown plan = %d:\n%s", w.Code, w.Body)
	}
	if len(e.fs.ad().adopted) != 0 {
		t.Error("adopted on an unknown plan")
	}
}

func TestAdoptLimitFailuresShowTheLine(t *testing.T) {
	fill := func(e *adoptEnv) {
		for _, id := range []string{adoptB, adoptC, adoptD} {
			e.owned(id, e.acc.ID, "x")
			e.p.accounts.LinkSite(e.acc, id)
		}
	}
	t.Run("sites at the plan's cap", func(t *testing.T) {
		e := newAdoptEnv(t, "pro")
		fill(e)
		e.anon(adoptA, "secretpw")
		w := e.post(adoptA, "secretpw")
		body := w.Body.String()
		if w.Code != http.StatusForbidden || !strings.Contains(body, "Sites") || !strings.Contains(body, "3") {
			t.Errorf("at cap = %d:\n%s", w.Code, body)
		}
		if strings.Contains(body, wrongText) {
			t.Error("a proven password is not 'wrong'")
		}
		if len(e.fs.ad().adopted) != 0 {
			t.Error("adopted over the cap")
		}
	})
	t.Run("a wrong password still says nothing about limits", func(t *testing.T) {
		e := newAdoptEnv(t, "pro")
		fill(e)
		e.anon(adoptA, "secretpw")
		w := e.post(adoptA, "nope")
		if !strings.Contains(w.Body.String(), wrongText) || strings.Contains(w.Body.String(), "Sites") {
			t.Errorf("unproven attempt leaked limits:\n%s", w.Body)
		}
	})
	t.Run("a site over the byte cap names itself", func(t *testing.T) {
		e := newAdoptEnv(t, "pro")
		e.anon(adoptA, "secretpw")
		e.fs.ad().bytes[adoptA] = 5000
		w := e.post(adoptA, "secretpw")
		body := w.Body.String()
		if w.Code != http.StatusForbidden || !strings.Contains(body, adoptA) || !strings.Contains(body, "Storage") {
			t.Errorf("over bytes = %d:\n%s", w.Code, body)
		}
		if len(e.fs.ad().adopted) != 0 {
			t.Error("adopted over the byte cap")
		}
	})
	t.Run("a measuring error refuses", func(t *testing.T) {
		e := newAdoptEnv(t, "pro")
		e.anon(adoptA, "secretpw")
		e.fs.ad().byteErr[adoptA] = errors.New("disk")
		w := e.post(adoptA, "secretpw")
		if w.Code != http.StatusServiceUnavailable || len(e.fs.ad().adopted) != 0 {
			t.Errorf("unknown size = %d, adopted %d", w.Code, len(e.fs.ad().adopted))
		}
	})
}

func TestAdoptNeedsCSRF(t *testing.T) {
	e := newAdoptEnv(t, "pro")
	e.anon(adoptA, "secretpw")
	for _, csrf := range []string{"", "forged"} {
		if w := e.postWith(e.cookie, csrf, adoptA, "secretpw"); w.Code != http.StatusForbidden {
			t.Errorf("csrf %q = %d, want 403", csrf, w.Code)
		}
	}
	if len(e.fs.ad().adopted) != 0 {
		t.Error("adopted without CSRF")
	}
}

func TestAdoptSignedOut(t *testing.T) {
	e := newAdoptEnv(t, "pro")
	e.anon(adoptA, "secretpw")
	site := "https://" + adoptA + ".sitebin.app"

	// POST signed out: nothing happens.
	if w := e.postWith(nil, "", site, "secretpw"); w.Code != http.StatusSeeOther || !strings.HasPrefix(w.Header().Get("Location"), "/account/login") {
		t.Errorf("signed-out POST = %d -> %q", w.Code, w.Header().Get("Location"))
	}

	// GET signed out: sign-in, with the site value kept for the way back.
	r := httptest.NewRequest("GET", "/account/adopt?site="+url.QueryEscape(site)+"&password=leak", nil)
	w := serve(e.mux, r)
	if w.Code != http.StatusSeeOther || !strings.HasPrefix(w.Header().Get("Location"), "/account/login") {
		t.Fatalf("signed-out GET = %d -> %q", w.Code, w.Header().Get("Location"))
	}
	if strings.Contains(w.Header().Get("Location"), "leak") || strings.Contains(w.Header().Get("Location"), "http") {
		t.Errorf("login redirect %q carries more than it should", w.Header().Get("Location"))
	}
	ret := cookieNamed(w, returnCookie)
	if ret == nil || strings.Contains(ret.Value, "leak") {
		t.Fatalf("return cookie = %+v", ret)
	}

	// Signing in with it goes back to the adopt page, site kept, no password.
	login := func(c *http.Cookie) *httptest.ResponseRecorder {
		lr := form(url.Values{"email": {"pro@example.com"}, "password": {"password123"}})
		lr.URL.Path = "/account/login"
		if c != nil {
			lr.AddCookie(c)
		}
		return serve(e.mux, lr)
	}
	lw := login(ret)
	loc := lw.Header().Get("Location")
	if lw.Code != http.StatusSeeOther || loc != "/account/adopt?site="+url.QueryEscape(site) {
		t.Errorf("login with the return cookie -> %q", loc)
	}
	if c := cookieNamed(lw, returnCookie); c == nil || c.Value != "" {
		t.Error("the return cookie is not cleared after use")
	}
	// Without it, or tampered with, the way back is the dashboard.
	if loc := login(nil).Header().Get("Location"); loc != "/account" {
		t.Errorf("login without a return -> %q", loc)
	}
	bad := *ret
	bad.Value = ret.Value[:len(ret.Value)-3] + "AAA"
	if loc := login(&bad).Header().Get("Location"); loc != "/account" {
		t.Errorf("login with a tampered return -> %q", loc)
	}
}

func TestAdoptFormIsPrefilledAndPostsTheBody(t *testing.T) {
	e := newAdoptEnv(t, "pro")
	site := "https://" + adoptA + ".sitebin.app"
	w := getAs(e.mux, "/account/adopt?site="+url.QueryEscape(site)+"&password=hunter2", e.cookie)
	body := w.Body.String()
	if w.Code != http.StatusOK || !strings.Contains(body, `value="`+site+`"`) {
		t.Errorf("GET = %d, not prefilled:\n%s", w.Code, body)
	}
	if strings.Contains(body, "hunter2") {
		t.Error("a password in the URL was echoed")
	}
	if !strings.Contains(body, `method="post"`) || !strings.Contains(body, `action="/account/adopt"`) || !strings.Contains(body, `type="password"`) || !strings.Contains(body, `name="csrf"`) {
		t.Errorf("the form is not a CSRF-protected POST with a password field:\n%s", body)
	}
	// The dashboard card posts to the same route.
	dash := getAs(e.mux, "/account", e.cookie).Body.String()
	if !strings.Contains(dash, "Adopt an anonymous drop") || !strings.Contains(dash, `action="/account/adopt"`) {
		t.Error("the dashboard has no adopt card")
	}
}

func TestLimitsCheck(t *testing.T) {
	failing := func(ls []checkLine) []checkLine {
		var out []checkLine
		for _, l := range ls {
			if !l.OK {
				out = append(out, l)
			}
		}
		return out
	}
	t.Run("a bundle counts every site against the plan", func(t *testing.T) {
		e := newAdoptEnv(t, "pro")
		e.anon(adoptA, "x")
		e.anon(adoptB, "x")
		e.anon(adoptC, "x")
		e.anon(adoptD, "x")
		ls, err := e.p.limitsCheck(e.acc, []string{adoptA, adoptB, adoptC})
		if err != nil || len(failing(ls)) != 0 {
			t.Fatalf("3 sites on a plan of 3 = %+v, %v", ls, err)
		}
		ls, err = e.p.limitsCheck(e.acc, []string{adoptA, adoptB, adoptC, adoptD})
		if err != nil || len(failing(ls)) != 1 || failing(ls)[0].Label != "Sites" {
			t.Errorf("4 sites on a plan of 3 = %+v, %v", ls, err)
		}
	})
	t.Run("domains, files and forms are per site; zone names do not count", func(t *testing.T) {
		e := newAdoptEnv(t, "pro")
		e.anon(adoptA, "x")
		e.anon(adoptB, "x")
		a := e.fs.infos[adoptA]
		a.Domains = []string{"one.example", "two.example", "three.example", "zone.example"}
		a.Forms = 2
		e.fs.infos[adoptA] = a
		e.fs.ad().files[adoptA] = 11
		e.fs.ad().notMove[adoptA] = []string{"zone.example"}
		ls, err := e.p.limitsCheck(e.acc, []string{adoptA, adoptB})
		if err != nil {
			t.Fatal(err)
		}
		labels := map[string]bool{}
		for _, l := range failing(ls) {
			if !strings.Contains(l.Site, adoptA) {
				t.Errorf("failing line without the site: %+v", l)
			}
			labels[l.Label] = true
		}
		for _, want := range []string{"Custom domains", "Files", "Forms"} {
			if !labels[want] {
				t.Errorf("missing failing line %q in %+v", want, ls)
			}
		}
		if len(labels) != 3 {
			t.Errorf("failing lines = %v", labels)
		}
		// Two movable names and a zone name: the zone name does not count.
		a.Domains = []string{"one.example", "two.example", "zone.example"}
		e.fs.infos[adoptA] = a
		ls, _ = e.p.limitsCheck(e.acc, []string{adoptA})
		for _, l := range failing(ls) {
			if l.Label == "Custom domains" {
				t.Errorf("a zone name was counted: %+v", l)
			}
		}
	})
	t.Run("container services add to what the account runs", func(t *testing.T) {
		e := newAdoptEnv(t, "pro")
		e.anon(adoptA, "x")
		e.fs.containers = []ext.ContainerSite{{ViewID: adoptA, Container: true, Enabled: true, Observed: ext.ContainerState{Status: "running", Services: []ext.ContainerService{{Name: "web"}, {Name: "db"}}}}}
		e.p.servicesOf = func(owner string) (int, error) { return 1, nil }
		ls, err := e.p.limitsCheck(e.acc, []string{adoptA})
		if err != nil || len(failing(ls)) != 1 || failing(ls)[0].Label != "Container services" {
			t.Errorf("1 running + 2 incoming against 2 = %+v, %v", ls, err)
		}
		e.p.servicesOf = func(owner string) (int, error) { return 0, errors.New("listing failed") }
		if _, err := e.p.limitsCheck(e.acc, []string{adoptA}); err == nil {
			t.Error("an unknown service count passed")
		}
	})
	t.Run("the bundle must fit the account's storage", func(t *testing.T) {
		e := newAdoptEnv(t, "pro")
		e.owned(adoptC, e.acc.ID, "x")
		c := e.fs.infos[adoptC]
		c.Bytes, c.GitBytes = 600, 200
		e.fs.infos[adoptC] = c
		e.p.accounts.LinkSite(e.acc, adoptC)
		e.anon(adoptA, "x")
		e.fs.ad().bytes[adoptA] = 700 // 800 + 700 = 1500: fits exactly
		ls, err := e.p.limitsCheck(e.acc, []string{adoptA})
		if err != nil || len(failing(ls)) != 0 {
			t.Fatalf("a bundle that fits = %+v, %v", failing(ls), err)
		}
		e.fs.ad().bytes[adoptA] = 701
		ls, err = e.p.limitsCheck(e.acc, []string{adoptA})
		if err != nil || len(failing(ls)) != 1 || failing(ls)[0].Label != "Account storage" ||
			!strings.Contains(failing(ls)[0].Detail, "Your sites hold 800 B, these add 701 B, and your plan allows 1.5 KB in total.") {
			t.Fatalf("one byte too many = %+v, %v", failing(ls), err)
		}
		// A locked site is the operator's hold and counts for nobody.
		c.Locked = &ext.SiteLock{Reason: "x"}
		e.fs.infos[adoptC] = c
		if ls, _ := e.p.limitsCheck(e.acc, []string{adoptA}); len(failing(ls)) != 0 {
			t.Fatalf("a locked site counted: %+v", failing(ls))
		}
	})
	t.Run("errors are errors", func(t *testing.T) {
		e := newAdoptEnv(t, "pro")
		if _, err := e.p.limitsCheck(e.acc, []string{adoptA}); err == nil {
			t.Error("a site that cannot be read passed")
		}
		e.anon(adoptA, "x")
		e.fs.ad().byteErr[adoptA] = errors.New("disk")
		if _, err := e.p.limitsCheck(e.acc, []string{adoptA}); err == nil {
			t.Error("a size that cannot be measured passed")
		}
	})
	t.Run("accounts mode checks sharing only", func(t *testing.T) {
		p, mux := sharingProvider(t, "accounts", "")
		_, id := adminUser(t, p, mux, "acc@example.com", "free")
		acc, _ := p.accounts.ByID(id)
		fs := p.host.Sites().(*fakeSites)
		fs.infos[adoptA] = ext.SiteInfo{ViewID: adoptA, Mode: "webserver"}
		fs.ad().bytes[adoptA] = 1 << 40
		ls, err := p.limitsCheck(acc, []string{adoptA})
		if err != nil || len(ls) != 1 || ls[0].Label != "Sharing" || !ls[0].OK {
			t.Errorf("accounts mode = %+v, %v", ls, err)
		}
	})
}
