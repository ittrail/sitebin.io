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

// Verify says why it refused only through the consent check, and only to a
// caller that installed a slot (the Git gate, whose credential helper never
// opens a browser on a 401): consent outstanding when the stack ANSWERED so,
// unavailable when the stack could not say. Verify itself notes nothing — a
// consent check that refuses without saying why notes nothing either — and
// every other refusal stays silent. What /mcp answers does not change.
func TestVerifyNotesConsentRefusal(t *testing.T) {
	ti := newTestIssuer(t)
	cs := newConsentStack(t)
	m := newMCPOAuth(ti.URL(), testResource, knownSubject(testSubject, "acct-1"))
	m.consent = cs.checker().complete
	tok := ti.sign(t, nil, nil)
	verify := func(raw string) (bool, ext.Refusal) {
		ctx := ext.WithRefusalNote(context.Background())
		_, ok := m.Verify(ctx, raw)
		return ok, ext.RefusalFrom(ctx)
	}

	cs.answer(200, outstandingBody)
	if ok, why := verify(tok); ok || why != ext.RefusalConsent {
		t.Fatalf("consent outstanding: accepted=%v noted %v, want refused with RefusalConsent", ok, why)
	}
	// Without a slot the refusal is the same refusal, and nothing breaks.
	if _, ok := m.Verify(context.Background(), tok); ok {
		t.Fatal("a token was accepted with consent outstanding and no slot")
	}

	// The stack could not say: refused, and noted as unavailable — never as
	// consent outstanding.
	for name, answer := range map[string][2]any{
		"stack 500":       {500, `{"error":"db down"}`},
		"stack no answer": {200, `{"gate":"enabled"}`},
	} {
		cs.answer(answer[0].(int), answer[1].(string))
		if ok, why := verify(tok); ok || why != ext.RefusalUnavailable {
			t.Errorf("%s: accepted=%v noted %v, want refused with RefusalUnavailable", name, ok, why)
		}
	}
	down := newConsentStack(t)
	unreachable := down.checker()
	down.srv.Close()
	m.consent = unreachable.complete
	if ok, why := verify(tok); ok || why != ext.RefusalUnavailable {
		t.Errorf("stack unreachable: accepted=%v noted %v, want refused with RefusalUnavailable", ok, why)
	}
	m.consent = cs.checker().complete

	// A subject that is not a stack user id is refused without a note: it is
	// neither the person's to fix nor transient.
	m.accounts = knownSubject("auth0|12345", "acct-1")
	if ok, why := verify(ti.sign(t, func(c map[string]any) { c["sub"] = "auth0|12345" }, nil)); ok || why != ext.RefusalNone {
		t.Errorf("non-UUID subject: accepted=%v noted %v, want refused with nothing noted", ok, why)
	}
	m.accounts = knownSubject(testSubject, "acct-1")

	// Verify adds no note of its own to a consent check's refusal.
	m.consent = func(context.Context, string) bool { return false }
	if ok, why := verify(tok); ok || why != ext.RefusalNone {
		t.Errorf("a silent consent refusal: accepted=%v noted %v, want refused with nothing noted", ok, why)
	}
	m.consent = cs.checker().complete

	// Every refusal before the consent check notes nothing.
	others := map[string]string{
		"wrong audience": ti.sign(t, func(c map[string]any) { c["aud"] = []string{"https://other.example/mcp"} }, nil),
		"expired":        ti.sign(t, func(c map[string]any) { c["exp"] = time.Now().Add(-time.Minute).Unix() }, nil),
		"ID token":       ti.sign(t, func(c map[string]any) { c["typ"] = "ID" }, nil),
		"not a JWS":      "eyJhbGciOiJub25lIn0.eyJzdWIiOiJ4In0.c2ln",
	}
	cs.answer(200, outstandingBody) // would note, if it were ever asked
	for name, raw := range others {
		if ok, why := verify(raw); ok || why != ext.RefusalNone {
			t.Errorf("%s: accepted=%v noted %v, want refused with nothing noted", name, ok, why)
		}
	}

	// Consent complete: accepted, nothing noted.
	cs.answer(200, completeBody)
	if ok, why := verify(tok); !ok || why != ext.RefusalNone {
		t.Fatalf("consent complete: accepted=%v noted %v", ok, why)
	}
}
