package mcp

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// fakeOps records what the tool handlers asked for and returns canned answers,
// so this package's tests never touch a filesystem or an account store.
type fakeOps struct {
	auth Auth

	gotAuth   Auth
	gotRef    SiteRef
	gotCreate CreateInput
	gotFiles  []DecodedFile
	gotPath   string
	gotDomain string
	replace   bool
	calls     []string

	err  error
	site *SiteResult
	list []SiteSummary
	zip  []byte
	file File
}

func (f *fakeOps) Authenticate(*http.Request) Auth { return f.auth }

func (f *fakeOps) note(name string, a Auth, ref SiteRef) {
	f.calls = append(f.calls, name)
	f.gotAuth = a
	f.gotRef = ref
}

func (f *fakeOps) result() *SiteResult {
	if f.site != nil {
		return f.site
	}
	return &SiteResult{ID: "abc", EditID: "e1", ViewURL: "https://abc.example.com", Files: []FileInfo{}}
}

func (f *fakeOps) CreateSite(_ context.Context, a Auth, in CreateInput) (*SiteResult, error) {
	f.note("create_site", a, SiteRef{})
	f.gotCreate = in
	return f.result(), f.err
}

func (f *fakeOps) ListSites(_ context.Context, a Auth) ([]SiteSummary, error) {
	f.note("list_sites", a, SiteRef{})
	return f.list, f.err
}

func (f *fakeOps) GetSite(_ context.Context, a Auth, ref SiteRef) (*SiteResult, error) {
	f.note("get_site", a, ref)
	return f.result(), f.err
}

func (f *fakeOps) UpdateSite(_ context.Context, a Auth, ref SiteRef, s Settings) (*SiteResult, error) {
	f.note("update_site", a, ref)
	f.gotCreate.Settings = s
	return f.result(), f.err
}

func (f *fakeOps) ListFiles(_ context.Context, a Auth, ref SiteRef) ([]FileInfo, error) {
	f.note("list_files", a, ref)
	return []FileInfo{{Path: "index.html", Bytes: 11}}, f.err
}

func (f *fakeOps) ReadFile(_ context.Context, a Auth, ref SiteRef, path string) (File, error) {
	f.note("read_file", a, ref)
	f.gotPath = path
	return f.file, f.err
}

func (f *fakeOps) WriteFiles(_ context.Context, a Auth, ref SiteRef, files []DecodedFile, replace bool) (*SiteResult, error) {
	f.note("write_files", a, ref)
	f.gotFiles, f.replace = files, replace
	return f.result(), f.err
}

func (f *fakeOps) DeleteFile(_ context.Context, a Auth, ref SiteRef, path string) (*SiteResult, error) {
	f.note("delete_file", a, ref)
	f.gotPath = path
	return f.result(), f.err
}

func (f *fakeOps) DeleteSite(_ context.Context, a Auth, ref SiteRef) error {
	f.note("delete_site", a, ref)
	return f.err
}

func (f *fakeOps) AddDomain(_ context.Context, a Auth, ref SiteRef, d string) (*SiteResult, error) {
	f.note("add_domain", a, ref)
	f.gotDomain = d
	return f.result(), f.err
}

func (f *fakeOps) RemoveDomain(_ context.Context, a Auth, ref SiteRef, d string) (*SiteResult, error) {
	f.note("remove_domain", a, ref)
	f.gotDomain = d
	return f.result(), f.err
}

func (f *fakeOps) DownloadSite(_ context.Context, a Auth, ref SiteRef) ([]byte, error) {
	f.note("download_site", a, ref)
	return f.zip, f.err
}

// connect starts the MCP handler over HTTP and returns a connected client
// session, exercising the real transport rather than calling handlers directly.
func connect(t *testing.T, ops Ops, header http.Header) *sdk.ClientSession {
	t.Helper()
	srv := httptest.NewServer(NewHandler(ops, Info{Name: "sitebin-test", Version: "test"}))
	t.Cleanup(srv.Close)

	c := sdk.NewClient(&sdk.Implementation{Name: "test-client", Version: "1"}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	cs, err := c.Connect(ctx, &sdk.StreamableClientTransport{
		Endpoint:             srv.URL,
		DisableStandaloneSSE: true, // the server is stateless: GET returns 405 by design
		HTTPClient:           &http.Client{Transport: headerTransport{header}},
	}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

type headerTransport struct{ h http.Header }

func (t headerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	for k, vs := range t.h {
		for _, v := range vs {
			r.Header.Add(k, v)
		}
	}
	return http.DefaultTransport.RoundTrip(r)
}

func call(t *testing.T, cs *sdk.ClientSession, name string, args map[string]any) *sdk.CallToolResult {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &sdk.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("CallTool(%s): %v", name, err)
	}
	return res
}

func resultText(res *sdk.CallToolResult) string {
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*sdk.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

// The catalog is the server's public contract. If a tool disappears or is
// renamed, every published connector configuration breaks, so the list is
// pinned rather than merely counted.
func TestToolCatalog(t *testing.T) {
	cs := connect(t, &fakeOps{}, nil)
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	want := map[string]bool{
		"create_site": true, "list_sites": true, "get_site": true, "update_site": true,
		"list_files": true, "read_file": true, "write_files": true, "delete_file": true,
		"delete_site": true, "add_domain": true, "remove_domain": true, "download_site": true,
	}
	got := map[string]bool{}
	for _, tool := range res.Tools {
		got[tool.Name] = true
		if tool.Description == "" {
			t.Errorf("tool %q has no description; the model reads nothing else", tool.Name)
		}
		if tool.InputSchema == nil {
			t.Errorf("tool %q has no input schema", tool.Name)
		}
	}
	for name := range want {
		if !got[name] {
			t.Errorf("tool %q is missing from the catalog", name)
		}
	}
	for name := range got {
		if !want[name] {
			t.Errorf("unexpected tool %q — adding one is a contract change", name)
		}
	}
}

func TestInitializeReportsInstructions(t *testing.T) {
	cs := connect(t, &fakeOps{}, nil)
	res := cs.InitializeResult()
	if res.ServerInfo.Name != "sitebin-test" {
		t.Errorf("ServerInfo.Name = %q", res.ServerInfo.Name)
	}
	// The instructions are an agent's only briefing before its first call.
	for _, want := range []string{"edit_id", "edit_password", "account API token"} {
		if !strings.Contains(res.Instructions, want) {
			t.Errorf("instructions do not mention %q", want)
		}
	}
}

// Every handler closes over the Auth resolved from its own request. This is
// the property that keeps one caller's authority from reaching another's tool
// call, so it is asserted directly.
func TestHandlersReceiveTheRequestsAuth(t *testing.T) {
	ops := &fakeOps{auth: Auth{AccountID: "acct-7", AccountsEnabled: true}}
	cs := connect(t, ops, http.Header{"Authorization": {"Bearer sbp_x"}})
	call(t, cs, "get_site", map[string]any{"edit_id": "e1"})
	if ops.gotAuth.AccountID != "acct-7" || !ops.gotAuth.AccountsEnabled {
		t.Fatalf("handler saw auth %+v", ops.gotAuth)
	}
}

func TestCreateSiteDecodesFiles(t *testing.T) {
	ops := &fakeOps{}
	cs := connect(t, ops, nil)
	res := call(t, cs, "create_site", map[string]any{
		"files": []any{map[string]any{"path": "index.html", "text": "<h1>hi</h1>"}},
		"settings": map[string]any{
			"mode": "webserver",
		},
	})
	if res.IsError {
		t.Fatalf("unexpected error: %s", resultText(res))
	}
	if len(ops.gotCreate.Files) != 1 || string(ops.gotCreate.Files[0].Data) != "<h1>hi</h1>" {
		t.Fatalf("files not decoded: %+v", ops.gotCreate.Files)
	}
	if ops.gotCreate.Settings.Mode == nil || *ops.gotCreate.Settings.Mode != "webserver" {
		t.Fatalf("settings not passed: %+v", ops.gotCreate.Settings)
	}
	if !strings.Contains(resultText(res), "https://abc.example.com") {
		t.Errorf("result does not carry the view URL: %s", resultText(res))
	}
}

// A malformed file argument must be refused before Ops is called: create_site
// would otherwise have to create a site and roll it back.
func TestCreateSiteRejectsBadFilesBeforeCallingOps(t *testing.T) {
	ops := &fakeOps{}
	cs := connect(t, ops, nil)
	res := call(t, cs, "create_site", map[string]any{
		"files": []any{map[string]any{"path": "a.png", "base64": "not base64!!"}},
	})
	if !res.IsError {
		t.Fatal("expected a tool error")
	}
	if len(ops.calls) != 0 {
		t.Fatalf("Ops was called anyway: %v", ops.calls)
	}
}

func TestWriteFilesPassesReplace(t *testing.T) {
	ops := &fakeOps{}
	cs := connect(t, ops, nil)
	call(t, cs, "write_files", map[string]any{
		"edit_id":       "e1",
		"edit_password": "pw",
		"replace":       true,
		"files":         []any{map[string]any{"path": "a.txt", "text": "x"}},
	})
	if !ops.replace {
		t.Error("replace was not passed through")
	}
	if ops.gotRef.EditID != "e1" || ops.gotRef.EditPassword != "pw" {
		t.Errorf("ref = %+v", ops.gotRef)
	}
}

// Writing nothing without replace is a mistake, not an empty operation.
// Writing nothing WITH replace is how you empty a site, and must be allowed.
func TestWriteFilesEmpty(t *testing.T) {
	ops := &fakeOps{}
	cs := connect(t, ops, nil)

	res := call(t, cs, "write_files", map[string]any{"edit_id": "e1", "files": []any{}})
	if !res.IsError {
		t.Error("write_files with no files and no replace should be refused")
	}

	res = call(t, cs, "write_files", map[string]any{"edit_id": "e1", "files": []any{}, "replace": true})
	if res.IsError {
		t.Errorf("write_files with replace and no files empties the site: %s", resultText(res))
	}
}

// An Ops error reaches the agent as a tool error carrying the message verbatim
// — the message is the only thing it can act on.
func TestOpsErrorBecomesToolError(t *testing.T) {
	ops := &fakeOps{err: errors.New("sign in to create sites from the API: https://example.com/account")}
	cs := connect(t, ops, nil)
	res := call(t, cs, "create_site", map[string]any{"files": []any{}})
	if !res.IsError {
		t.Fatal("expected IsError")
	}
	if !strings.Contains(resultText(res), "https://example.com/account") {
		t.Errorf("the upgrade hint was lost: %s", resultText(res))
	}
}

func TestDeleteSiteReportsStatus(t *testing.T) {
	ops := &fakeOps{}
	cs := connect(t, ops, nil)
	res := call(t, cs, "delete_site", map[string]any{"edit_id": "e9", "edit_password": "pw"})
	if res.IsError {
		t.Fatalf("unexpected error: %s", resultText(res))
	}
	if !strings.Contains(resultText(res), "deleted") {
		t.Errorf("result = %s", resultText(res))
	}
}

func TestDownloadSiteReturnsBlobResource(t *testing.T) {
	ops := &fakeOps{zip: []byte("PK\x03\x04zip")}
	cs := connect(t, ops, nil)
	res := call(t, cs, "download_site", map[string]any{"edit_id": "e1", "edit_password": "pw"})
	if res.IsError {
		t.Fatalf("unexpected error: %s", resultText(res))
	}
	if len(res.Content) != 1 {
		t.Fatalf("content = %+v", res.Content)
	}
	er, ok := res.Content[0].(*sdk.EmbeddedResource)
	if !ok {
		t.Fatalf("content is %T, want an embedded resource", res.Content[0])
	}
	if er.Resource.MIMEType != "application/zip" || string(er.Resource.Blob) != "PK\x03\x04zip" {
		t.Errorf("resource = %+v", er.Resource)
	}
}

func TestPathAndDomainArgumentsReachOps(t *testing.T) {
	ops := &fakeOps{file: File{Path: "index.html", Text: "hi"}}
	cs := connect(t, ops, nil)

	call(t, cs, "read_file", map[string]any{"edit_id": "e1", "edit_password": "p", "path": "assets/app.js"})
	if ops.gotPath != "assets/app.js" {
		t.Errorf("path = %q", ops.gotPath)
	}
	call(t, cs, "add_domain", map[string]any{"edit_id": "e1", "edit_password": "p", "domain": "docs.example.com"})
	if ops.gotDomain != "docs.example.com" {
		t.Errorf("domain = %q", ops.gotDomain)
	}
}

func TestListSitesReturnsRowsWithEditIDs(t *testing.T) {
	ops := &fakeOps{list: []SiteSummary{
		{ID: "abc", EditID: "e-abc", ViewURL: "https://abc.example.com", Origin: "mcp"},
	}}
	cs := connect(t, ops, http.Header{"Authorization": {"Bearer sbp_x"}})
	res := call(t, cs, "list_sites", map[string]any{})
	if res.IsError {
		t.Fatalf("unexpected error: %s", resultText(res))
	}
	// An agent that cannot get from "my sites" to an edit id has to ask a
	// human for a password it should never need.
	if !strings.Contains(resultText(res), "e-abc") {
		t.Errorf("listing has no edit id: %s", resultText(res))
	}
}
