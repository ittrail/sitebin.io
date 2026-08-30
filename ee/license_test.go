//go:build ee

package ee

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ittrail/sitebin.io/ee/licensing"
	"github.com/ittrail/sitebin.io/internal/ext"
)

// testChain mints licence keys the way the stack would, and points this build's
// trusted roots at its own root through the development-only override.
type testChain struct {
	rootPriv ed25519.PrivateKey
	appPub   ed25519.PublicKey
	appPriv  ed25519.PrivateKey
}

func newTestChain(t *testing.T) *testChain {
	t.Helper()
	rootPub, rootPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	appPub, appPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("SITEBIN_LICENSE_ROOTS_DEV", base64.StdEncoding.EncodeToString(rootPub))
	return &testChain{rootPriv, appPub, appPriv}
}

// key mints a licence with the given dates and entitlements.
func (c *testChain) key(t *testing.T, expires, grace time.Time, ent licensing.Entitlements) string {
	t.Helper()
	cert, err := licensing.SignCert(licensing.CertPayload{
		AppID:     licensing.AppID,
		PubKey:    base64.StdEncoding.EncodeToString(c.appPub),
		IssuedAt:  time.Now().Add(-time.Hour),
		ExpiresAt: time.Now().Add(10 * 365 * 24 * time.Hour),
	}, c.rootPriv)
	if err != nil {
		t.Fatal(err)
	}
	lic, err := licensing.SignLicense(licensing.LicensePayload{
		AppID:        licensing.AppID,
		Holder:       "ACME GmbH",
		Plan:         "team",
		IssuedAt:     time.Now().Add(-time.Hour),
		ExpiresAt:    expires,
		GraceUntil:   grace,
		Entitlements: ent,
	}, c.appPriv)
	if err != nil {
		t.Fatal(err)
	}
	return licensing.Key(cert, lic)
}

// licensedProvider boots a provider with the given licence key in the
// environment. An empty key means "no licence at all".
func licensedProvider(t *testing.T, key string) (*provider, *fakeHost) {
	t.Helper()
	t.Setenv("SITEBIN_ACCOUNT_MODE", "accounts")
	t.Setenv("SITEBIN_LICENSE_KEY", key)
	p := newProvider()
	host := &fakeHost{dir: t.TempDir(), sites: &fakeSites{infos: map[string]ext.SiteInfo{}}}
	if err := p.Init(host); err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() {
		if p.license != nil {
			p.license.Stop()
		}
	})
	return p, host
}

// The instance must start whatever the key says. This is the rule that used to
// be the other way round: an invalid key aborted Init.
func TestInitNeverFailsOnALicenseProblem(t *testing.T) {
	c := newTestChain(t)
	expired := c.key(t, time.Now().Add(-400*24*time.Hour), time.Now().Add(-300*24*time.Hour), licensing.Entitlements{})
	for name, key := range map[string]string{
		"garbage":       "not-a-license",
		"two segments":  "aaaa.bbbb",
		"expired":       expired,
		"empty":         "",
		"random base64": base64.RawURLEncoding.EncodeToString([]byte("x")) + ".a.b.c",
	} {
		t.Run(name, func(t *testing.T) {
			p, _ := licensedProvider(t, key)
			if p.license == nil {
				t.Fatal("licensing was not wired")
			}
		})
	}
}

// A key that cannot be verified is "none" with a trial — never "expired" — so
// creation keeps working on a fresh instance.
func TestUnverifiableKeyIsNoneAndDoesNotBlockCreation(t *testing.T) {
	newTestChain(t)
	p, _ := licensedProvider(t, "garbage.garbage.garbage.garbage")
	st := p.licenseStatus()
	if st.State != licensing.StateNone {
		t.Fatalf("state = %s, want none", st.State)
	}
	if st.Restricted {
		t.Fatal("an unverifiable key restricted creation on a fresh instance")
	}
	if err := p.licenseGate(); err != nil {
		t.Fatalf("licenseGate refused: %v", err)
	}
}

// Unknown is not expired: a provider whose licensing never loaded restricts
// nothing at all.
func TestAuthorizeCreatePermittedWhenLicenseUnknown(t *testing.T) {
	newTestChain(t)
	t.Setenv("SITEBIN_ACCOUNT_MODE", "open")
	t.Setenv("SITEBIN_LICENSE_KEY", "")
	p := newProvider()
	if err := p.Init(&fakeHost{dir: t.TempDir(), sites: &fakeSites{infos: map[string]ext.SiteInfo{}}}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	p.license.Stop()
	// The state cannot be determined: nothing has loaded. That is unknown, and
	// unknown restricts nothing — not creation, not domains.
	p.license = nil
	if got := p.licenseStatus().State; got != licensing.StateUnknown {
		t.Fatalf("state = %s, want unknown", got)
	}
	if err := p.licenseGate(); err != nil {
		t.Fatalf("an unknown state refused creation: %v", err)
	}
	if _, err := p.AuthorizeCreate(httptest.NewRequest("POST", "/api/sites", nil)); err != nil {
		t.Fatalf("AuthorizeCreate refused on an unknown licence state: %v", err)
	}
	if err := p.CustomDomainsAllowed(); err != nil {
		t.Error("an unknown licence state restricted custom domains")
	}
}

// The one place enforcement lives.
func TestAuthorizeCreateRefusedWhenExpired(t *testing.T) {
	c := newTestChain(t)
	key := c.key(t, time.Now().Add(-200*24*time.Hour), time.Now().Add(-100*24*time.Hour), licensing.Entitlements{})
	p, _ := licensedProvider(t, key)

	if got := p.licenseStatus().State; got != licensing.StateExpired {
		t.Fatalf("state = %s, want expired", got)
	}
	_, err := p.AuthorizeCreate(httptest.NewRequest("POST", "/api/sites", nil))
	var ce *ext.CreateError
	if !errors.As(err, &ce) || ce.Status != 402 {
		t.Fatalf("expected a 402 CreateError, got %v", err)
	}
}

// Grace is loud but permissive: service continues to grace_until.
func TestGraceStillPermitsCreation(t *testing.T) {
	c := newTestChain(t)
	key := c.key(t, time.Now().Add(-24*time.Hour), time.Now().Add(60*24*time.Hour), licensing.Entitlements{})
	p, _ := licensedProvider(t, key)

	if got := p.licenseStatus().State; got != licensing.StateGrace {
		t.Fatalf("state = %s, want grace", got)
	}
	if err := p.licenseGate(); err != nil {
		t.Fatalf("grace refused creation: %v", err)
	}
	if n := p.licenseNotice(); n == nil || n.Severity != "warn" {
		t.Fatalf("grace produced no warning notice: %+v", n)
	}
}

// An elapsed trial behaves as expired — and only then.
func TestElapsedTrialRefusesCreation(t *testing.T) {
	newTestChain(t)
	t.Setenv("SITEBIN_ACCOUNT_MODE", "accounts")
	t.Setenv("SITEBIN_LICENSE_KEY", "")
	dir := t.TempDir()
	// Pre-date the trial marker: this instance first ran 100 days ago.
	if err := os.MkdirAll(filepath.Join(dir, "license"), 0o700); err != nil {
		t.Fatal(err)
	}
	start := time.Now().Add(-100 * 24 * time.Hour).Format(time.RFC3339)
	if err := os.WriteFile(filepath.Join(dir, "license", "trial-start"), []byte(start), 0o600); err != nil {
		t.Fatal(err)
	}
	p := newProvider()
	if err := p.Init(&fakeHost{dir: dir, sites: &fakeSites{infos: map[string]ext.SiteInfo{}}}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() { p.license.Stop() })

	st := p.licenseStatus()
	if st.State != licensing.StateNone || !st.Restricted {
		t.Fatalf("state=%s restricted=%v, want none/true", st.State, st.Restricted)
	}
	var ce *ext.CreateError
	if _, err := p.AuthorizeCreate(httptest.NewRequest("POST", "/api/sites", nil)); !errors.As(err, &ce) || ce.Status != 402 {
		t.Fatalf("expected a 402 CreateError, got %v", err)
	}
	if n := p.licenseNotice(); n == nil || n.Severity != "err" {
		t.Fatalf("an elapsed trial produced no notice: %+v", n)
	}
}

// A licensed instance says nothing in the UI and restricts nothing.
func TestLicensedIsSilent(t *testing.T) {
	c := newTestChain(t)
	key := c.key(t, time.Now().Add(300*24*time.Hour), time.Now().Add(400*24*time.Hour), licensing.Entitlements{})
	p, _ := licensedProvider(t, key)

	st := p.licenseStatus()
	if st.State != licensing.StateLicensed || st.Holder != "ACME GmbH" {
		t.Fatalf("state=%s holder=%q", st.State, st.Holder)
	}
	if n := p.licenseNotice(); n != nil {
		t.Errorf("a licensed instance showed a notice: %+v", n)
	}
}

// ---- the custom-domain entitlement ----

// withDomains registers sites carrying the given numbers of custom domains.
func withDomains(host *fakeHost, counts ...int) {
	for i, n := range counts {
		id := string(rune('a' + i))
		doms := make([]string, n)
		for j := range doms {
			doms[j] = id + string(rune('0'+j)) + ".example"
		}
		host.sites.infos[id] = ext.SiteInfo{ViewID: id, Mode: "webserver", Domains: doms}
	}
}

func TestDomainEntitlementRefusesTheOneThatWouldExceedIt(t *testing.T) {
	c := newTestChain(t)
	key := c.key(t, time.Now().Add(300*24*time.Hour), time.Time{}, licensing.Entitlements{MaxCustomDomains: 3})
	p, host := licensedProvider(t, key)

	withDomains(host, 1, 1) // two configured, cap three
	if err := p.CustomDomainsAllowed(); err != nil {
		t.Error("the third domain was refused under a cap of three")
	}
	withDomains(host, 2, 1) // three configured
	if err := p.CustomDomainsAllowed(); err == nil {
		t.Error("the fourth domain was allowed past a cap of three")
	}
}

// The ceiling never removes anything. An instance already over its cap — a
// downgrade, or a cap introduced after the domains were added — keeps every
// domain it serves and is refused only the next one.
func TestDomainEntitlementNeverRemovesConfiguredDomains(t *testing.T) {
	c := newTestChain(t)
	key := c.key(t, time.Now().Add(300*24*time.Hour), time.Time{}, licensing.Entitlements{MaxCustomDomains: 1})
	p, host := licensedProvider(t, key)
	withDomains(host, 3, 2) // five configured under a cap of one

	if err := p.CustomDomainsAllowed(); err == nil {
		t.Error("another domain was allowed on an instance already over its cap")
	}
	if n, err := p.customDomainCount(); err != nil || n != 5 {
		t.Fatalf("count = %d, %v; the existing domains must be untouched", n, err)
	}
	if len(host.sites.deleted) != 0 {
		t.Errorf("the entitlement deleted sites: %v", host.sites.deleted)
	}
	for id, info := range host.sites.infos {
		if len(info.Domains) == 0 {
			t.Errorf("site %s lost its domains", id)
		}
	}
}

// Absent or zero entitlements mean unlimited, so a licence issued before the
// limit existed is not retroactively restrictive.
func TestAbsentDomainEntitlementIsUnlimited(t *testing.T) {
	c := newTestChain(t)
	key := c.key(t, time.Now().Add(300*24*time.Hour), time.Time{}, licensing.Entitlements{})
	p, host := licensedProvider(t, key)
	withDomains(host, 40, 40)
	if err := p.CustomDomainsAllowed(); err != nil {
		t.Error("a licence with no domain entitlement restricted custom domains")
	}

	// So does having no licence at all.
	p2, host2 := licensedProvider(t, "")
	withDomains(host2, 40)
	if err := p2.CustomDomainsAllowed(); err != nil {
		t.Error("an unlicensed instance restricted custom domains")
	}
}

// Counting failing is unknown, and unknown never restricts.
func TestDomainEntitlementAllowsWhenSitesCannotBeCounted(t *testing.T) {
	c := newTestChain(t)
	key := c.key(t, time.Now().Add(300*24*time.Hour), time.Time{}, licensing.Entitlements{MaxCustomDomains: 1})
	p, host := licensedProvider(t, key)
	withDomains(host, 5)
	host.sites.allErr = errors.New("cannot enumerate")
	if err := p.CustomDomainsAllowed(); err != nil {
		t.Error("an uncountable instance was refused a domain")
	}
}

// TestLicenseFetcherAuthenticatesWithTheLicence pins the correction that a
// customer's instance proves who it is with the licence itself. It must never
// send a stack credential: a self-hosted customer is not a registered app and
// must never hold the stack's admin key, which acts on every app there is.
func TestLicenseFetcherAuthenticatesWithTheLicence(t *testing.T) {
	var gotMethod, gotAuth, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotAuth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"license":"renewed.key.goes.here"}`)
	}))
	defer srv.Close()

	f := &stackLicenseFetcher{url: srv.URL, client: srv.Client()}
	key, ok, err := f.Fetch(context.Background(), "current.licence.four.segments")
	if err != nil {
		t.Fatal(err)
	}
	if !ok || key != "renewed.key.goes.here" {
		t.Errorf("key = %q ok = %v", key, ok)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %s, want POST", gotMethod)
	}
	if gotAuth != "" {
		t.Errorf("Authorization = %q: the renewal route takes no credential, and must never carry the stack admin key", gotAuth)
	}
	if !strings.Contains(gotBody, "current.licence.four.segments") {
		t.Errorf("body must present the current licence as the credential: %s", gotBody)
	}
}

// Without a licence there is nothing to authenticate with. That is an answer,
// not a failure: the first licence arrives by email, never from this route.
func TestLicenseFetcherWithoutALicenceAsksNothing(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	defer srv.Close()

	f := &stackLicenseFetcher{url: srv.URL, client: srv.Client()}
	key, ok, err := f.Fetch(context.Background(), "")
	if err != nil || ok || key != "" {
		t.Errorf("Fetch = (%q, %v, %v), want empty and no error", key, ok, err)
	}
	if called {
		t.Error("must not call the stack with no licence to present")
	}
}

// TestLicenseFetcherReadsTheStackEnvelope is the test that was missing. PayGate
// answers {"data":{"license":...}}, the client read only the top level, and the
// mismatch resolved to "the stack has no licence for us" -- silently. A renewed
// customer would have slid into grace and then expiry with nothing in the log.
// The old test stubbed the bare shape, which is exactly why it passed.
func TestLicenseFetcherReadsTheStackEnvelope(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"stack envelope", `{"data":{"license":"renewed.four.segment.key"}}`},
		{"bare license", `{"license":"renewed.four.segment.key"}`},
		{"bare key", `{"key":"renewed.four.segment.key"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				io.WriteString(w, tc.body)
			}))
			defer srv.Close()

			f := &stackLicenseFetcher{url: srv.URL, client: srv.Client()}
			key, ok, err := f.Fetch(context.Background(), "current.licence.four.segments")
			if err != nil {
				t.Fatal(err)
			}
			if !ok || key != "renewed.four.segment.key" {
				t.Errorf("key = %q ok = %v, want the renewed licence", key, ok)
			}
		})
	}
}

// A 200 whose shape we do not understand must not read as "no licence": that is
// indistinguishable from a cancelled subscription and would be acted on as one.
func TestLicenseFetcherTreatsAnUnknownShapeAsNoAnswer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"data":{"unexpected":"shape"}}`)
	}))
	defer srv.Close()

	f := &stackLicenseFetcher{url: srv.URL, client: srv.Client()}
	key, ok, err := f.Fetch(context.Background(), "current.licence.four.segments")
	if err != nil || ok || key != "" {
		t.Errorf("Fetch = (%q, %v, %v), want no key and no error", key, ok, err)
	}
}
