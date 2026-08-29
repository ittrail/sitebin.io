//go:build ee

package ee

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/coreos/go-oidc/v3/oidc"

	"github.com/ittrail/sitebin.io/internal/ext"
)

// mcpOAuth verifies OAuth access tokens presented to the MCP endpoint and
// resolves them to a Sitebin account.
//
// It exists in ee/ because everything it does presupposes accounts: without
// them there is no owner for a token to act as. Sitebin is a *resource server*
// here and nothing more — it validates tokens an authorization server issued
// and never issues one itself. That is what keeps "one container, no
// dependencies" true: an operator points this at whatever issuer they already
// run, or at none at all.
type mcpOAuth struct {
	issuer   string
	resource string
	accounts accountLookup

	once     sync.Once
	initErr  error
	verifier *oidc.IDTokenVerifier
}

// accountLookup turns an OIDC subject into a Sitebin account id. It is a
// function rather than the store itself so the verifier can be tested without
// one, and so the store's own types do not leak in here.
type accountLookup func(subject string) (accountID string, ok bool)

// newMCPOAuth returns a verifier, or nil when no issuer is configured — which
// is the normal case and must stay entirely inert.
func newMCPOAuth(issuer, resource string, accounts accountLookup) *mcpOAuth {
	if strings.TrimSpace(issuer) == "" {
		return nil
	}
	return &mcpOAuth{issuer: issuer, resource: resource, accounts: accounts}
}

// init resolves the issuer's metadata and key set, once and lazily.
//
// Lazily because startup must not depend on the issuer being reachable: an
// authorization server that is briefly down should make MCP OAuth calls fail,
// not stop Sitebin from serving sites.
func (m *mcpOAuth) init(ctx context.Context) error {
	m.once.Do(func() {
		prov, err := oidc.NewProvider(ctx, m.issuer)
		if err != nil {
			m.initErr = fmt.Errorf("mcp oauth: discover %s: %w", m.issuer, err)
			return
		}
		// SkipClientIDCheck because the audience this resource server cares
		// about is its own resource identifier, not a client id — the check
		// below is the one that matters and it is stricter than the default.
		m.verifier = prov.Verifier(&oidc.Config{SkipClientIDCheck: true})
	})
	return m.initErr
}

// Verify checks an access token and returns the credential it grants.
//
// ok=false for anything it does not fully trust. It never reports *why* to the
// caller: a resource server that distinguishes "expired" from "wrong audience"
// from "unknown account" for an unauthenticated caller is an oracle.
func (m *mcpOAuth) Verify(ctx context.Context, raw string) (ext.Credential, bool) {
	if err := m.init(ctx); err != nil {
		slog.Error("mcp oauth: issuer unavailable", "issuer", m.issuer, "err", err)
		return ext.Credential{}, false
	}

	tok, err := m.verifier.Verify(ctx, raw)
	if err != nil {
		return ext.Credential{}, false
	}

	var claims struct {
		Audience audience `json:"aud"`
		Scope    string   `json:"scope"`
	}
	if err := tok.Claims(&claims); err != nil {
		return ext.Credential{}, false
	}

	// The audience check is not optional. Without it a token minted for a
	// different resource server on the same issuer would be accepted here,
	// which is exactly the risk a shared authorization server creates.
	if !claims.Audience.has(m.resource) {
		slog.Warn("mcp oauth: token rejected for wrong audience",
			"want", m.resource, "got", []string(claims.Audience))
		return ext.Credential{}, false
	}

	accountID, ok := m.accounts(tok.Subject)
	if !ok {
		// A valid token for somebody who has never signed in here. Refusing is
		// right: creating an account from a token would let any user of a
		// shared issuer materialise a Sitebin account without ever visiting it.
		slog.Info("mcp oauth: no account for subject", "subject", tok.Subject)
		return ext.Credential{}, false
	}

	return ext.Credential{AccountID: accountID, Scopes: parseScopes(claims.Scope)}, true
}

// audience decodes the `aud` claim, which JSON-encodes as either a string or an
// array of strings.
type audience []string

func (a *audience) UnmarshalJSON(b []byte) error {
	var one string
	if err := json.Unmarshal(b, &one); err == nil {
		*a = audience{one}
		return nil
	}
	var many []string
	if err := json.Unmarshal(b, &many); err != nil {
		return err
	}
	*a = many
	return nil
}

func (a audience) has(want string) bool {
	for _, v := range a {
		if v == want {
			return true
		}
	}
	return false
}

// parseScopes splits the space-delimited `scope` claim. An access token that
// carries no scope claim at all grants nothing here rather than everything —
// the opposite of an account API token, and deliberately so: an absent scope
// on an OAuth token means the authorization server told us nothing, and
// "nothing" must not read as "everything".
func parseScopes(s string) []string {
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return []string{noScope}
	}
	return fields
}

// noScope is a placeholder that satisfies "the slice is not empty" — which is
// what the core reads as "restricted" — while matching no real scope, so every
// tool refuses.
const noScope = "\x00none"
