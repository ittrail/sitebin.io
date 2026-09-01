//go:build ee

package authn

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ittrail/sitebin.io/ee/account"
	"github.com/ittrail/sitebin.io/ee/eeconfig"
)

// discoveryAt serves a discovery document that advertises `issuer` and points
// authorization at `authz` -- the SaaS Stack's Auth Gateway in miniature: the
// document lives at the gateway, names Keycloak as the issuer, and sends the
// browser to the gateway, which is where the consent gate is.
func discoveryAt(t *testing.T, issuer, authz string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		if issuer == "" {
			issuer = srv.URL
		}
		if authz == "" {
			authz = srv.URL + "/protocol/openid-connect/auth"
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"issuer":                                issuer,
			"authorization_endpoint":                authz,
			"token_endpoint":                        issuer + "/protocol/openid-connect/token",
			"jwks_uri":                              issuer + "/protocol/openid-connect/certs",
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	return srv
}

func genericOIDC(t *testing.T, cfg eeconfig.GenericOIDC) *oidcProvider {
	t.Helper()
	m := NewOIDC(eeconfig.Config{OIDC: &cfg}, "https://sitebin.example")
	p, ok := m.providers[account.OIDCProv]
	if !ok {
		t.Fatal("generic OIDC provider was not configured")
	}
	return p
}

// The gate only applies to an app whose discovery points at the gateway, and
// the gateway's document names a DIFFERENT issuer than the URL it was fetched
// from. go-oidc refuses that by default, so without the split this
// configuration cannot even initialize.
func TestDiscoveryURLSplitFromIssuer(t *testing.T) {
	const issuer = "https://auth.example/realms/saas-stack"
	gw := discoveryAt(t, issuer, "https://gw.example/api/v1/sitebin/protocol/openid-connect/auth")

	p := genericOIDC(t, eeconfig.GenericOIDC{
		Issuer: issuer, DiscoveryURL: gw.URL, ClientID: "sitebin-app",
	})
	if err := p.init(context.Background()); err != nil {
		t.Fatalf("init with a split issuer/discovery URL: %v", err)
	}
	// The whole point: the browser is sent to the gateway, not to Keycloak.
	if got := p.oauthCfg.Endpoint.AuthURL; !strings.HasPrefix(got, "https://gw.example/") {
		t.Errorf("authorization endpoint = %q; the consent gate is bypassed unless it is the gateway's", got)
	}
	if got := p.oauthCfg.Endpoint.TokenURL; !strings.HasPrefix(got, issuer) {
		t.Errorf("token endpoint = %q, want the issuer's own", got)
	}
}

// The check is tightened, not skipped: what is given up is only "the document
// lived at the issuer's URL". A document advertising some OTHER issuer is
// still refused -- otherwise the discovery URL would be a way to accept tokens
// from a realm nobody configured.
func TestDiscoveryURLStillChecksTheAdvertisedIssuer(t *testing.T) {
	gw := discoveryAt(t, "https://attacker.example/realms/evil", "https://attacker.example/auth")

	p := genericOIDC(t, eeconfig.GenericOIDC{
		Issuer: "https://auth.example/realms/saas-stack", DiscoveryURL: gw.URL, ClientID: "sitebin-app",
	})
	err := p.init(context.Background())
	if err == nil {
		t.Fatal("a document advertising a different issuer must be refused")
	}
	if !strings.Contains(err.Error(), "attacker.example") ||
		!strings.Contains(err.Error(), "auth.example/realms/saas-stack") {
		t.Errorf("error should name both issuers, got: %v", err)
	}
}

// With no discovery URL nothing changes: the document is fetched from the
// issuer and go-oidc's own equality rule is left switched on.
func TestNoDiscoveryURLFetchesFromTheIssuer(t *testing.T) {
	srv := discoveryAt(t, "", "")

	p := genericOIDC(t, eeconfig.GenericOIDC{Issuer: srv.URL, ClientID: "cid"})
	if err := p.init(context.Background()); err != nil {
		t.Fatalf("init: %v", err)
	}
	if p.discoveryBase() != srv.URL {
		t.Errorf("discoveryBase = %q, want the issuer %q", p.discoveryBase(), srv.URL)
	}
}
