package httpapi

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ittrail/sitebin.io/internal/ext"
	"github.com/ittrail/sitebin.io/internal/provenance"
)

// The front door: a page on a session origin (the operator's own marketing
// site) may create a site in the signed-in visitor's account. Every other
// cross-origin request that carries the visitor's cookie creates an
// anonymous site, or none -- never an owned one.
// See docs/superpowers/specs/2026-11-03-front-door-design.md, section 2.

const (
	frontDoorOrigin  = "https://www.sitebin.io"
	plainEmbedOrigin = "https://embed.example"
)

// frontDoorEnv is a gated instance whose embed origins are the front door
// pair plus one plain embed origin, and whose session origins are the pair.
// Its provider names the owner of a new site from the sb_test cookie.
func frontDoorEnv(t *testing.T) (*env, *sessionProvider) {
	t.Helper()
	p := &sessionProvider{
		fakeProvider:     &fakeProvider{enabled: true, embedOK: true},
		sessions:         map[string]string{"owner-cookie": "acct-1"},
		ownerFromSession: true,
	}
	registerProvider(t, p)
	e := newEnv(t, map[string]string{
		"SITEBIN_EMBED_ORIGINS":   "https://sitebin.io,https://www.sitebin.io," + plainEmbedOrigin,
		"SITEBIN_SESSION_ORIGINS": "https://sitebin.io,https://www.sitebin.io",
	})
	return e, p
}

// dropFrom posts a create the way a browser would: from origin (none when
// ""), with the given Sec-Fetch-Site (none when ""), the session marker
// when marked, and the sb_test cookie when cookie is set.
func dropFrom(t *testing.T, e *env, origin, fetchSite string, marked bool, cookie string) *httptest.ResponseRecorder {
	t.Helper()
	h := map[string]string{}
	if origin != "" {
		h["Origin"] = origin
	}
	if fetchSite != "" {
		h["Sec-Fetch-Site"] = fetchSite
	}
	if marked {
		h[sessionHeader] = "1"
	}
	if cookie != "" {
		h["Cookie"] = "sb_test=" + cookie
	}
	return scriptCreate(t, e, h)
}

// created asserts a 201 whose body states "owned" outright -- a missing key
// would decode as false and hide the difference -- and that the site on disk
// agrees with it: owned by wantOwner, or by nobody when wantOwner is "".
func created(t *testing.T, e *env, w *httptest.ResponseRecorder, wantOwner string) createResp {
	t.Helper()
	if w.Code != 201 {
		t.Fatalf("create = %d %s, want 201", w.Code, w.Body)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
		t.Fatalf("create response: %v", err)
	}
	if _, ok := raw["owned"]; !ok {
		t.Fatalf("create response has no \"owned\": %s", w.Body)
	}
	var c createResp
	if err := json.Unmarshal(w.Body.Bytes(), &c); err != nil {
		t.Fatalf("create response: %v", err)
	}
	if c.Owned != (wantOwner != "") {
		t.Errorf("owned = %v, want %v", c.Owned, wantOwner != "")
	}
	site, err := e.st.ByViewID(c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if site.Meta.OwnerAccountID != wantOwner {
		t.Errorf("owner on disk = %q, want %q", site.Meta.OwnerAccountID, wantOwner)
	}
	return c
}

func TestCreateFromSessionOriginIsOwned(t *testing.T) {
	// Both halves of the apex/www pair are session origins.
	for _, origin := range []string{frontDoorOrigin, "https://sitebin.io"} {
		t.Run(origin, func(t *testing.T) {
			e, p := frontDoorEnv(t)
			w := dropFrom(t, e, origin, "same-site", true, "owner-cookie")
			c := created(t, e, w, "acct-1")
			if got := w.Header().Get("Access-Control-Allow-Origin"); got != origin {
				t.Errorf("ACAO = %q, want %q", got, origin)
			}
			if got := w.Header().Get("Access-Control-Allow-Credentials"); got != "true" {
				t.Errorf("ACAC = %q, want true", got)
			}
			if !strings.Contains(w.Header().Get("Vary"), "Origin") {
				t.Errorf("Vary = %q, want Origin", w.Header().Get("Vary"))
			}
			if len(p.created) != 1 || p.created[0] != "acct-1:"+c.ID {
				t.Errorf("OnSiteCreated = %v, want the site linked to acct-1", p.created)
			}
		})
	}
}

func TestCreateFromSessionOriginWithoutHeaderIsAnonymous(t *testing.T) {
	e, p := frontDoorEnv(t)
	// A credentialed form or fetch from the front door that did not ask for
	// the session: the cookie is ignored, not refused.
	created(t, e, dropFrom(t, e, frontDoorOrigin, "same-site", false, "owner-cookie"), "")
	if len(p.created) != 0 {
		t.Errorf("OnSiteCreated = %v, want nothing linked", p.created)
	}
}

func TestCreateFromPlainEmbedOriginIgnoresCookie(t *testing.T) {
	e, _ := frontDoorEnv(t)
	w := dropFrom(t, e, plainEmbedOrigin, "cross-site", true, "owner-cookie")
	created(t, e, w, "")
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != plainEmbedOrigin {
		t.Errorf("ACAO = %q, want the embed origin as today", got)
	}
	if got := w.Header().Get("Access-Control-Allow-Credentials"); got != "" {
		t.Errorf("ACAC = %q, want none for a plain embed origin", got)
	}
}

// The latent hole the spec closes: a same-site page that is not a session
// origin used to be able to create a site in the visitor's account with a
// plain credentialed POST.
func TestCreateSameSiteWithCookieIsAnonymous(t *testing.T) {
	t.Run("embed origin", func(t *testing.T) {
		e, _ := frontDoorEnv(t)
		created(t, e, dropFrom(t, e, plainEmbedOrigin, "same-site", false, "owner-cookie"), "")
	})
	// Pages that are not even embed origins: anonymous, and an anonymous
	// create from a foreign page is refused as it always was.
	for _, c := range []struct {
		name, origin, fetchSite string
		marked                  bool
	}{
		{"sibling host", "https://user.sitebin.example", "same-site", false},
		{"sibling host with the marker", "https://user.sitebin.example", "same-site", true},
		{"sibling host without fetch metadata", "https://user.sitebin.example", "", false},
		{"session origin on another port", "https://www.sitebin.io:8443", "same-site", true},
		{"opaque origin", "null", "cross-site", true},
		{"fetch metadata only", "", "same-site", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			e, p := frontDoorEnv(t)
			w := dropFrom(t, e, c.origin, c.fetchSite, c.marked, "owner-cookie")
			if w.Code != 401 {
				t.Fatalf("create = %d %s, want 401 (anonymous, from a foreign page)", w.Code, w.Body)
			}
			if len(p.created) != 0 {
				t.Errorf("OnSiteCreated = %v, want nothing linked", p.created)
			}
		})
	}
}

// Safari before 16.4 sends Origin but no Sec-Fetch-Site. A signed-out
// visitor's drop on the front door is still an anonymous drop from a browser,
// not a script; a plain embed origin keeps needing the fetch metadata. The
// session itself still needs the session header: the cookie rule does not
// move.
func TestCreateFromSessionOriginWithoutFetchMetadata(t *testing.T) {
	t.Run("signed out", func(t *testing.T) {
		e, _ := frontDoorEnv(t)
		created(t, e, dropFrom(t, e, frontDoorOrigin, "", true, ""), "")
	})
	t.Run("signed in", func(t *testing.T) {
		e, _ := frontDoorEnv(t)
		created(t, e, dropFrom(t, e, frontDoorOrigin, "", true, "owner-cookie"), "acct-1")
	})
	t.Run("cookie without the session header", func(t *testing.T) {
		e, p := frontDoorEnv(t)
		created(t, e, dropFrom(t, e, frontDoorOrigin, "", false, "owner-cookie"), "")
		if len(p.created) != 0 {
			t.Errorf("OnSiteCreated = %v, want nothing linked", p.created)
		}
	})
	t.Run("plain embed origin is still refused", func(t *testing.T) {
		e, _ := frontDoorEnv(t)
		if w := dropFrom(t, e, plainEmbedOrigin, "", false, ""); w.Code != 401 {
			t.Fatalf("create = %d %s, want 401", w.Code, w.Body)
		}
	})
}

func TestCreateSameOriginStillOwned(t *testing.T) {
	// The landing page's own fetch: no session marker, the cookie counts.
	for _, c := range []struct{ name, origin string }{
		{"with origin", "http://sitebin.example"},
		{"origin with the terminator's scheme", "https://sitebin.example"},
		{"without origin", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			e, _ := frontDoorEnv(t)
			created(t, e, dropFrom(t, e, c.origin, "same-origin", false, "owner-cookie"), "acct-1")
		})
	}
	t.Run("own-host origin without fetch metadata", func(t *testing.T) {
		e, _ := frontDoorEnv(t)
		created(t, e, dropFrom(t, e, "http://sitebin.example", "", false, "owner-cookie"), "acct-1")
	})
}

func TestCreateNonBrowserStillOwned(t *testing.T) {
	e, _ := frontDoorEnv(t)
	// curl with a cookie: no Origin, no Sec-Fetch-Site.
	created(t, e, dropFrom(t, e, "", "", false, "owner-cookie"), "acct-1")
}

func TestCreateExpiredSessionFromSessionOrigin(t *testing.T) {
	// The embed said "publishes to your account", but the session ended
	// before the upload: an anonymous site, no error (Review Focus 1).
	for _, c := range []struct{ name, cookie string }{
		{"invalid cookie", "expired-cookie"},
		{"no cookie", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			e, _ := frontDoorEnv(t)
			created(t, e, dropFrom(t, e, frontDoorOrigin, "same-site", true, c.cookie), "")
		})
	}
}

func preflight(t *testing.T, e *env, origin string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("OPTIONS", "/api/sites", nil)
	req.Header.Set("Origin", origin)
	req.Header.Set("Access-Control-Request-Method", "POST")
	return e.public(t, req)
}

func TestPreflightSessionOrigin(t *testing.T) {
	e, _ := frontDoorEnv(t)

	w := preflight(t, e, frontDoorOrigin)
	if w.Code != 204 {
		t.Fatalf("session origin preflight = %d %s", w.Code, w.Body)
	}
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != frontDoorOrigin {
		t.Errorf("ACAO = %q", got)
	}
	if got := w.Header().Get("Access-Control-Allow-Headers"); !strings.Contains(got, "X-Sitebin-Session") || !strings.Contains(got, "Content-Type") {
		t.Errorf("Allow-Headers = %q, want Content-Type and X-Sitebin-Session", got)
	}
	if got := w.Header().Get("Access-Control-Allow-Credentials"); got != "true" {
		t.Errorf("ACAC = %q, want true", got)
	}
	if !strings.Contains(w.Header().Get("Vary"), "Origin") {
		t.Errorf("Vary = %q, want Origin", w.Header().Get("Vary"))
	}

	// A plain embed origin: exactly as before -- no session header, no
	// credentials.
	w = preflight(t, e, plainEmbedOrigin)
	if w.Code != 204 {
		t.Fatalf("embed origin preflight = %d %s", w.Code, w.Body)
	}
	if got := w.Header().Get("Access-Control-Allow-Headers"); got != "Content-Type" {
		t.Errorf("embed Allow-Headers = %q, want exactly Content-Type", got)
	}
	if got := w.Header().Get("Access-Control-Allow-Credentials"); got != "" {
		t.Errorf("embed ACAC = %q, want none", got)
	}

	// Anyone else: refused, nothing allowed.
	w = preflight(t, e, "https://evil.example")
	if w.Code != 404 {
		t.Errorf("foreign preflight = %d, want 404", w.Code)
	}
	for _, h := range []string{"Access-Control-Allow-Origin", "Access-Control-Allow-Credentials", "Access-Control-Allow-Headers"} {
		if got := w.Header().Get(h); got != "" {
			t.Errorf("foreign %s = %q, want none", h, got)
		}
	}
}

func TestSessionOriginsInertInCommunity(t *testing.T) {
	// No provider: the community build. Creation is open and nobody owns
	// anything; the session origins grant no credentials.
	e := newEnv(t, map[string]string{
		"SITEBIN_EMBED_ORIGINS":   "https://sitebin.io,https://www.sitebin.io",
		"SITEBIN_SESSION_ORIGINS": "https://sitebin.io,https://www.sitebin.io",
	})
	w := dropFrom(t, e, frontDoorOrigin, "same-site", true, "owner-cookie")
	created(t, e, w, "")
	for _, h := range []string{"Access-Control-Allow-Origin", "Access-Control-Allow-Credentials"} {
		if got := w.Header().Get(h); got != "" {
			t.Errorf("community %s = %q, want none", h, got)
		}
	}
	w = preflight(t, e, frontDoorOrigin)
	if w.Code != 404 || w.Header().Get("Access-Control-Allow-Credentials") != "" {
		t.Errorf("community preflight = %d ACAC %q, want 404 and none", w.Code, w.Header().Get("Access-Control-Allow-Credentials"))
	}
	req := httptest.NewRequest("POST", "/api/sites", nil)
	req.Header.Set("Origin", frontDoorOrigin)
	req.Header.Set(sessionHeader, "1")
	if e.api.sessionOriginsHonoured() || e.api.isSessionOrigin(frontDoorOrigin) || e.api.sessionOriginRequest(req) {
		t.Error("session origins honoured with no provider")
	}
}

// Session origins need all three: accounts, the embed capability, and a
// session lookup. Missing any one, they are plain embed origins at most.
func TestSessionOriginsHonouredOnlyWithEverything(t *testing.T) {
	over := map[string]string{
		"SITEBIN_EMBED_ORIGINS":   "https://sitebin.io,https://www.sitebin.io",
		"SITEBIN_SESSION_ORIGINS": "https://sitebin.io,https://www.sitebin.io",
	}
	sessions := func(f *fakeProvider) ext.Provider {
		return &sessionProvider{fakeProvider: f, sessions: map[string]string{}}
	}
	cases := []struct {
		name string
		p    ext.Provider
		want bool
	}{
		{"everything", sessions(&fakeProvider{enabled: true, embedOK: true}), true},
		{"accounts off", sessions(&fakeProvider{enabled: false, embedOK: true}), false},
		{"no embed capability", sessions(&fakeProvider{enabled: true, embedOK: false}), false},
		{"no session lookup", &fakeProvider{enabled: true, embedOK: true}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			registerProvider(t, c.p)
			e := newEnv(t, over)
			if got := e.api.sessionOriginsHonoured(); got != c.want {
				t.Errorf("sessionOriginsHonoured = %v, want %v", got, c.want)
			}
			if got := e.api.isSessionOrigin(frontDoorOrigin); got != c.want {
				t.Errorf("isSessionOrigin = %v, want %v", got, c.want)
			}
		})
	}
}

func TestSessionOriginRequest(t *testing.T) {
	e, _ := frontDoorEnv(t)
	cases := []struct {
		name    string
		headers map[string]string
		want    bool
	}{
		{"session origin with the marker", map[string]string{"Origin": frontDoorOrigin, sessionHeader: "1"}, true},
		{"session origin in any case", map[string]string{"Origin": "https://WWW.Sitebin.IO", sessionHeader: "1"}, true},
		{"session origin without the marker", map[string]string{"Origin": frontDoorOrigin}, false},
		{"session origin with a wrong marker", map[string]string{"Origin": frontDoorOrigin, sessionHeader: "true"}, false},
		{"plain embed origin with the marker", map[string]string{"Origin": plainEmbedOrigin, sessionHeader: "1"}, false},
		{"own host with the marker", map[string]string{"Origin": "http://sitebin.example", sessionHeader: "1"}, false},
		{"no origin with the marker", map[string]string{sessionHeader: "1"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", "/api/sites", nil)
			for k, v := range c.headers {
				req.Header.Set(k, v)
			}
			if got := e.api.sessionOriginRequest(req); got != c.want {
				t.Errorf("sessionOriginRequest = %v, want %v", got, c.want)
			}
		})
	}
}

func TestCrossOriginBrowser(t *testing.T) {
	e := newEnv(t, nil)
	cases := []struct {
		name    string
		headers map[string]string
		want    bool
	}{
		{"no metadata (curl)", nil, false},
		{"same-origin", map[string]string{"Sec-Fetch-Site": "same-origin"}, false},
		{"own host origin", map[string]string{"Origin": "http://sitebin.example"}, false},
		{"own host in another scheme", map[string]string{"Origin": "https://sitebin.example", "Sec-Fetch-Site": "same-origin"}, false},
		{"foreign origin", map[string]string{"Origin": "https://evil.example"}, true},
		{"opaque origin", map[string]string{"Origin": "null"}, true},
		{"same-site", map[string]string{"Sec-Fetch-Site": "same-site"}, true},
		{"cross-site", map[string]string{"Sec-Fetch-Site": "cross-site"}, true},
		{"user-initiated", map[string]string{"Sec-Fetch-Site": "none"}, true},
		{"own host origin but same-site", map[string]string{"Origin": "http://sitebin.example", "Sec-Fetch-Site": "same-site"}, true},
		{"foreign origin claiming same-origin", map[string]string{"Origin": "https://evil.example", "Sec-Fetch-Site": "same-origin"}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", "/api/sites", nil)
			for k, v := range c.headers {
				req.Header.Set(k, v)
			}
			if got := e.api.crossOriginBrowser(req); got != c.want {
				t.Errorf("crossOriginBrowser = %v, want %v", got, c.want)
			}
		})
	}
}

// Only the cookie is taken away: a bearer token on a cross-origin request
// still names its account, as it always has (a browser cannot send one
// cross-origin without a preflight no origin passes).
func TestCreateCrossOriginKeepsTheBearer(t *testing.T) {
	p := &sessionProvider{
		fakeProvider: &fakeProvider{enabled: true, embedOK: true, ownerFromBearer: true,
			bearer: map[string]string{"sbp_good": "acct-2"}},
		sessions: map[string]string{"owner-cookie": "acct-1"},
	}
	registerProvider(t, p)
	e := newEnv(t, map[string]string{"SITEBIN_EMBED_ORIGINS": plainEmbedOrigin})
	w := scriptCreate(t, e, map[string]string{
		"Origin":         plainEmbedOrigin,
		"Sec-Fetch-Site": "cross-site",
		"Cookie":         "sb_test=owner-cookie",
		"Authorization":  "Bearer sbp_good",
	})
	created(t, e, w, "acct-2")
}

// A front-door creation is recorded like any signed-in creation from a
// browser: the UI surface, the session credential, the account.
func TestCreateFromSessionOriginProvenance(t *testing.T) {
	e, _ := frontDoorEnv(t)
	c := created(t, e, dropFrom(t, e, frontDoorOrigin, "same-site", true, "owner-cookie"), "acct-1")
	es := e.trail(t, c.ID)
	if len(es) != 1 {
		t.Fatalf("want one entry, got %+v", es)
	}
	if got := es[0]; got.Action != provenance.ActionCreate || got.Surface != provenance.SurfaceUI ||
		got.Auth != provenance.AuthSession || got.Account != "acct-1" {
		t.Errorf("create entry: %+v", got)
	}
}

// With the front door on, GET / on the base host sends a signed-in visitor to
// the dashboard (303) and everyone else to the front door (302). A non-base
// host is still a 404, and with the front door off the landing page stays.
func TestFrontDoorRedirects(t *testing.T) {
	const home = "https://sitebin.io"
	frontEnv := func(t *testing.T) *env {
		t.Helper()
		registerProvider(t, &sessionProvider{
			fakeProvider: &fakeProvider{enabled: true, embedOK: true},
			sessions:     map[string]string{"owner-cookie": "acct-1"},
		})
		return newEnv(t, map[string]string{
			"SITEBIN_FRONT_DOOR":      "true",
			"SITEBIN_HOME_URL":        home,
			"SITEBIN_EMBED_ORIGINS":   home,
			"SITEBIN_SESSION_ORIGINS": home,
		})
	}

	t.Run("signed out goes to the front door", func(t *testing.T) {
		e := frontEnv(t)
		w := e.public(t, httptest.NewRequest("GET", "/", nil))
		if w.Code != 302 || w.Header().Get("Location") != home {
			t.Fatalf("got %d %q, want 302 %q", w.Code, w.Header().Get("Location"), home)
		}
	})
	t.Run("an unknown cookie is signed out", func(t *testing.T) {
		e := frontEnv(t)
		req := httptest.NewRequest("GET", "/", nil)
		req.Header.Set("Cookie", "sb_test=stale")
		w := e.public(t, req)
		if w.Code != 302 || w.Header().Get("Location") != home {
			t.Fatalf("got %d %q, want 302 %q", w.Code, w.Header().Get("Location"), home)
		}
	})
	t.Run("signed in goes to the account", func(t *testing.T) {
		e := frontEnv(t)
		req := httptest.NewRequest("GET", "/", nil)
		req.Header.Set("Cookie", "sb_test=owner-cookie")
		w := e.public(t, req)
		if w.Code != 303 || w.Header().Get("Location") != "/account" {
			t.Fatalf("got %d %q, want 303 /account", w.Code, w.Header().Get("Location"))
		}
	})
	t.Run("another host is still a 404", func(t *testing.T) {
		e := frontEnv(t)
		req := httptest.NewRequest("GET", "/", nil)
		req.Host = "other.example"
		w := e.public(t, req)
		if w.Code != 404 {
			t.Fatalf("got %d, want 404", w.Code)
		}
	})
	t.Run("front door off keeps the landing page", func(t *testing.T) {
		registerProvider(t, &sessionProvider{
			fakeProvider: &fakeProvider{enabled: true},
			sessions:     map[string]string{"owner-cookie": "acct-1"},
		})
		e := newEnv(t, nil)
		req := httptest.NewRequest("GET", "/", nil)
		req.Header.Set("Cookie", "sb_test=owner-cookie")
		w := e.public(t, req)
		if w.Code != 200 || !strings.Contains(w.Body.String(), "landing") {
			t.Fatalf("got %d, want the landing page", w.Code)
		}
	})
}
