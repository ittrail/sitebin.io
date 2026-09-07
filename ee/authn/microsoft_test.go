//go:build ee

package authn

import (
	"context"
	"testing"

	"github.com/ittrail/sitebin.io/ee/account"
	"github.com/ittrail/sitebin.io/ee/eeconfig"
)

// Microsoft's multi-tenant endpoints (tenant "common", "organizations",
// "consumers") publish the literal issuer
// https://login.microsoftonline.com/{tenantid}/v2.0 in discovery, and every
// ID token carries the signing tenant's GUID in its place. An exact issuer
// match — go-oidc's default — therefore rejected every token, so Microsoft
// sign-in with the default tenant could never have worked as written.

func microsoft(t *testing.T, tenant string) *oidcProvider {
	t.Helper()
	m := NewOIDC(eeconfig.Config{Microsoft: &eeconfig.OAuthProvider{ClientID: "mid", Tenant: tenant}}, "https://sitebin.example")
	p, ok := m.providers[account.Microsoft]
	if !ok {
		t.Fatal("microsoft provider was not configured")
	}
	return p
}

func TestMicrosoftMultiTenantIssuerHandling(t *testing.T) {
	p := microsoft(t, "common")
	if p.issuer != "https://login.microsoftonline.com/{tenantid}/v2.0" {
		t.Fatalf("multi-tenant issuer = %q; discovery advertises the literal {tenantid} template", p.issuer)
	}
	if p.discoveryBase() != "https://login.microsoftonline.com/common/v2.0" {
		t.Fatalf("discovery base = %q", p.discoveryBase())
	}
	for iss, want := range map[string]bool{
		"https://login.microsoftonline.com/9188040d-6c67-4c5b-b112-36a304b66dad/v2.0": true,
		"https://login.microsoftonline.com/72f988bf-86f1-41af-91ab-2d7cd011db47/v2.0": true,
		"https://login.microsoftonline.com/{tenantid}/v2.0":                           false, // the template itself is not a tenant
		"https://login.microsoftonline.com/common/v2.0":                               false,
		"https://evil.example/72f988bf-86f1-41af-91ab-2d7cd011db47/v2.0":              false,
		"https://login.microsoftonline.com/72f988bf-86f1-41af-91ab-2d7cd011db47/v1.0": false,
		"https://login.microsoftonline.com/not-a-guid/v2.0":                           false,
	} {
		if got := p.issuerAccepted(iss); got != want {
			t.Errorf("issuerAccepted(%q) = %v, want %v", iss, got, want)
		}
	}
}

func TestMicrosoftExplicitTenantIsExact(t *testing.T) {
	p := microsoft(t, "72f988bf-86f1-41af-91ab-2d7cd011db47")
	if p.issuer != "https://login.microsoftonline.com/72f988bf-86f1-41af-91ab-2d7cd011db47/v2.0" {
		t.Fatalf("single-tenant issuer = %q", p.issuer)
	}
	if !p.issuerAccepted(p.issuer) {
		t.Error("the configured tenant's own issuer is refused")
	}
	if p.issuerAccepted("https://login.microsoftonline.com/9188040d-6c67-4c5b-b112-36a304b66dad/v2.0") {
		t.Error("an explicit tenant accepted another tenant's tokens")
	}
}

// Discovery for the multi-tenant endpoint advertises the template, and init
// has to accept exactly that and nothing else.
func TestMicrosoftMultiTenantDiscoveryInit(t *testing.T) {
	srv := discoveryAt(t, "https://login.microsoftonline.com/{tenantid}/v2.0", "https://login.microsoftonline.com/common/oauth2/v2.0/authorize")
	p := microsoft(t, "common")
	p.discoveryURL = srv.URL
	if err := p.init(context.Background()); err != nil {
		t.Fatalf("init against the multi-tenant discovery document: %v", err)
	}
	if p.verifier == nil {
		t.Fatal("no verifier built")
	}
	// A document advertising a real tenant while the instance is configured
	// for the template is still a mismatch.
	q := microsoft(t, "common")
	q.discoveryURL = discoveryAt(t, "https://login.microsoftonline.com/72f988bf-86f1-41af-91ab-2d7cd011db47/v2.0", "").URL
	if err := q.init(context.Background()); err == nil {
		t.Error("a discovery document naming a different issuer was accepted")
	}
}
