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

// The Git marker is the second place an OAuth access token may act. It is a
// marker of its own: a Git request is not an MCP request, and the OAuth
// surface is exactly the two of them.
func TestGitCallerMarkerAndOAuthSurface(t *testing.T) {
	bare := context.Background()
	if IsGitCaller(bare) || OAuthSurface(bare) {
		t.Fatal("a bare context reads as a Git caller or an OAuth surface")
	}
	git := WithGitCaller(bare)
	if !IsGitCaller(git) || !OAuthSurface(git) {
		t.Fatal("WithGitCaller did not mark the context")
	}
	if IsMCPCaller(git) {
		t.Error("the Git marker reads as the MCP marker")
	}
	mcp := WithMCPCaller(bare)
	if IsGitCaller(mcp) {
		t.Error("the MCP marker reads as the Git marker")
	}
	if !OAuthSurface(mcp) {
		t.Error("an MCP request is not an OAuth surface")
	}
	type ctxKey int
	forged := context.WithValue(bare, ctxKey(1), true)
	if IsGitCaller(forged) || OAuthSurface(forged) {
		t.Fatal("a look-alike key forged the Git marker")
	}
	// A credential in the context is not a marker either.
	if OAuthSurface(WithCredential(bare, Credential{AccountID: "a", OAuth: true})) {
		t.Error("a credential reads as an OAuth surface")
	}
}

// A refusal note is a slot the caller installs: without one, noting is a
// no-op and the answer is RefusalNone, so a verifier can note on every path
// (the community build has no verifier at all and never notes anything).
func TestRefusalNote(t *testing.T) {
	bare := context.Background()
	NoteRefusal(bare, RefusalConsent) // no slot: must not panic, must not stick
	if got := RefusalFrom(bare); got != RefusalNone {
		t.Fatalf("RefusalFrom(bare) = %v, want RefusalNone", got)
	}
	ctx := WithRefusalNote(bare)
	if got := RefusalFrom(ctx); got != RefusalNone {
		t.Fatalf("a fresh slot reads %v, want RefusalNone", got)
	}
	// A context derived from the slotted one writes into the same slot: the
	// verifier wraps the request's context before it notes.
	type k struct{}
	NoteRefusal(context.WithValue(ctx, k{}, 1), RefusalConsent)
	if got := RefusalFrom(ctx); got != RefusalConsent {
		t.Fatalf("RefusalFrom = %v, want RefusalConsent", got)
	}
	// The last note stands, and the reasons are distinct values.
	NoteRefusal(ctx, RefusalUnavailable)
	if got := RefusalFrom(ctx); got != RefusalUnavailable {
		t.Fatalf("RefusalFrom = %v, want RefusalUnavailable", got)
	}
	if RefusalNone == RefusalConsent || RefusalConsent == RefusalUnavailable || RefusalNone == RefusalUnavailable {
		t.Fatal("refusal reasons collide")
	}
	// Each slot is its own: a second request's note is not this one's.
	if got := RefusalFrom(WithRefusalNote(bare)); got != RefusalNone {
		t.Errorf("a new slot inherited a note: %v", got)
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

func TestLimitsOr(t *testing.T) {
	d := ContainerLimits{MemoryMB: 512, CPUs: 0.5, Pids: 256}
	if got := (ContainerLimits{}).Or(d); got != d {
		t.Errorf("unset = %+v", got)
	}
	if got := (ContainerLimits{MemoryMB: 1024, Pids: 64}).Or(d); got != (ContainerLimits{MemoryMB: 1024, CPUs: 0.5, Pids: 64}) {
		t.Errorf("partial = %+v", got)
	}
}
