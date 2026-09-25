//go:build ee

package ee

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

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
	// now is the clock the discovery retry is measured on; a field so a test
	// can move it.
	now func() time.Time

	// mu guards the lazily discovered verifier and the last failed attempt.
	// A failure is remembered only to space out retries, never as the answer
	// until restart.
	mu       sync.Mutex
	verifier *oidc.IDTokenVerifier
	lastTry  time.Time
	lastErr  error
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
	return &mcpOAuth{issuer: issuer, resource: resource, accounts: accounts, now: time.Now}
}

const (
	// discoveryTimeout bounds one discovery on its own clock. It never runs
	// under a caller's request context: a client that hangs up during the
	// very first MCP call must not decide whether OAuth works for everyone
	// after it.
	discoveryTimeout = 10 * time.Second
	// discoveryRetry is the least time between two attempts after a failure,
	// so an issuer that is restarting is not asked again by every request
	// that arrives meanwhile.
	discoveryRetry = 10 * time.Second
)

// tokenVerifier resolves the issuer's metadata and key set lazily, and
// retries after a failure.
//
// Lazily because startup must not depend on the issuer being reachable: an
// authorization server that is briefly down should make MCP OAuth calls fail,
// not stop Sitebin from serving sites. Retried because the first version
// cached the first failure until the next restart, which turned a
// ten-second blip at the wrong moment into an outage.
//
// Callers wait on the lock while a discovery runs; they would otherwise all
// fail, or all ask the issuer at once.
func (m *mcpOAuth) tokenVerifier() (*oidc.IDTokenVerifier, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.verifier != nil {
		return m.verifier, nil
	}
	now := m.now()
	if !m.lastTry.IsZero() && now.Sub(m.lastTry) < discoveryRetry {
		return nil, m.lastErr
	}
	m.lastTry = now
	ctx, cancel := context.WithTimeout(context.Background(), discoveryTimeout)
	defer cancel()
	prov, err := oidc.NewProvider(ctx, m.issuer)
	if err != nil {
		m.lastErr = fmt.Errorf("mcp oauth: discover %s: %w", m.issuer, err)
		slog.Error("mcp oauth: issuer unavailable; retrying on a request at least 10s from now", "issuer", m.issuer, "err", err)
		return nil, m.lastErr
	}
	// SkipClientIDCheck because the audience this resource server cares
	// about is its own resource identifier, not a client id — the check in
	// Verify is the one that matters and it is stricter than the default.
	m.verifier = prov.Verifier(&oidc.Config{SkipClientIDCheck: true})
	return m.verifier, nil
}

// Verify checks an access token and returns the credential it grants.
//
// ok=false for anything it does not fully trust. It never reports *why* to the
// caller: a resource server that distinguishes "expired" from "wrong audience"
// from "unknown account" for an unauthenticated caller is an oracle.
func (m *mcpOAuth) Verify(ctx context.Context, raw string) (ext.Credential, bool) {
	v, err := m.tokenVerifier()
	if err != nil {
		return ext.Credential{}, false // logged where the attempt failed
	}

	tok, err := v.Verify(ctx, raw)
	if err != nil {
		return ext.Credential{}, false
	}

	var claims struct {
		Audience audience `json:"aud"`
		Scope    string   `json:"scope"`
		Type     *string  `json:"typ"`
	}
	if err := tok.Claims(&claims); err != nil {
		return ext.Credential{}, false
	}

	// Only access tokens. The go-oidc verifier checks what an ID token and an
	// access token share — signature, issuer, expiry — and so accepts both;
	// a Keycloak ID token carrying the resource in its audience used to pass
	// here. Keycloak marks its tokens in the `typ` claim, and an RFC 9068
	// issuer in the JOSE header, so a token either one marks as something
	// else is refused. The header is read after the signature check, which
	// covers it.
	if claims.Type != nil && !strings.EqualFold(*claims.Type, "Bearer") {
		slog.Info("mcp oauth: token refused: not an access token", "typ", *claims.Type)
		return ext.Credential{}, false
	}
	if typ, ok := headerType(raw); !ok {
		slog.Info("mcp oauth: token refused: not an access token", "header_typ", typ)
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

	return ext.Credential{AccountID: accountID, Scopes: parseScopes(claims.Scope), OAuth: true}, true
}

// headerType reports the JOSE header's `typ` and whether it is one an access
// token carries: absent, JWT (what Keycloak writes), or RFC 9068's at+jwt,
// with or without its media-type prefix. Anything else — an ID token's or a
// logout token's type, say — is refused, and so is a header that cannot be
// read.
func headerType(raw string) (string, bool) {
	head, _, ok := strings.Cut(raw, ".")
	if !ok {
		return "", false
	}
	b, err := base64.RawURLEncoding.DecodeString(head)
	if err != nil {
		return "", false
	}
	var h struct {
		Typ *string `json:"typ"`
	}
	if err := json.Unmarshal(b, &h); err != nil {
		return "", false
	}
	if h.Typ == nil {
		return "", true
	}
	switch strings.ToLower(*h.Typ) {
	case "jwt", "at+jwt", "application/at+jwt":
		return *h.Typ, true
	}
	return *h.Typ, false
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
