package httpapi

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ittrail/sitebin.io/internal/ext"
	"github.com/ittrail/sitebin.io/internal/mcp"
)

// ---- an OAuth access token is for /mcp only ----
//
// Its audience is the MCP resource, and the JSON API has no scope check at
// all, so a token honoured here is a read-only grant that can create,
// overwrite and delete sites. That was a real hole: a credential with only
// sitebin:sites:read could DELETE /api/sites/{id}. The fake provider below
// hands the OAuth credential out on every route, marker or not, so these tests
// prove the core refuses it on its own.

type restCall struct {
	method, path, ctype string
	body                func() *bytes.Buffer
}

// siteRoutes is every per-site route of the JSON API, writes first and the
// delete last, each with a body its handler would accept.
func siteRoutes(editID string) []restCall {
	base := "/api/sites/" + editID
	jsonBody := func(s string) func() *bytes.Buffer { return func() *bytes.Buffer { return bytes.NewBufferString(s) } }
	var form bytes.Buffer
	mw := multipart.NewWriter(&form)
	p, _ := mw.CreateFormFile("files", "index.html")
	p.Write([]byte("<h1>overwritten</h1>"))
	mw.Close()
	ctype := mw.FormDataContentType()
	upload := func() *bytes.Buffer { return bytes.NewBuffer(append([]byte(nil), form.Bytes()...)) }
	return []restCall{
		{"GET", base, "", jsonBody("")},
		{"GET", base + "/download", "", jsonBody("")},
		{"PUT", base, "application/json", jsonBody(`{"name":"taken over"}`)},
		{"POST", base + "/files", ctype, upload},
		{"DELETE", base + "/files/index.html", "", jsonBody("")},
		{"POST", base + "/domains", "application/json", jsonBody(`{"domain":"taken.example.com"}`)},
		{"DELETE", base + "/domains/taken.example.com", "", jsonBody("")},
		{"POST", base + "/containers/start", "", jsonBody("")},
		{"POST", base + "/forms", "application/json", jsonBody(`{"name":"Contact","recipient":"a@example.com"}`)},
		{"PUT", base + "/forms/k1", "application/json", jsonBody(`{"name":"x"}`)},
		{"DELETE", base + "/forms/k1", "", jsonBody("")},
		{"POST", base + "/forms/k1/confirmation", "", jsonBody("")},
		{"DELETE", base, "", jsonBody("")},
	}
}

func (c restCall) request(token string) *http.Request {
	req := httptest.NewRequest(c.method, c.path, c.body())
	if c.ctype != "" {
		req.Header.Set("Content-Type", c.ctype)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	return req
}

func oauthRESTProvider() *fakeProvider {
	return &fakeProvider{
		enabled: true, owner: "acct-1", domainsOK: true,
		bearer: map[string]string{"jwt-ro": "acct-1", "jwt-none": "acct-1", "jwt-rw": "acct-1", "sbp_tok": "acct-1"},
		scopes: map[string][]string{
			"jwt-ro":   {mcp.ScopeRead},
			"jwt-none": nil,
			"jwt-rw":   {mcp.ScopeRead, mcp.ScopeWrite},
		},
		oauth: map[string]bool{"jwt-ro": true, "jwt-none": true, "jwt-rw": true},
	}
}

func TestOAuthTokenIsRefusedByTheJSONAPI(t *testing.T) {
	for _, tok := range []string{"jwt-ro", "jwt-none", "jwt-rw"} {
		t.Run(tok, func(t *testing.T) {
			e := newEnv(t, oauthEnv())
			fp := oauthRESTProvider()
			ext.Register(fp)
			defer ext.Reset()
			editID, _ := ownedSiteWithPassword(t, e)

			for _, c := range siteRoutes(editID) {
				w := e.public(t, c.request(tok))
				if w.Code != 401 {
					t.Errorf("%s %s with an OAuth token = %d, want 401 (%s)", c.method, c.path, w.Code, w.Body)
				}
			}
			site, err := e.st.ByEditID(editID)
			if err != nil {
				t.Fatalf("the site did not survive: %v", err)
			}
			if b, err := e.st.ReadContentFile(site, "index.html"); err != nil || string(b) != "<h1>owned</h1>" {
				t.Errorf("index.html = %q, %v", b, err)
			}
			if site.Meta.Name != "" || len(site.Meta.DomainClaims) != 0 {
				t.Errorf("the site was changed: name %q, claims %v", site.Meta.Name, site.Meta.DomainClaims)
			}

			// Creation resolves its owner in the extension; the core hands it
			// a request without the MCP marker, so the token names nobody and
			// a script without an account is refused.
			fp.ownerFromBearer = true
			if w := scriptCreate(t, e, map[string]string{"Authorization": "Bearer " + tok}); w.Code != 401 {
				t.Errorf("POST /api/sites with an OAuth token = %d, want 401 (%s)", w.Code, w.Body)
			}
		})
	}
}

// Account API tokens are the JSON API's credential and keep working on every
// route, OAuth on or not.
func TestAccountTokenStillWorksOnEveryRoute(t *testing.T) {
	e := newEnv(t, oauthEnv())
	fp := oauthRESTProvider()
	ext.Register(fp)
	defer ext.Reset()
	editID, _ := ownedSiteWithPassword(t, e)

	for _, c := range siteRoutes(editID) {
		w := e.public(t, c.request("sbp_tok"))
		if w.Code == 401 || strings.Contains(w.Body.String(), "edit password required") {
			t.Errorf("%s %s with an account token = %d (%s)", c.method, c.path, w.Code, w.Body)
		}
	}
	if _, err := e.st.ByEditID(editID); err == nil {
		t.Error("DELETE with the owning account token left the site in place")
	}
	fp.ownerFromBearer = true
	if w := scriptCreate(t, e, map[string]string{"Authorization": "Bearer sbp_tok"}); w.Code != 201 {
		t.Errorf("POST /api/sites with an account token = %d (%s)", w.Code, w.Body)
	}
}

// The same OAuth token that the JSON API refuses creates an owned site
// through MCP: the request reaching the extension carries the marker.
func TestOAuthTokenCreatesThroughMCP(t *testing.T) {
	e := newEnv(t, oauthEnv())
	fp := oauthRESTProvider()
	fp.ownerFromBearer = true
	ext.Register(fp)
	defer ext.Reset()

	cs := mcpClient(t, e, bearerHeader("jwt-rw"))
	editID, _ := mcpCreate(t, cs, "<h1>by an agent</h1>")
	site, err := e.st.ByEditID(editID)
	if err != nil {
		t.Fatal(err)
	}
	if site.Meta.OwnerAccountID != "acct-1" {
		t.Errorf("owner = %q, want acct-1", site.Meta.OwnerAccountID)
	}
	// And it manages that site over MCP without a password.
	if res := mcpCall(t, cs, "get_site", map[string]any{"edit_id": editID}); res.IsError {
		t.Errorf("get_site with the owning OAuth token: %s", mcpText(res))
	}
}
