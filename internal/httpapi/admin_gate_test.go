package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ittrail/sitebin.io/internal/ext"
	"github.com/ittrail/sitebin.io/internal/mcp"
	"github.com/ittrail/sitebin.io/internal/provenance"
	"github.com/ittrail/sitebin.io/internal/store"
)

// ---- the admin standing in the gate ----
//
// An owner shares a site with admins (Meta.Admins). An admin manages the site
// the way its owner does — through their own account token, their own browser
// session on the edit page, MCP and Git — except what only an owner may do:
// delete the site, set or clear its expiry, delete its repository. The plan
// half of the standing is the extension's (ext.SharingAccounts); without it
// nobody is an admin. See docs/superpowers/specs/2026-11-03-site-ownership-design.md.

// ownerOnlyText is the API's and MCP's answer to an admin asking for an
// owner-only action.
const ownerOnlyText = "only the site's owner can do this"

// jwtAdmin is an OAuth-shaped access token of the admin (acct-2).
const jwtAdmin = "eyJhbGciOiJSUzI1NiJ9.eyJzdWIiOiJhZG1pbiJ9.sig-admin"

// sharingFake is the extension with sharing: account tokens and browser
// sessions (sessionProvider), the plan answer, the admins' own tags, the
// index of shared sites and the account directory.
//
// The accounts: acct-1 owns ("sbp_owner", "owner-cookie"), acct-2 is the
// admin ("sbp_admin", "admin-cookie", jwtAdmin), acct-3 a stranger
// ("sbp_other", "other-cookie").
type sharingFake struct {
	*sessionProvider

	mu      sync.Mutex
	plans   map[string]bool  // SharingAllowed; an absent account has no sharing
	planErr map[string]error // SharingAllowed fails for these
	tags    map[string][]string
	shared  map[string][]string
	emails  map[string]string
	// planCalls counts SharingAllowed lookups.
	planCalls int
	// setTagsErr fails SetAdminTags, as an unreachable registry would.
	setTagsErr error
	// onAllowed runs once SharingAllowed has answered for an account, and
	// onBearer once BearerCredential has answered for a request: a test's
	// hooks into the moment between the gate and what follows it.
	onAllowed func(accountID string)
	onBearer  func(r *http.Request)
}

var (
	_ ext.SharingAccounts  = (*sharingFake)(nil)
	_ ext.AccountDirectory = (*sharingFake)(nil)
	_ ext.SessionAccounts  = (*sharingFake)(nil)
)

func (f *sharingFake) SharingAllowed(id string) (bool, error) {
	f.mu.Lock()
	f.planCalls++
	ok, err, hook := f.plans[id], f.planErr[id], f.onAllowed
	f.mu.Unlock()
	if hook != nil {
		hook(id)
	}
	return ok, err
}

func (f *sharingFake) BearerCredential(r *http.Request) (ext.Credential, bool) {
	cred, ok := f.sessionProvider.BearerCredential(r)
	f.mu.Lock()
	hook := f.onBearer
	f.mu.Unlock()
	if hook != nil {
		hook(r)
	}
	return cred, ok
}

func (f *sharingFake) AdminTags(id, viewID string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.tags[id+"|"+viewID]...)
}

func (f *sharingFake) SetAdminTags(id, viewID string, tags []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.setTagsErr != nil {
		return f.setTagsErr
	}
	f.tags[id+"|"+viewID] = append([]string(nil), tags...)
	return nil
}

func (f *sharingFake) SharedSiteIDs(id string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.shared[id]...)
}

func (f *sharingFake) AccountEmail(id string) (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	e, ok := f.emails[id]
	return e, ok
}

func (f *sharingFake) setPlan(id string, ok bool, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.plans[id] = ok
	f.planErr[id] = err
}

func (f *sharingFake) adminTagsOf(id, viewID string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.tags[id+"|"+viewID]
}

// registerSharing installs a sharingFake whose created sites belong to
// acct-1 and are stamped from grant; every account's plan shares.
func registerSharing(t *testing.T, grant ext.CreateGrant) *sharingFake {
	t.Helper()
	f := &sharingFake{
		sessionProvider: &sessionProvider{
			fakeProvider: &fakeProvider{
				enabled: true, owner: "acct-1", domainsOK: true, grant: grant,
				bearer: map[string]string{"sbp_owner": "acct-1", "sbp_admin": "acct-2", "sbp_other": "acct-3", jwtAdmin: "acct-2"},
				scopes: map[string][]string{jwtAdmin: {mcp.ScopeRead, mcp.ScopeWrite}},
				oauth:  map[string]bool{jwtAdmin: true},
			},
			sessions: map[string]string{"owner-cookie": "acct-1", "admin-cookie": "acct-2", "other-cookie": "acct-3"},
		},
		plans:   map[string]bool{"acct-1": true, "acct-2": true, "acct-3": true},
		planErr: map[string]error{},
		tags:    map[string][]string{},
		shared:  map[string][]string{},
		emails:  map[string]string{"acct-1": "owner@example.com", "acct-2": "admin@example.com"},
	}
	registerProvider(t, f)
	return f
}

// shareWith sets the site's admins on disk, as the extension's share does.
func shareWith(t *testing.T, e *env, viewID string, admins ...store.SiteAdmin) {
	t.Helper()
	site, err := e.st.ByViewID(viewID)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.st.UpdateAdmins(site, site.Meta.OwnerAccountID, func([]store.SiteAdmin) ([]store.SiteAdmin, error) { return admins, nil }); err != nil {
		t.Fatal(err)
	}
}

// sharedSite creates a site owned by acct-1 and shares it with acct-2, with
// no end date. It returns the edit id, the edit password and the view id.
func sharedSite(t *testing.T, e *env, f *sharingFake) (edit, pw, viewID string) {
	t.Helper()
	c := e.createSite(t, nil, map[string]string{"index.html": "<h1>shared</h1>"})
	site, err := e.st.ByViewID(c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if site.Meta.OwnerAccountID != "acct-1" {
		t.Fatalf("fixture: owner %q, want acct-1", site.Meta.OwnerAccountID)
	}
	shareWith(t, e, c.ID, store.SiteAdmin{Account: "acct-2"})
	f.mu.Lock()
	f.shared["acct-2"] = append(f.shared["acct-2"], c.ID)
	f.mu.Unlock()
	return editIDFrom(t, c.EditURL), c.EditPassword, c.ID
}

// bodyReq is a request with a JSON body (nil for none).
func bodyReq(method, path string, body any) *http.Request {
	var rd *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return req
}

// editPage is req as Sitebin's edit page sends it for someone signed in:
// the session cookie, the marker header, same-origin fetch metadata — and
// the edit password it may also hold, side by side (R16).
func editPage(req *http.Request, cookie, pw string) *http.Request {
	req.AddCookie(&http.Cookie{Name: "sb_test", Value: cookie})
	req.Header.Set("X-Sitebin-Session", "1")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	if pw != "" {
		req.Header.Set("X-Edit-Password", pw)
	}
	return req
}

// uploadReq is a multipart upload of one file to the site.
func uploadReq(t *testing.T, edit, name, content string) *http.Request {
	t.Helper()
	body, ct := uploadBody(t, nil, map[string]string{name: content})
	req := httptest.NewRequest("POST", "/api/sites/"+edit+"/files", body)
	req.Header.Set("Content-Type", ct)
	return req
}

// payloadOf decodes a JSON object answer.
func payloadOf(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
		t.Fatalf("answer is not a JSON object: %v: %s", err, w.Body)
	}
	return m
}

// adminDoor is one way an admin reaches the per-site API.
type adminDoor struct {
	name string
	as   func(*http.Request) *http.Request
}

// adminDoors are the admin's account token and the edit page (session AND
// the site's real edit password, which the session must outrank).
func adminDoors(pw string) []adminDoor {
	return []adminDoor{
		{"token", func(r *http.Request) *http.Request { return bearer(r, "sbp_admin") }},
		{"editpage", func(r *http.Request) *http.Request { return editPage(r, "admin-cookie", pw) }},
	}
}

func TestAdminMayDoOwnerThings(t *testing.T) {
	e, _ := formsEnv(t, nil)
	f := registerSharing(t, ext.CreateGrant{Trusted: true, MaxForms: intp(5)})
	edit, pw, viewID := sharedSite(t, e, f)

	for _, d := range adminDoors(pw) {
		t.Run(d.name, func(t *testing.T) {
			w := e.public(t, d.as(uploadReq(t, edit, d.name+".txt", "from the admin")))
			if w.Code != 200 {
				t.Fatalf("upload = %d %s", w.Code, w.Body)
			}
			if got := payloadOf(t, w)["role"]; got != "admin" {
				t.Errorf("upload answer role = %v, want admin", got)
			}
			w = e.public(t, d.as(bodyReq("PUT", "/api/sites/"+edit, map[string]any{"name": "by " + d.name})))
			if w.Code != 200 {
				t.Fatalf("settings = %d %s", w.Code, w.Body)
			}
			if site, _ := e.st.ByViewID(viewID); site.Meta.Name != "by "+d.name {
				t.Errorf("name = %q, the admin's settings did not apply", site.Meta.Name)
			}
			w = e.public(t, d.as(bodyReq("POST", "/api/sites/"+edit+"/domains", map[string]any{"domain": d.name + ".example.com"})))
			if w.Code != 200 && w.Code != 202 {
				t.Fatalf("add domain = %d %s", w.Code, w.Body)
			}
			w = e.public(t, d.as(bodyReq("POST", "/api/sites/"+edit+"/forms", map[string]any{"name": "Contact " + d.name, "recipient": "office@example.com"})))
			if w.Code != 201 {
				t.Fatalf("add form = %d %s", w.Code, w.Body)
			}
			w = e.public(t, d.as(bodyReq("GET", "/api/sites/"+edit, nil)))
			if w.Code != 200 {
				t.Fatalf("get = %d %s", w.Code, w.Body)
			}
			p := payloadOf(t, w)
			if p["role"] != "admin" || p["shared_by"] != "owner@example.com" {
				t.Errorf("payload role = %v, shared_by = %v; want admin, owner@example.com", p["role"], p["shared_by"])
			}
		})
	}

	t.Run("mcp", func(t *testing.T) {
		cs := mcpClient(t, e, http.Header{"Authorization": {"Bearer sbp_admin"}})
		for _, call := range []struct {
			tool string
			args map[string]any
		}{
			{"write_files", map[string]any{"files": []any{map[string]any{"path": "mcp.txt", "text": "from the admin"}}}},
			{"update_site", map[string]any{"settings": map[string]any{"name": "by mcp"}}},
			{"add_domain", map[string]any{"domain": "mcp.example.com"}},
			{"add_form", map[string]any{"form": map[string]any{"name": "Contact mcp", "recipient": "office@example.com"}}},
			{"get_site", map[string]any{}},
		} {
			call.args["edit_id"] = edit
			if res := mcpCall(t, cs, call.tool, call.args); res.IsError {
				t.Errorf("%s as the admin: %s", call.tool, mcpText(res))
			}
		}
		site, _ := e.st.ByViewID(viewID)
		if site.Meta.Name != "by mcp" {
			t.Errorf("name = %q after update_site", site.Meta.Name)
		}
		if _, err := e.st.ReadContentFile(site, "mcp.txt"); err != nil {
			t.Errorf("write_files did not write: %v", err)
		}
	})
}

func TestAdminRefusedOwnerOnly(t *testing.T) {
	e := newEnv(t, nil)
	f := registerSharing(t, ext.CreateGrant{Trusted: true})
	edit, pw, viewID := sharedSite(t, e, f)
	if err := e.st.Update(siteByEdit(t, e, edit), func(m *store.Meta) error { m.Name = "kept"; return nil }); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(48 * time.Hour).UTC().Format(time.RFC3339)
	before := siteByEdit(t, e, edit).Meta

	wantOwnerOnly := func(t *testing.T, w *httptest.ResponseRecorder, what string) {
		t.Helper()
		if w.Code != 403 || !strings.Contains(w.Body.String(), ownerOnlyText) {
			t.Errorf("%s = %d %s, want 403 %q", what, w.Code, w.Body, ownerOnlyText)
		}
	}
	for _, d := range adminDoors(pw) {
		t.Run(d.name, func(t *testing.T) {
			wantOwnerOnly(t, e.public(t, d.as(bodyReq("DELETE", "/api/sites/"+edit, nil))), "delete")
			wantOwnerOnly(t, e.public(t, d.as(bodyReq("PUT", "/api/sites/"+edit, map[string]any{"expires_at": future}))), "set expiry")
			wantOwnerOnly(t, e.public(t, d.as(bodyReq("PUT", "/api/sites/"+edit, map[string]any{"expires_at": nil}))), "clear expiry")
			wantOwnerOnly(t, e.public(t, d.as(bodyReq("PUT", "/api/sites/"+edit, map[string]any{"name": "x", "expires_at": future}))), "name and expiry")
			wantOwnerOnly(t, e.public(t, d.as(bodyReq("DELETE", "/api/sites/"+edit+"/git", nil))), "delete repository")
		})
	}
	t.Run("mcp", func(t *testing.T) {
		cs := mcpClient(t, e, http.Header{"Authorization": {"Bearer sbp_admin"}})
		for _, call := range []struct {
			tool string
			args map[string]any
		}{
			{"delete_site", map[string]any{}},
			{"update_site", map[string]any{"settings": map[string]any{"expires_at": future}}},
			{"update_site", map[string]any{"settings": map[string]any{"expires_at": ""}}},
			{"update_site", map[string]any{"settings": map[string]any{"name": "x", "expires_at": future}}},
		} {
			call.args["edit_id"] = edit
			res := mcpCall(t, cs, call.tool, call.args)
			if !res.IsError || !strings.Contains(mcpText(res), ownerOnlyText) {
				t.Errorf("%s %v as the admin = %s, want a tool error %q", call.tool, call.args, mcpText(res), ownerOnlyText)
			}
		}
	})

	// Nothing was written: not the name that came with the expiry, not the
	// expiry, and the site is still there.
	after, err := e.st.ByViewID(viewID)
	if err != nil {
		t.Fatalf("the site is gone: %v", err)
	}
	if after.Meta.Name != "kept" || !reflect.DeepEqual(after.Meta.ExpiresAt, before.ExpiresAt) || after.Meta.ExpiryFromTier != before.ExpiryFromTier {
		t.Errorf("a refused write changed the site: name %q, expiry %v (was %v)", after.Meta.Name, after.Meta.ExpiresAt, before.ExpiresAt)
	}

	// The owner, by token, may.
	w := e.public(t, bearer(bodyReq("PUT", "/api/sites/"+edit, map[string]any{"expires_at": future}), "sbp_owner"))
	if w.Code != 200 {
		t.Fatalf("the owner setting the expiry = %d %s", w.Code, w.Body)
	}
	if w := e.public(t, bearer(bodyReq("DELETE", "/api/sites/"+edit, nil), "sbp_owner")); w.Code != 200 {
		t.Fatalf("the owner deleting = %d %s", w.Code, w.Body)
	}
}

// The edit password stays what it always was: the owner's credential, on an
// owned site and an anonymous one alike. Only an admin is refused.
func TestPasswordActorKeepsOwnerActions(t *testing.T) {
	e := newEnv(t, nil)
	f := registerSharing(t, ext.CreateGrant{Trusted: true})
	edit, pw, viewID := sharedSite(t, e, f)
	future := time.Now().Add(48 * time.Hour).UTC().Truncate(time.Second)

	w := e.call(t, "PUT", "/api/sites/"+edit, pw, map[string]any{"name": "pw", "expires_at": future.Format(time.RFC3339)})
	if w.Code != 200 {
		t.Fatalf("expiry with the edit password = %d %s", w.Code, w.Body)
	}
	if p := payloadOf(t, w); p["role"] != "password" {
		t.Errorf("role = %v, want password", p["role"])
	}
	if site, _ := e.st.ByViewID(viewID); site.Meta.ExpiresAt == nil || !site.Meta.ExpiresAt.Equal(future) {
		t.Errorf("expiry = %v, want %v", site.Meta.ExpiresAt, future)
	}
	if w := e.call(t, "DELETE", "/api/sites/"+edit+"/git", pw, nil); w.Code != 204 {
		t.Errorf("delete repository with the edit password = %d %s", w.Code, w.Body)
	}

	// MCP with the edit password and no account: owner-equivalent too.
	cs := mcpClient(t, e, nil)
	res := mcpCall(t, cs, "update_site", map[string]any{"edit_id": edit, "edit_password": pw, "settings": map[string]any{"expires_at": ""}})
	if res.IsError {
		t.Fatalf("update_site expiry with the edit password: %s", mcpText(res))
	}
	if w := e.call(t, "DELETE", "/api/sites/"+edit, pw, nil); w.Code != 200 {
		t.Fatalf("delete with the edit password = %d %s", w.Code, w.Body)
	}
}

// "Delete repository" is the owner's: an admin is refused, the owner gets in
// through the edit password and through the session alike.
func TestGitDeleteIsOwnerOnly(t *testing.T) {
	e := newEnv(t, nil)
	f := registerSharing(t, ext.CreateGrant{Trusted: true})
	edit, pw, _ := sharedSite(t, e, f)

	w := e.public(t, bearer(bodyReq("DELETE", "/api/sites/"+edit+"/git", nil), "sbp_admin"))
	if w.Code != 403 || !strings.Contains(w.Body.String(), ownerOnlyText) {
		t.Fatalf("admin = %d %s, want 403 %q", w.Code, w.Body, ownerOnlyText)
	}
	w = e.public(t, editPage(bodyReq("DELETE", "/api/sites/"+edit+"/git", nil), "admin-cookie", ""))
	if w.Code != 403 || !strings.Contains(w.Body.String(), ownerOnlyText) {
		t.Fatalf("admin's session = %d %s, want 403", w.Code, w.Body)
	}
	if w := e.call(t, "DELETE", "/api/sites/"+edit+"/git", pw, nil); w.Code != 204 {
		t.Errorf("edit password = %d %s, want 204", w.Code, w.Body)
	}
	if w := e.public(t, editPage(bodyReq("DELETE", "/api/sites/"+edit+"/git", nil), "owner-cookie", "")); w.Code != 204 {
		t.Errorf("owner's session = %d %s, want 204", w.Code, w.Body)
	}
}

// The Git tab reads the caller's role, so the edit page can hide "Delete
// repository" from an admin.
func TestGitInfoCarriesRole(t *testing.T) {
	e := newEnv(t, nil)
	f := registerSharing(t, ext.CreateGrant{Trusted: true})
	edit, pw, _ := sharedSite(t, e, f)
	for _, c := range []struct {
		name string
		req  *http.Request
		want string
	}{
		{"admin token", bearer(bodyReq("GET", "/api/sites/"+edit+"/git", nil), "sbp_admin"), "admin"},
		{"admin session", editPage(bodyReq("GET", "/api/sites/"+edit+"/git", nil), "admin-cookie", pw), "admin"},
		{"owner session", editPage(bodyReq("GET", "/api/sites/"+edit+"/git", nil), "owner-cookie", ""), "owner"},
		{"edit password", authed(bodyReq("GET", "/api/sites/"+edit+"/git", nil), pw), "password"},
	} {
		w := e.public(t, c.req)
		if w.Code != 200 {
			t.Fatalf("%s: %d %s", c.name, w.Code, w.Body)
		}
		if got := payloadOf(t, w)["role"]; got != c.want {
			t.Errorf("%s: role = %v, want %s", c.name, got, c.want)
		}
	}
}

// A share grants nothing once its end date has passed, while either plan
// lacks sharing, or while the plan cannot be asked — and none of that ends
// the share: Meta.Admins is left exactly as it was.
func TestExpiredOrPausedShareGrantsNothing(t *testing.T) {
	past := time.Now().Add(-time.Minute)
	ahead := time.Now().Add(time.Hour)
	// want is the API's answer: 200 standing, 401 (the password prompt) for
	// no standing, 503 when the plan could not be asked.
	for _, c := range []struct {
		name  string
		admin store.SiteAdmin
		plans func(f *sharingFake)
		want  int
	}{
		{"until ahead", store.SiteAdmin{Account: "acct-2", Until: &ahead}, nil, 200},
		{"until passed", store.SiteAdmin{Account: "acct-2", Until: &past}, nil, 401},
		{"owner's plan without sharing", store.SiteAdmin{Account: "acct-2"}, func(f *sharingFake) { f.setPlan("acct-1", false, nil) }, 401},
		{"admin's plan without sharing", store.SiteAdmin{Account: "acct-2"}, func(f *sharingFake) { f.setPlan("acct-2", false, nil) }, 401},
		{"owner's plan unknown", store.SiteAdmin{Account: "acct-2"}, func(f *sharingFake) { f.setPlan("acct-1", true, errors.New("paygate down")) }, 503},
		{"admin's plan unknown", store.SiteAdmin{Account: "acct-2"}, func(f *sharingFake) { f.setPlan("acct-2", true, errors.New("paygate down")) }, 503},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t, nil)
			f := registerSharing(t, ext.CreateGrant{Trusted: true})
			edit, _, viewID := sharedSite(t, e, f)
			shareWith(t, e, viewID, c.admin)
			if c.plans != nil {
				c.plans(f)
			}
			before := siteByEdit(t, e, edit).Meta.Admins

			stand := c.want == 200
			if w := e.public(t, bearer(bodyReq("GET", "/api/sites/"+edit, nil), "sbp_admin")); w.Code != c.want {
				t.Errorf("token = %d %s, want %d", w.Code, w.Body, c.want)
			}
			if w := e.public(t, editPage(bodyReq("GET", "/api/sites/"+edit, nil), "admin-cookie", "")); w.Code != c.want {
				t.Errorf("session = %d %s, want %d", w.Code, w.Body, c.want)
			}
			cs := mcpClient(t, e, http.Header{"Authorization": {"Bearer sbp_admin"}})
			if res := mcpCall(t, cs, "get_site", map[string]any{"edit_id": edit}); res.IsError == stand {
				t.Errorf("MCP get_site error = %v (%s), want error %v", res.IsError, mcpText(res), !stand)
			}
			if after := siteByEdit(t, e, edit).Meta.Admins; !reflect.DeepEqual(after, before) {
				t.Errorf("Meta.Admins changed: %+v, was %+v", after, before)
			}
			// The owner is never affected by the share's state.
			if w := e.public(t, bearer(bodyReq("GET", "/api/sites/"+edit, nil), "sbp_owner")); w.Code != 200 {
				t.Errorf("owner = %d", w.Code)
			}
		})
	}
}

// With no provider implementing ext.SharingAccounts there is no admin
// standing at all, whatever a site's meta lists: the community build and a
// provider without sharing stay as they were.
func TestNoSharingProviderMeansNoAdmins(t *testing.T) {
	e := newEnv(t, nil)
	p := &sessionProvider{
		fakeProvider: &fakeProvider{enabled: true, owner: "acct-1", bearer: map[string]string{"sbp_owner": "acct-1", "sbp_admin": "acct-2"}},
		sessions:     map[string]string{"admin-cookie": "acct-2"},
	}
	registerProvider(t, p)
	c := e.createSite(t, nil, map[string]string{"index.html": "x"})
	edit := editIDFrom(t, c.EditURL)
	shareWith(t, e, c.ID, store.SiteAdmin{Account: "acct-2"})

	if w := e.public(t, bearer(bodyReq("GET", "/api/sites/"+edit, nil), "sbp_admin")); w.Code != 401 {
		t.Errorf("listed admin's token = %d, want 401", w.Code)
	}
	if w := e.public(t, editPage(bodyReq("GET", "/api/sites/"+edit, nil), "admin-cookie", "")); w.Code != 401 {
		t.Errorf("listed admin's session = %d, want 401", w.Code)
	}
	cs := mcpClient(t, e, http.Header{"Authorization": {"Bearer sbp_admin"}})
	if res := mcpCall(t, cs, "get_site", map[string]any{"edit_id": edit}); !res.IsError {
		t.Error("MCP opened the site for a listed admin with no sharing provider")
	}
	if w := e.public(t, bearer(bodyReq("GET", "/api/sites/"+edit, nil), "sbp_owner")); w.Code != 200 {
		t.Errorf("owner = %d", w.Code)
	}
}

// An admin's write is recorded as the admin's: their account, and the role.
// The owner's and the password's lines read as they always did.
func TestProvenanceRecordsAdmin(t *testing.T) {
	e := newEnv(t, nil)
	f := registerSharing(t, ext.CreateGrant{Trusted: true})
	edit, pw, viewID := sharedSite(t, e, f)
	last := func(t *testing.T) provenance.Entry {
		t.Helper()
		site, err := e.st.ByViewID(viewID)
		if err != nil {
			t.Fatal(err)
		}
		es, err := e.st.Provenance(site)
		if err != nil || len(es) == 0 {
			t.Fatalf("provenance: %v, %d entries", err, len(es))
		}
		return es[len(es)-1]
	}
	want := func(t *testing.T, got provenance.Entry, surface, auth, account, role string) {
		t.Helper()
		if got.Surface != surface || got.Auth != auth || got.Account != account || got.Role != role {
			t.Errorf("entry = %+v, want surface %s auth %s account %q role %q", got, surface, auth, account, role)
		}
	}

	if w := e.public(t, bearer(bodyReq("PUT", "/api/sites/"+edit, map[string]any{"name": "a"}), "sbp_admin")); w.Code != 200 {
		t.Fatalf("admin token: %d %s", w.Code, w.Body)
	}
	want(t, last(t), provenance.SurfaceAPI, provenance.AuthToken, "acct-2", provenance.RoleAdmin)

	if w := e.public(t, editPage(bodyReq("PUT", "/api/sites/"+edit, map[string]any{"name": "b"}), "admin-cookie", pw)); w.Code != 200 {
		t.Fatalf("admin session: %d %s", w.Code, w.Body)
	}
	want(t, last(t), provenance.SurfaceUI, provenance.AuthSession, "acct-2", provenance.RoleAdmin)

	if w := e.public(t, bearer(bodyReq("PUT", "/api/sites/"+edit, map[string]any{"name": "c"}), "sbp_owner")); w.Code != 200 {
		t.Fatalf("owner token: %d %s", w.Code, w.Body)
	}
	want(t, last(t), provenance.SurfaceAPI, provenance.AuthToken, "acct-1", "")

	if w := e.call(t, "PUT", "/api/sites/"+edit, pw, map[string]any{"name": "d"}); w.Code != 200 {
		t.Fatalf("password: %d %s", w.Code, w.Body)
	}
	want(t, last(t), provenance.SurfaceAPI, provenance.AuthPassword, "", "")

	cs := mcpClient(t, e, http.Header{"Authorization": {"Bearer sbp_admin"}})
	if res := mcpCall(t, cs, "write_files", map[string]any{"edit_id": edit, "files": []any{map[string]any{"path": "m.txt", "text": "m"}}}); res.IsError {
		t.Fatalf("write_files: %s", mcpText(res))
	}
	want(t, last(t), provenance.SurfaceMCP, provenance.AuthToken, "acct-2", provenance.RoleAdmin)

	// open_upload issues the admin's token, and what it uploads is the admin's.
	res := mcpCall(t, cs, "open_upload", map[string]any{"edit_id": edit})
	if res.IsError {
		t.Fatalf("open_upload: %s", mcpText(res))
	}
	want(t, last(t), provenance.SurfaceMCP, provenance.AuthToken, "acct-2", provenance.RoleAdmin)
	var up mcp.UploadResult
	raw, _ := json.Marshal(res.StructuredContent)
	if err := json.Unmarshal(raw, &up); err != nil {
		t.Fatal(err)
	}
	if got := e.api.uploads.issuer(up.Token); got != "acct-2" {
		t.Errorf("upload token issuer = %q, want the admin acct-2", got)
	}
	if w := e.public(t, bearer(uploadReq(t, edit, "u.txt", "u"), up.Token)); w.Code != 200 {
		t.Fatalf("upload with the admin's token: %d %s", w.Code, w.Body)
	}
	want(t, last(t), provenance.SurfaceUploadToken, provenance.AuthUploadToken, "acct-2", provenance.RoleAdmin)
}

// The plan is asked only for an account the site's meta lists as an admin:
// an owner, a stranger and an unshared site cost no lookup.
func TestStandingAsksThePlanOnlyForListedAdmins(t *testing.T) {
	e := newEnv(t, nil)
	f := registerSharing(t, ext.CreateGrant{Trusted: true})
	edit, _, _ := sharedSite(t, e, f)
	calls := func() int {
		f.mu.Lock()
		defer f.mu.Unlock()
		return f.planCalls
	}
	for _, c := range []struct {
		token string
		code  int
		asks  int
	}{
		{"sbp_owner", 200, 0},
		{"sbp_other", 401, 0},
		{"sbp_admin", 200, 2}, // the owner's plan and the admin's
	} {
		before := calls()
		if w := e.public(t, bearer(bodyReq("GET", "/api/sites/"+edit, nil), c.token)); w.Code != c.code {
			t.Fatalf("%s: %d, want %d", c.token, w.Code, c.code)
		}
		if got := calls() - before; got != c.asks {
			t.Errorf("%s: SharingAllowed asked %d times, want %d", c.token, got, c.asks)
		}
	}
}

// adminUploadToken opens an upload token through MCP as the admin (acct-2).
func adminUploadToken(t *testing.T, e *env, edit string) string {
	t.Helper()
	cs := mcpClient(t, e, http.Header{"Authorization": {"Bearer sbp_admin"}})
	res := mcpCall(t, cs, "open_upload", map[string]any{"edit_id": edit})
	if res.IsError {
		t.Fatalf("open_upload as the admin: %s", mcpText(res))
	}
	tok, _ := res.StructuredContent.(map[string]any)["token"].(string)
	if e.api.uploads.issuer(tok) != "acct-2" {
		t.Fatalf("the token is not the admin's: issuer %q", e.api.uploads.issuer(tok))
	}
	return tok
}

// An admin's upload token is the admin's only while the admin manages the
// site: once the share's end date passes, while a plan does not share, or
// while a plan cannot be asked, the token is refused on POST /files (replace
// included) and on WebDAV, and nothing is written.
func TestAdminUploadTokenEndsWithTheShare(t *testing.T) {
	past := time.Now().Add(-time.Minute)
	for _, c := range []struct {
		name string
		end  func(t *testing.T, e *env, f *sharingFake, viewID string)
		code int
		msg  string
	}{
		{"until passed", func(t *testing.T, e *env, f *sharingFake, viewID string) {
			shareWith(t, e, viewID, store.SiteAdmin{Account: "acct-2", Until: &past})
		}, 403, msgUploadTokenShareEnded},
		{"admin's plan without sharing", func(t *testing.T, e *env, f *sharingFake, viewID string) { f.setPlan("acct-2", false, nil) }, 403, msgUploadTokenShareEnded},
		{"owner's plan without sharing", func(t *testing.T, e *env, f *sharingFake, viewID string) { f.setPlan("acct-1", false, nil) }, 403, msgUploadTokenShareEnded},
		{"plan unknown", func(t *testing.T, e *env, f *sharingFake, viewID string) {
			f.setPlan("acct-2", true, errors.New("paygate down"))
		}, 503, msgSharingUnknown},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t, nil)
			f := registerSharing(t, ext.CreateGrant{Trusted: true})
			edit, _, viewID := sharedSite(t, e, f)
			tok := adminUploadToken(t, e, edit)
			post := func(name string, replace bool) *httptest.ResponseRecorder {
				req := uploadReq(t, edit, name, "x")
				if replace {
					req.URL.RawQuery = "replace=true"
				}
				return e.public(t, bearer(req, tok))
			}
			put := func(name string) *httptest.ResponseRecorder {
				return e.public(t, bearer(httptest.NewRequest("PUT", "/dav/"+edit+"/"+name, strings.NewReader("x")), tok))
			}

			// While the share is live, the token works on both routes.
			if w := post("live.txt", false); w.Code != 200 {
				t.Fatalf("POST while live = %d %s", w.Code, w.Body)
			}
			if w := put("live.bin"); w.Code != 201 {
				t.Fatalf("DAV PUT while live = %d %s", w.Code, w.Body)
			}

			c.end(t, e, f, viewID)
			for what, w := range map[string]*httptest.ResponseRecorder{
				"POST":         post("after.txt", false),
				"POST replace": post("replaced.txt", true),
				"DAV PUT":      put("after.bin"),
			} {
				if w.Code != c.code || !strings.Contains(w.Body.String(), c.msg) {
					t.Errorf("%s after the share ended = %d %s, want %d %q", what, w.Code, w.Body, c.code, c.msg)
				}
			}
			site, _ := e.st.ByViewID(viewID)
			for _, name := range []string{"after.txt", "replaced.txt", "after.bin"} {
				if _, err := e.st.ReadContentFile(site, name); err == nil {
					t.Errorf("%s was written with a token whose share ended", name)
				}
			}
			if _, err := e.st.ReadContentFile(site, "live.txt"); err != nil {
				t.Errorf("a refused replace touched the site: live.txt %v", err)
			}
		})
	}
}

// An owner's upload token and the edit password's are not re-checked: only
// an admin's standing can end while its token lives.
func TestOwnerUploadTokenUnaffectedByPlans(t *testing.T) {
	e := newEnv(t, nil)
	f := registerSharing(t, ext.CreateGrant{Trusted: true})
	edit, _, _ := sharedSite(t, e, f)
	cs := mcpClient(t, e, http.Header{"Authorization": {"Bearer sbp_owner"}})
	res := mcpCall(t, cs, "open_upload", map[string]any{"edit_id": edit})
	if res.IsError {
		t.Fatalf("open_upload: %s", mcpText(res))
	}
	tok, _ := res.StructuredContent.(map[string]any)["token"].(string)
	f.setPlan("acct-1", true, errors.New("paygate down"))
	before := f.planCallsNow()
	if w := e.public(t, bearer(uploadReq(t, edit, "o.txt", "o"), tok)); w.Code != 200 {
		t.Fatalf("owner's token = %d %s", w.Code, w.Body)
	}
	if f.planCallsNow() != before {
		t.Error("an owner's token asked the plan")
	}
}

func (f *sharingFake) planCallsNow() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.planCalls
}

// siteTrail is the site's provenance log.
func siteTrail(t *testing.T, e *env, viewID string) []provenance.Entry {
	t.Helper()
	site, err := e.st.ByViewID(viewID)
	if err != nil {
		t.Fatal(err)
	}
	es, err := e.st.Provenance(site)
	if err != nil {
		t.Fatal(err)
	}
	return es
}

// The admin's tags are stored before anything of the site: when that fails,
// the whole write fails with nothing written and nothing recorded — on the
// API and over MCP.
func TestAdminTagsFailureWritesNothing(t *testing.T) {
	e := newEnv(t, nil)
	f := registerSharing(t, ext.CreateGrant{Trusted: true})
	edit, _, viewID := sharedSite(t, e, f)
	f.mu.Lock()
	f.setTagsErr = errors.New("registry unreachable")
	f.mu.Unlock()
	before := siteByEdit(t, e, edit).Meta
	trail := len(siteTrail(t, e, viewID))

	w := e.public(t, bearer(bodyReq("PUT", "/api/sites/"+edit, map[string]any{"name": "x", "mode": "viewer", "spa_fallback": true, "tags": []string{"a"}}), "sbp_admin"))
	if w.Code != 503 || !strings.Contains(w.Body.String(), msgAdminTagsUnavailable) {
		t.Fatalf("API = %d %s, want 503 %q", w.Code, w.Body, msgAdminTagsUnavailable)
	}
	cs := mcpClient(t, e, http.Header{"Authorization": {"Bearer sbp_admin"}})
	res := mcpCall(t, cs, "update_site", map[string]any{"edit_id": edit, "settings": map[string]any{"name": "y", "tags": []string{"b"}}})
	if !res.IsError || !strings.Contains(mcpText(res), msgAdminTagsUnavailable) {
		t.Fatalf("MCP = %s, want a tool error %q", mcpText(res), msgAdminTagsUnavailable)
	}

	after := siteByEdit(t, e, edit).Meta
	if after.Name != before.Name || after.Mode != before.Mode || after.SPAFallback != before.SPAFallback || !after.UpdatedAt.Equal(before.UpdatedAt) {
		t.Errorf("a failed write changed the site: name %q mode %q spa %v updated %v (was %q %q %v %v)",
			after.Name, after.Mode, after.SPAFallback, after.UpdatedAt, before.Name, before.Mode, before.SPAFallback, before.UpdatedAt)
	}
	if n := len(siteTrail(t, e, viewID)); n != trail {
		t.Errorf("the failed write was recorded: %d entries, was %d", n, trail)
	}
	if got := f.adminTagsOf("acct-2", viewID); got != nil {
		t.Errorf("tags stored: %v", got)
	}
}

// A write by an admin that carries nothing but tags changes only the admin's
// own labels: meta.json is not rewritten and the site's log records nothing.
// A write with anything else still writes and records it.
func TestAdminTagsOnlyWriteLeavesTheSite(t *testing.T) {
	e := newEnv(t, nil)
	f := registerSharing(t, ext.CreateGrant{Trusted: true})
	edit, _, viewID := sharedSite(t, e, f)
	before := siteByEdit(t, e, edit).Meta
	trail := len(siteTrail(t, e, viewID))

	w := e.public(t, bearer(bodyReq("PUT", "/api/sites/"+edit, map[string]any{"tags": []string{"mine"}}), "sbp_admin"))
	if w.Code != 200 {
		t.Fatalf("API tags = %d %s", w.Code, w.Body)
	}
	if got := tagsOf(t, payloadOf(t, w)["tags"]); !reflect.DeepEqual(got, []string{"mine"}) {
		t.Errorf("answer tags = %v", got)
	}
	cs := mcpClient(t, e, http.Header{"Authorization": {"Bearer sbp_admin"}})
	res := mcpCall(t, cs, "update_site", map[string]any{"edit_id": edit, "settings": map[string]any{"tags": []string{"agent"}}})
	if res.IsError {
		t.Fatalf("MCP tags: %s", mcpText(res))
	}
	if got := f.adminTagsOf("acct-2", viewID); !reflect.DeepEqual(got, []string{"agent"}) {
		t.Errorf("admin tags = %v", got)
	}
	if after := siteByEdit(t, e, edit).Meta; !after.UpdatedAt.Equal(before.UpdatedAt) {
		t.Errorf("meta.json was rewritten by a tags-only write: updated %v, was %v", after.UpdatedAt, before.UpdatedAt)
	}
	if n := len(siteTrail(t, e, viewID)); n != trail {
		t.Errorf("a tags-only write was recorded: %d entries, was %d", n, trail)
	}

	w = e.public(t, bearer(bodyReq("PUT", "/api/sites/"+edit, map[string]any{"name": "n", "tags": []string{"x"}}), "sbp_admin"))
	if w.Code != 200 {
		t.Fatalf("API name+tags = %d %s", w.Code, w.Body)
	}
	if es := siteTrail(t, e, viewID); len(es) != trail+1 || es[len(es)-1].Action != provenance.ActionSettings {
		t.Errorf("name+tags: trail %+v, want one settings entry more", es)
	}
	if after := siteByEdit(t, e, edit).Meta; after.Name != "n" || !reflect.DeepEqual(after.Tags, before.Tags) {
		t.Errorf("name+tags: name %q tags %v", after.Name, after.Tags)
	}
}

// A request refused only because a sharing plan could not be asked says so:
// 503 on the API (never the password prompt, even with the password beside
// the session), a tool error on MCP, a readable refusal on Git. The warning
// is logged once per account a minute, not on every request.
func TestPlanUnknownIsAnsweredAsSuch(t *testing.T) {
	e := newEnv(t, nil)
	logs := &lockedBuffer{}
	e.api.log = slog.New(slog.NewTextHandler(logs, nil))
	f := registerSharing(t, ext.CreateGrant{Trusted: true})
	edit, pw, _ := sharedSite(t, e, f)
	f.setPlan("acct-2", true, errors.New("paygate down"))

	for i := 0; i < 3; i++ {
		w := e.public(t, bearer(bodyReq("GET", "/api/sites/"+edit, nil), "sbp_admin"))
		if w.Code != 503 || !strings.Contains(w.Body.String(), msgSharingUnknown) {
			t.Fatalf("token = %d %s, want 503 %q", w.Code, w.Body, msgSharingUnknown)
		}
	}
	if w := e.public(t, editPage(bodyReq("GET", "/api/sites/"+edit, nil), "admin-cookie", pw)); w.Code != 503 {
		t.Errorf("session + password = %d %s, want 503", w.Code, w.Body)
	}
	cs := mcpClient(t, e, http.Header{"Authorization": {"Bearer sbp_admin"}})
	if res := mcpCall(t, cs, "get_site", map[string]any{"edit_id": edit}); !res.IsError || !strings.Contains(mcpText(res), msgSharingUnknown) {
		t.Errorf("MCP = %s, want a tool error %q", mcpText(res), msgSharingUnknown)
	}
	site := siteByEdit(t, e, edit)
	for _, secret := range []string{"sbp_admin", jwtAdmin} {
		if _, d := e.api.gitAuth(gitReq("drop", secret), site, false); d == nil || !d.refusal || d.msg != msgSharingUnknown {
			t.Errorf("git with %s: denial %+v, want a readable refusal %q", secret[:6], d, msgSharingUnknown)
		}
	}
	if n := strings.Count(logs.String(), "sharing: plan unknown"); n != 1 {
		t.Errorf("the plan warning was logged %d times, want once a minute per account:\n%s", n, logs.String())
	}
	if !reflect.DeepEqual(siteByEdit(t, e, edit).Meta.Admins, []store.SiteAdmin{{Account: "acct-2"}}) {
		t.Error("an unknown plan changed the share")
	}
}

// operatorSharing is the sharing fake that also knows the operator.
type operatorSharing struct {
	*sharingFake
	ops map[string]bool
}

func (o *operatorSharing) IsOperator(id string) bool { return o.ops[id] }

var _ ext.OperatorAccounts = (*operatorSharing)(nil)

// An admin has no standing on a site whose OWNER is the operator (the
// operator's exemptions would be lent to the admin), but the operator as an
// ADMIN elsewhere stands like anyone: the rule is keyed on the owner only.
func TestStandingNeedsAnOwnerWhoIsNotTheOperator(t *testing.T) {
	e := newEnv(t, nil)
	f := registerSharing(t, ext.CreateGrant{Trusted: true})
	_, _, viewID := sharedSite(t, e, f)
	ops := map[string]bool{}
	ext.Reset()
	ext.Register(&operatorSharing{sharingFake: f, ops: ops})
	site, err := e.st.ByViewID(viewID)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name string
		ops  map[string]bool
		want standing
	}{
		{"nobody is the operator", map[string]bool{}, standAdmin},
		{"the owner is the operator", map[string]bool{"acct-1": true}, standNone},
		{"the admin is the operator", map[string]bool{"acct-2": true}, standAdmin},
	} {
		for k := range ops {
			delete(ops, k)
		}
		for k, v := range c.ops {
			ops[k] = v
		}
		got, err := e.api.accountStanding(site, "acct-2")
		if err != nil || got != c.want {
			t.Errorf("%s: standing = %v, %v; want %v", c.name, got, err, c.want)
		}
	}
	if got, _ := e.api.accountStanding(site, "acct-1"); got != standOwner {
		t.Errorf("owner = %v", got)
	}
}
