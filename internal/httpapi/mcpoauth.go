package httpapi

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	sdkauth "github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
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
// Everything below but the MCP marker is inert unless SITEBIN_MCP_OAUTH_ISSUER
// is set. See docs/superpowers/specs/2026-09-25-mcp-oauth-consent-lazy-auth-design.md.

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

// mcpResourceMetadataURL is the document the challenge points clients at. It
// is the /mcp-suffixed one: RFC 9728 §3.1 inserts the resource's path after
// the well-known prefix, and pairs the bare document with the origin itself.
func (a *API) mcpResourceMetadataURL() string {
	return a.cfg.SiteURL(a.cfg.BaseDomain) + "/.well-known/oauth-protected-resource/mcp"
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

// mcpHandler returns the handler mounted at /mcp.
//
// Every request is marked as an MCP request, OAuth or not: that marker is the
// only thing that lets an OAuth access token act, so it has to be present on
// exactly this route. With an issuer configured the lazy challenge sits
// inside it; without one the SDK handler is reached as before, and the tools
// authenticate with edit passwords and account tokens.
func (a *API) mcpHandler() http.Handler {
	var h http.Handler = mcp.NewHandler(mcpOps{a}, mcp.Info{Name: "sitebin", Version: Version})
	if a.mcpOAuthEnabled() {
		h = a.mcpLazyAuth(h)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.ServeHTTP(w, r.WithContext(ext.WithMCPCaller(r.Context())))
	})
}

// mcpLazyAuth challenges only the calls that need an account.
//
// It replaces a wrapper that refused every request without a bearer, which
// with OAuth on removed the edit-password path altogether. An MCP client
// starts a sign-in only on an HTTP 401, so the challenge has to happen here
// rather than as a tool error — but only for a call that cannot work without
// one. Everything else goes through, and the tools authenticate exactly as
// they always did:
//
//   - no Authorization header: only a tools/call that needs an account (see
//     mcp.AnonymousCallAllowed) gets the 401;
//   - a bearer that does not resolve: 401 invalid_token, for every request;
//   - a bearer that does: a tools/call whose scope it lacks gets the step-up
//     403, and every other request goes on with the credential in its context,
//     so it is verified once rather than again by the tools.
//
// It is a door, not the lock. A body it cannot read — too large, not JSON, a
// batch — goes to the SDK untouched, which answers it, and the tools still
// check both the credential and the scope themselves.
func (a *API) mcpLazyAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			next.ServeHTTP(w, r)
			return
		}
		if r.Header.Get("Authorization") == "" {
			if name, args, ok := peekToolCall(r); ok && !mcp.AnonymousCallAllowed(name, args, a.accountsEnabled()) {
				a.mcpChallenge(w, http.StatusUnauthorized, "", mcp.AllScopes,
					"sign in to your Sitebin account to call "+name+
						" (your client offers it on this response), or pass the site's edit_password to a tool that takes one\n")
				return
			}
			next.ServeHTTP(w, r)
			return
		}
		// The header was sent, so it is answered on its own merits: a wrong
		// credential must fail rather than quietly degrade to "anonymous".
		cred, ok := a.mcpBearer(r)
		if !ok {
			a.mcpChallenge(w, http.StatusUnauthorized, "invalid_token", mcp.AllScopes,
				"the bearer credential was not accepted: sign in again, or use an account API token\n")
			return
		}
		if name, _, ok := peekToolCall(r); ok {
			if scope, known := mcp.ToolScope(name); known && !mcp.Allows(cred.Scopes, cred.OAuth, scope) {
				want := mcp.HeldScopes(append(append([]string(nil), cred.Scopes...), scope))
				a.mcpChallenge(w, http.StatusForbidden, "insufficient_scope", want,
					"this connection was not granted "+scope+" for "+name+": reconnect and approve that permission\n")
				return
			}
		}
		next.ServeHTTP(w, r.WithContext(ext.WithCredential(r.Context(), cred)))
	})
}

// mcpBearer resolves the request's credential through the extension. No
// provider means no accounts, so no credential can name an owner; an
// instance in that state should not have OAuth configured at all, and
// refusing is the safe answer if it does.
func (a *API) mcpBearer(r *http.Request) (ext.Credential, bool) {
	p, ok := ext.Get()
	if !ok {
		return ext.Credential{}, false
	}
	return p.BearerCredential(r)
}

// accountsEnabled reports whether the instance has accounts to sign in to.
func (a *API) accountsEnabled() bool {
	p, ok := ext.Get()
	return ok && p.AccountsEnabled()
}

// mcpChallenge answers with an RFC 6750 challenge that also names the
// RFC 9728 metadata, which is what an MCP client follows to its sign-in.
// errCode is empty when no credential was sent at all (RFC 6750 §3.1). The
// values are this instance's own URL and scope names, which never contain a
// quote, so they are written as quoted strings without escaping.
func (a *API) mcpChallenge(w http.ResponseWriter, code int, errCode string, scopes []string, msg string) {
	var params []string
	if errCode != "" {
		params = append(params, `error="`+errCode+`"`)
	}
	params = append(params, `resource_metadata="`+a.mcpResourceMetadataURL()+`"`)
	if len(scopes) > 0 {
		params = append(params, `scope="`+strings.Join(scopes, " ")+`"`)
	}
	w.Header().Set("WWW-Authenticate", "Bearer "+strings.Join(params, ", "))
	http.Error(w, msg, code)
}

// peekToolCall reads the request body and puts it back for the SDK. When the
// body is a single JSON-RPC tools/call it returns the tool's name and
// arguments; ok=false for anything else, including a body larger than the
// SDK will accept, which is restored whole so the SDK answers it with its own
// 413.
//
// The envelope is decoded by the SDK's own JSON-RPC decoder, so this reads
// the message the SDK will read. The params are looked up by their exact
// keys; a client that disagrees is at worst challenged needlessly, since the
// tools make the decision that counts.
func peekToolCall(r *http.Request) (name string, args json.RawMessage, ok bool) {
	if r.Body == nil {
		return "", nil, false
	}
	orig := r.Body
	buf, err := io.ReadAll(io.LimitReader(orig, mcp.MaxRequestBytes+1))
	r.Body = struct {
		io.Reader
		io.Closer
	}{io.MultiReader(bytes.NewReader(buf), orig), orig}
	if err != nil || int64(len(buf)) > mcp.MaxRequestBytes {
		return "", nil, false
	}
	msg, err := jsonrpc.DecodeMessage(buf)
	if err != nil {
		return "", nil, false
	}
	req, isReq := msg.(*jsonrpc.Request)
	if !isReq || req.Method != "tools/call" {
		return "", nil, false
	}
	var params map[string]json.RawMessage
	if err := json.Unmarshal(req.Params, &params); err != nil {
		// A tools/call whose params are unreadable names no tool: treat it
		// as an unknown one and let the SDK refuse it.
		return "", nil, true
	}
	json.Unmarshal(params["name"], &name) // not a string: an unknown tool, as above
	return name, params["arguments"], true
}

// mcpDiscoveryRoutes registers the protected-resource metadata, at both paths
// clients are known to request, for GET and for the CORS preflight a
// browser-based client sends first. The SDK's handler answers both.
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
	for _, path := range []string{"/.well-known/oauth-protected-resource", "/.well-known/oauth-protected-resource/mcp"} {
		mux.Handle("GET "+path, doc)
		mux.Handle("OPTIONS "+path, doc)
	}
}
