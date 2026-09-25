package httpapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ittrail/sitebin.io/internal/ext"
	"github.com/ittrail/sitebin.io/internal/mcp"
	"github.com/ittrail/sitebin.io/internal/store"
)

// mcpClient connects a real MCP client to the real mounted /mcp endpoint, so
// these tests exercise the whole path: JSON-RPC framing, the tool schemas, the
// adapter, and the store underneath it. Nothing is faked but the clock.
func mcpClient(t *testing.T, e *env, header http.Header) *sdk.ClientSession {
	t.Helper()
	// The public mux resolves the site by Host, so requests must look like they
	// arrived on the main domain, exactly as env.public arranges.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Host = "sitebin.example"
		e.api.Public().ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)

	c := sdk.NewClient(&sdk.Implementation{Name: "test", Version: "1"}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	cs, err := c.Connect(ctx, &sdk.StreamableClientTransport{
		Endpoint:             srv.URL + "/mcp",
		DisableStandaloneSSE: true,
		HTTPClient:           &http.Client{Transport: hdrRT{header}},
	}, nil)
	if err != nil {
		t.Fatalf("connect to /mcp: %v", err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

type hdrRT struct{ h http.Header }

func (t hdrRT) RoundTrip(r *http.Request) (*http.Response, error) {
	for k, vs := range t.h {
		for _, v := range vs {
			r.Header.Add(k, v)
		}
	}
	return http.DefaultTransport.RoundTrip(r)
}

func mcpCall(t *testing.T, cs *sdk.ClientSession, name string, args map[string]any) *sdk.CallToolResult {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &sdk.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("CallTool(%s): %v", name, err)
	}
	return res
}

func mcpText(res *sdk.CallToolResult) string {
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*sdk.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

// mcpCreate publishes a site through MCP and returns its edit id and password.
func mcpCreate(t *testing.T, cs *sdk.ClientSession, body string) (editID, editPassword string) {
	t.Helper()
	res := mcpCall(t, cs, "create_site", map[string]any{
		"files": []any{map[string]any{"path": "index.html", "text": body}},
	})
	if res.IsError {
		t.Fatalf("create_site: %s", mcpText(res))
	}
	m, ok := res.StructuredContent.(map[string]any)
	if !ok {
		t.Fatalf("no structured content: %#v", res.StructuredContent)
	}
	id, _ := m["edit_id"].(string)
	pw, _ := m["edit_password"].(string)
	if id == "" || pw == "" {
		t.Fatalf("create_site returned no credentials: %v", m)
	}
	return id, pw
}

// ---- community build: no provider, fully open, exactly like the JSON API ----

func TestMCPCommunityCreateAndRead(t *testing.T) {
	e := newEnv(t, nil)
	cs := mcpClient(t, e, nil)

	editID, pw := mcpCreate(t, cs, "<h1>hello</h1>")

	res := mcpCall(t, cs, "read_file", map[string]any{
		"edit_id": editID, "edit_password": pw, "path": "index.html",
	})
	if res.IsError {
		t.Fatalf("read_file: %s", mcpText(res))
	}
	// The structured result is the contract; the text content is its JSON
	// serialisation, so HTML arrives escaped there.
	m := res.StructuredContent.(map[string]any)
	if m["text"] != "<h1>hello</h1>" {
		t.Errorf("read_file returned %v", m)
	}
}

// The provenance marker is the point of the Origin field: a site made by an
// agent must be identifiable as one afterwards.
func TestMCPCreateStampsOrigin(t *testing.T) {
	e := newEnv(t, nil)
	cs := mcpClient(t, e, nil)
	editID, _ := mcpCreate(t, cs, "hi")

	site, err := e.st.ByEditID(editID)
	if err != nil {
		t.Fatal(err)
	}
	if site.Meta.Origin != store.OriginMCP {
		t.Errorf("meta.origin = %q, want %q", site.Meta.Origin, store.OriginMCP)
	}
}

// A site made through the JSON API must NOT be marked as an agent's, or the
// marker means nothing.
func TestAPICreateLeavesOriginEmpty(t *testing.T) {
	e := newEnv(t, nil)
	out := e.createSite(t, nil, map[string]string{"index.html": "hi"})
	site, err := e.st.ByEditID(editIDFrom(t, out.EditURL))
	if err != nil {
		t.Fatal(err)
	}
	if site.Meta.Origin != "" {
		t.Errorf("meta.origin = %q, want empty for an API-created site", site.Meta.Origin)
	}
}

func TestMCPWriteAndDeleteFiles(t *testing.T) {
	e := newEnv(t, nil)
	cs := mcpClient(t, e, nil)
	editID, pw := mcpCreate(t, cs, "one")

	res := mcpCall(t, cs, "write_files", map[string]any{
		"edit_id": editID, "edit_password": pw,
		"files": []any{
			map[string]any{"path": "about.html", "text": "<p>about</p>"},
			map[string]any{"path": "img/dot.png", "base64": base64.StdEncoding.EncodeToString([]byte{0x89, 'P', 'N', 'G'})},
		},
	})
	if res.IsError {
		t.Fatalf("write_files: %s", mcpText(res))
	}
	site, _ := e.st.ByEditID(editID)
	files, _ := e.st.ListFiles(site)
	if len(files) != 3 {
		t.Fatalf("files = %v, want index.html + about.html + img/dot.png", files)
	}

	// A binary file must come back as base64, not as mangled "text".
	res = mcpCall(t, cs, "read_file", map[string]any{
		"edit_id": editID, "edit_password": pw, "path": "img/dot.png",
	})
	m := res.StructuredContent.(map[string]any)
	if m["base64"] == nil || m["text"] != nil && m["text"] != "" {
		t.Errorf("binary file came back as %v", m)
	}

	res = mcpCall(t, cs, "delete_file", map[string]any{
		"edit_id": editID, "edit_password": pw, "path": "about.html",
	})
	if res.IsError {
		t.Fatalf("delete_file: %s", mcpText(res))
	}
	files, _ = e.st.ListFiles(site)
	if len(files) != 2 {
		t.Errorf("files after delete = %v", files)
	}
}

func TestMCPWriteFilesReplace(t *testing.T) {
	e := newEnv(t, nil)
	cs := mcpClient(t, e, nil)
	editID, pw := mcpCreate(t, cs, "one")

	mcpCall(t, cs, "write_files", map[string]any{
		"edit_id": editID, "edit_password": pw, "replace": true,
		"files": []any{map[string]any{"path": "only.html", "text": "only"}},
	})
	site, _ := e.st.ByEditID(editID)
	files, _ := e.st.ListFiles(site)
	if len(files) != 1 || files[0].Path != "only.html" {
		t.Errorf("replace did not clear the site: %v", files)
	}
}

func TestMCPUpdateSiteSettings(t *testing.T) {
	e := newEnv(t, nil)
	cs := mcpClient(t, e, nil)
	editID, pw := mcpCreate(t, cs, "hi")

	res := mcpCall(t, cs, "update_site", map[string]any{
		"edit_id": editID, "edit_password": pw,
		"settings": map[string]any{"view_password": "sesame", "spa_fallback": true},
	})
	if res.IsError {
		t.Fatalf("update_site: %s", mcpText(res))
	}
	site, _ := e.st.ByEditID(editID)
	if !site.Meta.ViewPasswordProtected || !site.Meta.SPAFallback {
		t.Errorf("settings not applied: %+v", site.Meta)
	}
}

func TestMCPDeleteSite(t *testing.T) {
	e := newEnv(t, nil)
	cs := mcpClient(t, e, nil)
	editID, pw := mcpCreate(t, cs, "hi")

	res := mcpCall(t, cs, "delete_site", map[string]any{"edit_id": editID, "edit_password": pw})
	if res.IsError {
		t.Fatalf("delete_site: %s", mcpText(res))
	}
	if _, err := e.st.ByEditID(editID); err == nil {
		t.Error("site still exists after delete_site")
	}
}

func TestMCPDownloadSiteReturnsZip(t *testing.T) {
	e := newEnv(t, nil)
	cs := mcpClient(t, e, nil)
	editID, pw := mcpCreate(t, cs, "<h1>zip me</h1>")

	res := mcpCall(t, cs, "download_site", map[string]any{"edit_id": editID, "edit_password": pw})
	if res.IsError {
		t.Fatalf("download_site: %s", mcpText(res))
	}
	er, ok := res.Content[0].(*sdk.EmbeddedResource)
	if !ok {
		t.Fatalf("content is %T", res.Content[0])
	}
	if string(er.Resource.Blob[:2]) != "PK" {
		t.Errorf("not a zip: %q", er.Resource.Blob[:4])
	}
}

// ---- the password gate ----

func TestMCPWrongEditPassword(t *testing.T) {
	e := newEnv(t, nil)
	cs := mcpClient(t, e, nil)
	editID, _ := mcpCreate(t, cs, "hi")

	res := mcpCall(t, cs, "get_site", map[string]any{"edit_id": editID, "edit_password": "nope"})
	if !res.IsError || !strings.Contains(mcpText(res), "wrong edit_password") {
		t.Errorf("res = %v %s", res.IsError, mcpText(res))
	}
}

// The message has to tell the agent what to do, not only that it failed.
func TestMCPMissingEditPasswordExplainsItself(t *testing.T) {
	e := newEnv(t, nil)
	cs := mcpClient(t, e, nil)
	editID, _ := mcpCreate(t, cs, "hi")

	res := mcpCall(t, cs, "get_site", map[string]any{"edit_id": editID})
	if !res.IsError {
		t.Fatal("expected a refusal")
	}
	if !strings.Contains(mcpText(res), "edit_password") {
		t.Errorf("message = %s", mcpText(res))
	}
}

func TestMCPUnknownEditID(t *testing.T) {
	e := newEnv(t, nil)
	cs := mcpClient(t, e, nil)
	res := mcpCall(t, cs, "get_site", map[string]any{"edit_id": "nosuchid", "edit_password": "x"})
	if !res.IsError {
		t.Fatal("expected a refusal")
	}
}

// Review fix round 1, Finding 2: an upload token handed to MCP as
// edit_password must not be tried as one — it belongs to open_upload's own
// upload_url/webdav_url, not to the JSON-RPC tools.
func TestMCPEditPasswordRejectsAnUploadToken(t *testing.T) {
	e := newEnv(t, nil)
	cs := mcpClient(t, e, nil)
	editID, _ := mcpCreate(t, cs, "hi")

	res := mcpCall(t, cs, "get_site", map[string]any{"edit_id": editID, "edit_password": "sbu_notatoken"})
	if !res.IsError || !strings.Contains(mcpText(res), "upload token") {
		t.Errorf("res = %v %s", res.IsError, mcpText(res))
	}
}

// ---- list_sites without a token ----

func TestMCPListSitesWithoutAccountsExplains(t *testing.T) {
	e := newEnv(t, nil)
	cs := mcpClient(t, e, nil)
	res := mcpCall(t, cs, "list_sites", map[string]any{})
	if !res.IsError {
		t.Fatal("the community build has no account to list sites for")
	}
	if !strings.Contains(mcpText(res), "no accounts") {
		t.Errorf("message = %s", mcpText(res))
	}
}

// ---- the config switch ----

func TestMCPDisabledIsNotMounted(t *testing.T) {
	e := newEnv(t, map[string]string{"SITEBIN_MCP_ENABLED": "false"})
	req := httptest.NewRequest("POST", "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := e.public(t, req)
	if rec.Code != 404 {
		t.Errorf("POST /mcp with MCP disabled = %d, want 404", rec.Code)
	}
}

func TestMCPEnabledByDefault(t *testing.T) {
	e := newEnv(t, nil)
	cs := mcpClient(t, e, nil)
	if _, err := cs.ListTools(context.Background(), nil); err != nil {
		t.Fatalf("tools/list: %v", err)
	}
}

// ---- accounts enabled ----

// On a gated instance MCP is an account feature, exactly like the JSON API,
// and it does NOT get the browser escape hatch the API gives Sitebin's own
// pages: an MCP client is never one of those pages.
func TestMCPCreateRefusedWithoutAccount(t *testing.T) {
	e := newEnv(t, nil)
	ext.Register(&fakeProvider{enabled: true})
	defer ext.Reset()

	cs := mcpClient(t, e, nil)
	res := mcpCall(t, cs, "create_site", map[string]any{
		"files": []any{map[string]any{"path": "index.html", "text": "hi"}},
	})
	if !res.IsError {
		t.Fatal("an anonymous MCP caller must not create sites on a gated instance")
	}
	if !strings.Contains(mcpText(res), "sign in") {
		t.Errorf("message = %s", mcpText(res))
	}
}

// Browser fetch metadata must not open the create path for MCP, even though
// the same headers do open it for the JSON API.
func TestMCPCreateIgnoresBrowserHeaders(t *testing.T) {
	e := newEnv(t, nil)
	ext.Register(&fakeProvider{enabled: true})
	defer ext.Reset()

	cs := mcpClient(t, e, http.Header{
		"Sec-Fetch-Site": {"same-origin"},
		"Origin":         {"http://sitebin.example"},
	})
	res := mcpCall(t, cs, "create_site", map[string]any{
		"files": []any{map[string]any{"path": "index.html", "text": "hi"}},
	})
	if !res.IsError {
		t.Fatal("browser-shaped headers must not let an MCP client create anonymous sites")
	}
}

func TestMCPTokenCreatesOwnedSite(t *testing.T) {
	e := newEnv(t, nil)
	ext.Register(&fakeProvider{
		enabled: true,
		owner:   "acct-1",
		bearer:  map[string]string{"sbp_tok": "acct-1"},
	})
	defer ext.Reset()

	cs := mcpClient(t, e, http.Header{"Authorization": {"Bearer sbp_tok"}})
	editID, _ := mcpCreate(t, cs, "<h1>owned</h1>")

	site, err := e.st.ByEditID(editID)
	if err != nil {
		t.Fatal(err)
	}
	if site.Meta.OwnerAccountID != "acct-1" {
		t.Errorf("owner = %q, want acct-1", site.Meta.OwnerAccountID)
	}
	if site.Meta.Origin != store.OriginMCP {
		t.Errorf("origin = %q", site.Meta.Origin)
	}
}

// The token stands in for the edit password on the account's own sites. That
// is the whole point of a token: an agent should not have to carry one secret
// per site.
func TestMCPTokenReplacesEditPassword(t *testing.T) {
	e := newEnv(t, nil)
	ext.Register(&fakeProvider{
		enabled: true,
		owner:   "acct-1",
		bearer:  map[string]string{"sbp_tok": "acct-1"},
	})
	defer ext.Reset()

	cs := mcpClient(t, e, http.Header{"Authorization": {"Bearer sbp_tok"}})
	editID, _ := mcpCreate(t, cs, "hi")

	res := mcpCall(t, cs, "get_site", map[string]any{"edit_id": editID})
	if res.IsError {
		t.Fatalf("an owning token should not need a password: %s", mcpText(res))
	}
}

// A token reaches only its own account's sites — never another account's.
func TestMCPTokenCannotReachAnotherAccountsSite(t *testing.T) {
	e := newEnv(t, nil)
	ext.Register(&fakeProvider{
		enabled: true,
		owner:   "acct-1",
		bearer:  map[string]string{"sbp_a": "acct-1", "sbp_b": "acct-2"},
	})
	defer ext.Reset()

	owner := mcpClient(t, e, http.Header{"Authorization": {"Bearer sbp_a"}})
	editID, _ := mcpCreate(t, owner, "private")

	other := mcpClient(t, e, http.Header{"Authorization": {"Bearer sbp_b"}})
	res := mcpCall(t, other, "get_site", map[string]any{"edit_id": editID})
	if !res.IsError {
		t.Fatal("another account's token reached this site")
	}
	if !strings.Contains(mcpText(res), "not owned by the connected account") {
		t.Errorf("message = %s", mcpText(res))
	}
}

// An anonymous site on a gated instance has no API, so it has no MCP either —
// even when the caller knows its edit password.
func TestMCPRefusesAnonymousSiteOnGatedInstance(t *testing.T) {
	e := newEnv(t, nil)
	// Create the site while open, then turn accounts on: this is the shape of
	// a real instance that enabled accounts after the fact, and of a drop made
	// on Sitebin's own pages.
	site, pw, err := e.st.Create()
	if err != nil {
		t.Fatal(err)
	}
	ext.Register(&fakeProvider{enabled: true})
	defer ext.Reset()

	cs := mcpClient(t, e, nil)
	res := mcpCall(t, cs, "get_site", map[string]any{
		"edit_id": site.EditID, "edit_password": pw,
	})
	if !res.IsError {
		t.Fatal("an account-less site must not be scriptable")
	}
	if !strings.Contains(mcpText(res), "without an account") {
		t.Errorf("message = %s", mcpText(res))
	}
}

func TestMCPListSitesWithToken(t *testing.T) {
	e := newEnv(t, nil)
	p := &fakeProvider{
		enabled: true,
		owner:   "acct-1",
		bearer:  map[string]string{"sbp_tok": "acct-1"},
		owned:   map[string][]string{},
	}
	ext.Register(p)
	defer ext.Reset()

	cs := mcpClient(t, e, http.Header{"Authorization": {"Bearer sbp_tok"}})
	editID, _ := mcpCreate(t, cs, "hi")
	site, _ := e.st.ByEditID(editID)
	p.owned["acct-1"] = []string{site.ViewID}

	res := mcpCall(t, cs, "list_sites", map[string]any{})
	if res.IsError {
		t.Fatalf("list_sites: %s", mcpText(res))
	}
	text := mcpText(res)
	if !strings.Contains(text, editID) {
		t.Errorf("listing does not carry the edit id: %s", text)
	}
	if !strings.Contains(text, `"origin":"mcp"`) && !strings.Contains(text, `"origin": "mcp"`) {
		t.Errorf("listing does not report the origin: %s", text)
	}
}

// A stale ownership record — the site was deleted straight through the store —
// is a row to skip, not a failed listing.
func TestMCPListSitesSkipsStaleIDs(t *testing.T) {
	e := newEnv(t, nil)
	p := &fakeProvider{
		enabled: true,
		owner:   "acct-1",
		bearer:  map[string]string{"sbp_tok": "acct-1"},
		owned:   map[string][]string{"acct-1": {"gone-id"}},
	}
	ext.Register(p)
	defer ext.Reset()

	cs := mcpClient(t, e, http.Header{"Authorization": {"Bearer sbp_tok"}})
	res := mcpCall(t, cs, "list_sites", map[string]any{})
	if res.IsError {
		t.Fatalf("a stale id must not fail the listing: %s", mcpText(res))
	}
}

// Custom domains stay an enterprise capability over MCP, exactly as over HTTP.
func TestMCPAddDomainRefusedInCommunityBuild(t *testing.T) {
	e := newEnv(t, nil)
	cs := mcpClient(t, e, nil)
	editID, pw := mcpCreate(t, cs, "hi")

	res := mcpCall(t, cs, "add_domain", map[string]any{
		"edit_id": editID, "edit_password": pw, "domain": "docs.example.com",
	})
	if !res.IsError || !strings.Contains(mcpText(res), "enterprise") {
		t.Errorf("res = %v %s", res.IsError, mcpText(res))
	}
}

// ---- OAuth: Sitebin as a protected resource ----
//
// These cover the wiring, not the token cryptography: verifying a real JWT
// needs an issuer, and a test that reaches the network is a test that fails for
// reasons unrelated to Sitebin. What matters here is that the endpoint
// advertises itself correctly, challenges correctly, and — the regression that
// would hurt most — keeps accepting account API tokens once OAuth is on.

const testIssuer = "https://auth.example.test/realms/x"

func oauthEnv() map[string]string {
	return map[string]string{"SITEBIN_MCP_OAUTH_ISSUER": testIssuer}
}

// With no issuer configured nothing changes: no discovery routes, no
// challenge. Every existing instance is in this state.
func TestMCPOAuthOffByDefault(t *testing.T) {
	e := newEnv(t, nil)
	for _, path := range []string{
		"/.well-known/oauth-protected-resource",
		"/.well-known/oauth-protected-resource/mcp",
	} {
		rec := e.public(t, httptest.NewRequest("GET", path, nil))
		if rec.Code != 404 {
			t.Errorf("%s = %d with no issuer configured, want 404", path, rec.Code)
		}
	}
	// And /mcp still answers without any credential at all.
	cs := mcpClient(t, e, nil)
	if _, err := cs.ListTools(context.Background(), nil); err != nil {
		t.Fatalf("MCP must work with OAuth unconfigured: %v", err)
	}
}

func TestMCPProtectedResourceMetadata(t *testing.T) {
	e := newEnv(t, oauthEnv())
	for _, path := range []string{
		"/.well-known/oauth-protected-resource",
		"/.well-known/oauth-protected-resource/mcp",
	} {
		rec := e.public(t, httptest.NewRequest("GET", path, nil))
		if rec.Code != 200 {
			t.Fatalf("%s = %d, want 200", path, rec.Code)
		}
		var doc struct {
			Resource             string   `json:"resource"`
			AuthorizationServers []string `json:"authorization_servers"`
			ScopesSupported      []string `json:"scopes_supported"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
			t.Fatalf("%s: %v (%s)", path, err, rec.Body)
		}
		if doc.Resource != "http://sitebin.example/mcp" {
			t.Errorf("%s resource = %q", path, doc.Resource)
		}
		if len(doc.AuthorizationServers) != 1 || doc.AuthorizationServers[0] != testIssuer {
			t.Errorf("%s authorization_servers = %v", path, doc.AuthorizationServers)
		}
		// The scopes are how a client learns what to ask its authorization
		// server for; getting them wrong means the token comes back without
		// the audience.
		if len(doc.ScopesSupported) != 2 {
			t.Errorf("%s scopes_supported = %v", path, doc.ScopesSupported)
		}
	}
}

// The challenge is what turns a 401 into something a client can act on: it has
// to name where the metadata lives, or discovery never starts.
func TestMCPUnauthenticatedChallenge(t *testing.T) {
	e := newEnv(t, oauthEnv())
	req := httptest.NewRequest("POST", "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := e.public(t, req)

	if rec.Code != 401 {
		t.Fatalf("POST /mcp without a credential = %d, want 401", rec.Code)
	}
	got := rec.Header().Get("WWW-Authenticate")
	if !strings.Contains(got, "Bearer") || !strings.Contains(got, "resource_metadata=") {
		t.Fatalf("WWW-Authenticate = %q", got)
	}
	if !strings.Contains(got, "/.well-known/oauth-protected-resource") {
		t.Errorf("challenge does not point at the metadata: %q", got)
	}
}

// The regression that matters most: account API tokens are a shipped feature,
// and switching OAuth on must not break the scripts already using them.
func TestMCPAccountTokenStillWorksWithOAuthOn(t *testing.T) {
	e := newEnv(t, oauthEnv())
	ext.Register(&fakeProvider{
		enabled: true,
		owner:   "acct-1",
		bearer:  map[string]string{"sbp_tok": "acct-1"},
	})
	defer ext.Reset()

	cs := mcpClient(t, e, http.Header{"Authorization": {"Bearer sbp_tok"}})
	editID, _ := mcpCreate(t, cs, "<h1>still works</h1>")
	if editID == "" {
		t.Fatal("an account API token must keep working with OAuth enabled")
	}
}

// A credential the extension does not recognise is refused at the door, before
// any tool runs.
func TestMCPUnknownCredentialRefused(t *testing.T) {
	e := newEnv(t, oauthEnv())
	ext.Register(&fakeProvider{enabled: true, bearer: map[string]string{}})
	defer ext.Reset()

	req := httptest.NewRequest("POST", "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer not-a-real-token")
	rec := e.public(t, req)
	if rec.Code != 401 {
		t.Errorf("unknown credential = %d, want 401", rec.Code)
	}
}

// An OAuth credential carrying scopes reaches the tools with them, so the
// per-tool checks in internal/mcp have something to enforce.
func TestMCPOAuthScopesReachTheTools(t *testing.T) {
	e := newEnv(t, oauthEnv())
	ext.Register(&fakeProvider{
		enabled: true,
		owner:   "acct-1",
		bearer:  map[string]string{"jwt-ish": "acct-1"},
		scopes:  map[string][]string{"jwt-ish": {mcp.ScopeRead}},
	})
	defer ext.Reset()

	cs := mcpClient(t, e, http.Header{"Authorization": {"Bearer jwt-ish"}})
	res := mcpCall(t, cs, "create_site", map[string]any{"files": []any{}})
	if !res.IsError {
		t.Fatal("a read-only OAuth credential must not create sites")
	}
	if !strings.Contains(mcpText(res), mcp.ScopeWrite) {
		t.Errorf("refusal does not name the missing scope: %s", mcpText(res))
	}
}

// A pending domain is a result, not a tool error: the agent gets the record
// to create and the instruction to call add_domain again.
func TestMCPAddDomainReportsPendingVerification(t *testing.T) {
	ext.Register(&fakeProvider{domainsOK: true})
	defer ext.Reset()
	e := newEnv(t, nil)
	e.st.SetDomainVerifier(&apiVerifier{ok: map[string]bool{}}, e.cfg.ViewDomain)
	cs := mcpClient(t, e, nil)
	editID, pw := mcpCreate(t, cs, "hi")

	res := mcpCall(t, cs, "add_domain", map[string]any{
		"edit_id": editID, "edit_password": pw, "domain": "docs.example.com",
	})
	if res.IsError {
		t.Fatalf("a pending domain must not be a tool error: %s", mcpText(res))
	}
	text := mcpText(res)
	if !strings.Contains(text, "pending_domains") || !strings.Contains(text, "_sitebin-challenge.docs.example.com") || !strings.Contains(text, "sitebin-verify=") {
		t.Errorf("the result does not carry the record to create: %s", text)
	}
	if strings.Contains(text, `"custom_domains":["docs.example.com"]`) {
		t.Error("the result lists an unverified domain as attached")
	}
}

// ---- forms over MCP ----

func TestMCPFormsLifecycle(t *testing.T) {
	e, rs := formsEnv(t, nil)
	cs := mcpClient(t, e, nil)
	id, pw := mcpCreate(t, cs, "<h1>hi</h1>")
	forms := func(res *sdk.CallToolResult) []map[string]any {
		t.Helper()
		if res.IsError {
			t.Fatalf("tool error: %s", mcpText(res))
		}
		m, _ := res.StructuredContent.(map[string]any)
		var out []map[string]any
		list, _ := m["forms"].([]any)
		for _, f := range list {
			out = append(out, f.(map[string]any))
		}
		return out
	}
	site := map[string]any{"edit_id": id, "edit_password": pw}
	with := func(extra map[string]any) map[string]any {
		m := map[string]any{}
		for k, v := range site {
			m[k] = v
		}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}

	fs := forms(mcpCall(t, cs, "add_form", with(map[string]any{"form": map[string]any{"name": "Contact", "recipient": "office@example.com", "captcha": true}})))
	if len(fs) != 1 || fs[0]["status"] != "pending" || !strings.Contains(fs[0]["snippet"].(string), "altcha-widget") || rs.count() != 1 {
		t.Fatalf("add_form = %v, mails %d", fs, rs.count())
	}
	key := fs[0]["key"].(string)
	fs = forms(mcpCall(t, cs, "update_form", with(map[string]any{"key": key, "form": map[string]any{"name": "Kontakt"}})))
	if fs[0]["name"] != "Kontakt" {
		t.Fatalf("update_form = %v", fs)
	}
	forms(mcpCall(t, cs, "resend_form_confirmation", with(map[string]any{"key": key})))
	if rs.count() != 2 {
		t.Fatalf("resend sent %d mails in total, want 2", rs.count())
	}
	if fs = forms(mcpCall(t, cs, "list_forms", site)); len(fs) != 1 {
		t.Fatalf("list_forms = %v", fs)
	}
	if fs = forms(mcpCall(t, cs, "remove_form", with(map[string]any{"key": key}))); len(fs) != 0 {
		t.Fatalf("remove_form left %v", fs)
	}
	res := mcpCall(t, cs, "remove_form", with(map[string]any{"key": key}))
	if !res.IsError || !strings.Contains(mcpText(res), "list_forms") {
		t.Fatalf("removing twice = %s", mcpText(res))
	}
}

// The adapter hands the MCP caller's IP to the confirmation throttle, for
// every tool that mails a recipient.
func TestMCPFormsConfirmationsAreThrottledPerCaller(t *testing.T) {
	e, rs := formsEnv(t, nil)
	const ip = "203.0.113.9"
	cs := mcpClient(t, e, http.Header{"X-Forwarded-For": {ip}})
	id, pw := mcpCreate(t, cs, "<h1>hi</h1>")
	site := map[string]any{"edit_id": id, "edit_password": pw}
	res := mcpCall(t, cs, "add_form", map[string]any{"edit_id": id, "edit_password": pw, "form": map[string]any{"name": "A", "recipient": "a@example.com"}})
	if res.IsError {
		t.Fatalf("add_form = %s", mcpText(res))
	}
	key := res.StructuredContent.(map[string]any)["forms"].([]any)[0].(map[string]any)["key"].(string)
	for e.api.forms.confirmCaller.Allow(ip) { // spend the rest of this caller's day
	}
	for name, args := range map[string]map[string]any{
		"add_form":                 {"form": map[string]any{"name": "B", "recipient": "b@example.com"}},
		"update_form":              {"key": key, "form": map[string]any{"recipient": "c@example.com"}},
		"resend_form_confirmation": {"key": key},
	} {
		for k, v := range site {
			args[k] = v
		}
		if res := mcpCall(t, cs, name, args); !res.IsError || !strings.Contains(mcpText(res), "too many confirmation") {
			t.Errorf("%s after the caller's budget is spent = %s, want the throttle", name, mcpText(res))
		}
	}
	if rs.count() != 1 {
		t.Errorf("mails = %d, want only the first", rs.count())
	}
}

func TestMCPAddFormWithFormsOff(t *testing.T) {
	e := newEnv(t, nil)
	cs := mcpClient(t, e, nil)
	id, pw := mcpCreate(t, cs, "<h1>hi</h1>")
	res := mcpCall(t, cs, "add_form", map[string]any{"edit_id": id, "edit_password": pw, "form": map[string]any{"name": "A", "recipient": "a@example.com"}})
	if !res.IsError || !strings.Contains(mcpText(res), "not enabled") {
		t.Fatalf("add_form with forms off = %s", mcpText(res))
	}
}

func TestMCPFormsNeedTheEditPassword(t *testing.T) {
	e, _ := formsEnv(t, nil)
	cs := mcpClient(t, e, nil)
	id, _ := mcpCreate(t, cs, "<h1>hi</h1>")
	res := mcpCall(t, cs, "list_forms", map[string]any{"edit_id": id})
	if !res.IsError || !strings.Contains(mcpText(res), "edit_password") {
		t.Fatalf("list_forms without a password = %s", mcpText(res))
	}
}

func TestMCPOpenUploadThenUploadOverHTTP(t *testing.T) {
	e := newEnv(t, nil)
	cs := mcpClient(t, e, nil)
	editID, pw := mcpCreate(t, cs, "<h1>small</h1>")

	res := mcpCall(t, cs, "open_upload", map[string]any{"edit_id": editID, "edit_password": pw})
	if res.IsError {
		t.Fatalf("open_upload: %s", mcpText(res))
	}
	var up mcp.UploadResult
	raw, _ := json.Marshal(res.StructuredContent)
	if err := json.Unmarshal(raw, &up); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(up.Token, "sbu_") {
		t.Fatalf("token = %q", up.Token)
	}
	if !strings.HasSuffix(up.UploadURL, "/api/sites/"+editID+"/files") {
		t.Errorf("upload_url = %q", up.UploadURL)
	}
	if !strings.HasSuffix(up.WebDAVURL, "/dav/"+editID+"/") {
		t.Errorf("webdav_url = %q", up.WebDAVURL)
	}
	if up.IdleTimeoutSeconds != int(uploadTokenIdle/time.Second) {
		t.Errorf("idle_timeout_seconds = %d", up.IdleTimeoutSeconds)
	}
	if d := time.Until(up.ExpiresAt); d < 59*time.Minute || d > 61*time.Minute {
		t.Errorf("expires_at = %v", up.ExpiresAt)
	}
	if len(up.Examples) != 3 {
		t.Fatalf("examples = %v", up.Examples)
	}
	for _, ex := range up.Examples {
		if !strings.HasPrefix(ex, "curl ") || !strings.Contains(ex, up.Token) {
			t.Errorf("example is not ready to run: %q", ex)
		}
	}

	// The token works with a plain HTTP client, for a file larger than any
	// tool call may carry.
	big := strings.Repeat("x", mcp.MaxContentBytes+1)
	body, ct := uploadBody(t, nil, map[string]string{"big.txt": big})
	req := bearer(httptest.NewRequest("POST", "/api/sites/"+editID+"/files", body), up.Token)
	req.Header.Set("Content-Type", ct)
	if w := e.public(t, req); w.Code != 200 {
		t.Fatalf("upload with the issued token: %d %s", w.Code, w.Body)
	}
	site, _ := e.st.ByEditID(editID)
	files, err := e.st.ListFiles(site)
	if err != nil {
		t.Fatal(err)
	}
	var got int64 = -1
	for _, f := range files {
		if f.Path == "big.txt" {
			got = f.Size
		}
	}
	if got != int64(len(big)) {
		t.Fatalf("big.txt: %d bytes stored, want %d", got, len(big))
	}
}

func TestMCPOpenUploadWithoutWebDAVOffersOnlyTheAPI(t *testing.T) {
	e := newEnv(t, map[string]string{"SITEBIN_WEBDAV_ENABLED": "false"})
	cs := mcpClient(t, e, nil)
	editID, pw := mcpCreate(t, cs, "x")
	res := mcpCall(t, cs, "open_upload", map[string]any{"edit_id": editID, "edit_password": pw})
	m, _ := res.StructuredContent.(map[string]any)
	if res.IsError || m == nil {
		t.Fatalf("open_upload: %s", mcpText(res))
	}
	if v, ok := m["webdav_url"]; ok {
		t.Errorf("webdav_url offered with WebDAV off: %v", v)
	}
	if ex, _ := m["examples"].([]any); len(ex) != 2 {
		t.Errorf("examples = %v", m["examples"])
	}
}

func TestMCPOpenUploadNeedsTheSitesAuthority(t *testing.T) {
	e := newEnv(t, nil)
	cs := mcpClient(t, e, nil)
	editID, _ := mcpCreate(t, cs, "x")
	res := mcpCall(t, cs, "open_upload", map[string]any{"edit_id": editID, "edit_password": "wrong"})
	if !res.IsError {
		t.Fatal("open_upload with a wrong password issued a token")
	}
	if n := len(e.api.uploads.m); n != 0 {
		t.Fatalf("%d tokens issued", n)
	}
}

func TestMCPOpenUploadWithAnOwningAccountToken(t *testing.T) {
	e := newEnv(t, nil)
	ext.Register(&fakeProvider{
		enabled: true,
		owner:   "acct-1",
		bearer:  map[string]string{"sbp_a": "acct-1", "sbp_b": "acct-2"},
	})
	defer ext.Reset()

	owner := mcpClient(t, e, http.Header{"Authorization": {"Bearer sbp_a"}})
	editID, _ := mcpCreate(t, owner, "x")
	if res := mcpCall(t, owner, "open_upload", map[string]any{"edit_id": editID}); res.IsError {
		t.Fatalf("owning token: %s", mcpText(res))
	}
	other := mcpClient(t, e, http.Header{"Authorization": {"Bearer sbp_b"}})
	if res := mcpCall(t, other, "open_upload", map[string]any{"edit_id": editID}); !res.IsError {
		t.Fatal("another account's token opened an upload")
	}
}

func TestMCPDeleteSiteRevokesUploadTokens(t *testing.T) {
	e := newEnv(t, nil)
	cs := mcpClient(t, e, nil)
	editID, pw := mcpCreate(t, cs, "x")
	if res := mcpCall(t, cs, "open_upload", map[string]any{"edit_id": editID, "edit_password": pw}); res.IsError {
		t.Fatalf("open_upload: %s", mcpText(res))
	}
	if res := mcpCall(t, cs, "delete_site", map[string]any{"edit_id": editID, "edit_password": pw}); res.IsError {
		t.Fatalf("delete_site: %s", mcpText(res))
	}
	if n := len(e.api.uploads.m); n != 0 {
		t.Fatalf("%d upload tokens outlived their site", n)
	}
}
