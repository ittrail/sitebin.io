//go:build ee

package authn

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"github.com/ittrail/sitebin.io/ee/account"
	"github.com/ittrail/sitebin.io/ee/eeconfig"
)

// Identity is the verified result of an OAuth login.
type Identity struct {
	Provider      account.Provider
	Subject       string
	Email         string
	EmailVerified bool
}

// oidcProvider lazily initializes one provider's oauth2 config + verifier. The
// discovery fetch happens on first use, so an instance without OAuth traffic
// never makes the network call, and startup does not depend on the IdP.
type oidcProvider struct {
	name account.Provider
	// issuer is the value every token's `iss` must equal — unless
	// issuerPattern is set, in which case it is what DISCOVERY must
	// advertise, and tokens are matched against the pattern instead.
	issuer string
	// issuerPattern, when set, is what a token's `iss` must match. It exists
	// for exactly one provider shape: Microsoft's multi-tenant endpoints,
	// whose discovery document advertises the literal template
	// https://login.microsoftonline.com/{tenantid}/v2.0 while every ID token
	// carries the signing tenant's GUID in its place. An exact match rejected
	// every token, so Microsoft sign-in with the default tenant never worked.
	issuerPattern *regexp.Regexp
	// discoveryURL is where the document is fetched, when that is not the
	// issuer. Empty = fetch it from issuer.
	discoveryURL string
	clientID     string
	secret       string
	redirectURL  string

	once     sync.Once
	initErr  error
	oauthCfg *oauth2.Config
	verifier *oidc.IDTokenVerifier
}

// discoveryBase is where go-oidc should fetch discovery from. go-oidc appends
// the well-known path itself.
func (p *oidcProvider) discoveryBase() string {
	if p.discoveryURL == "" {
		return p.issuer
	}
	return p.discoveryURL
}

func (p *oidcProvider) init(ctx context.Context) error {
	p.once.Do(func() {
		base := p.discoveryBase()
		if base != p.issuer {
			// The issuer split, in one call: fetch the document from `base`,
			// but require and record the issuer as p.issuer.
			//
			// go-oidc refuses a document whose `issuer` is not the URL it was
			// fetched from, and calls the escape hatch "insecure". The rule it
			// disables is the URL-equality one, which the SaaS Stack's Auth
			// Gateway deliberately breaks: it serves Keycloak's document —
			// Keycloak's `issuer`, Keycloak's token and JWKS endpoints — with
			// `authorization_endpoint` pointed at itself, because that is
			// where the consent gate lives.
			//
			// So the check is TIGHTENED rather than skipped: the document's
			// issuer is re-checked against the configured one below, and the
			// verifier built from it still enforces `iss` on every ID token.
			// What is given up is only "the document lived at the issuer's
			// URL", which was never the thing protecting anything here.
			ctx = oidc.InsecureIssuerURLContext(ctx, p.issuer)
		}
		prov, err := oidc.NewProvider(ctx, base)
		if err != nil {
			p.initErr = fmt.Errorf("oidc discovery for %s at %s: %w", p.name, base, err)
			return
		}
		var doc struct {
			Issuer string `json:"issuer"`
		}
		if err := prov.Claims(&doc); err != nil {
			p.initErr = fmt.Errorf("oidc discovery for %s at %s: unusable document: %w", p.name, base, err)
			return
		}
		if doc.Issuer != p.issuer {
			p.initErr = fmt.Errorf("oidc discovery for %s at %s advertises issuer %q, but this instance is configured for %q",
				p.name, base, doc.Issuer, p.issuer)
			return
		}
		vcfg := &oidc.Config{ClientID: p.clientID}
		if p.issuerPattern != nil {
			// go-oidc can only compare `iss` for equality. The check is not
			// dropped but moved: Exchange matches it against the pattern.
			vcfg.SkipIssuerCheck = true
		}
		p.verifier = prov.Verifier(vcfg)
		p.oauthCfg = &oauth2.Config{
			ClientID:     p.clientID,
			ClientSecret: p.secret,
			Endpoint:     prov.Endpoint(),
			RedirectURL:  p.redirectURL,
			Scopes:       []string{oidc.ScopeOpenID, "email", "profile"},
		}
	})
	return p.initErr
}

// issuerAccepted reports whether iss is an issuer this provider trusts: the
// configured one exactly, or — for a multi-tenant provider — one matching the
// pattern. The verifier already checked the signature against the keys the
// (single) discovery document named, so this decides only which tenants may
// sign in, never who signed.
func (p *oidcProvider) issuerAccepted(iss string) bool {
	if p.issuerPattern != nil {
		return p.issuerPattern.MatchString(iss)
	}
	return iss == p.issuer
}

// microsoftMultiTenant are the Microsoft endpoint aliases that serve every
// tenant: their tokens' `iss` names the actual tenant.
var microsoftMultiTenant = map[string]bool{"common": true, "organizations": true, "consumers": true}

// microsoftTenantIssuer matches the issuer of any Azure AD / Microsoft
// account tenant on the v2.0 endpoint.
var microsoftTenantIssuer = regexp.MustCompile(`^https://login\.microsoftonline\.com/[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}/v2\.0$`)

// microsoftProvider configures Microsoft sign-in for tenant. An explicit
// tenant (a GUID or a verified domain) is matched exactly; the multi-tenant
// aliases discover at their own URL, expect the {tenantid} template there,
// and accept any tenant's tokens.
func microsoftProvider(ms *eeconfig.OAuthProvider, redirectBase string) *oidcProvider {
	tenant := strings.ToLower(strings.TrimSpace(ms.Tenant))
	p := &oidcProvider{
		name:        account.Microsoft,
		clientID:    ms.ClientID,
		secret:      ms.ClientSecret,
		redirectURL: redirectBase + "/account/auth/microsoft/callback",
	}
	if microsoftMultiTenant[tenant] {
		p.issuer = "https://login.microsoftonline.com/{tenantid}/v2.0"
		p.discoveryURL = "https://login.microsoftonline.com/" + tenant + "/v2.0"
		p.issuerPattern = microsoftTenantIssuer
		return p
	}
	p.issuer = "https://login.microsoftonline.com/" + tenant + "/v2.0"
	return p
}

// OIDC manages the configured OAuth providers.
type OIDC struct {
	providers map[account.Provider]*oidcProvider
}

// NewOIDC builds the OAuth manager from config. redirectBase is the main-domain
// origin (e.g. https://sitebin.example); callbacks are
// <base>/account/auth/<provider>/callback.
func NewOIDC(cfg eeconfig.Config, redirectBase string) *OIDC {
	m := &OIDC{providers: map[account.Provider]*oidcProvider{}}
	if g := cfg.Google; g != nil {
		m.providers[account.Google] = &oidcProvider{
			name: account.Google, issuer: "https://accounts.google.com",
			clientID: g.ClientID, secret: g.ClientSecret,
			redirectURL: redirectBase + "/account/auth/google/callback",
		}
	}
	if ms := cfg.Microsoft; ms != nil {
		m.providers[account.Microsoft] = microsoftProvider(ms, redirectBase)
	}
	if g := cfg.OIDC; g != nil {
		m.providers[account.OIDCProv] = &oidcProvider{
			name: account.OIDCProv, issuer: g.Issuer, discoveryURL: g.DiscoveryURL,
			clientID: g.ClientID, secret: g.ClientSecret,
			redirectURL: redirectBase + "/account/auth/oidc/callback",
		}
	}
	return m
}

// Configured reports whether a given provider is set up.
func (m *OIDC) Configured(p account.Provider) bool { _, ok := m.providers[p]; return ok }

// Providers lists the configured provider names.
func (m *OIDC) Providers() []account.Provider {
	out := make([]account.Provider, 0, len(m.providers))
	for name := range m.providers {
		out = append(out, name)
	}
	return out
}

var ErrProviderNotConfigured = errors.New("oauth provider not configured")

// AuthCodeURL returns the provider's authorization URL for the given state and
// nonce.
func (m *OIDC) AuthCodeURL(ctx context.Context, provider account.Provider, state, nonce string) (string, error) {
	p, ok := m.providers[provider]
	if !ok {
		return "", ErrProviderNotConfigured
	}
	if err := p.init(ctx); err != nil {
		return "", err
	}
	return p.oauthCfg.AuthCodeURL(state, oidc.Nonce(nonce), oauth2.AccessTypeOnline), nil
}

// Exchange completes the callback: swaps code for tokens, verifies the ID
// token and nonce, and returns the verified Identity.
func (m *OIDC) Exchange(ctx context.Context, provider account.Provider, code, nonce string) (Identity, error) {
	p, ok := m.providers[provider]
	if !ok {
		return Identity{}, ErrProviderNotConfigured
	}
	if err := p.init(ctx); err != nil {
		return Identity{}, err
	}
	tok, err := p.oauthCfg.Exchange(ctx, code)
	if err != nil {
		return Identity{}, fmt.Errorf("code exchange: %w", err)
	}
	rawID, ok := tok.Extra("id_token").(string)
	if !ok {
		return Identity{}, errors.New("no id_token in response")
	}
	idTok, err := p.verifier.Verify(ctx, rawID)
	if err != nil {
		return Identity{}, fmt.Errorf("verify id_token: %w", err)
	}
	if !p.issuerAccepted(idTok.Issuer) {
		return Identity{}, fmt.Errorf("verify id_token: issuer %q is not one this provider accepts", idTok.Issuer)
	}
	if idTok.Nonce != nonce {
		return Identity{}, errors.New("nonce mismatch")
	}
	var claims struct {
		Email         string `json:"email"`
		EmailVerified bool   `json:"email_verified"`
	}
	if err := idTok.Claims(&claims); err != nil {
		return Identity{}, fmt.Errorf("parse claims: %w", err)
	}
	return Identity{
		Provider:      provider,
		Subject:       idTok.Subject,
		Email:         claims.Email,
		EmailVerified: claims.EmailVerified,
	}, nil
}
