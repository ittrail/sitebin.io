//go:build ee

package ee

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ittrail/sitebin.io/internal/ext"
)

// The audience claim arrives as either a string or an array, and getting that
// wrong means either rejecting every token or accepting the wrong ones.
func TestAudienceDecoding(t *testing.T) {
	cases := []struct {
		raw  string
		want []string
	}{
		{`"https://a/mcp"`, []string{"https://a/mcp"}},
		{`["https://a/mcp","account"]`, []string{"https://a/mcp", "account"}},
		{`[]`, nil},
	}
	for _, c := range cases {
		var a audience
		if err := json.Unmarshal([]byte(c.raw), &a); err != nil {
			t.Fatalf("%s: %v", c.raw, err)
		}
		if len(a) != len(c.want) {
			t.Fatalf("%s -> %v, want %v", c.raw, []string(a), c.want)
		}
		for i := range c.want {
			if a[i] != c.want[i] {
				t.Errorf("%s -> %v, want %v", c.raw, []string(a), c.want)
			}
		}
	}
}

// This is the check that makes a shared authorization server safe: a token
// minted for another resource must not be accepted here.
func TestAudienceMatching(t *testing.T) {
	a := audience{"https://other.example/mcp", "account"}
	if a.has("https://app.sitebin.io/mcp") {
		t.Error("a token for another resource was accepted")
	}
	right := audience{"https://app.sitebin.io/mcp"}
	if !right.has("https://app.sitebin.io/mcp") {
		t.Error("the right audience was rejected")
	}
	empty := audience{}
	if empty.has("https://app.sitebin.io/mcp") {
		t.Error("an empty audience must not match")
	}
}

// An OAuth token with no scope claim must grant nothing, not everything. Empty
// scopes mean "unrestricted" to the core — which is correct for an account API
// token and would be a privilege escalation here.
func TestMissingScopeClaimGrantsNothing(t *testing.T) {
	got := parseScopes("")
	if len(got) == 0 {
		t.Fatal("an absent scope claim must not produce empty scopes, which the core reads as unrestricted")
	}
	if got[0] == "sitebin:sites:read" || got[0] == "sitebin:sites:write" {
		t.Errorf("the placeholder must match no real scope, got %q", got[0])
	}
}

func TestScopeParsing(t *testing.T) {
	got := parseScopes("openid sitebin:sites:read  sitebin:sites:write")
	want := "openid sitebin:sites:read sitebin:sites:write"
	if strings.Join(got, " ") != want {
		t.Errorf("parseScopes = %v", got)
	}
}

// No issuer means no verifier at all, which is what keeps the whole path inert
// on every instance that has not opted in.
func TestNoIssuerMeansNoVerifier(t *testing.T) {
	if newMCPOAuth("", "https://x/mcp", nil) != nil {
		t.Error("an empty issuer must produce no verifier")
	}
	if newMCPOAuth("   ", "https://x/mcp", nil) != nil {
		t.Error("a blank issuer must produce no verifier")
	}
	if newMCPOAuth("https://auth.example/realms/x", "https://x/mcp", nil) == nil {
		t.Error("a configured issuer must produce a verifier")
	}
}

// The verifier must not reach the network at construction: an authorization
// server that is briefly down should fail MCP calls, not stop Sitebin starting.
func TestVerifierDoesNotDialAtConstruction(t *testing.T) {
	m := newMCPOAuth("https://127.0.0.1:1/realms/nope", "https://x/mcp", nil)
	if m == nil {
		t.Fatal("expected a verifier")
	}
	if m.verifier != nil {
		t.Error("the verifier resolved the issuer eagerly")
	}
}

// Verify says why it refused in exactly one case, consent outstanding, and
// only to a caller that installed a slot for it: the Git gate turns that one
// refusal into a readable message, because a credential helper never opens a
// browser on a 401. Every other refusal stays silent, and what /mcp answers
// does not change.
func TestVerifyNotesConsentRefusal(t *testing.T) {
	ti := newTestIssuer(t)
	m := newMCPOAuth(ti.URL(), testResource, knownSubject(testSubject, "acct-1"))
	complete := false
	m.consent = func(context.Context, string) bool { return complete }
	tok := ti.sign(t, nil, nil)

	ctx := ext.WithRefusalNote(context.Background())
	if _, ok := m.Verify(ctx, tok); ok {
		t.Fatal("a token was accepted with consent outstanding")
	}
	if got := ext.RefusalFrom(ctx); got != ext.RefusalConsent {
		t.Fatalf("RefusalFrom = %v, want RefusalConsent", got)
	}
	// Without a slot the refusal is the same refusal, and nothing breaks.
	if _, ok := m.Verify(context.Background(), tok); ok {
		t.Fatal("a token was accepted with consent outstanding and no slot")
	}

	// Every other refusal notes nothing.
	others := map[string]string{
		"wrong audience": ti.sign(t, func(c map[string]any) { c["aud"] = []string{"https://other.example/mcp"} }, nil),
		"expired":        ti.sign(t, func(c map[string]any) { c["exp"] = time.Now().Add(-time.Minute).Unix() }, nil),
		"ID token":       ti.sign(t, func(c map[string]any) { c["typ"] = "ID" }, nil),
		"not a JWS":      "eyJhbGciOiJub25lIn0.eyJzdWIiOiJ4In0.c2ln",
	}
	for name, raw := range others {
		ctx := ext.WithRefusalNote(context.Background())
		if _, ok := m.Verify(ctx, raw); ok {
			t.Fatalf("%s: accepted", name)
		}
		if got := ext.RefusalFrom(ctx); got != ext.RefusalNone {
			t.Errorf("%s: RefusalFrom = %v, want RefusalNone", name, got)
		}
	}

	// Consent complete: accepted, nothing noted.
	complete = true
	ctx = ext.WithRefusalNote(context.Background())
	if _, ok := m.Verify(ctx, tok); !ok {
		t.Fatal("a token was refused with consent complete")
	}
	if got := ext.RefusalFrom(ctx); got != ext.RefusalNone {
		t.Errorf("an accepted token noted %v", got)
	}
}
