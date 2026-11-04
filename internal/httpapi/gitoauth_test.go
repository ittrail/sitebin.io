package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/ittrail/sitebin.io/internal/ext"
)

func gitOAuthIssuer(t *testing.T, hits *int) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	var srv *httptest.Server
	mux.HandleFunc("/.well-known/oauth-authorization-server/realms/x", func(w http.ResponseWriter, r *http.Request) {
		if hits != nil {
			*hits++
		}
		fmt.Fprintf(w, `{"issuer":%q,"authorization_endpoint":%q,"token_endpoint":%q,"device_authorization_endpoint":%q}`,
			srv.URL+"/realms/x", srv.URL+"/gate/authorize", srv.URL+"/realms/x/token", srv.URL+"/realms/x/device")
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestGitOAuthInfoFromDiscovery(t *testing.T) {
	hits := 0
	iss := gitOAuthIssuer(t, &hits)
	ext.Register(&fakeProvider{enabled: true})
	t.Cleanup(ext.Reset)
	e := newEnv(t, map[string]string{
		"SITEBIN_MCP_OAUTH_ISSUER":    iss.URL + "/realms/x",
		"SITEBIN_GIT_OAUTH_CLIENT_ID": "sitebin-git",
	})
	got := e.api.gitOAuthInfo(context.Background())
	want := &gitOAuth{
		ClientID:          "sitebin-git",
		AuthorizeEndpoint: iss.URL + "/gate/authorize",
		TokenEndpoint:     iss.URL + "/realms/x/token",
		DeviceEndpoint:    iss.URL + "/realms/x/device",
		Scopes:            []string{"openid", "offline_access", "sitebin:sites:read", "sitebin:sites:write"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("info = %+v, want %+v", got, want)
	}
	e.api.gitOAuthInfo(context.Background())
	if hits != 1 {
		t.Errorf("discovery hits = %d, want 1 (cached)", hits)
	}
}

func TestGitOAuthInfoNilWithoutClientID(t *testing.T) {
	iss := gitOAuthIssuer(t, nil)
	ext.Register(&fakeProvider{enabled: true})
	t.Cleanup(ext.Reset)
	e := newEnv(t, map[string]string{"SITEBIN_MCP_OAUTH_ISSUER": iss.URL + "/realms/x"})
	if got := e.api.gitOAuthInfo(context.Background()); got != nil {
		t.Fatalf("info = %+v, want nil", got)
	}
}

func TestGitOAuthInfoNilInCommunity(t *testing.T) {
	iss := gitOAuthIssuer(t, nil)
	ext.Reset()
	e := newEnv(t, map[string]string{
		"SITEBIN_MCP_OAUTH_ISSUER":    iss.URL + "/realms/x",
		"SITEBIN_GIT_OAUTH_CLIENT_ID": "sitebin-git",
	})
	if got := e.api.gitOAuthInfo(context.Background()); got != nil {
		t.Fatalf("info = %+v, want nil", got)
	}
}

func TestGitOAuthInfoNilOnFailureAndCachedBriefly(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		http.Error(w, "no", 500)
	}))
	t.Cleanup(srv.Close)
	ext.Register(&fakeProvider{enabled: true})
	t.Cleanup(ext.Reset)
	e := newEnv(t, map[string]string{
		"SITEBIN_MCP_OAUTH_ISSUER":    srv.URL + "/realms/x",
		"SITEBIN_GIT_OAUTH_CLIENT_ID": "sitebin-git",
	})
	for i := 0; i < 3; i++ {
		if got := e.api.gitOAuthInfo(context.Background()); got != nil {
			t.Fatalf("info = %+v, want nil", got)
		}
	}
	if hits != 1 {
		t.Errorf("hits = %d, want 1 (failure cached)", hits)
	}
}
