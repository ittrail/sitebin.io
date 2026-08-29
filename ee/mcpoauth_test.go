//go:build ee

package ee

import (
	"encoding/json"
	"strings"
	"testing"
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
