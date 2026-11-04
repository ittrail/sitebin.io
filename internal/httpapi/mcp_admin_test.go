package httpapi

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ittrail/sitebin.io/internal/ext"
	"github.com/ittrail/sitebin.io/internal/mcp"
	"github.com/ittrail/sitebin.io/internal/store"
)

// ---- an admin over MCP, and the admin's own tags ----

// tagsOf reads a payload's tags as strings.
func tagsOf(t *testing.T, v any) []string {
	t.Helper()
	list, ok := v.([]any)
	if !ok {
		t.Fatalf("tags = %#v, want a list", v)
	}
	out := []string{}
	for _, x := range list {
		out = append(out, x.(string))
	}
	return out
}

// An admin's tags are the admin's own, on the API and over MCP: written to
// the extension (SetAdminTags), read from it (AdminTags), and the owner's
// Meta.Tags are neither shown to the admin nor touched by them.
func TestAdminTagsAreOwnInAPIAndMCP(t *testing.T) {
	e := newEnv(t, nil)
	f := registerSharing(t, ext.CreateGrant{Trusted: true})
	edit, _, viewID := sharedSite(t, e, f)
	if w := e.public(t, bearer(bodyReq("PUT", "/api/sites/"+edit, map[string]any{"tags": []string{"Blue"}}), "sbp_owner")); w.Code != 200 {
		t.Fatalf("owner tags: %d %s", w.Code, w.Body)
	}
	ownerTags := []string{"Blue"}

	// The API.
	w := e.public(t, bearer(bodyReq("PUT", "/api/sites/"+edit, map[string]any{"tags": []string{"work", "Mine", "mine"}}), "sbp_admin"))
	if w.Code != 200 {
		t.Fatalf("admin tags: %d %s", w.Code, w.Body)
	}
	if got := tagsOf(t, payloadOf(t, w)["tags"]); !reflect.DeepEqual(got, []string{"Mine", "work"}) {
		t.Errorf("answer tags = %v, want the admin's own [Mine work]", got)
	}
	if got := f.adminTagsOf("acct-2", viewID); !reflect.DeepEqual(got, []string{"Mine", "work"}) {
		t.Errorf("SetAdminTags got %v, want the cleaned [Mine work]", got)
	}
	site, _ := e.st.ByViewID(viewID)
	if !reflect.DeepEqual(site.Meta.Tags, ownerTags) {
		t.Errorf("Meta.Tags = %v, the admin's write touched the owner's tags", site.Meta.Tags)
	}
	p := payloadOf(t, e.public(t, bearer(bodyReq("GET", "/api/sites/"+edit, nil), "sbp_admin")))
	if got := tagsOf(t, p["tags"]); !reflect.DeepEqual(got, []string{"Mine", "work"}) {
		t.Errorf("admin GET tags = %v", got)
	}
	p = payloadOf(t, e.public(t, bearer(bodyReq("GET", "/api/sites/"+edit, nil), "sbp_owner")))
	if got := tagsOf(t, p["tags"]); !reflect.DeepEqual(got, ownerTags) || p["role"] != "owner" || p["shared_by"] != nil {
		t.Errorf("owner GET: tags %v role %v shared_by %v; want %v, owner, none", got, p["role"], p["shared_by"], ownerTags)
	}
	// A bad tag is refused, and nothing is stored.
	bad := strings.Repeat("x", store.MaxTagRunes+1)
	if w := e.public(t, bearer(bodyReq("PUT", "/api/sites/"+edit, map[string]any{"tags": []string{bad}}), "sbp_admin")); w.Code != 400 {
		t.Errorf("bad tag = %d, want 400", w.Code)
	}
	if got := f.adminTagsOf("acct-2", viewID); !reflect.DeepEqual(got, []string{"Mine", "work"}) {
		t.Errorf("a refused write changed the admin's tags: %v", got)
	}

	// MCP.
	cs := mcpClient(t, e, http.Header{"Authorization": {"Bearer sbp_admin"}})
	res := mcpCall(t, cs, "get_site", map[string]any{"edit_id": edit})
	if res.IsError {
		t.Fatalf("get_site: %s", mcpText(res))
	}
	if got := tagsOf(t, res.StructuredContent.(map[string]any)["tags"]); !reflect.DeepEqual(got, []string{"Mine", "work"}) {
		t.Errorf("MCP get_site tags = %v, want the admin's own", got)
	}
	res = mcpCall(t, cs, "update_site", map[string]any{"edit_id": edit, "settings": map[string]any{"tags": []string{"agent"}}})
	if res.IsError {
		t.Fatalf("update_site tags: %s", mcpText(res))
	}
	if got := tagsOf(t, res.StructuredContent.(map[string]any)["tags"]); !reflect.DeepEqual(got, []string{"agent"}) {
		t.Errorf("update_site answer tags = %v", got)
	}
	if got := f.adminTagsOf("acct-2", viewID); !reflect.DeepEqual(got, []string{"agent"}) {
		t.Errorf("MCP SetAdminTags got %v", got)
	}
	site, _ = e.st.ByViewID(viewID)
	if !reflect.DeepEqual(site.Meta.Tags, ownerTags) {
		t.Errorf("Meta.Tags = %v after the admin's update_site", site.Meta.Tags)
	}
	owner := mcpClient(t, e, http.Header{"Authorization": {"Bearer sbp_owner"}})
	res = mcpCall(t, owner, "get_site", map[string]any{"edit_id": edit})
	if got := tagsOf(t, res.StructuredContent.(map[string]any)["tags"]); !reflect.DeepEqual(got, ownerTags) {
		t.Errorf("owner's get_site tags = %v, want %v", got, ownerTags)
	}
}

// list_sites shows the sites shared with the caller after its own, each with
// role "admin" and the admin's own tags — but only where the standing really
// holds: never a share that ended, a site whose meta does not list the caller
// (a stale index entry), a site that is gone, or one of its own twice.
func TestListSitesIncludesSharedWithRole(t *testing.T) {
	e := newEnv(t, nil)
	f := registerSharing(t, ext.CreateGrant{Trusted: true})

	// The admin's own site.
	own := e.createSite(t, nil, map[string]string{"index.html": "own"})
	if err := e.st.Update(siteByEdit(t, e, editIDFrom(t, own.EditURL)), func(m *store.Meta) error { m.OwnerAccountID = "acct-2"; return nil }); err != nil {
		t.Fatal(err)
	}
	f.owned = map[string][]string{"acct-2": {own.ID}}

	live, _, liveID := sharedSite(t, e, f)
	f.tags["acct-2|"+liveID] = []string{"mine"}
	_, _, endedID := sharedSite(t, e, f)
	past := time.Now().Add(-time.Minute)
	shareWith(t, e, endedID, store.SiteAdmin{Account: "acct-2", Until: &past})
	_, _, unlistedID := sharedSite(t, e, f)
	shareWith(t, e, unlistedID, store.SiteAdmin{Account: "acct-3"})
	f.mu.Lock()
	f.shared["acct-2"] = append(f.shared["acct-2"], "gone0000000000000000000000", own.ID)
	f.mu.Unlock()

	cs := mcpClient(t, e, http.Header{"Authorization": {"Bearer sbp_admin"}})
	res := mcpCall(t, cs, "list_sites", map[string]any{})
	if res.IsError {
		t.Fatalf("list_sites: %s", mcpText(res))
	}
	var out struct {
		Sites []mcp.SiteSummary `json:"sites"`
	}
	raw, _ := json.Marshal(res.StructuredContent)
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Sites) != 2 {
		t.Fatalf("list_sites = %+v, want the own site and the one live share", out.Sites)
	}
	if out.Sites[0].ID != own.ID || out.Sites[0].Role != "" {
		t.Errorf("first row = %+v, want the own site without a role", out.Sites[0])
	}
	if s := out.Sites[1]; s.ID != liveID || s.Role != "admin" || !reflect.DeepEqual(s.Tags, []string{"mine"}) || s.EditID != live {
		t.Errorf("second row = %+v, want the shared site with role admin and the admin's own tags", s)
	}
	if !strings.Contains(string(raw), `"role":"admin"`) {
		t.Errorf("the JSON carries no role: %s", raw)
	}

	// The owner's listing is unchanged: no role on its rows.
	f.owned["acct-1"] = []string{liveID}
	owner := mcpClient(t, e, http.Header{"Authorization": {"Bearer sbp_owner"}})
	raw, _ = json.Marshal(mcpCall(t, owner, "list_sites", map[string]any{}).StructuredContent)
	if strings.Contains(string(raw), `"role"`) {
		t.Errorf("the owner's listing carries a role: %s", raw)
	}
}
