//go:build ee

package authn

import (
	"context"
	"errors"
	"fmt"
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
	name        account.Provider
	issuer      string
	clientID    string
	secret      string
	redirectURL string

	once     sync.Once
	initErr  error
	oauthCfg *oauth2.Config
	verifier *oidc.IDTokenVerifier
}

func (p *oidcProvider) init(ctx context.Context) error {
	p.once.Do(func() {
		prov, err := oidc.NewProvider(ctx, p.issuer)
		if err != nil {
			p.initErr = fmt.Errorf("oidc discovery for %s: %w", p.name, err)
			return
		}
		p.verifier = prov.Verifier(&oidc.Config{ClientID: p.clientID})
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
		m.providers[account.Microsoft] = &oidcProvider{
			name: account.Microsoft, issuer: "https://login.microsoftonline.com/" + ms.Tenant + "/v2.0",
			clientID: ms.ClientID, secret: ms.ClientSecret,
			redirectURL: redirectBase + "/account/auth/microsoft/callback",
		}
	}
	if g := cfg.OIDC; g != nil {
		m.providers[account.OIDCProv] = &oidcProvider{
			name: account.OIDCProv, issuer: g.Issuer,
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
