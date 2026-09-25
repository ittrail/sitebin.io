package httpapi

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/ittrail/sitebin.io/internal/ext"
)

// fakeProvider is a test double for the enterprise extension, exercising the
// core create-gate + route-mount seam without the ee/ build tag.
type fakeProvider struct {
	enabled   bool
	domainsOK bool
	embedOK   bool
	owner     string
	grant     ext.CreateGrant // extra caps to stamp (owner merged in)
	rejErr    error
	created   []string
	quota     ext.CreateGrant
	// bearer maps a token secret to the account it belongs to, so a test can
	// present one the way a script would.
	bearer map[string]string
	// owned maps an account id to the view ids it owns, for list_sites.
	owned map[string][]string
	// scopes maps a token secret to the OAuth scopes it grants; absent means
	// an account API token, which is unrestricted.
	scopes   map[string][]string
	quotaOK  bool
	quotaErr error
	// oauth marks a token secret as an OAuth access token rather than an
	// account API token. The fake hands it out on every route, marker or not,
	// so a test proves the core refuses it outside /mcp on its own.
	oauth map[string]bool
	// ownerFromBearer makes AuthorizeCreate name the owner from the bearer the
	// way the extension's accountForAPI does — an account token anywhere, an
	// OAuth token only on a request that came through /mcp — so the core's
	// half of that rule, carrying the marker to the extension, is testable.
	ownerFromBearer bool

	// bearerCalls counts BearerCredential lookups, so a test can see a
	// credential verified once per request. The markers record whether each
	// lookup and each create came through /mcp.
	bearerCalls  atomic.Int32
	mu           sync.Mutex
	bearerSawMCP []bool
	createSawMCP []bool
}

func (f *fakeProvider) Name() string          { return "fake" }
func (f *fakeProvider) Version() string       { return "0" }
func (f *fakeProvider) Init(ext.Host) error   { return nil }
func (f *fakeProvider) AccountsEnabled() bool { return f.enabled }
func (f *fakeProvider) CustomDomainsAllowed() error {
	if f.domainsOK {
		return nil
	}
	return errors.New("custom domains are not available on this instance")
}
func (f *fakeProvider) EmbedOriginsAllowed() bool { return f.embedOK }
func (f *fakeProvider) AuthorizeCreate(r *http.Request) (ext.CreateGrant, error) {
	f.mu.Lock()
	f.createSawMCP = append(f.createSawMCP, ext.IsMCPCaller(r.Context()))
	f.mu.Unlock()
	g := f.grant
	if f.ownerFromBearer {
		g.OwnerAccountID = ""
		secret := fakeBearer(r)
		if id, ok := f.bearer[secret]; ok && (!f.oauth[secret] || ext.IsMCPCaller(r.Context())) {
			g.OwnerAccountID = id
		}
		return g, f.rejErr
	}
	if g.OwnerAccountID == "" {
		g.OwnerAccountID = f.owner
	}
	return g, f.rejErr
}

// fakeBearer extracts an Authorization: Bearer secret, or "".
func fakeBearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if len(h) < 7 || !strings.EqualFold(h[:7], "bearer ") {
		return ""
	}
	return strings.TrimSpace(h[7:])
}

func (f *fakeProvider) BearerCredential(r *http.Request) (ext.Credential, bool) {
	f.bearerCalls.Add(1)
	f.mu.Lock()
	f.bearerSawMCP = append(f.bearerSawMCP, ext.IsMCPCaller(r.Context()))
	f.mu.Unlock()
	secret := fakeBearer(r)
	if secret == "" {
		return ext.Credential{}, false
	}
	id, ok := f.bearer[secret]
	if !ok {
		return ext.Credential{}, false
	}
	// scopes lets a test present an OAuth-shaped credential; an absent entry
	// means an account API token, which carries none.
	return ext.Credential{AccountID: id, Scopes: f.scopes[secret], OAuth: f.oauth[secret]}, true
}

func (f *fakeProvider) AccountSiteIDs(accountID string) ([]string, bool) {
	if !f.enabled {
		return nil, false
	}
	ids, ok := f.owned[accountID]
	return ids, ok
}

func (f *fakeProvider) OnSiteCreated(owner, viewID string) error {
	f.created = append(f.created, owner+":"+viewID)
	return nil
}
func (f *fakeProvider) QuotaFor(string) (ext.CreateGrant, bool, error) {
	return f.quota, f.quotaOK, f.quotaErr
}
func (f *fakeProvider) PublicRoutes() map[string]http.Handler {
	return map[string]http.Handler{
		"GET /account": http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(200)
			w.Write([]byte("dashboard"))
		}),
	}
}

func TestCreateGateRejects(t *testing.T) {
	e := newEnv(t, nil)
	fp := &fakeProvider{enabled: true, rejErr: &ext.CreateError{Status: 401, Msg: "sign in first"}}
	ext.Register(fp)
	defer ext.Reset()

	req := httptest.NewRequest("POST", "/api/sites", nil)
	if w := e.public(t, req); w.Code != 401 {
		t.Fatalf("gated create = %d, want 401", w.Code)
	}
}

func TestCreateGateStampsOwner(t *testing.T) {
	e := newEnv(t, nil)
	fp := &fakeProvider{enabled: true, owner: "acct-xyz"}
	ext.Register(fp)
	defer ext.Reset()

	c := e.createSite(t, nil, map[string]string{"index.html": "x"})
	site, err := e.st.ByViewID(c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if site.Meta.OwnerAccountID != "acct-xyz" {
		t.Errorf("owner = %q, want acct-xyz", site.Meta.OwnerAccountID)
	}
	if len(fp.created) != 1 || fp.created[0] != "acct-xyz:"+c.ID {
		t.Errorf("OnSiteCreated not called correctly: %v", fp.created)
	}
}

func TestCreateOpenWhenProviderDisabled(t *testing.T) {
	e := newEnv(t, nil)
	fp := &fakeProvider{enabled: false} // mode=open
	ext.Register(fp)
	defer ext.Reset()

	c := e.createSite(t, nil, map[string]string{"index.html": "x"})
	site, _ := e.st.ByViewID(c.ID)
	if site.Meta.OwnerAccountID != "" {
		t.Errorf("owner stamped despite disabled provider: %q", site.Meta.OwnerAccountID)
	}
}

func TestProviderRoutesMounted(t *testing.T) {
	e := newEnv(t, nil)
	ext.Register(&fakeProvider{enabled: true})
	defer ext.Reset()

	w := e.public(t, httptest.NewRequest("GET", "/account", nil))
	if w.Code != 200 || w.Body.String() != "dashboard" {
		t.Fatalf("dashboard route = %d %q", w.Code, w.Body)
	}
}
