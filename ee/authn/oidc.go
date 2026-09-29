//go:build ee

package authn

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
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
	// LogoutHint is the raw ID token of this sign-in, set only for a
	// provider whose session this instance ends at sign-out (the generic
	// issuer, when its discovery names an end_session_endpoint): it is the
	// id_token_hint that lets the provider sign the person out without
	// asking. It carries the person's email and name -- never log it.
	LogoutHint string
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
	// rpLogout says a sign-out ends this provider's own session too (OpenID
	// Connect RP-Initiated Logout). Only the operator's generic issuer: for
	// Google or Microsoft signed in directly it would end the person's whole
	// Google or Microsoft session, mail included, for leaving Sitebin.
	rpLogout bool
	// freshPrompt is the `prompt` that makes the provider ask who is signing
	// in, for a sign-in that must not be answered silently by the session
	// the provider already holds. The values differ by provider: Google
	// knows no `login` in that sense, Keycloak no useful `select_account`.
	freshPrompt string

	once     sync.Once
	initErr  error
	oauthCfg *oauth2.Config
	verifier *oidc.IDTokenVerifier
	// endSession is the discovery document's end_session_endpoint, kept only
	// for an rpLogout provider and only when it is an absolute http(s) URL.
	endSession string
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
			Issuer     string `json:"issuer"`
			EndSession string `json:"end_session_endpoint"`
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
		if p.rpLogout && httpURL(doc.EndSession) {
			p.endSession = doc.EndSession
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
		freshPrompt: "select_account",
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
			freshPrompt: "select_account",
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
			rpLogout:    true, freshPrompt: "login",
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
// nonce. fresh asks the provider who is signing in (its freshPrompt) instead
// of letting a session it already holds answer silently -- the sign-in after
// a sign-out, or "use a different account". An ordinary sign-in carries no
// prompt, so single sign-on stays one click.
func (m *OIDC) AuthCodeURL(ctx context.Context, provider account.Provider, state, nonce string, fresh bool) (string, error) {
	p, ok := m.providers[provider]
	if !ok {
		return "", ErrProviderNotConfigured
	}
	if err := p.init(ctx); err != nil {
		return "", err
	}
	opts := []oauth2.AuthCodeOption{oidc.Nonce(nonce), oauth2.AccessTypeOnline}
	if fresh && p.freshPrompt != "" {
		opts = append(opts, oauth2.SetAuthURLParam("prompt", p.freshPrompt))
	}
	return p.oauthCfg.AuthCodeURL(state, opts...), nil
}

// LogoutURL is where the browser goes, at sign-out, to end the provider's own
// session (OpenID Connect RP-Initiated Logout 1.0): the discovery document's
// end_session_endpoint with client_id, post_logout_redirect_uri and -- when
// it is this account's -- the ID token of the sign-in as id_token_hint.
//
// "" with no error means there is no provider session to end from here: a
// provider that is not rpLogout (Google, Microsoft, a local account), or one
// whose discovery names no endpoint. An error means discovery failed; the
// caller signs the person out of Sitebin regardless.
//
// The hint is checked against subject, issuer and client before it is sent,
// and dropped -- not refused -- when it does not match: Keycloak answers a
// hint issued to another client with an error page, so a stale or planted
// cookie must never reach it. Its signature is Keycloak's to verify, and its
// expiry is deliberately not checked: Keycloak 26.7 accepts an expired hint
// (it verifies the signature only), and its ID tokens live for minutes.
func (m *OIDC) LogoutURL(ctx context.Context, provider account.Provider, subject, idTokenHint, postLogoutRedirect string) (string, error) {
	p, ok := m.providers[provider]
	if !ok || !p.rpLogout {
		return "", nil
	}
	if err := p.init(ctx); err != nil {
		return "", err
	}
	if p.endSession == "" {
		return "", nil
	}
	u, err := url.Parse(p.endSession)
	if err != nil {
		return "", nil // init kept only a URL that parses
	}
	q := u.Query()
	q.Set("client_id", p.clientID)
	q.Set("post_logout_redirect_uri", postLogoutRedirect)
	if idTokenHint != "" && p.hintNames(idTokenHint, subject) {
		q.Set("id_token_hint", idTokenHint)
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// hintNames reports whether raw's (unverified) payload names subject, an
// issuer this provider accepts, and this client -- as `azp`, or in `aud`
// when there is no `azp`, as the spec allows.
func (p *oidcProvider) hintNames(raw, subject string) bool {
	parts := strings.Split(raw, ".")
	if len(parts) != 3 || subject == "" {
		return false
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return false
	}
	var c struct {
		Iss string          `json:"iss"`
		Sub string          `json:"sub"`
		Azp string          `json:"azp"`
		Aud json.RawMessage `json:"aud"`
	}
	if json.Unmarshal(payload, &c) != nil || c.Sub != subject || !p.issuerAccepted(c.Iss) {
		return false
	}
	if c.Azp != "" {
		return c.Azp == p.clientID
	}
	var one string
	if json.Unmarshal(c.Aud, &one) == nil {
		return one == p.clientID
	}
	var many []string
	if json.Unmarshal(c.Aud, &many) == nil {
		for _, a := range many {
			if a == p.clientID {
				return true
			}
		}
	}
	return false
}

// httpURL reports whether s is an absolute http(s) URL with a host.
func httpURL(s string) bool {
	u, err := url.Parse(s)
	return err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != ""
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
	id := Identity{
		Provider:      provider,
		Subject:       idTok.Subject,
		Email:         claims.Email,
		EmailVerified: claims.EmailVerified,
	}
	if p.endSession != "" {
		id.LogoutHint = rawID
	}
	return id, nil
}
