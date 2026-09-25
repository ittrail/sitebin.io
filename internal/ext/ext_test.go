package ext

import (
	"context"
	"testing"
)

// The MCP marker is what lets an OAuth access token act at all, so a bare
// context must never read as one, and nothing but WithMCPCaller may set it.
func TestMCPCallerMarker(t *testing.T) {
	if IsMCPCaller(context.Background()) {
		t.Fatal("a bare context reads as an MCP caller")
	}
	if !IsMCPCaller(WithMCPCaller(context.Background())) {
		t.Fatal("WithMCPCaller did not mark the context")
	}
	// A look-alike key of another type is a different key: the marker cannot
	// be forged by anything that does not call WithMCPCaller.
	type ctxKey int
	forged := context.WithValue(context.Background(), ctxKey(0), true)
	if IsMCPCaller(forged) {
		t.Fatal("a look-alike key forged the MCP marker")
	}
}

func TestCredentialInContext(t *testing.T) {
	if _, ok := CredentialFrom(context.Background()); ok {
		t.Fatal("a bare context carries a credential")
	}
	want := Credential{AccountID: "acct-1", Scopes: []string{"sitebin:sites:read"}, OAuth: true}
	got, ok := CredentialFrom(WithCredential(context.Background(), want))
	if !ok || got.AccountID != want.AccountID || !got.OAuth || len(got.Scopes) != 1 || got.Scopes[0] != want.Scopes[0] {
		t.Fatalf("CredentialFrom = %+v, %v", got, ok)
	}
	// The two keys are distinct: a credential is not an MCP marker, and the
	// marker is not a credential.
	if IsMCPCaller(WithCredential(context.Background(), want)) {
		t.Error("a credential reads as the MCP marker")
	}
	if _, ok := CredentialFrom(WithMCPCaller(context.Background())); ok {
		t.Error("the MCP marker reads as a credential")
	}
}
