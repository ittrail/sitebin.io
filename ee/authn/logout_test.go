//go:build ee

package authn

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/ittrail/sitebin.io/ee/account"
	"github.com/ittrail/sitebin.io/ee/eeconfig"
)

// issuerWithLogout serves a discovery document for an issuer at its own URL
// that advertises endSession as its end_session_endpoint ("" = none). The
// end-session URL is built from the server's URL when it starts with "/".
func issuerWithLogout(t *testing.T, endSession string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		doc := map[string]any{
			"issuer":                                srv.URL,
			"authorization_endpoint":                srv.URL + "/protocol/openid-connect/auth",
			"token_endpoint":                        srv.URL + "/protocol/openid-connect/token",
			"jwks_uri":                              srv.URL + "/protocol/openid-connect/certs",
			"id_token_signing_alg_values_supported": []string{"RS256"},
		}
		if strings.HasPrefix(endSession, "/") {
			doc["end_session_endpoint"] = srv.URL + endSession
		} else if endSession != "" {
			doc["end_session_endpoint"] = endSession
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(doc)
	})
	return srv
}

// hint makes an ID-token-shaped string with the given claims. Its signature
// is junk: Sitebin never verifies a hint, the identity provider does.
func hint(t *testing.T, claims map[string]any) string {
	t.Helper()
	enc := func(v any) string {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return base64.RawURLEncoding.EncodeToString(b)
	}
	return enc(map[string]any{"alg": "RS256", "kid": "k1"}) + "." + enc(claims) + ".c2ln"
}

const (
	logoutSubject = "7c9e6679-7425-40de-944b-e07fc1f90ae7"
	returnTo      = "https://sitebin.example/account/signed-out"
)

func logoutOIDC(t *testing.T, srv *httptest.Server) *OIDC {
	t.Helper()
	return NewOIDC(eeconfig.Config{OIDC: &eeconfig.GenericOIDC{Issuer: srv.URL, ClientID: "sitebin-app"}}, "https://sitebin.example")
}

func mustQuery(t *testing.T, raw string) (*url.URL, url.Values) {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("logout URL %q: %v", raw, err)
	}
	return u, u.Query()
}

func TestLogoutURLCarriesTheHintTheClientAndTheReturnAddress(t *testing.T) {
	srv := issuerWithLogout(t, "/protocol/openid-connect/logout")
	m := logoutOIDC(t, srv)
	h := hint(t, map[string]any{"iss": srv.URL, "sub": logoutSubject, "azp": "sitebin-app", "aud": "sitebin-app", "exp": 1})

	got, err := m.LogoutURL(context.Background(), account.OIDCProv, logoutSubject, h, returnTo)
	if err != nil {
		t.Fatal(err)
	}
	u, q := mustQuery(t, got)
	if u.Scheme+"://"+u.Host+u.Path != srv.URL+"/protocol/openid-connect/logout" {
		t.Errorf("logout URL %q does not go to the advertised end_session_endpoint", got)
	}
	if q.Get("client_id") != "sitebin-app" {
		t.Errorf("client_id = %q", q.Get("client_id"))
	}
	if q.Get("post_logout_redirect_uri") != returnTo {
		t.Errorf("post_logout_redirect_uri = %q", q.Get("post_logout_redirect_uri"))
	}
	// Expired (exp 1970) and still sent: Keycloak checks a hint's signature,
	// not its expiry, and its ID tokens live for minutes.
	if q.Get("id_token_hint") != h {
		t.Errorf("id_token_hint = %q, want the stored token even though it has expired", q.Get("id_token_hint"))
	}
}

// An end-session endpoint that already carries a query keeps it.
func TestLogoutURLKeepsTheEndpointsOwnQuery(t *testing.T) {
	srv := issuerWithLogout(t, "/logout?tenant=acme")
	m := logoutOIDC(t, srv)
	got, err := m.LogoutURL(context.Background(), account.OIDCProv, logoutSubject, "", returnTo)
	if err != nil {
		t.Fatal(err)
	}
	if _, q := mustQuery(t, got); q.Get("tenant") != "acme" || q.Get("client_id") != "sitebin-app" {
		t.Errorf("logout URL %q lost the endpoint's own query or the client", got)
	}
}

// Without a hint the request is client_id + post_logout_redirect_uri alone:
// the identity provider may ask "do you want to log out?", but it knows who
// is asking and where to send the browser back.
func TestLogoutURLWithoutAHintSendsTheClientAlone(t *testing.T) {
	srv := issuerWithLogout(t, "/logout")
	m := logoutOIDC(t, srv)
	got, err := m.LogoutURL(context.Background(), account.OIDCProv, logoutSubject, "", returnTo)
	if err != nil {
		t.Fatal(err)
	}
	_, q := mustQuery(t, got)
	if q.Has("id_token_hint") {
		t.Errorf("an empty hint was sent: %q", got)
	}
	if q.Get("client_id") != "sitebin-app" || q.Get("post_logout_redirect_uri") != returnTo {
		t.Errorf("logout URL %q", got)
	}
}

// A hint is sent only when it is this account's, from this issuer, for this
// client. Keycloak answers a hint issued to another client with an error
// page and one of another session with a confirmation, so a stale or planted
// cookie must never reach it -- the sign-out goes with client_id alone.
func TestLogoutURLDropsAHintThatIsNotThisAccounts(t *testing.T) {
	srv := issuerWithLogout(t, "/logout")
	m := logoutOIDC(t, srv)
	good := map[string]any{"iss": srv.URL, "sub": logoutSubject, "azp": "sitebin-app"}
	with := func(k string, v any) map[string]any {
		c := map[string]any{}
		for kk, vv := range good {
			c[kk] = vv
		}
		if v == nil {
			delete(c, k)
		} else {
			c[k] = v
		}
		return c
	}
	cases := map[string]string{
		"another subject":       hint(t, with("sub", "someone-else")),
		"another client":        hint(t, with("azp", "other-app")),
		"another issuer":        hint(t, with("iss", "https://evil.example/realms/x")),
		"no subject":            hint(t, with("sub", nil)),
		"payload not base64":    "eyJhbGciOiJSUzI1NiJ9.!!!.c2ln",
		"payload not json":      "eyJhbGciOiJSUzI1NiJ9." + base64.RawURLEncoding.EncodeToString([]byte("nope")) + ".c2ln",
		"two segments":          "eyJhbGciOiJSUzI1NiJ9.eyJzdWIiOiJ4In0",
		"aud of another client": hint(t, with("azp", nil)),
	}
	for name, h := range cases {
		got, err := m.LogoutURL(context.Background(), account.OIDCProv, logoutSubject, h, returnTo)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if _, q := mustQuery(t, got); q.Has("id_token_hint") || q.Get("client_id") != "sitebin-app" {
			t.Errorf("%s: logout URL %q, want client_id alone", name, got)
		}
	}
	// No azp but this client in aud: the spec's other way of naming it.
	audOnly := hint(t, map[string]any{"iss": srv.URL, "sub": logoutSubject, "aud": []string{"sitebin-app", "account"}})
	got, _ := m.LogoutURL(context.Background(), account.OIDCProv, logoutSubject, audOnly, returnTo)
	if _, q := mustQuery(t, got); q.Get("id_token_hint") != audOnly {
		t.Errorf("a hint naming this client in aud was dropped: %q", got)
	}
}

// A provider that advertises no end_session_endpoint has no session to end
// from here: the answer is "", not an error, and the sign-out goes straight
// to the signed-out page.
func TestNoEndSessionEndpointMeansNoProviderLogout(t *testing.T) {
	srv := issuerWithLogout(t, "")
	m := logoutOIDC(t, srv)
	got, err := m.LogoutURL(context.Background(), account.OIDCProv, logoutSubject, "", returnTo)
	if err != nil || got != "" {
		t.Errorf("LogoutURL = %q, %v; want \"\", nil", got, err)
	}
	// Nor one that is not an http(s) URL.
	odd := issuerWithLogout(t, "javascript:alert(1)")
	if got, err := logoutOIDC(t, odd).LogoutURL(context.Background(), account.OIDCProv, logoutSubject, "", returnTo); err != nil || got != "" {
		t.Errorf("a non-http end_session_endpoint was used: %q, %v", got, err)
	}
}

// Google and Microsoft signed in directly are never logged out at the
// provider: their sign-out would end the person's whole Google or Microsoft
// session -- mail included -- for leaving Sitebin. Answered without a network
// call.
func TestDirectProvidersAreNeverLoggedOut(t *testing.T) {
	m := NewOIDC(eeconfig.Config{
		Google:    &eeconfig.OAuthProvider{ClientID: "gid"},
		Microsoft: &eeconfig.OAuthProvider{ClientID: "mid", Tenant: "common"},
	}, "https://sitebin.example")
	for _, prov := range []account.Provider{account.Google, account.Microsoft, account.Local, "nonsense"} {
		got, err := m.LogoutURL(context.Background(), prov, logoutSubject, "a.b.c", returnTo)
		if err != nil || got != "" {
			t.Errorf("%s: LogoutURL = %q, %v; want no provider logout", prov, got, err)
		}
	}
}

// A discovery that fails is an error: the caller signs the person out of
// Sitebin anyway and says so in the log.
func TestLogoutURLDiscoveryFailureIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	srv.Close()
	m := NewOIDC(eeconfig.Config{OIDC: &eeconfig.GenericOIDC{Issuer: srv.URL, ClientID: "sitebin-app"}}, "https://sitebin.example")
	if got, err := m.LogoutURL(context.Background(), account.OIDCProv, logoutSubject, "", returnTo); err == nil || got != "" {
		t.Errorf("LogoutURL = %q, %v; want an error", got, err)
	}
}

// A fresh sign-in asks who is signing in: prompt=login for the operator's
// issuer (Keycloak shows its login page, where password, Google or Microsoft
// can be chosen), prompt=select_account for Google and Microsoft signed in
// directly. An ordinary sign-in carries no prompt, so single sign-on from the
// stack's hub stays one click.
func TestFreshSignInAsksWhoIsSigningIn(t *testing.T) {
	srv := issuerWithLogout(t, "/logout")
	m := logoutOIDC(t, srv)

	plain, err := m.AuthCodeURL(context.Background(), account.OIDCProv, "st", "no", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, q := mustQuery(t, plain); q.Has("prompt") {
		t.Errorf("an ordinary sign-in carries prompt=%q; SSO would stop being one click", q.Get("prompt"))
	}
	fresh, err := m.AuthCodeURL(context.Background(), account.OIDCProv, "st", "no", true)
	if err != nil {
		t.Fatal(err)
	}
	if _, q := mustQuery(t, fresh); q.Get("prompt") != "login" || q.Get("state") != "st" || q.Get("nonce") != "no" {
		t.Errorf("fresh sign-in URL %q, want prompt=login with state and nonce", fresh)
	}

	direct := NewOIDC(eeconfig.Config{
		Google:    &eeconfig.OAuthProvider{ClientID: "gid"},
		Microsoft: &eeconfig.OAuthProvider{ClientID: "mid", Tenant: "common"},
	}, "https://sitebin.example")
	for _, prov := range []account.Provider{account.Google, account.Microsoft} {
		if got := direct.providers[prov].freshPrompt; got != "select_account" {
			t.Errorf("%s fresh prompt = %q, want select_account", prov, got)
		}
	}
}
