package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// The HTTP layer decides from the catalog table whether to challenge a call
// and which scope to demand, so the table and the registered tools must be the
// same set: a tool without a row would be neither challenged nor step-upped,
// and a row without a tool would be a promise nobody keeps.
func TestCatalogCoversEveryRegisteredTool(t *testing.T) {
	cs := connect(t, &fakeOps{}, nil)
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	registered := map[string]bool{}
	for _, tool := range res.Tools {
		registered[tool.Name] = true
		spec, ok := catalog[tool.Name]
		if !ok {
			t.Errorf("tool %q has no row in the catalog", tool.Name)
			continue
		}
		if spec.scope != ScopeRead && spec.scope != ScopeWrite {
			t.Errorf("tool %q has scope %q, not one of %v", tool.Name, spec.scope, AllScopes)
		}
		// Per-site means "addressed by edit_id", which is what makes an edit
		// password an alternative to an account. Derived from the schema the
		// agent actually sees, so the classification cannot be wrong quietly.
		schema, _ := json.Marshal(tool.InputSchema)
		var s struct {
			Properties map[string]any `json:"properties"`
		}
		if err := json.Unmarshal(schema, &s); err != nil {
			t.Fatalf("%s schema: %v", tool.Name, err)
		}
		_, hasEditID := s.Properties["edit_id"]
		_, hasPassword := s.Properties["edit_password"]
		if spec.perSite != hasEditID {
			t.Errorf("tool %q: perSite = %v, but its input has edit_id = %v", tool.Name, spec.perSite, hasEditID)
		}
		if spec.perSite && !hasPassword {
			t.Errorf("per-site tool %q takes no edit_password", tool.Name)
		}
	}
	for name := range catalog {
		if !registered[name] {
			t.Errorf("catalog row %q has no registered tool", name)
		}
	}
}

// The connector directories require every tool to say what it is and what it
// does to the world. Unset hints are not neutral: MCP defaults a missing
// destructiveHint and openWorldHint to TRUE, so every write tool states both.
func TestEveryToolDeclaresTitleAndHints(t *testing.T) {
	cs := connect(t, &fakeOps{}, nil)
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	for _, tool := range res.Tools {
		a := tool.Annotations
		if strings.TrimSpace(tool.Title) == "" || a == nil || a.Title != tool.Title {
			t.Errorf("tool %q: title %q, annotations %+v", tool.Name, tool.Title, a)
			continue
		}
		read := catalog[tool.Name].scope == ScopeRead
		if a.ReadOnlyHint != read {
			t.Errorf("tool %q: readOnlyHint = %v, but its scope is %s", tool.Name, a.ReadOnlyHint, catalog[tool.Name].scope)
		}
		if a.OpenWorldHint == nil {
			t.Errorf("tool %q has no explicit openWorldHint (the default is true)", tool.Name)
		}
		if read {
			if a.OpenWorldHint != nil && *a.OpenWorldHint {
				t.Errorf("read tool %q claims an open world", tool.Name)
			}
			continue
		}
		if a.DestructiveHint == nil {
			t.Errorf("write tool %q has no explicit destructiveHint (the default is true)", tool.Name)
		}
	}
	// Spot checks on the judgements most worth pinning: deleting is
	// destructive, publishing reaches the open web, creating destroys nothing.
	hint := func(name string) *struct{ destructive, openWorld, idempotent bool } {
		a := catalog[name].annotations
		return &struct{ destructive, openWorld, idempotent bool }{*a.DestructiveHint, *a.OpenWorldHint, a.IdempotentHint}
	}
	if h := hint("delete_site"); !h.destructive {
		t.Error("delete_site is not marked destructive")
	}
	if h := hint("create_site"); h.destructive || !h.openWorld || h.idempotent {
		t.Errorf("create_site hints = %+v", *h)
	}
	if h := hint("write_files"); !h.destructive || !h.openWorld {
		t.Errorf("write_files hints = %+v", *h)
	}
	if h := hint("add_domain"); h.destructive || !h.openWorld {
		t.Errorf("add_domain hints = %+v", *h)
	}
}

// The anonymous-call rule is what lets an agent work with edit passwords while
// OAuth is on, and challenges it only where an account is the only way in.
func TestAnonymousCallAllowed(t *testing.T) {
	cases := []struct {
		name     string
		tool     string
		args     string
		accounts bool
		want     bool
	}{
		{"per-site tool with a password", "get_site", `{"edit_id":"e1","edit_password":"pw"}`, true, true},
		{"per-site write with a password", "delete_site", `{"edit_id":"e1","edit_password":"pw"}`, true, true},
		{"per-site tool without a password", "get_site", `{"edit_id":"e1"}`, true, false},
		{"per-site tool with an empty password", "write_files", `{"edit_id":"e1","edit_password":""}`, true, false},
		{"per-site tool with a non-string password", "get_site", `{"edit_id":"e1","edit_password":123}`, true, false},
		{"per-site tool with no arguments at all", "get_site", ``, true, false},
		{"per-site tool with null arguments", "get_site", `null`, true, false},
		// Exact key match: a look-alike key is not the password the tool reads.
		{"per-site tool with a look-alike key", "get_site", `{"edit_id":"e1","EDIT_PASSWORD":"pw"}`, true, false},
		{"list_sites always needs an account", "list_sites", `{}`, true, false},
		{"list_sites needs one even without accounts", "list_sites", `{}`, false, false},
		{"create_site on an account instance", "create_site", `{"files":[]}`, true, false},
		{"create_site on an instance without accounts", "create_site", `{"files":[]}`, false, true},
		// A password does not stand in for an account where no site is named.
		{"create_site with a password field", "create_site", `{"edit_password":"pw"}`, true, false},
		{"unknown tool passes to the SDK", "no_such_tool", `{}`, true, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := AnonymousCallAllowed(c.tool, json.RawMessage(c.args), c.accounts); got != c.want {
				t.Fatalf("AnonymousCallAllowed(%s, %s, accounts=%v) = %v, want %v", c.tool, c.args, c.accounts, got, c.want)
			}
		})
	}
}

func TestToolScope(t *testing.T) {
	if s, ok := ToolScope("delete_site"); !ok || s != ScopeWrite {
		t.Errorf("delete_site scope = %q, %v", s, ok)
	}
	if s, ok := ToolScope("list_sites"); !ok || s != ScopeRead {
		t.Errorf("list_sites scope = %q, %v", s, ok)
	}
	if _, ok := ToolScope("no_such_tool"); ok {
		t.Error("an unknown tool has a scope")
	}
}

// What a credential holds is shown to agents and put in a 403 challenge, so
// only Sitebin's own scopes appear — never openid, never the extension's
// "no scope" placeholder.
func TestHeldScopes(t *testing.T) {
	got := HeldScopes([]string{"openid", ScopeWrite, "\x00none", "email", ScopeRead})
	if strings.Join(got, " ") != ScopeRead+" "+ScopeWrite {
		t.Errorf("HeldScopes = %q", got)
	}
	if got := HeldScopes([]string{"\x00none"}); len(got) != 0 {
		t.Errorf("the placeholder is a held scope: %q", got)
	}
}

// An OAuth credential is restricted even when it carries no scopes: the
// issuer telling us nothing must never read as "everything". An account API
// token with no scopes stays unrestricted, as it always was.
func TestOAuthWithoutScopesIsRestricted(t *testing.T) {
	if !Allows(nil, false, ScopeWrite) {
		t.Error("an account token with no scopes was restricted")
	}
	if Allows(nil, true, ScopeRead) || Allows(nil, true, ScopeWrite) {
		t.Error("an OAuth credential with no scopes was allowed")
	}
	if !Allows([]string{ScopeRead}, true, ScopeRead) || Allows([]string{ScopeRead}, true, ScopeWrite) {
		t.Error("a read-only OAuth credential is judged wrongly")
	}

	ops := &fakeOps{auth: Auth{AccountID: "a1", AccountsEnabled: true, OAuth: true}}
	cs := connect(t, ops, nil)
	res := call(t, cs, "get_site", map[string]any{"edit_id": "e1"})
	if !res.IsError || !strings.Contains(resultText(res), "was not granted") {
		t.Fatalf("a scope-less OAuth session read a site: %s", resultText(res))
	}
}

// The extension's placeholder for "no scope claim" is an implementation
// detail with a NUL byte in it; an agent is told plainly what it holds.
func TestRefusalNeverShowsThePlaceholder(t *testing.T) {
	ops := &fakeOps{auth: Auth{AccountID: "a1", AccountsEnabled: true, OAuth: true, Scopes: []string{"\x00none"}}}
	cs := connect(t, ops, nil)
	res := call(t, cs, "delete_site", map[string]any{"edit_id": "e1"})
	text := resultText(res)
	if !res.IsError || strings.Contains(text, "\x00") {
		t.Fatalf("refusal = %q", text)
	}
	if !strings.Contains(text, "holds no Sitebin scopes") {
		t.Errorf("refusal does not say the connection holds nothing: %q", text)
	}
}
