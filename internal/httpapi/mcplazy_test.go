package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ittrail/sitebin.io/internal/ext"
	"github.com/ittrail/sitebin.io/internal/mcp"
)

// ---- lazy authentication: OAuth on, and only the calls that need an account
// are challenged ----
//
// Only an HTTP 401 makes a client start a sign-in, so these tests speak raw
// JSON-RPC where the status and the WWW-Authenticate header are the point,
// and a real MCP client where the point is that an agent can simply work.

const (
	challengeMetadata = `resource_metadata="http://sitebin.example/.well-known/oauth-protected-resource/mcp"`
	challengeScopes   = `scope="sitebin:sites:read sitebin:sites:write"`
)

// mcpRaw posts one JSON-RPC body to /mcp the way a streamable-HTTP client
// does, with no protocol version header, which the stateless server accepts
// without an initialize first.
func mcpRaw(t *testing.T, e *env, body string, header http.Header) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", "/mcp", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	for k, vs := range header {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	return e.public(t, req)
}

// toolCall is the JSON-RPC body of one tools/call.
func toolCall(t *testing.T, name string, args map[string]any) string {
	t.Helper()
	b, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": name, "arguments": args},
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func bearerHeader(secret string) http.Header {
	return http.Header{"Authorization": {"Bearer " + secret}}
}

// ownedSiteWithPassword creates a site owned by acct-1 through the JSON API,
// as the owner would on Sitebin's own pages, and returns its edit id and
// password.
func ownedSiteWithPassword(t *testing.T, e *env) (editID, pw string) {
	t.Helper()
	c := e.createSite(t, nil, map[string]string{"index.html": "<h1>owned</h1>"})
	return editIDFrom(t, c.EditURL), c.EditPassword
}

// The handshake needs no account: an agent connects, lists the tools, and is
// asked to sign in only when it calls something that needs one.
func TestMCPLazyAuthLetsTheHandshakeThrough(t *testing.T) {
	e := newEnv(t, oauthEnv())
	ext.Register(&fakeProvider{enabled: true})
	defer ext.Reset()

	cs := mcpClient(t, e, nil) // initialize, with no credential at all
	if _, err := cs.ListTools(context.Background(), nil); err != nil {
		t.Fatalf("tools/list without a credential: %v", err)
	}
	for _, body := range []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"ping"}`,
	} {
		if rec := mcpRaw(t, e, body, nil); rec.Code != 200 {
			t.Errorf("%s without a credential = %d, want 200 (%s)", body, rec.Code, rec.Body)
		}
	}
}

// A call that needs an account gets the challenge that starts a sign-in:
// where the metadata lives and what to ask for — and, since no credential was
// sent at all, no error code (RFC 6750 §3.1).
func TestMCPLazyAuthChallengesListSites(t *testing.T) {
	e := newEnv(t, oauthEnv())
	ext.Register(&fakeProvider{enabled: true})
	defer ext.Reset()

	rec := mcpRaw(t, e, toolCall(t, "list_sites", map[string]any{}), nil)
	if rec.Code != 401 {
		t.Fatalf("list_sites without a credential = %d, want 401 (%s)", rec.Code, rec.Body)
	}
	got := rec.Header().Get("WWW-Authenticate")
	for _, want := range []string{"Bearer ", challengeMetadata, challengeScopes} {
		if !strings.Contains(got, want) {
			t.Errorf("WWW-Authenticate %q lacks %q", got, want)
		}
	}
	if strings.Contains(got, "error=") {
		t.Errorf("a request with no credential got an error code: %q", got)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "sign in") || !strings.Contains(body, "edit_password") {
		t.Errorf("the body does not tell the agent what to do: %q", body)
	}
}

// The edit password is still a way in with OAuth on: a per-site tool that
// carries one reaches the tool, which verifies it exactly as before.
func TestMCPLazyAuthEditPasswordStillWorks(t *testing.T) {
	e := newEnv(t, oauthEnv())
	ext.Register(&fakeProvider{enabled: true, owner: "acct-1"})
	defer ext.Reset()
	editID, pw := ownedSiteWithPassword(t, e)

	cs := mcpClient(t, e, nil)
	res := mcpCall(t, cs, "get_site", map[string]any{"edit_id": editID, "edit_password": pw})
	if res.IsError {
		t.Fatalf("get_site with the edit password: %s", mcpText(res))
	}
	res = mcpCall(t, cs, "write_files", map[string]any{
		"edit_id": editID, "edit_password": pw,
		"files": []any{map[string]any{"path": "about.html", "text": "hi"}},
	})
	if res.IsError {
		t.Fatalf("write_files with the edit password: %s", mcpText(res))
	}
	// A wrong password still reaches the tool, and the tool still refuses it.
	res = mcpCall(t, cs, "get_site", map[string]any{"edit_id": editID, "edit_password": "wrong"})
	if !res.IsError || !strings.Contains(mcpText(res), "wrong edit_password") {
		t.Fatalf("a wrong password was not refused by the tool: %s", mcpText(res))
	}

	// Without the password the same call needs an account.
	rec := mcpRaw(t, e, toolCall(t, "get_site", map[string]any{"edit_id": editID}), nil)
	if rec.Code != 401 || !strings.Contains(rec.Header().Get("WWW-Authenticate"), challengeMetadata) {
		t.Fatalf("get_site without a password = %d %q", rec.Code, rec.Header().Get("WWW-Authenticate"))
	}
	if _, err := cs.CallTool(context.Background(), &sdk.CallToolParams{
		Name: "get_site", Arguments: map[string]any{"edit_id": editID},
	}); err == nil {
		t.Fatal("a client calling get_site without a password was not challenged")
	}
}

// create_site needs an account exactly where the instance has accounts.
func TestMCPLazyAuthCreateSite(t *testing.T) {
	body := func(t *testing.T) string {
		return toolCall(t, "create_site", map[string]any{"files": []any{map[string]any{"path": "index.html", "text": "hi"}}})
	}

	t.Run("accounts enabled", func(t *testing.T) {
		e := newEnv(t, oauthEnv())
		ext.Register(&fakeProvider{enabled: true})
		defer ext.Reset()
		if rec := mcpRaw(t, e, body(t), nil); rec.Code != 401 {
			t.Fatalf("create_site without a credential = %d, want 401", rec.Code)
		}
	})
	t.Run("accounts disabled", func(t *testing.T) {
		e := newEnv(t, oauthEnv())
		ext.Register(&fakeProvider{enabled: false})
		defer ext.Reset()
		cs := mcpClient(t, e, nil)
		mcpCreate(t, cs, "<h1>open</h1>")
	})
	t.Run("community build", func(t *testing.T) {
		e := newEnv(t, oauthEnv())
		cs := mcpClient(t, e, nil)
		mcpCreate(t, cs, "<h1>open</h1>")
	})
}

// A credential that does not check out is a 401 naming the error, whatever
// the call — a wrong credential never quietly degrades to "anonymous".
func TestMCPLazyAuthInvalidBearer(t *testing.T) {
	e := newEnv(t, oauthEnv())
	ext.Register(&fakeProvider{enabled: true, owner: "acct-1", bearer: map[string]string{}})
	defer ext.Reset()
	editID, pw := ownedSiteWithPassword(t, e)

	for _, body := range []string{
		`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`,
		toolCall(t, "get_site", map[string]any{"edit_id": editID, "edit_password": pw}),
	} {
		rec := mcpRaw(t, e, body, bearerHeader("expired-or-forged"))
		if rec.Code != 401 {
			t.Fatalf("an invalid bearer = %d, want 401", rec.Code)
		}
		got := rec.Header().Get("WWW-Authenticate")
		for _, want := range []string{`error="invalid_token"`, challengeMetadata, challengeScopes} {
			if !strings.Contains(got, want) {
				t.Errorf("WWW-Authenticate %q lacks %q", got, want)
			}
		}
	}
	// Another scheme is a credential too, and not one this endpoint takes.
	rec := mcpRaw(t, e, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`, http.Header{"Authorization": {"Basic eDp5"}})
	if rec.Code != 401 || !strings.Contains(rec.Header().Get("WWW-Authenticate"), `error="invalid_token"`) {
		t.Errorf("a Basic credential = %d %q", rec.Code, rec.Header().Get("WWW-Authenticate"))
	}
}

// A missing scope is the step-up signal: 403 insufficient_scope naming what
// the credential holds plus what the call needs, so the client asks for both.
func TestMCPLazyAuthStepUp(t *testing.T) {
	e := newEnv(t, oauthEnv())
	fp := &fakeProvider{
		enabled: true, owner: "acct-1",
		bearer: map[string]string{"jwt-ro": "acct-1", "jwt-none": "acct-1"},
		scopes: map[string][]string{"jwt-ro": {"openid", mcp.ScopeRead}, "jwt-none": {"\x00none"}},
		oauth:  map[string]bool{"jwt-ro": true, "jwt-none": true},
	}
	ext.Register(fp)
	defer ext.Reset()
	editID, _ := ownedSiteWithPassword(t, e)

	rec := mcpRaw(t, e, toolCall(t, "delete_site", map[string]any{"edit_id": editID}), bearerHeader("jwt-ro"))
	if rec.Code != 403 {
		t.Fatalf("read-only token calling delete_site = %d, want 403 (%s)", rec.Code, rec.Body)
	}
	got := rec.Header().Get("WWW-Authenticate")
	for _, want := range []string{`error="insufficient_scope"`, challengeScopes, challengeMetadata} {
		if !strings.Contains(got, want) {
			t.Errorf("WWW-Authenticate %q lacks %q", got, want)
		}
	}
	if _, err := e.st.ByEditID(editID); err != nil {
		t.Fatalf("the refused delete reached the store: %v", err)
	}

	// The read it holds works, end to end, with no password.
	cs := mcpClient(t, e, bearerHeader("jwt-ro"))
	if res := mcpCall(t, cs, "get_site", map[string]any{"edit_id": editID}); res.IsError {
		t.Fatalf("read-only token calling get_site: %s", mcpText(res))
	}

	// A token granted nothing is asked for exactly what the call needs, and
	// the placeholder never reaches the header.
	rec = mcpRaw(t, e, toolCall(t, "get_site", map[string]any{"edit_id": editID}), bearerHeader("jwt-none"))
	got = rec.Header().Get("WWW-Authenticate")
	if rec.Code != 403 || !strings.Contains(got, `scope="sitebin:sites:read"`) || strings.Contains(got, "\x00") {
		t.Fatalf("scope-less token = %d %q", rec.Code, got)
	}
}

// Account API tokens are the shipped way in and carry no scopes: they pass,
// on every tool, with OAuth on.
func TestMCPLazyAuthAccountTokenPasses(t *testing.T) {
	e := newEnv(t, oauthEnv())
	ext.Register(&fakeProvider{enabled: true, owner: "acct-1", bearer: map[string]string{"sbp_tok": "acct-1"}, owned: map[string][]string{"acct-1": {}}})
	defer ext.Reset()
	editID, _ := ownedSiteWithPassword(t, e)

	site := map[string]any{"edit_id": editID}
	for _, c := range []struct {
		name string
		args map[string]any
	}{{"list_sites", map[string]any{}}, {"get_site", site}, {"delete_site", site}} {
		if rec := mcpRaw(t, e, toolCall(t, c.name, c.args), bearerHeader("sbp_tok")); rec.Code != 200 {
			t.Errorf("%s with an account token = %d (%s)", c.name, rec.Code, rec.Body)
		}
	}
	if _, err := e.st.ByEditID(editID); err == nil {
		t.Error("delete_site with the owning account token did not delete")
	}
}

// The bearer is verified once per request: the wrapper resolves it and the
// tools read the result from the request context.
func TestMCPLazyAuthVerifiesOnce(t *testing.T) {
	e := newEnv(t, oauthEnv())
	fp := &fakeProvider{enabled: true, owner: "acct-1", bearer: map[string]string{"sbp_tok": "acct-1"}}
	ext.Register(fp)
	defer ext.Reset()
	editID, _ := ownedSiteWithPassword(t, e)

	fp.bearerCalls.Store(0)
	if rec := mcpRaw(t, e, toolCall(t, "get_site", map[string]any{"edit_id": editID}), bearerHeader("sbp_tok")); rec.Code != 200 {
		t.Fatalf("get_site = %d", rec.Code)
	}
	if n := fp.bearerCalls.Load(); n != 1 {
		t.Errorf("the bearer was resolved %d times for one request, want 1", n)
	}
}

// Authenticate takes the credential the wrapper verified, scopes and kind
// included, and does not ask the extension again.
func TestAuthenticateUsesTheVerifiedCredential(t *testing.T) {
	e := newEnv(t, nil)
	fp := &fakeProvider{enabled: true}
	ext.Register(fp)
	defer ext.Reset()

	r := httptest.NewRequest("POST", "/mcp", nil)
	r.Header.Set("Authorization", "Bearer whatever")
	r = r.WithContext(ext.WithCredential(r.Context(), ext.Credential{AccountID: "acct-9", Scopes: []string{mcp.ScopeRead}, OAuth: true}))
	auth := mcpOps{e.api}.Authenticate(r)
	if auth.AccountID != "acct-9" || !auth.OAuth || len(auth.Scopes) != 1 || auth.Scopes[0] != mcp.ScopeRead {
		t.Fatalf("Authenticate = %+v", auth)
	}
	if n := fp.bearerCalls.Load(); n != 0 {
		t.Errorf("Authenticate resolved the bearer again (%d calls)", n)
	}
}

// Every request through /mcp carries the MCP marker — OAuth on or off — and
// no request through the JSON API does. The marker is what lets the
// extension honour an OAuth token at all.
func TestMCPMarksItsRequests(t *testing.T) {
	for _, oauth := range []bool{false, true} {
		t.Run(map[bool]string{false: "oauth off", true: "oauth on"}[oauth], func(t *testing.T) {
			var over map[string]string
			if oauth {
				over = oauthEnv()
			}
			e := newEnv(t, over)
			fp := &fakeProvider{enabled: true, owner: "acct-1", bearer: map[string]string{"sbp_tok": "acct-1"}}
			ext.Register(fp)
			defer ext.Reset()

			cs := mcpClient(t, e, bearerHeader("sbp_tok"))
			mcpCreate(t, cs, "hi")
			if w := scriptCreate(t, e, map[string]string{"Authorization": "Bearer sbp_tok"}); w.Code != 201 {
				t.Fatalf("API create = %d %s", w.Code, w.Body)
			}

			fp.mu.Lock()
			creates, lookups := fp.createSawMCP, fp.bearerSawMCP
			fp.mu.Unlock()
			if len(creates) != 2 || !creates[0] || creates[1] {
				t.Errorf("create markers = %v, want [true false]", creates)
			}
			if len(lookups) == 0 {
				t.Error("the bearer was never resolved")
			}
			for i, m := range lookups {
				if !m {
					t.Errorf("bearer lookup %d on /mcp carried no marker", i)
				}
			}
		})
	}
}

// The tools stay the authority: a request the wrapper cannot read goes to
// the SDK untouched, and the tool behind it still refuses what it must.
func TestMCPLazyAuthPassesWhatItCannotRead(t *testing.T) {
	e := newEnv(t, oauthEnv())
	ext.Register(&fakeProvider{
		enabled: true, owner: "acct-1",
		bearer: map[string]string{"jwt-ro": "acct-1"},
		scopes: map[string][]string{"jwt-ro": {mcp.ScopeRead}},
		oauth:  map[string]bool{"jwt-ro": true},
	})
	defer ext.Reset()

	// Not JSON: the SDK's 400, not a sign-in.
	if rec := mcpRaw(t, e, `{not json`, nil); rec.Code == 401 {
		t.Errorf("a malformed body was challenged instead of reaching the SDK")
	}
	// Over the SDK's limit: the SDK's 413.
	big := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"list_sites","arguments":{"x":"` +
		strings.Repeat("a", int(mcp.MaxRequestBytes)) + `"}}}`
	if rec := mcpRaw(t, e, big, nil); rec.Code != 413 {
		t.Errorf("an oversized body = %d, want the SDK's 413", rec.Code)
	}
	// Not a POST: the SDK's 405.
	req := httptest.NewRequest("GET", "/mcp", nil)
	if rec := e.public(t, req); rec.Code != 405 {
		t.Errorf("GET /mcp = %d, want the SDK's 405", rec.Code)
	}
	// A batch (older protocol versions allow them) is not challenged — and the
	// tool inside still enforces its scope, which is the second line.
	batch := `[` + toolCall(t, "create_site", map[string]any{"files": []any{}}) + `]`
	rec := mcpRaw(t, e, batch, bearerHeader("jwt-ro"))
	if rec.Code != 200 {
		t.Fatalf("a batch = %d, want the SDK to answer it (%s)", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), "was not granted") {
		t.Errorf("the tool did not refuse a read-only token's create inside a batch: %s", rec.Body)
	}
}

// Browser-based clients preflight the metadata; both routes answer.
func TestMCPMetadataAnswersPreflight(t *testing.T) {
	e := newEnv(t, oauthEnv())
	for _, path := range []string{
		"/.well-known/oauth-protected-resource",
		"/.well-known/oauth-protected-resource/mcp",
	} {
		rec := e.public(t, httptest.NewRequest("OPTIONS", path, nil))
		if rec.Code != 204 || rec.Header().Get("Access-Control-Allow-Origin") != "*" {
			t.Errorf("OPTIONS %s = %d, ACAO %q", path, rec.Code, rec.Header().Get("Access-Control-Allow-Origin"))
		}
	}
}
