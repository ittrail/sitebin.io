//go:build ee

package ee

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ittrail/sitebin.io/ee/account"
	"github.com/ittrail/sitebin.io/internal/ext"
)

// ---- a local authorization server ----
//
// JWT verification is tested against an httptest issuer and a key pair made
// here, never a live issuer: a test that reaches the network fails for
// reasons that have nothing to do with Sitebin. The tokens are signed by hand
// (RS256 over the compact serialisation) so the test depends on nothing the
// product does not.

const testResource = "https://sitebin.example/mcp" // fakeHost.MCPResource

var (
	testKeyOnce sync.Once
	testKey     *rsa.PrivateKey
)

func signingKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	testKeyOnce.Do(func() {
		k, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			panic(err)
		}
		testKey = k
	})
	return testKey
}

type testIssuer struct {
	srv *httptest.Server
	key *rsa.PrivateKey
	// down makes discovery answer 503, the way an issuer mid-restart does.
	down        atomic.Bool
	discoveries atomic.Int32
}

func newTestIssuer(t *testing.T) *testIssuer {
	t.Helper()
	ti := &testIssuer{key: signingKey(t)}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		ti.discoveries.Add(1)
		if ti.down.Load() {
			http.Error(w, "restarting", http.StatusServiceUnavailable)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"issuer":                                ti.srv.URL,
			"authorization_endpoint":                ti.srv.URL + "/auth",
			"token_endpoint":                        ti.srv.URL + "/token",
			"jwks_uri":                              ti.srv.URL + "/jwks",
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	mux.HandleFunc("GET /jwks", func(w http.ResponseWriter, r *http.Request) {
		pub := ti.key.PublicKey
		json.NewEncoder(w).Encode(map[string]any{"keys": []any{map[string]any{
			"kty": "RSA", "kid": "k1", "alg": "RS256", "use": "sig",
			"n": base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes()),
		}}})
	})
	ti.srv = httptest.NewServer(mux)
	t.Cleanup(ti.srv.Close)
	return ti
}

func (ti *testIssuer) URL() string { return ti.srv.URL }

// testSubject is a stack user id: a UUID, as Keycloak issues them.
const testSubject = "7c9e6679-7425-40de-944b-e07fc1f90ae7"

// accessClaims are the claims of an access token the stack issues for a
// client that asked for the MCP scopes.
func (ti *testIssuer) accessClaims() map[string]any {
	now := time.Now()
	return map[string]any{
		"iss":            ti.srv.URL,
		"sub":            testSubject,
		"aud":            []string{testResource, "account"},
		"exp":            now.Add(5 * time.Minute).Unix(),
		"iat":            now.Unix(),
		"typ":            "Bearer",
		"azp":            "mcp-client-1",
		"scope":          "openid email sitebin:sites:read sitebin:sites:write",
		"email":          "agent-owner@example.com",
		"email_verified": true,
	}
}

// sign makes a compact JWS. edit changes the claims first; header overrides
// or, with a nil value, removes a header field.
func (ti *testIssuer) sign(t *testing.T, edit func(map[string]any), header map[string]any) string {
	t.Helper()
	claims := ti.accessClaims()
	if edit != nil {
		edit(claims)
	}
	h := map[string]any{"alg": "RS256", "typ": "JWT", "kid": "k1"}
	for k, v := range header {
		if v == nil {
			delete(h, k)
		} else {
			h[k] = v
		}
	}
	enc := func(v any) string {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return base64.RawURLEncoding.EncodeToString(b)
	}
	input := enc(h) + "." + enc(claims)
	sum := sha256.Sum256([]byte(input))
	sig, err := rsa.SignPKCS1v15(rand.Reader, ti.key, crypto.SHA256, sum[:])
	if err != nil {
		t.Fatal(err)
	}
	return input + "." + base64.RawURLEncoding.EncodeToString(sig)
}

// knownSubject is an account lookup that knows one subject.
func knownSubject(subject, accountID string) accountLookup {
	return func(s string) (string, bool) {
		if s == subject {
			return accountID, true
		}
		return "", false
	}
}

// ---- verification ----

func TestMCPOAuthAcceptsAnAccessToken(t *testing.T) {
	ti := newTestIssuer(t)
	m := newMCPOAuth(ti.URL(), testResource, knownSubject(testSubject, "acct-1"))

	cred, ok := m.Verify(context.Background(), ti.sign(t, nil, nil))
	if !ok {
		t.Fatal("a valid access token was refused")
	}
	if cred.AccountID != "acct-1" || !cred.OAuth {
		t.Errorf("credential = %+v", cred)
	}
	if got := strings.Join(cred.Scopes, " "); got != "openid email sitebin:sites:read sitebin:sites:write" {
		t.Errorf("scopes = %q", got)
	}
}

// ---- only on /mcp ----

// A stack-shaped instance whose MCP issuer is the test issuer. The stack
// itself is stubbed: registration and consent both answer, consent always
// complete, so these tests are about where a token is honoured.
func setupMCPOAuthInstance(t *testing.T, ti *testIssuer, stack http.Handler) (*provider, *fakeHost) {
	t.Helper()
	if stack == nil {
		stack = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasSuffix(r.URL.Path, "/consent/status") {
				w.Write([]byte(`{"appId":"sitebin","userId":"` + testSubject + `","gate":"enabled","complete":true,"outstanding":[]}`))
				return
			}
			w.Write([]byte(`{}`))
		})
	}
	st := httptest.NewServer(stack)
	t.Cleanup(st.Close)
	t.Setenv("SITEBIN_ACCOUNT_MODE", "accounts")
	t.Setenv("SITEBIN_OAUTH_OIDC_ISSUER", ti.URL())
	t.Setenv("SITEBIN_OAUTH_OIDC_CLIENT_ID", "sitebin-app")
	t.Setenv("SITEBIN_STACK_URL", st.URL)
	t.Setenv("SITEBIN_STACK_APP_ID", "sitebin")
	t.Setenv("SITEBIN_STACK_ADMIN_KEY", "admin-key-for-tests")
	t.Setenv("SITEBIN_STACK_GDPR_SECRET", strings.Repeat("g", 32))
	p := newProvider()
	host := &fakeHost{dir: t.TempDir(), sites: &fakeSites{infos: map[string]ext.SiteInfo{}}, mcpIssuer: ti.URL()}
	if err := p.Init(host); err != nil {
		t.Fatalf("Init: %v", err)
	}
	return p, host
}

func bearerRequest(secret string, mcp bool) *http.Request {
	r := httptest.NewRequest("POST", "/mcp", nil)
	r.Header.Set("Authorization", "Bearer "+secret)
	if mcp {
		r = r.WithContext(ext.WithMCPCaller(r.Context()))
	}
	return r
}

// The token's audience is the MCP resource, so it is honoured only on a
// request that came through /mcp. Elsewhere it is not even verified.
func TestOAuthTokenIsHonouredOnlyOnMCP(t *testing.T) {
	ti := newTestIssuer(t)
	p, _ := setupMCPOAuthInstance(t, ti, nil)
	acc, err := p.accounts.CreateOAuth(account.OIDCProv, testSubject, "agent-owner@example.com", true, "")
	if err != nil {
		t.Fatal(err)
	}
	tok := ti.sign(t, nil, nil)

	if _, ok := p.BearerCredential(bearerRequest(tok, false)); ok {
		t.Error("BearerCredential honoured an OAuth token outside /mcp")
	}
	if _, ok := p.accountForAPI(bearerRequest(tok, false)); ok {
		t.Error("accountForAPI honoured an OAuth token outside /mcp")
	}
	if n := ti.discoveries.Load(); n != 0 {
		t.Errorf("a JSON API request made the verifier reach the issuer (%d discoveries)", n)
	}

	cred, ok := p.BearerCredential(bearerRequest(tok, true))
	if !ok || cred.AccountID != acc.ID || !cred.OAuth {
		t.Fatalf("BearerCredential on /mcp = %+v, %v", cred, ok)
	}
	if got, ok := p.accountForAPI(bearerRequest(tok, true)); !ok || got.ID != acc.ID {
		t.Fatalf("accountForAPI on /mcp = %v, %v", got, ok)
	}
	// create_site on /mcp resolves its owner through the same path.
	if g, err := p.AuthorizeCreate(bearerRequest(tok, true)); err != nil || g.OwnerAccountID != acc.ID {
		t.Fatalf("AuthorizeCreate on /mcp = %+v, %v", g, err)
	}
}

// An account API token is the JSON API's credential and is honoured
// everywhere, marker or not.
func TestAccountTokenIsHonouredEverywhere(t *testing.T) {
	ti := newTestIssuer(t)
	p, _ := setupMCPOAuthInstance(t, ti, nil)
	acc, err := p.accounts.CreateOAuth(account.OIDCProv, testSubject, "agent-owner@example.com", true, "")
	if err != nil {
		t.Fatal(err)
	}
	_, secret, err := p.accounts.CreateToken(acc, "ci")
	if err != nil {
		t.Fatal(err)
	}
	for _, mcp := range []bool{false, true} {
		cred, ok := p.BearerCredential(bearerRequest(secret, mcp))
		if !ok || cred.AccountID != acc.ID || cred.OAuth || len(cred.Scopes) != 0 {
			t.Errorf("mcp=%v: account token credential = %+v, %v", mcp, cred, ok)
		}
		if got, ok := p.accountForAPI(bearerRequest(secret, mcp)); !ok || got.ID != acc.ID {
			t.Errorf("mcp=%v: accountForAPI = %v, %v", mcp, got, ok)
		}
	}
}

// The audience check is the one that makes a shared authorization server safe
// to share: a token minted for another resource is worth nothing here.
func TestMCPOAuthRefusesAnotherAudience(t *testing.T) {
	ti := newTestIssuer(t)
	m := newMCPOAuth(ti.URL(), testResource, knownSubject(testSubject, "acct-1"))
	tok := ti.sign(t, func(c map[string]any) { c["aud"] = []string{"https://other.example/mcp", "account"} }, nil)
	if _, ok := m.Verify(context.Background(), tok); ok {
		t.Fatal("a token for another resource was accepted")
	}
}

// Only access tokens. A Keycloak ID token carries typ "ID" and can carry the
// resource in its audience, and it used to pass.
func TestMCPOAuthAcceptsOnlyAccessTokens(t *testing.T) {
	ti := newTestIssuer(t)
	m := newMCPOAuth(ti.URL(), testResource, knownSubject(testSubject, "acct-1"))
	cases := []struct {
		name   string
		claims func(map[string]any)
		header map[string]any
		want   bool
	}{
		{"keycloak access token", nil, nil, true},
		{"typ claim in lower case", func(c map[string]any) { c["typ"] = "bearer" }, nil, true},
		{"no typ claim", func(c map[string]any) { delete(c, "typ") }, nil, true},
		{"ID token", func(c map[string]any) { c["typ"] = "ID" }, nil, false},
		{"refresh token", func(c map[string]any) { c["typ"] = "Refresh" }, nil, false},
		{"empty typ claim", func(c map[string]any) { c["typ"] = "" }, nil, false},
		{"RFC 9068 header", nil, map[string]any{"typ": "at+jwt"}, true},
		{"RFC 9068 media type", nil, map[string]any{"typ": "application/at+jwt"}, true},
		{"header typ in upper case", nil, map[string]any{"typ": "AT+JWT"}, true},
		{"no header typ", nil, map[string]any{"typ": nil}, true},
		{"logout token header", nil, map[string]any{"typ": "logout+jwt"}, false},
		{"ID token header", nil, map[string]any{"typ": "id_token+jwt"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, ok := m.Verify(context.Background(), ti.sign(t, c.claims, c.header)); ok != c.want {
				t.Fatalf("accepted = %v, want %v", ok, c.want)
			}
		})
	}
}

func TestMCPOAuthRefusesAnExpiredToken(t *testing.T) {
	ti := newTestIssuer(t)
	m := newMCPOAuth(ti.URL(), testResource, knownSubject(testSubject, "acct-1"))
	tok := ti.sign(t, func(c map[string]any) { c["exp"] = time.Now().Add(-time.Minute).Unix() }, nil)
	if _, ok := m.Verify(context.Background(), tok); ok {
		t.Fatal("an expired token was accepted")
	}
}

// An issuer that is down at the first MCP call must not leave OAuth broken
// until the next restart: discovery is retried, at most every ten seconds.
func TestMCPOAuthRetriesAFailedDiscovery(t *testing.T) {
	ti := newTestIssuer(t)
	m := newMCPOAuth(ti.URL(), testResource, knownSubject(testSubject, "acct-1"))
	now := time.Now()
	m.now = func() time.Time { return now }
	tok := ti.sign(t, nil, nil)

	ti.down.Store(true)
	if _, ok := m.Verify(context.Background(), tok); ok {
		t.Fatal("a token was accepted with the issuer down")
	}
	ti.down.Store(false)

	// Inside the retry floor the failure stands; the issuer is not hammered
	// by every request while it restarts.
	now = now.Add(5 * time.Second)
	if _, ok := m.Verify(context.Background(), tok); ok {
		t.Fatal("discovery was retried inside ten seconds")
	}
	if n := ti.discoveries.Load(); n != 1 {
		t.Fatalf("%d discoveries inside the retry floor, want 1", n)
	}

	now = now.Add(6 * time.Second)
	if _, ok := m.Verify(context.Background(), tok); !ok {
		t.Fatal("the issuer is back, but the token is still refused")
	}
	if n := ti.discoveries.Load(); n != 2 {
		t.Errorf("%d discoveries, want 2", n)
	}
	// Once discovered, it stays discovered.
	if _, ok := m.Verify(context.Background(), tok); !ok || ti.discoveries.Load() != 2 {
		t.Errorf("a later call rediscovered the issuer (%d)", ti.discoveries.Load())
	}
}

// Discovery runs under its own timeout, not the first caller's context: a
// client that hung up during the very first call must not break OAuth for
// everyone after it.
func TestMCPOAuthCancelledFirstCallDoesNotPoisonLaterOnes(t *testing.T) {
	ti := newTestIssuer(t)
	m := newMCPOAuth(ti.URL(), testResource, knownSubject(testSubject, "acct-1"))
	tok := ti.sign(t, nil, nil)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	m.Verify(ctx, tok) // whatever it answers, it answers only for itself

	if _, ok := m.Verify(context.Background(), tok); !ok {
		t.Fatal("a cancelled first call poisoned the verifier")
	}
	if n := ti.discoveries.Load(); n != 1 {
		t.Errorf("%d discoveries, want 1: the cancelled call should have discovered under its own context", n)
	}
}

// ---- consent and auto-provision, in the verifier ----

// accountsMap is an account lookup a test can grow, standing in for the store.
type accountsMap struct {
	mu   sync.Mutex
	subs map[string]string
}

func (a *accountsMap) lookup(sub string) (string, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	id, ok := a.subs[sub]
	return id, ok
}

type provisionCall struct {
	subject, email string
	verified       bool
}

// A token whose subject has an outstanding document is refused, account or
// not: the gate is never bypassed.
func TestMCPOAuthRefusesWithoutConsent(t *testing.T) {
	ti := newTestIssuer(t)
	m := newMCPOAuth(ti.URL(), testResource, knownSubject(testSubject, "acct-1"))
	var asked []string
	complete := false
	m.consent = func(_ context.Context, sub string) bool { asked = append(asked, sub); return complete }
	tok := ti.sign(t, nil, nil)

	if _, ok := m.Verify(context.Background(), tok); ok {
		t.Fatal("a token was accepted with consent outstanding")
	}
	complete = true
	if _, ok := m.Verify(context.Background(), tok); !ok {
		t.Fatal("a token was refused with consent complete")
	}
	if len(asked) != 2 || asked[0] != testSubject {
		t.Errorf("consent asked for %v", asked)
	}
}

// A newcomer with complete consent gets the account a first browser sign-in
// would have created, once, instead of a 401 loop.
func TestMCPOAuthProvisionsANewcomerOnce(t *testing.T) {
	ti := newTestIssuer(t)
	accts := &accountsMap{subs: map[string]string{}}
	m := newMCPOAuth(ti.URL(), testResource, accts.lookup)
	m.consent = func(context.Context, string) bool { return true }
	var calls []provisionCall
	m.provision = func(sub, email string, verified bool) (string, error) {
		calls = append(calls, provisionCall{sub, email, verified})
		accts.mu.Lock()
		accts.subs[sub] = "acct-new"
		accts.mu.Unlock()
		return "acct-new", nil
	}
	tok := ti.sign(t, nil, nil)

	for i := 0; i < 2; i++ {
		cred, ok := m.Verify(context.Background(), tok)
		if !ok || cred.AccountID != "acct-new" || !cred.OAuth {
			t.Fatalf("call %d: %+v, %v", i, cred, ok)
		}
	}
	if len(calls) != 1 || calls[0] != (provisionCall{testSubject, "agent-owner@example.com", true}) {
		t.Errorf("provision calls = %+v", calls)
	}
}

func TestMCPOAuthProvisionRefusals(t *testing.T) {
	ti := newTestIssuer(t)
	cases := []struct {
		name    string
		claims  func(map[string]any)
		consent bool
		err     error
		wantRun bool
	}{
		{"no email claim", func(c map[string]any) { delete(c, "email") }, true, nil, false},
		{"blank email claim", func(c map[string]any) { c["email"] = "  " }, true, nil, false},
		{"consent outstanding", nil, false, nil, false},
		{"email belongs to another account", nil, true, account.ErrEmailTaken, true},
		{"store failure", nil, true, fmt.Errorf("disk full"), true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := newMCPOAuth(ti.URL(), testResource, knownSubject("someone-else", "acct-1"))
			m.consent = func(context.Context, string) bool { return c.consent }
			ran := false
			m.provision = func(string, string, bool) (string, error) {
				ran = true
				if c.err != nil {
					return "", c.err
				}
				return "acct-new", nil
			}
			if _, ok := m.Verify(context.Background(), ti.sign(t, c.claims, nil)); ok {
				t.Fatal("the token was accepted")
			}
			if ran != c.wantRun {
				t.Errorf("provision ran = %v, want %v", ran, c.wantRun)
			}
		})
	}
}

// email_verified arrives as a boolean from Keycloak and as a string from some
// other issuers; either is read, and neither fails the token.
func TestMCPOAuthReadsEmailVerifiedEitherWay(t *testing.T) {
	ti := newTestIssuer(t)
	for _, v := range []any{true, "true", false, "false"} {
		accts := &accountsMap{subs: map[string]string{}}
		m := newMCPOAuth(ti.URL(), testResource, accts.lookup)
		m.consent = func(context.Context, string) bool { return true }
		var got provisionCall
		m.provision = func(sub, email string, verified bool) (string, error) {
			got = provisionCall{sub, email, verified}
			return "acct-new", nil
		}
		if _, ok := m.Verify(context.Background(), ti.sign(t, func(c map[string]any) { c["email_verified"] = v }, nil)); !ok {
			t.Fatalf("email_verified=%#v: refused", v)
		}
		if want := v == true || v == "true"; got.verified != want {
			t.Errorf("email_verified=%#v read as %v", v, got.verified)
		}
	}
}

// ---- consent and auto-provision, wired into the provider ----

// stackStub answers the registration and hands consent questions to consent.
func stackStub(consent *consentStack) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/consent/status") {
			consent.srv.Config.Handler.ServeHTTP(w, r)
			return
		}
		w.Write([]byte(`{}`))
	})
}

func TestProviderProvisionsAStackUserFromAToken(t *testing.T) {
	ti := newTestIssuer(t)
	consent := newConsentStack(t)
	p, _ := setupMCPOAuthInstance(t, ti, stackStub(consent))
	tok := ti.sign(t, nil, nil)

	cred, ok := p.BearerCredential(bearerRequest(tok, true))
	if !ok {
		t.Fatal("a newcomer with complete consent was refused")
	}
	acc, err := p.accounts.ByOAuth(account.OIDCProv, testSubject)
	if err != nil || acc.ID != cred.AccountID {
		t.Fatalf("the account was not created under the subject: %v, %v", acc, err)
	}
	if acc.Email != "agent-owner@example.com" || !acc.EmailVerified {
		t.Errorf("account email = %q verified=%v", acc.Email, acc.EmailVerified)
	}
	again, ok := p.BearerCredential(bearerRequest(tok, true))
	if !ok || again.AccountID != acc.ID {
		t.Errorf("the second call got %+v, %v", again, ok)
	}
}

func TestProviderRefusesATokenWithConsentOutstanding(t *testing.T) {
	ti := newTestIssuer(t)
	consent := newConsentStack(t)
	consent.answer(200, outstandingBody)
	p, _ := setupMCPOAuthInstance(t, ti, stackStub(consent))
	tok := ti.sign(t, nil, nil)

	if _, ok := p.BearerCredential(bearerRequest(tok, true)); ok {
		t.Fatal("a token was honoured with consent outstanding")
	}
	if _, err := p.accounts.ByOAuth(account.OIDCProv, testSubject); err == nil {
		t.Error("an account was created for someone who has not consented")
	}
	// An existing account is held to the same gate.
	if _, err := p.accounts.CreateOAuth(account.OIDCProv, testSubject, "agent-owner@example.com", true, ""); err != nil {
		t.Fatal(err)
	}
	if _, ok := p.BearerCredential(bearerRequest(tok, true)); ok {
		t.Fatal("an existing account's token was honoured with consent outstanding")
	}
	consent.answer(500, `{}`)
	if _, ok := p.BearerCredential(bearerRequest(tok, true)); ok {
		t.Fatal("a token was honoured while the stack could not answer")
	}
}

// Without stack registration there is no gate to ask about and no stack
// identity to trust: an unknown subject is refused, as it always was.
func TestProviderWithoutAStackRefusesAnUnknownSubject(t *testing.T) {
	ti := newTestIssuer(t)
	t.Setenv("SITEBIN_ACCOUNT_MODE", "accounts")
	t.Setenv("SITEBIN_OAUTH_OIDC_ISSUER", ti.URL())
	t.Setenv("SITEBIN_OAUTH_OIDC_CLIENT_ID", "sitebin-app")
	p := newProvider()
	if err := p.Init(&fakeHost{dir: t.TempDir(), sites: &fakeSites{infos: map[string]ext.SiteInfo{}}, mcpIssuer: ti.URL()}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	tok := ti.sign(t, nil, nil)
	if _, ok := p.BearerCredential(bearerRequest(tok, true)); ok {
		t.Fatal("an unknown subject was honoured without a stack")
	}
	if _, err := p.accounts.ByOAuth(account.OIDCProv, testSubject); err == nil {
		t.Fatal("an account was created without a stack")
	}
	// A known one works, with no consent to ask about.
	acc, err := p.accounts.CreateOAuth(account.OIDCProv, testSubject, "agent-owner@example.com", true, "")
	if err != nil {
		t.Fatal(err)
	}
	if cred, ok := p.BearerCredential(bearerRequest(tok, true)); !ok || cred.AccountID != acc.ID {
		t.Fatalf("a known subject without a stack = %+v, %v", cred, ok)
	}
}

// ---- the issuer guard ----

// A mismatched MCP issuer stops the start with a message naming both
// variables; an instance with MCP OAuth unset starts whatever its sign-in is.
func TestInitRefusesAnMCPIssuerThatIsNotTheLoginIssuer(t *testing.T) {
	start := func(t *testing.T, loginIssuer, mcpIssuer string) error {
		t.Setenv("SITEBIN_ACCOUNT_MODE", "accounts")
		if loginIssuer != "" {
			t.Setenv("SITEBIN_OAUTH_OIDC_ISSUER", loginIssuer)
			t.Setenv("SITEBIN_OAUTH_OIDC_CLIENT_ID", "sitebin-app")
		}
		return newProvider().Init(&fakeHost{dir: t.TempDir(), sites: &fakeSites{infos: map[string]ext.SiteInfo{}}, mcpIssuer: mcpIssuer})
	}
	const realm = "https://auth.example.com/realms/saas-stack"

	t.Run("mismatch", func(t *testing.T) {
		err := start(t, realm, "https://auth.example.com/realms/other")
		if err == nil || !strings.Contains(err.Error(), "SITEBIN_MCP_OAUTH_ISSUER") || !strings.Contains(err.Error(), "SITEBIN_OAUTH_OIDC_ISSUER") {
			t.Fatalf("Init = %v", err)
		}
	})
	t.Run("no login issuer", func(t *testing.T) {
		if err := start(t, "", realm); err == nil {
			t.Fatal("Init accepted an MCP issuer with no sign-in issuer")
		}
	})
	t.Run("the same issuer", func(t *testing.T) {
		if err := start(t, realm, realm+"/"); err != nil {
			t.Fatalf("Init: %v", err)
		}
	})
	t.Run("MCP OAuth unset", func(t *testing.T) {
		if err := start(t, "", ""); err != nil {
			t.Fatalf("Init: %v", err)
		}
	})
}
