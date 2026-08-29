package httpapi

import (
	"context"
	"net/http"

	sdkauth "github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/oauthex"

	"github.com/ittrail/sitebin.io/internal/ext"
	"github.com/ittrail/sitebin.io/internal/mcp"
)

// This file makes /mcp an OAuth 2.1 protected resource. It does NOT make
// Sitebin an authorization server: it publishes what this resource is, points
// at whichever issuer the operator configured, and validates the tokens that
// issuer signed. Nothing here mints a token, registers a client or renders a
// consent screen — those belong to the authorization server, and keeping them
// there is what lets Sitebin run with any issuer, or none.
//
// Everything below is inert unless SITEBIN_MCP_OAUTH_ISSUER is set.

// mcpOAuthEnabled reports whether an authorization server is configured.
func (a *API) mcpOAuthEnabled() bool { return a.cfg.MCPOAuthIssuer != "" }

// mcpResource is this server's OAuth resource identifier — the value that must
// appear in an access token's audience, and the `resource` of the metadata
// document. Derived from the base domain so it cannot vary per request: a
// resource identifier that moved would invalidate every token minted for it.
func (a *API) mcpResource() string {
	if a.cfg.MCPResource != "" {
		return a.cfg.MCPResource
	}
	return a.cfg.SiteURL(a.cfg.BaseDomain) + "/mcp"
}

// mcpResourceMetadataURL is where clients fetch the document, and what the 401
// challenge points them at.
func (a *API) mcpResourceMetadataURL() string {
	return a.cfg.SiteURL(a.cfg.BaseDomain) + "/.well-known/oauth-protected-resource"
}

// mcpProtectedResourceMetadata builds the RFC 9728 document.
func (a *API) mcpProtectedResourceMetadata() *oauthex.ProtectedResourceMetadata {
	return &oauthex.ProtectedResourceMetadata{
		Resource:               a.mcpResource(),
		AuthorizationServers:   []string{a.cfg.MCPOAuthIssuer},
		ScopesSupported:        mcp.AllScopes,
		BearerMethodsSupported: []string{"header"},
		ResourceName:           "Sitebin",
		ResourceDocumentation:  "https://sitebin.io/docs/mcp/",
	}
}

// verifyMCPToken authenticates one credential presented to /mcp.
//
// It accepts BOTH kinds, and that is the point: account API tokens are a
// shipped, documented feature, and turning OAuth on must not break the scripts
// and agents already using them. Which kind a credential is remains the
// extension's judgement — the core only asks what it grants.
func (a *API) verifyMCPToken(ctx context.Context, token string, r *http.Request) (*sdkauth.TokenInfo, error) {
	p, ok := ext.Get()
	if !ok {
		// No provider means no accounts, so no credential can name an owner.
		// An instance in that state should not have OAuth configured at all,
		// but refusing is the safe answer if it does.
		return nil, sdkauth.ErrInvalidToken
	}
	cred, ok := p.BearerCredential(r)
	if !ok {
		return nil, sdkauth.ErrInvalidToken
	}
	return &sdkauth.TokenInfo{
		Scopes: cred.Scopes,
		UserID: cred.AccountID,
		// Expiration is deliberately left zero: the credential's lifetime is
		// the extension's business, and an account API token has none at all.
		// AllowMissingExpiration below is what makes that legal.
	}, nil
}

// mcpHandler returns the handler mounted at /mcp, wrapped in the bearer
// challenge when OAuth is configured.
//
// Without an issuer the handler is returned bare, exactly as before: a
// community instance, or an enterprise one that has not opted in, keeps
// authenticating inside the tools with edit passwords and account tokens.
func (a *API) mcpHandler() http.Handler {
	h := mcp.NewHandler(mcpOps{a}, mcp.Info{Name: "sitebin", Version: Version})
	if !a.mcpOAuthEnabled() {
		return h
	}
	return sdkauth.RequireBearerToken(a.verifyMCPToken, &sdkauth.RequireBearerTokenOptions{
		ResourceMetadataURL: a.mcpResourceMetadataURL(),
		// No endpoint-wide scope requirement: Sitebin enforces scopes per tool,
		// because read and write are not the same permission and demanding both
		// here would make a read-only connection useless.
		AllowMissingExpiration: true,
	})(h)
}

// mcpDiscoveryRoutes registers the protected-resource metadata, at both paths
// clients are known to request.
//
// RFC 9728 defines the path-suffixed form for a resource that lives at a path,
// while several clients ask for the bare one. Serving both costs one route and
// removes a class of "works in one client, not another" bug that is miserable
// to diagnose from the outside.
func (a *API) mcpDiscoveryRoutes(mux *http.ServeMux) {
	if !a.mcpOAuthEnabled() {
		return
	}
	doc := sdkauth.ProtectedResourceMetadataHandler(a.mcpProtectedResourceMetadata())
	mux.Handle("GET /.well-known/oauth-protected-resource", doc)
	mux.Handle("GET /.well-known/oauth-protected-resource/mcp", doc)
}
