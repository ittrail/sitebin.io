package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ittrail/sitebin.io/internal/ext"
)

// oauthDoc serves a steered discovery document at /realms/x's path-insertion
// URL, built by doc from the server's own URL, counting the requests and
// taking delay over each.
type oauthDoc struct {
	srv   *httptest.Server
	hits  atomic.Int32
	delay time.Duration
	doc   func(base string) string
}

func newOAuthDoc(t *testing.T, tls bool, doc func(base string) string) *oauthDoc {
	t.Helper()
	d := &oauthDoc{doc: doc}
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/.well-known/oauth-authorization-server/realms/x" {
			http.NotFound(w, r)
			return
		}
		d.hits.Add(1)
		time.Sleep(d.delay)
		fmt.Fprint(w, d.doc(d.srv.URL))
	})
	if tls {
		d.srv = httptest.NewTLSServer(h)
	} else {
		d.srv = httptest.NewServer(h)
	}
	t.Cleanup(d.srv.Close)
	return d
}

// keycloakDoc is the document the stack steers: the issuer, the gate's
// authorize endpoint, the realm's token and device endpoints.
func keycloakDoc(base string) string {
	return fmt.Sprintf(`{"issuer":%q,"authorization_endpoint":%q,"token_endpoint":%q,"device_authorization_endpoint":%q}`,
		base+"/realms/x", base+"/gate/authorize", base+"/realms/x/token", base+"/realms/x/device")
}

func gitOAuthEnv(t *testing.T, issuer string) *env {
	t.Helper()
	ext.Register(&fakeProvider{enabled: true})
	t.Cleanup(ext.Reset)
	return newEnv(t, map[string]string{
		"SITEBIN_MCP_OAUTH_ISSUER":    issuer,
		"SITEBIN_GIT_OAUTH_CLIENT_ID": "sitebin-git",
	})
}

func TestGitOAuthInfoFromDiscovery(t *testing.T) {
	iss := newOAuthDoc(t, false, keycloakDoc)
	e := gitOAuthEnv(t, iss.srv.URL+"/realms/x")
	got := e.api.gitOAuthInfo(context.Background())
	want := &gitOAuth{
		ClientID:          "sitebin-git",
		AuthorizeEndpoint: iss.srv.URL + "/gate/authorize",
		TokenEndpoint:     iss.srv.URL + "/realms/x/token",
		DeviceEndpoint:    iss.srv.URL + "/realms/x/device",
		Scopes:            []string{"openid", "offline_access", "sitebin:sites:read", "sitebin:sites:write"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("info = %+v, want %+v", got, want)
	}
	e.api.gitOAuthInfo(context.Background())
	if n := iss.hits.Load(); n != 1 {
		t.Errorf("discovery hits = %d, want 1 (cached)", n)
	}
}

func TestGitOAuthInfoNilWithoutClientID(t *testing.T) {
	iss := newOAuthDoc(t, false, keycloakDoc)
	ext.Register(&fakeProvider{enabled: true})
	t.Cleanup(ext.Reset)
	e := newEnv(t, map[string]string{"SITEBIN_MCP_OAUTH_ISSUER": iss.srv.URL + "/realms/x"})
	if got := e.api.gitOAuthInfo(context.Background()); got != nil {
		t.Fatalf("info = %+v, want nil", got)
	}
}

func TestGitOAuthInfoNilInCommunity(t *testing.T) {
	iss := newOAuthDoc(t, false, keycloakDoc)
	ext.Reset()
	e := newEnv(t, map[string]string{
		"SITEBIN_MCP_OAUTH_ISSUER":    iss.srv.URL + "/realms/x",
		"SITEBIN_GIT_OAUTH_CLIENT_ID": "sitebin-git",
	})
	if got := e.api.gitOAuthInfo(context.Background()); got != nil {
		t.Fatalf("info = %+v, want nil", got)
	}
}

func TestGitOAuthInfoNilOnFailureAndCachedBriefly(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.Error(w, "no", 500)
	}))
	t.Cleanup(srv.Close)
	e := gitOAuthEnv(t, srv.URL+"/realms/x")
	for i := 0; i < 3; i++ {
		if got := e.api.gitOAuthInfo(context.Background()); got != nil {
			t.Fatalf("info = %+v, want nil", got)
		}
	}
	if n := hits.Load(); n != 1 {
		t.Errorf("hits = %d, want 1 (failure cached)", n)
	}
}

// The configured issuer may carry a trailing slash the document's does not
// (or the other way round): the same issuer.
func TestGitOAuthInfoTrailingSlashIssuer(t *testing.T) {
	iss := newOAuthDoc(t, false, keycloakDoc)
	e := gitOAuthEnv(t, iss.srv.URL+"/realms/x/")
	if got := e.api.gitOAuthInfo(context.Background()); got == nil || got.TokenEndpoint != iss.srv.URL+"/realms/x/token" {
		t.Fatalf("info = %+v, want the document's endpoints", got)
	}
}

// A document that names another issuer is not this issuer's: no sign-in is
// offered.
func TestGitOAuthInfoRefusesAnotherIssuer(t *testing.T) {
	iss := newOAuthDoc(t, false, func(base string) string {
		return strings.Replace(keycloakDoc(base), `"issuer":"`+base+`/realms/x"`, `"issuer":"https://other.example/realms/x"`, 1)
	})
	e := gitOAuthEnv(t, iss.srv.URL+"/realms/x")
	if got := e.api.gitOAuthInfo(context.Background()); got != nil {
		t.Fatalf("info = %+v for a document naming another issuer, want nil", got)
	}
}

// An https issuer's endpoints are https: a plain http one (or one that is no
// URL) means no sign-in is offered. An http issuer (a test instance) may
// name http endpoints.
func TestGitOAuthInfoEndpointSchemes(t *testing.T) {
	for _, c := range []struct {
		name string
		tls  bool
		doc  func(base string) string
		ok   bool
	}{
		{"https issuer, https endpoints", true, keycloakDoc, true},
		{"https issuer, an http token endpoint", true, func(base string) string {
			return strings.Replace(keycloakDoc(base), `"token_endpoint":"https://`, `"token_endpoint":"http://`, 1)
		}, false},
		{"https issuer, no device endpoint", true, func(base string) string {
			return strings.Replace(keycloakDoc(base), `"device_authorization_endpoint":"`+base+`/realms/x/device"`, `"device_authorization_endpoint":"/device"`, 1)
		}, false},
		{"http issuer, http endpoints", false, keycloakDoc, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			iss := newOAuthDoc(t, c.tls, c.doc)
			info, err := discoverGitOAuth(context.Background(), iss.srv.Client(), iss.srv.URL+"/realms/x", "sitebin-git")
			if c.ok && (err != nil || info == nil) {
				t.Fatalf("discovery failed: %v", err)
			}
			if !c.ok && err == nil {
				t.Fatalf("discovery accepted %+v", info)
			}
		})
	}
}

// The discovery is not the first caller's: a caller that gives up (its
// request ended) gets nil, while the discovery goes on and is cached for the
// next caller — not cached as a failure for a minute.
func TestGitOAuthInfoOutlivesTheCaller(t *testing.T) {
	iss := newOAuthDoc(t, false, keycloakDoc)
	iss.delay = 300 * time.Millisecond
	e := gitOAuthEnv(t, iss.srv.URL+"/realms/x")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got := e.api.gitOAuthInfo(ctx); got != nil {
		t.Fatalf("a caller that gave up got %+v", got)
	}
	if got := e.api.gitOAuthInfo(context.Background()); got == nil {
		t.Fatal("the next caller got nil: the first caller's end cut the discovery short")
	}
	if n := iss.hits.Load(); n != 1 {
		t.Errorf("discovery hits = %d, want 1", n)
	}
}

// Callers at the same moment share one discovery and none holds the cache's
// lock while it runs.
func TestGitOAuthInfoOneDiscoveryAtATime(t *testing.T) {
	iss := newOAuthDoc(t, false, keycloakDoc)
	iss.delay = 300 * time.Millisecond
	e := gitOAuthEnv(t, iss.srv.URL+"/realms/x")
	var wg sync.WaitGroup
	var nils atomic.Int32
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if e.api.gitOAuthInfo(context.Background()) == nil {
				nils.Add(1)
			}
		}()
	}
	// While the discovery runs, the lock is free: a caller with a short
	// deadline is answered by its deadline, not after the discovery.
	time.Sleep(50 * time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	start := time.Now()
	e.api.gitOAuthInfo(ctx)
	if took := time.Since(start); took > 200*time.Millisecond {
		t.Errorf("a caller during the discovery waited %v, past its own deadline", took)
	}
	wg.Wait()
	if n := nils.Load(); n != 0 {
		t.Errorf("%d callers got nil", n)
	}
	if n := iss.hits.Load(); n != 1 {
		t.Errorf("discovery hits = %d, want 1", n)
	}
}
