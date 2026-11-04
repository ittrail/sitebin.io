package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/ittrail/sitebin.io/internal/auth"
	"github.com/ittrail/sitebin.io/internal/ext"
	"github.com/ittrail/sitebin.io/internal/mcp"
	"github.com/ittrail/sitebin.io/internal/provenance"
	"github.com/ittrail/sitebin.io/internal/store"
)

// ---- the Git credential gate ----
//
// Git sends HTTP Basic and nothing else, so the gate reads the password and
// decides by its shape what it is: an upload token (refused), an account
// token, an OAuth access token (three base64url segments), or the site's
// edit password. A credential that fails is refused; it is never tried again
// as a weaker one, and a browser session never counts.

// OAuth-shaped secrets: three base64url segments, as a compact JWS is. The
// fake below does not verify them; it only knows them by name.
const (
	jwtRead    = "eyJhbGciOiJSUzI1NiJ9.eyJzY29wZSI6InJlYWQifQ.sig-read"
	jwtWrite   = "eyJhbGciOiJSUzI1NiJ9.eyJzY29wZSI6IndyaXRlIn0.sig-write"
	jwtNone    = "eyJhbGciOiJSUzI1NiJ9.eyJzY29wZSI6IiJ9.sig-none"
	jwtOther   = "eyJhbGciOiJSUzI1NiJ9.eyJzdWIiOiJvdGhlciJ9.sig-other"
	jwtConsent = "eyJhbGciOiJSUzI1NiJ9.eyJzdWIiOiJsYXRlIn0.sig-consent"
	jwtDown    = "eyJhbGciOiJSUzI1NiJ9.eyJzdWIiOiJkb3duIn0.sig-down"
	// jwtAcct is JWS-shaped but resolves as an account token: no scopes,
	// which for anything but OAuth means unrestricted.
	jwtAcct = "eyJhbGciOiJSUzI1NiJ9.eyJzdWIiOiJhY2N0In0.sig-acct"
)

// gitFake is the extension as the Git gate meets it, shaped like
// ee.BearerCredential: an account token anywhere; an OAuth access token only
// on a request carrying an OAuth surface marker; a subject with a document
// outstanding refused with the refusal noted, and one whose consent the stack
// could not answer refused as unavailable — as ee's consent check does.
// It has sessions too ("owner-cookie" is acct-1), so a test can show the
// gate never reads them.
type gitFake struct {
	*sessionProvider
	// oauthEverywhere hands an OAuth credential out with or without the
	// marker, the way a broken extension would, so a test proves the core's
	// own half of the rule.
	oauthEverywhere bool
	// consentOutstanding marks an OAuth secret whose subject has a required
	// document outstanding.
	consentOutstanding map[string]bool
	// stackDown marks an OAuth secret whose consent the stack could not
	// answer for.
	stackDown map[string]bool

	mu        sync.Mutex
	sawCookie bool
	sawGit    []bool // per lookup: did it carry the Git marker?
}

func (g *gitFake) BearerCredential(r *http.Request) (ext.Credential, bool) {
	g.mu.Lock()
	if r.Header.Get("Cookie") != "" {
		g.sawCookie = true
	}
	g.sawGit = append(g.sawGit, ext.IsGitCaller(r.Context()))
	g.mu.Unlock()
	secret := fakeBearer(r)
	if g.oauth[secret] {
		if !g.oauthEverywhere && !ext.OAuthSurface(r.Context()) {
			return ext.Credential{}, false
		}
		if g.consentOutstanding[secret] {
			ext.NoteRefusal(r.Context(), ext.RefusalConsent)
			return ext.Credential{}, false
		}
		if g.stackDown[secret] {
			ext.NoteRefusal(r.Context(), ext.RefusalUnavailable)
			return ext.Credential{}, false
		}
	}
	return g.sessionProvider.BearerCredential(r)
}

func newGitFake(t *testing.T) *gitFake {
	t.Helper()
	g := &gitFake{
		sessionProvider: &sessionProvider{
			fakeProvider: &fakeProvider{
				enabled: true, owner: "acct-1",
				bearer: map[string]string{
					"sbp_owner": "acct-1", "sbp_other": "acct-2", "sbp_oauthish": "acct-1",
					jwtRead: "acct-1", jwtWrite: "acct-1", jwtNone: "acct-1", jwtOther: "acct-2", jwtConsent: "acct-1",
					jwtAcct: "acct-1", jwtDown: "acct-1",
				},
				scopes: map[string][]string{
					jwtRead:    {mcp.ScopeRead},
					jwtWrite:   {mcp.ScopeRead, mcp.ScopeWrite},
					jwtNone:    nil,
					jwtOther:   {mcp.ScopeRead, mcp.ScopeWrite},
					jwtConsent: {mcp.ScopeRead, mcp.ScopeWrite},
					jwtDown:    {mcp.ScopeRead, mcp.ScopeWrite},
				},
				oauth: map[string]bool{jwtRead: true, jwtWrite: true, jwtNone: true, jwtOther: true, jwtConsent: true, jwtDown: true, "sbp_oauthish": true},
			},
			sessions: map[string]string{"owner-cookie": "acct-1"},
		},
		consentOutstanding: map[string]bool{jwtConsent: true},
		stackDown:          map[string]bool{jwtDown: true},
	}
	registerProvider(t, g)
	return g
}

// gitReq is what a Git client sends for a fetch's first round: Basic, when
// pw or user is set.
func gitReq(user, pw string) *http.Request {
	r := httptest.NewRequest("GET", "/git/x.git/info/refs?service=git-upload-pack", nil)
	if user != "" || pw != "" {
		r.SetBasicAuth(user, pw)
	}
	return r
}

func siteByEdit(t *testing.T, e *env, edit string) *store.Site {
	t.Helper()
	site, err := e.st.ByEditID(edit)
	if err != nil {
		t.Fatal(err)
	}
	return site
}

func wantChallenge(t *testing.T, d *gitDenial, what string) {
	t.Helper()
	if d == nil {
		t.Fatalf("%s: let in, want a 401 Basic challenge", what)
	}
	if !d.challenge || d.status != http.StatusUnauthorized || d.refusal {
		t.Fatalf("%s: denial = %+v, want a 401 Basic challenge", what, *d)
	}
}

func wantActor(t *testing.T, act actor, d *gitDenial, want actor, what string) {
	t.Helper()
	if d != nil {
		t.Fatalf("%s: refused %+v, want %+v", what, *d, want)
	}
	if act != want {
		t.Fatalf("%s: actor = %+v, want %+v", what, act, want)
	}
}

// setEditPassword gives the site an edit password a test chooses, to stand
// a token-shaped string in for one.
func setEditPassword(t *testing.T, e *env, site *store.Site, pw string) *store.Site {
	t.Helper()
	if err := e.st.Update(site, func(m *store.Meta) error { m.EditPasswordHash = auth.HashPassword(pw); return nil }); err != nil {
		t.Fatal(err)
	}
	return siteByEdit(t, e, site.EditID)
}

func TestGitAuthNoCredentialChallenges(t *testing.T) {
	if gitBasicChallenge != `Basic realm="Sitebin"` {
		t.Fatalf("challenge = %q", gitBasicChallenge)
	}
	run := func(t *testing.T, e *env, edit string) {
		site := siteByEdit(t, e, edit)
		bearer := gitReq("", "")
		bearer.Header.Set("Authorization", "Bearer sbp_owner") // Git sends Basic only
		header := gitReq("", "")
		header.Header.Set("X-Edit-Password", "anything") // nor does it send this
		for name, r := range map[string]*http.Request{
			"no Authorization":         gitReq("", ""),
			"a Bearer header":          bearer,
			"X-Edit-Password":          header,
			"Basic with an empty pass": gitReq("drop", ""),
		} {
			for _, write := range []bool{false, true} {
				_, d := e.api.gitAuth(r, site, write)
				wantChallenge(t, d, name)
			}
		}
	}
	t.Run("community", func(t *testing.T) {
		e := newEnv(t, nil)
		run(t, e, editIDFrom(t, e.createSite(t, nil, map[string]string{"index.html": "x"}).EditURL))
	})
	t.Run("accounts", func(t *testing.T) {
		e := newEnv(t, oauthEnv())
		newGitFake(t)
		edit, _ := ownedSiteWithPassword(t, e)
		run(t, e, edit)
	})
}

// A browser session is no credential here. The owner's cookie, with every
// header the edit page sends, still leaves a wrong password a wrong password,
// and the extension is never shown the cookie.
func TestGitAuthIgnoresSessionCookie(t *testing.T) {
	e := newEnv(t, oauthEnv())
	g := newGitFake(t)
	edit, _ := ownedSiteWithPassword(t, e)
	site := siteByEdit(t, e, edit)

	withSession := func(pw string) *http.Request {
		r := gitReq("drop", pw)
		r.AddCookie(&http.Cookie{Name: "sb_test", Value: "owner-cookie"})
		r.Header.Set("X-Sitebin-Session", "1")
		r.Header.Set("Sec-Fetch-Site", "same-origin")
		return r
	}
	// The cookie on its own would pass sessionOwns: the test means something.
	if !e.api.sessionOwns(withSession(""), site) {
		t.Fatal("the fixture session does not own the site")
	}
	for _, write := range []bool{false, true} {
		_, d := e.api.gitAuth(withSession("wrong-password"), site, write)
		wantChallenge(t, d, "owner session + wrong password")
		_, d = e.api.gitAuth(withSession(""), site, write)
		wantChallenge(t, d, "owner session, no password")
		_, d = e.api.gitAuth(withSession("sbp_unknown"), site, write)
		wantChallenge(t, d, "owner session + unknown account token")
		_, d = e.api.gitAuth(withSession(jwtOther), site, write)
		wantChallenge(t, d, "owner session + another account's OAuth token")
	}
	// The credentials that do pass are judged without the cookie as well.
	if _, d := e.api.gitAuth(withSession("sbp_owner"), site, true); d != nil {
		t.Fatalf("owner token beside a session refused: %+v", *d)
	}
	if _, d := e.api.gitAuth(withSession(jwtWrite), site, true); d != nil {
		t.Fatalf("owner OAuth token beside a session refused: %+v", *d)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.sawCookie {
		t.Error("the extension was handed the request's cookie")
	}
}

func TestGitAuthEditPassword(t *testing.T) {
	t.Run("community", func(t *testing.T) {
		e := newEnv(t, nil)
		c := e.createSite(t, nil, map[string]string{"index.html": "x"})
		site := siteByEdit(t, e, editIDFrom(t, c.EditURL))
		for _, write := range []bool{false, true} {
			// The username is not checked: the tab suggests the drop's id,
			// a credential helper may send anything.
			for _, user := range []string{site.ViewID, "anything", "OAUTH-USER"} {
				act, d := e.api.gitAuth(gitReq(user, c.EditPassword), site, write)
				wantActor(t, act, d, actor{surface: provenance.SurfaceGit, auth: provenance.AuthPassword}, "edit password, user "+user)
			}
		}
	})
	t.Run("accounts", func(t *testing.T) {
		e := newEnv(t, oauthEnv())
		newGitFake(t)
		edit, pw := ownedSiteWithPassword(t, e)
		site := siteByEdit(t, e, edit)
		act, d := e.api.gitAuth(gitReq("drop", pw), site, true)
		// The password names no account, as on every other surface.
		wantActor(t, act, d, actor{surface: provenance.SurfaceGit, auth: provenance.AuthPassword}, "edit password on an owned site")
	})
}

// A wrong password is a wrong password, whatever else the request carries:
// the right password in a header Git never sends, or a working token as the
// (unchecked) username.
func TestGitAuthWrongPasswordNoFallback(t *testing.T) {
	e := newEnv(t, oauthEnv())
	newGitFake(t)
	edit, pw := ownedSiteWithPassword(t, e)
	site := siteByEdit(t, e, edit)

	_, d := e.api.gitAuth(gitReq("drop", "wrong-password"), site, false)
	wantChallenge(t, d, "wrong password")

	r := gitReq("drop", "wrong-password")
	r.Header.Set("X-Edit-Password", pw)
	_, d = e.api.gitAuth(r, site, true)
	wantChallenge(t, d, "wrong Basic password, right X-Edit-Password")

	_, d = e.api.gitAuth(gitReq("sbp_owner", "wrong-password"), site, true)
	wantChallenge(t, d, "owner token as the username, wrong password")

	_, d = e.api.gitAuth(gitReq(pw, "wrong-password"), site, true)
	wantChallenge(t, d, "right password as the username")
}

func TestGitAuthThrottled(t *testing.T) {
	e := newEnv(t, map[string]string{"SITEBIN_RATE_AUTH_PER_5MIN": "3"})
	c := e.createSite(t, nil, map[string]string{"index.html": "x"})
	site := siteByEdit(t, e, editIDFrom(t, c.EditURL))
	var d *gitDenial
	for i := 0; i < 5; i++ {
		_, d = e.api.gitAuth(gitReq("drop", "wrong"), site, false)
	}
	if d == nil || d.status != http.StatusTooManyRequests || d.challenge || d.refusal || d.msg == "" {
		t.Fatalf("after repeated failures: %+v, want a plain 429", d)
	}
	// The Git gate and the JSON API spend the same budget: one site, one
	// set of password limits, whichever door the guesses come through.
	if w := e.public(t, authed(httptest.NewRequest("GET", "/api/sites/"+site.EditID, nil), "wrong")); w.Code != 429 {
		t.Errorf("the API after the Git gate's failures = %d, want 429", w.Code)
	}
}

// An upload token opens only its own routes. It is refused outright — not a
// challenge, which would only make Git ask again — and never spends the
// password limits.
func TestGitAuthUploadTokenRefused(t *testing.T) {
	e := newEnv(t, map[string]string{"SITEBIN_RATE_AUTH_PER_5MIN": "3"})
	c := e.createSite(t, nil, map[string]string{"index.html": "x"})
	site := siteByEdit(t, e, editIDFrom(t, c.EditURL))
	for i := 0; i < 5; i++ {
		_, d := e.api.gitAuth(gitReq("drop", "sbu_0123456789abcdefghijklmnopqrstuvwxyzAB"), site, i%2 == 0)
		if d == nil || d.status != http.StatusForbidden || d.challenge || d.refusal || d.msg == "" {
			t.Fatalf("upload token: %+v, want a plain 403", d)
		}
	}
	// An upload token anywhere on the request is refused the same way, even
	// beside a password that would pass.
	r := gitReq("drop", c.EditPassword)
	r.Header.Set("X-Edit-Password", "sbu_0123456789abcdefghijklmnopqrstuvwxyzAB")
	if _, d := e.api.gitAuth(r, site, false); d == nil || d.status != http.StatusForbidden {
		t.Fatalf("an upload token beside the password: %+v, want a plain 403", d)
	}
	act, d := e.api.gitAuth(gitReq("drop", c.EditPassword), site, true)
	wantActor(t, act, d, actor{surface: provenance.SurfaceGit, auth: provenance.AuthPassword}, "the password after five upload tokens")
}

// An account token opens the sites its account stands on, and nothing else.
func TestGitAuthAccountToken(t *testing.T) {
	e := newEnv(t, oauthEnv())
	g := newGitFake(t)
	edit, _ := ownedSiteWithPassword(t, e)
	site := siteByEdit(t, e, edit)

	for _, write := range []bool{false, true} {
		act, d := e.api.gitAuth(gitReq("drop", "sbp_owner"), site, write)
		wantActor(t, act, d, actor{surface: provenance.SurfaceGit, auth: provenance.AuthToken, account: "acct-1"}, "the owner's token")
		_, d = e.api.gitAuth(gitReq("drop", "sbp_other"), site, write)
		wantChallenge(t, d, "another account's token")
	}

	// An account-token-shaped secret the extension calls OAuth is not an
	// account token, even where the extension hands it out without a marker.
	g.oauthEverywhere = true
	_, d := e.api.gitAuth(gitReq("drop", "sbp_oauthish"), site, false)
	wantChallenge(t, d, "an sbp_ secret the extension resolved as OAuth")
	g.oauthEverywhere = false

	// Only the OAuth path carries the Git marker; an account token needs none.
	g.mu.Lock()
	for i, git := range g.sawGit {
		if git {
			t.Errorf("lookup %d of an account token carried the Git marker", i)
		}
	}
	g.mu.Unlock()

	// An anonymous site belongs to no account: no token stands on it.
	anon := editIDFrom(t, e.createSite(t, nil, map[string]string{"index.html": "x"}).EditURL)
	anonSite := siteByEdit(t, e, anon)
	if err := e.st.Update(anonSite, func(m *store.Meta) error { m.OwnerAccountID = ""; return nil }); err != nil {
		t.Fatal(err)
	}
	anonSite = siteByEdit(t, e, anon)
	_, d = e.api.gitAuth(gitReq("drop", "sbp_owner"), anonSite, false)
	wantChallenge(t, d, "a token on an anonymous site")
}

// A token that fails is refused, never tried as the edit password — even a
// site whose password happens to be exactly that string.
func TestGitAuthBadTokenNeverFallsBack(t *testing.T) {
	const tokenShaped = "sbp_0123456789abcdefghijklmnopqrstuvwxyzAB"
	const jwsShaped = "eyJhbGciOiJub25lIn0.eyJzdWIiOiJ4In0.c2ln"
	run := func(t *testing.T, e *env, edit string) {
		site := siteByEdit(t, e, edit)
		for _, pw := range []string{tokenShaped, jwsShaped} {
			site = setEditPassword(t, e, site, pw)
			if !auth.VerifyPassword(site.Meta.EditPasswordHash, pw) {
				t.Fatal("the fixture password did not take")
			}
			for _, write := range []bool{false, true} {
				_, d := e.api.gitAuth(gitReq("drop", pw), site, write)
				wantChallenge(t, d, "an unknown token equal to the edit password ("+pw[:4]+"…)")
			}
		}
	}
	t.Run("community", func(t *testing.T) {
		e := newEnv(t, nil)
		run(t, e, editIDFrom(t, e.createSite(t, nil, map[string]string{"index.html": "x"}).EditURL))
	})
	t.Run("accounts", func(t *testing.T) {
		e := newEnv(t, oauthEnv())
		newGitFake(t)
		edit, _ := ownedSiteWithPassword(t, e)
		run(t, e, edit)
	})
}

// An OAuth token is held to what it was granted: read to fetch, write to
// push. A missing scope is a readable refusal, not a challenge — signing in
// again with the same grant would not help, and Git prints nothing useful
// for a bare 403.
func TestGitAuthOAuthScopes(t *testing.T) {
	e := newEnv(t, oauthEnv())
	g := newGitFake(t)
	edit, _ := ownedSiteWithPassword(t, e)
	site := siteByEdit(t, e, edit)
	owner := actor{surface: provenance.SurfaceGit, auth: provenance.AuthOAuth, account: "acct-1"}

	act, d := e.api.gitAuth(gitReq("OAUTH-USER", jwtRead), site, false)
	wantActor(t, act, d, owner, "a read token fetching")

	_, d = e.api.gitAuth(gitReq("OAUTH-USER", jwtRead), site, true)
	if d == nil || !d.refusal || d.challenge || d.msg != msgGitNoWrite {
		t.Fatalf("a read token pushing: %+v, want the refusal %q", d, msgGitNoWrite)
	}

	for _, write := range []bool{false, true} {
		act, d = e.api.gitAuth(gitReq("OAUTH-USER", jwtWrite), site, write)
		wantActor(t, act, d, owner, "a read+write token")
	}

	// No scopes at all is no grant at all for an OAuth token.
	_, d = e.api.gitAuth(gitReq("OAUTH-USER", jwtNone), site, false)
	if d == nil || !d.refusal || d.challenge || d.msg != msgGitNoRead {
		t.Fatalf("a token without scopes fetching: %+v, want the refusal %q", d, msgGitNoRead)
	}

	// Another account's token is a failed credential, whatever its scopes.
	_, d = e.api.gitAuth(gitReq("OAUTH-USER", jwtOther), site, false)
	wantChallenge(t, d, "another account's OAuth token")

	// A JWS-shaped secret is an OAuth token or nothing: one the extension
	// resolves as anything else is not let in unscoped through this door.
	for _, write := range []bool{false, true} {
		_, d = e.api.gitAuth(gitReq("OAUTH-USER", jwtAcct), site, write)
		wantChallenge(t, d, "a JWS-shaped secret that is not an OAuth credential")
	}

	// The extension was asked with the Git marker: that is what lets it
	// verify an OAuth token here at all.
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.sawGit) == 0 {
		t.Fatal("the extension was never asked")
	}
	for i, git := range g.sawGit {
		if !git {
			t.Errorf("OAuth lookup %d did not carry the Git marker", i)
		}
	}
}

// A token refused because its subject has a document outstanding is the one
// failure Git must explain: a credential helper keeps its refresh token on a
// 401 and would loop without ever opening a browser.
func TestGitAuthOAuthConsentIsReadableRefusal(t *testing.T) {
	e := newEnv(t, oauthEnv())
	newGitFake(t)
	edit, _ := ownedSiteWithPassword(t, e)
	site := siteByEdit(t, e, edit)

	const want = "sign in once at http://sitebin.example/account and accept the updated terms, then push again"
	for _, write := range []bool{false, true} {
		_, d := e.api.gitAuth(gitReq("OAUTH-USER", jwtConsent), site, write)
		if d == nil || !d.refusal || d.challenge || d.msg != want {
			t.Fatalf("consent outstanding (write=%v): %+v, want the readable refusal %q", write, d, want)
		}
	}
	// The note belongs to its own request: the next failure is a plain
	// challenge again.
	_, d := e.api.gitAuth(gitReq("OAUTH-USER", jwtOther), site, false)
	wantChallenge(t, d, "a refusal for another reason after a consent refusal")
}

// A stack that cannot say whether the person may use the token is a refusal
// Git prints, and one that says to wait — never "accept the updated terms",
// which the person may long have done.
func TestGitAuthOAuthUnavailableIsReadableRefusal(t *testing.T) {
	e := newEnv(t, oauthEnv())
	newGitFake(t)
	edit, _ := ownedSiteWithPassword(t, e)
	site := siteByEdit(t, e, edit)

	const want = "the sign-in service cannot be reached right now; try again in a minute"
	for _, write := range []bool{false, true} {
		_, d := e.api.gitAuth(gitReq("OAUTH-USER", jwtDown), site, write)
		if d == nil || !d.refusal || d.challenge || d.msg != want {
			t.Fatalf("stack unavailable (write=%v): %+v, want the readable refusal %q", write, d, want)
		}
	}
	_, d := e.api.gitAuth(gitReq("OAUTH-USER", jwtOther), site, false)
	wantChallenge(t, d, "a refusal for another reason after an unavailable one")
}

// /mcp installs no refusal slot: a token the verifier refuses for consent or
// for an unavailable stack is answered exactly like any other bad bearer.
func TestMCPAnswersNotedRefusalsAsBefore(t *testing.T) {
	e := newEnv(t, oauthEnv())
	newGitFake(t)
	editID, pw := ownedSiteWithPassword(t, e)
	body := toolCall(t, "get_site", map[string]any{"edit_id": editID, "edit_password": pw})

	plain := mcpRaw(t, e, body, bearerHeader("expired-or-forged"))
	if plain.Code != 401 || !strings.Contains(plain.Header().Get("WWW-Authenticate"), `error="invalid_token"`) {
		t.Fatalf("baseline bad bearer = %d %q", plain.Code, plain.Header().Get("WWW-Authenticate"))
	}
	for _, tok := range []string{jwtConsent, jwtDown} {
		rec := mcpRaw(t, e, body, bearerHeader(tok))
		if rec.Code != plain.Code || rec.Header().Get("WWW-Authenticate") != plain.Header().Get("WWW-Authenticate") || rec.Body.String() != plain.Body.String() {
			t.Errorf("a noted refusal on /mcp = %d %q %q, want the bad-bearer answer %d %q %q",
				rec.Code, rec.Header().Get("WWW-Authenticate"), rec.Body, plain.Code, plain.Header().Get("WWW-Authenticate"), plain.Body)
		}
	}
}

// The REST API still refuses an OAuth token, the very token the Git gate
// accepts — whether the extension holds its half of the rule or not.
func TestOAuthStillRefusedOnREST(t *testing.T) {
	for _, everywhere := range []bool{false, true} {
		name := "extension holds the rule"
		if everywhere {
			name = "extension hands OAuth out everywhere"
		}
		t.Run(name, func(t *testing.T) {
			e := newEnv(t, oauthEnv())
			g := newGitFake(t)
			g.oauthEverywhere = everywhere
			edit, _ := ownedSiteWithPassword(t, e)
			site := siteByEdit(t, e, edit)

			if _, d := e.api.gitAuth(gitReq("OAUTH-USER", jwtWrite), site, true); d != nil {
				t.Fatalf("the Git gate refused the token: %+v", *d)
			}
			g.mu.Lock()
			g.sawGit = nil
			g.mu.Unlock()

			get := httptest.NewRequest("GET", "/api/sites/"+edit, nil)
			get.Header.Set("Authorization", "Bearer "+jwtWrite)
			if w := e.public(t, get); w.Code != 401 {
				t.Errorf("GET /api/sites/{id} with the OAuth token = %d, want 401 (%s)", w.Code, w.Body)
			}
			// Basic is how Git carries it; the API reads Basic as an edit
			// password, and the token is not one.
			basic := httptest.NewRequest("GET", "/api/sites/"+edit, nil)
			basic.SetBasicAuth("OAUTH-USER", jwtWrite)
			if w := e.public(t, basic); w.Code != 401 {
				t.Errorf("GET /api/sites/{id} with the token as Basic = %d, want 401 (%s)", w.Code, w.Body)
			}
			put := httptest.NewRequest("PUT", "/api/sites/"+edit, strings.NewReader(`{"name":"taken over"}`))
			put.Header.Set("Content-Type", "application/json")
			put.Header.Set("Authorization", "Bearer "+jwtWrite)
			if w := e.public(t, put); w.Code != 401 {
				t.Errorf("PUT /api/sites/{id} with the OAuth token = %d, want 401 (%s)", w.Code, w.Body)
			}
			g.mu.Lock()
			for i, git := range g.sawGit {
				if git {
					t.Errorf("REST lookup %d carried the Git marker", i)
				}
			}
			g.mu.Unlock()
			if s := siteByEdit(t, e, edit); s.Meta.Name != "" {
				t.Errorf("the site was renamed to %q", s.Meta.Name)
			}
		})
	}
}
