package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ittrail/sitebin/internal/ext"
)

// fakeProvider is a test double for the enterprise extension, exercising the
// core create-gate + route-mount seam without the ee/ build tag.
type fakeProvider struct {
	enabled   bool
	domainsOK bool
	embedOK   bool
	owner     string
	rejErr    error
	created   []string
}

func (f *fakeProvider) Name() string               { return "fake" }
func (f *fakeProvider) Version() string            { return "0" }
func (f *fakeProvider) Init(ext.Host) error        { return nil }
func (f *fakeProvider) AccountsEnabled() bool      { return f.enabled }
func (f *fakeProvider) CustomDomainsAllowed() bool { return f.domainsOK }
func (f *fakeProvider) EmbedOriginsAllowed() bool  { return f.embedOK }
func (f *fakeProvider) AuthorizeCreate(*http.Request) (ext.CreateGrant, error) {
	return ext.CreateGrant{OwnerAccountID: f.owner}, f.rejErr
}
func (f *fakeProvider) OnSiteCreated(owner, viewID string) error {
	f.created = append(f.created, owner+":"+viewID)
	return nil
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
