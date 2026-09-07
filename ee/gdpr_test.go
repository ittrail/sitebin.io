//go:build ee

package ee

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ittrail/sitebin.io/ee/account"
	"github.com/ittrail/sitebin.io/internal/ext"
)

// A stack instance: OIDC accounts, the GDPR secret, no self-registration (the
// declaration is stackreg_test's business; these tests are about what the
// declared endpoints DO).
func setupStackInstance(t *testing.T, secret string) (*provider, *fakeHost, http.Handler) {
	t.Helper()
	t.Setenv("SITEBIN_ACCOUNT_MODE", "tiers")
	t.Setenv("SITEBIN_TIERS", stackTiersJSON)
	t.Setenv("SITEBIN_DEFAULT_TIER", "free")
	t.Setenv("SITEBIN_OAUTH_OIDC_ISSUER", "https://auth.example.com/realms/saas-stack")
	t.Setenv("SITEBIN_OAUTH_OIDC_CLIENT_ID", "sitebin-app")
	if secret != "" {
		t.Setenv("SITEBIN_STACK_GDPR_SECRET", secret)
	}
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

// stackUser is an account the stack issued: an OIDC identity whose subject is
// the stack's user id, with two sites and a token — everything an erasure has
// to find.
func stackUser(t *testing.T, p *provider, host *fakeHost, subject, email string) (*account.Account, string) {
	t.Helper()
	acc, err := p.accounts.CreateOAuth(account.OIDCProv, subject, email, true, "free")
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"aaaaaaaaaaaaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbbbbbbbbbbbb"} {
		host.sites.site(id)
		info := host.sites.infos[id]
		info.ViewURL = "http://" + id + ".sitebin.example"
		info.Domains = []string{id[:3] + ".example.org"}
		host.sites.infos[id] = info
		if err := p.accounts.LinkSite(acc, id); err != nil {
			t.Fatal(err)
		}
	}
	_, secret, err := p.accounts.CreateToken(acc, "ci deploy")
	if err != nil {
		t.Fatal(err)
	}
	return acc, secret
}

// stackOrder signs an order exactly as the stack does: X-Signature over
// "<timestamp>.<body>", X-Timestamp the seconds that went into it.
func stackOrder(path, secret string, at time.Time, body string) *http.Request {
	ts := strconv.FormatInt(at.Unix(), 10)
	r := httptest.NewRequest("POST", path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Timestamp", ts)
	r.Header.Set("X-Signature", gdprSignature(secret, ts, []byte(body)))
	return r
}

func serve(mux http.Handler, r *http.Request) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	return w
}

func TestGDPRRoutesAbsentWithoutASecret(t *testing.T) {
	_, _, mux := setupStackInstance(t, "")
	for _, path := range []string{gdprDeletePath, gdprExportPath} {
		r := httptest.NewRequest("POST", path, strings.NewReader(`{"userId":"x"}`))
		if w := serve(mux, r); w.Code != http.StatusNotFound {
			t.Errorf("%s without a secret = %d, want 404: an endpoint nothing can verify must not exist", path, w.Code)
		}
	}
}

// Every way a request can fail to be the stack's, and the one thing that
// makes it the stack's.
func TestGDPRSignatureVerification(t *testing.T) {
	p, host, mux := setupStackInstance(t, testGDPRSecret)
	acc, _ := stackUser(t, p, host, "11111111-1111-4111-8111-111111111111", "subject@example.com")
	body := `{"userId":"` + acc.OAuthSubject + `","email":"subject@example.com"}`
	now := time.Now()

	refused := func(name string, r *http.Request) {
		t.Helper()
		w := serve(mux, r)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("%s: %d, want 401 (%s)", name, w.Code, w.Body)
		}
		if !strings.Contains(w.Body.String(), "invalid signature") {
			t.Errorf("%s: every refusal must read the same to the caller, got %q", name, w.Body)
		}
		// Refused means NOTHING happened: the account is still there.
		if _, err := p.accounts.ByID(acc.ID); err != nil {
			t.Fatalf("%s: a refused order deleted the account", name)
		}
	}

	refused("no headers at all", func() *http.Request {
		r := httptest.NewRequest("POST", gdprDeletePath, strings.NewReader(body))
		return r
	}())
	refused("wrong secret", stackOrder(gdprDeletePath, "not-the-secret-0123456789abcdef0123456789", now, body))
	refused("signature over the body alone (the documented trap)", func() *http.Request {
		r := stackOrder(gdprDeletePath, testGDPRSecret, now, body)
		mac := hmac.New(sha256.New, []byte(testGDPRSecret))
		mac.Write([]byte(body))
		r.Header.Set("X-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
		return r
	}())
	refused("bare digest without the sha256= prefix", func() *http.Request {
		r := stackOrder(gdprDeletePath, testGDPRSecret, now, body)
		r.Header.Set("X-Signature", strings.TrimPrefix(r.Header.Get("X-Signature"), "sha256="))
		return r
	}())
	refused("stale timestamp: a captured request replayed after the window", stackOrder(gdprDeletePath, testGDPRSecret, now.Add(-gdprMaxSkew-time.Minute), body))
	refused("timestamp from the future", stackOrder(gdprDeletePath, testGDPRSecret, now.Add(gdprMaxSkew+time.Minute), body))
	refused("timestamp altered after signing", func() *http.Request {
		r := stackOrder(gdprDeletePath, testGDPRSecret, now.Add(-gdprMaxSkew-time.Minute), body)
		// A replayer freshens the timestamp; the MAC covered the old one.
		r.Header.Set("X-Timestamp", strconv.FormatInt(now.Unix(), 10))
		return r
	}())
	refused("body altered after signing", func() *http.Request {
		r := stackOrder(gdprDeletePath, testGDPRSecret, now, body)
		r.Body = httptest.NewRequest("POST", "/", strings.NewReader(strings.Replace(body, acc.OAuthSubject, "22222222-2222-4222-8222-222222222222", 1))).Body
		return r
	}())
	refused("malformed timestamp", func() *http.Request {
		r := stackOrder(gdprDeletePath, testGDPRSecret, now, body)
		r.Header.Set("X-Timestamp", "yesterday")
		return r
	}())

	// Inside the window, both ways, is fine: clocks drift.
	for _, skew := range []time.Duration{-gdprMaxSkew + time.Second, 0, gdprMaxSkew - time.Second} {
		w := serve(mux, stackOrder(gdprExportPath, testGDPRSecret, now.Add(skew), body))
		if w.Code != 200 {
			t.Errorf("export with a %v skew = %d, want 200 (%s)", skew, w.Code, w.Body)
		}
	}

	// Verified but useless: the stack's fault, said plainly.
	w := serve(mux, stackOrder(gdprExportPath, testGDPRSecret, now, `{"email":"nobody@example.com"}`))
	if w.Code != http.StatusBadRequest {
		t.Errorf("an order without a userId = %d, want 400", w.Code)
	}
	w = serve(mux, stackOrder(gdprExportPath, testGDPRSecret, now, `not json`))
	if w.Code != http.StatusBadRequest {
		t.Errorf("a non-JSON order = %d, want 400", w.Code)
	}
}

func TestGDPRExportCarriesEverythingHeld(t *testing.T) {
	p, host, mux := setupStackInstance(t, testGDPRSecret)
	acc, tokenSecret := stackUser(t, p, host, "11111111-1111-4111-8111-111111111111", "subject@example.com")

	w := serve(mux, stackOrder(gdprExportPath, testGDPRSecret, time.Now(), `{"userId":"`+acc.OAuthSubject+`","email":"subject@example.com"}`))
	if w.Code != 200 {
		t.Fatalf("export = %d (%s)", w.Code, w.Body)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q", ct)
	}
	var out struct {
		Account *struct {
			ID           string `json:"id"`
			Provider     string `json:"provider"`
			Email        string `json:"email"`
			OAuthSubject string `json:"oauth_subject"`
			Tier         string `json:"tier"`
			PasswordHash string `json:"password_hash"`
		} `json:"account"`
		Sites []struct {
			ID      string   `json:"id"`
			ViewURL string   `json:"view_url"`
			Domains []string `json:"custom_domains"`
		} `json:"sites"`
		Tokens []struct {
			ID     string `json:"id"`
			Name   string `json:"name"`
			Prefix string `json:"prefix"`
		} `json:"api_tokens"`
		Sessions *struct {
			Stored       bool `json:"stored"`
			TokenVersion int  `json:"token_version"`
		} `json:"sessions"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("export is not JSON: %v: %s", err, w.Body)
	}
	if out.Account == nil || out.Account.ID != acc.ID || out.Account.Email != "subject@example.com" ||
		out.Account.OAuthSubject != acc.OAuthSubject || out.Account.Provider != "oidc" || out.Account.Tier != "free" {
		t.Errorf("account = %+v", out.Account)
	}
	if len(out.Sites) != 2 {
		t.Fatalf("sites = %+v, want both owned sites", out.Sites)
	}
	if out.Sites[0].ViewURL == "" || len(out.Sites[0].Domains) != 1 {
		t.Errorf("site metadata incomplete: %+v", out.Sites[0])
	}
	if len(out.Tokens) != 1 || out.Tokens[0].Name != "ci deploy" || out.Tokens[0].Prefix == "" {
		t.Errorf("tokens = %+v", out.Tokens)
	}
	// Never the secrets. The token's secret is not even stored; the export
	// must not somehow have it, and must not have a password hash either.
	if strings.Contains(w.Body.String(), tokenSecret) {
		t.Error("the export leaked an API token secret")
	}
	if strings.Contains(w.Body.String(), "password_hash") {
		t.Error("the export carried a password hash")
	}
	if out.Sessions == nil || out.Sessions.Stored || out.Sessions.TokenVersion != acc.TokenVersion {
		t.Errorf("sessions = %+v", out.Sessions)
	}

	// An export changes nothing.
	if _, err := p.accounts.ByID(acc.ID); err != nil {
		t.Fatal("the export deleted the account")
	}
	if len(host.sites.deleted) != 0 {
		t.Error("the export deleted sites")
	}
}

func TestGDPRExportOfAnUnknownUserIsEmptyNotAnError(t *testing.T) {
	_, _, mux := setupStackInstance(t, testGDPRSecret)
	w := serve(mux, stackOrder(gdprExportPath, testGDPRSecret, time.Now(), `{"userId":"99999999-9999-4999-8999-999999999999","email":"nobody@example.com"}`))
	if w.Code != 200 {
		t.Fatalf("export of an unknown user = %d, want 200 (%s)", w.Code, w.Body)
	}
	var out map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if string(out["account"]) != "null" || string(out["sites"]) != "[]" || string(out["api_tokens"]) != "[]" {
		t.Errorf("an unknown user must export nothing, in the same shape: %s", w.Body)
	}
}

func TestGDPRDeleteErasesEverythingAndIsIdempotent(t *testing.T) {
	p, host, mux := setupStackInstance(t, testGDPRSecret)
	acc, tokenSecret := stackUser(t, p, host, "11111111-1111-4111-8111-111111111111", "subject@example.com")
	// A dangling ownership marker for a site that was deleted elsewhere: the
	// erasure must step over it, not stop on it.
	if err := p.accounts.LinkSite(acc, "cccccccccccccccccccccccccc"); err != nil {
		t.Fatal(err)
	}
	// Somebody else's account must be untouched by all of it.
	other, err := p.accounts.CreateOAuth(account.OIDCProv, "22222222-2222-4222-8222-222222222222", "other@example.com", true, "free")
	if err != nil {
		t.Fatal(err)
	}
	host.sites.site("dddddddddddddddddddddddddd")
	p.accounts.LinkSite(other, "dddddddddddddddddddddddddd")

	// A live session, to prove it dies with the account.
	cookie := p.sessions.Cookie(acc.ID, acc.TokenVersion)
	dash := httptest.NewRequest("GET", "/account", nil)
	dash.AddCookie(cookie)
	if w := serve(mux, dash); w.Code != 200 {
		t.Fatalf("the session should work before the deletion, got %d", w.Code)
	}

	order := `{"userId":"` + acc.OAuthSubject + `","email":"subject@example.com"}`
	w := serve(mux, stackOrder(gdprDeletePath, testGDPRSecret, time.Now(), order))
	if w.Code != 200 {
		t.Fatalf("delete = %d (%s)", w.Code, w.Body)
	}
	var out struct {
		Status    string   `json:"status"`
		Found     bool     `json:"found"`
		Resources []string `json:"deletedResources"`
		Sites     int      `json:"sites"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Status != "deleted" || !out.Found || out.Sites != 2 || len(out.Resources) == 0 {
		t.Errorf("delete answered %+v", out)
	}

	// The account, its ownership markers and its indexes.
	if _, err := p.accounts.ByID(acc.ID); !errors.Is(err, account.ErrNotFound) {
		t.Errorf("account after deletion: %v", err)
	}
	if _, err := p.accounts.ByOAuth(account.OIDCProv, acc.OAuthSubject); !errors.Is(err, account.ErrNotFound) {
		t.Error("the oauth index still resolves the erased subject")
	}
	if _, err := p.accounts.ByEmail("subject@example.com"); !errors.Is(err, account.ErrNotFound) {
		t.Error("the email index still resolves the erased address")
	}
	// The sites.
	if len(host.sites.deleted) != 2 {
		t.Errorf("sites deleted = %v, want both owned sites", host.sites.deleted)
	}
	for _, id := range []string{"aaaaaaaaaaaaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbbbbbbbbbbbb"} {
		if _, ok := host.sites.infos[id]; ok {
			t.Errorf("site %s survived", id)
		}
	}
	// The tokens.
	if _, ok := p.accounts.ByToken(tokenSecret); ok {
		t.Error("an API token of the erased account still authenticates")
	}
	// The sessions: the cookie is still valid as a signature, and it is
	// refused because the record it names is gone.
	dash = httptest.NewRequest("GET", "/account", nil)
	dash.AddCookie(cookie)
	if w := serve(mux, dash); w.Code != http.StatusSeeOther {
		t.Errorf("the erased account's session still opens the dashboard: %d", w.Code)
	}
	// The other account, entirely intact.
	if _, err := p.accounts.ByID(other.ID); err != nil {
		t.Error("somebody else's account went with it")
	}
	if _, ok := host.sites.infos["dddddddddddddddddddddddddd"]; !ok {
		t.Error("somebody else's site went with it")
	}

	// Again: nothing to delete is the desired end state, and it is a 200 —
	// the stack treats any other answer as "the app still holds the data"
	// and never deletes the identity.
	w = serve(mux, stackOrder(gdprDeletePath, testGDPRSecret, time.Now(), order))
	if w.Code != 200 {
		t.Fatalf("second delete = %d, want 200 (%s)", w.Code, w.Body)
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Status != "deleted" || out.Found {
		t.Errorf("second delete answered %+v; want deleted and not found", out)
	}
}

// A site that cannot be deleted stops the order: the account stays, the
// stack gets a 5xx, keeps the identity, and the operator retries. What was
// deleted before the failure stays deleted and the retry steps over it.
func TestGDPRDeleteStopsOnAnUndeletableSite(t *testing.T) {
	p, host, mux := setupStackInstance(t, testGDPRSecret)
	acc, _ := stackUser(t, p, host, "11111111-1111-4111-8111-111111111111", "subject@example.com")
	host.sites.deleteErrs = map[string]error{"bbbbbbbbbbbbbbbbbbbbbbbbbb": errors.New("disk on fire")}

	order := `{"userId":"` + acc.OAuthSubject + `","email":"subject@example.com"}`
	w := serve(mux, stackOrder(gdprDeletePath, testGDPRSecret, time.Now(), order))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("delete with an undeletable site = %d, want 500 (%s)", w.Code, w.Body)
	}
	if _, err := p.accounts.ByID(acc.ID); err != nil {
		t.Fatal("the account was removed although one of its sites could not be: the stack would now delete the identity and orphan the site")
	}
	if _, ok := host.sites.infos["bbbbbbbbbbbbbbbbbbbbbbbbbb"]; !ok {
		t.Fatal("the undeletable site is gone from the fake; the test is not testing what it thinks")
	}

	// The operator fixed the disk. The retry finds the first site already
	// gone and the second deletable, and finishes.
	host.sites.deleteErrs = nil
	w = serve(mux, stackOrder(gdprDeletePath, testGDPRSecret, time.Now(), order))
	if w.Code != 200 {
		t.Fatalf("retry = %d (%s)", w.Code, w.Body)
	}
	if _, err := p.accounts.ByID(acc.ID); !errors.Is(err, account.ErrNotFound) {
		t.Errorf("account after the retry: %v", err)
	}
	if _, ok := host.sites.infos["bbbbbbbbbbbbbbbbbbbbbbbbbb"]; ok {
		t.Error("the second site survived the retry")
	}
}

// A local account is unknown to the stack, and an order naming a subject
// that happens to equal a local account's id must not find it: the lookup is
// by OIDC subject, never by anything else.
func TestGDPROrdersNeverReachLocalAccounts(t *testing.T) {
	p, _, mux := setupStackInstance(t, testGDPRSecret)
	local, err := p.accounts.CreateLocal("local@example.com", "$argon2$notreal", "free")
	if err != nil {
		t.Fatal(err)
	}
	w := serve(mux, stackOrder(gdprDeletePath, testGDPRSecret, time.Now(), `{"userId":"`+local.ID+`","email":"local@example.com"}`))
	if w.Code != 200 {
		t.Fatalf("delete = %d", w.Code)
	}
	if _, err := p.accounts.ByID(local.ID); err != nil {
		t.Error("a local account was erased on a stack order that could only have meant an OIDC subject")
	}
}
