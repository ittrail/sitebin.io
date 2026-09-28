package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ittrail/sitebin.io/internal/ext"
	"github.com/ittrail/sitebin.io/internal/store"
)

// lockSite locks the site made by createSite, as the operator would.
func lockSite(t *testing.T, e *env, viewID, reason string) {
	t.Helper()
	if err := e.api.SiteService().SetLock(viewID, &ext.SiteLock{Reason: reason, By: ext.LockByAdmin}); err != nil {
		t.Fatalf("SetLock: %v", err)
	}
}

func unlockSite(t *testing.T, e *env, viewID string) {
	t.Helper()
	if err := e.api.SiteService().SetLock(viewID, nil); err != nil {
		t.Fatalf("unlock: %v", err)
	}
}

func assertSuspended(t *testing.T, what string, w *httptest.ResponseRecorder) {
	t.Helper()
	if w.Code != 410 {
		t.Fatalf("%s: %d, want 410", what, w.Code)
	}
	if b := w.Body.String(); !strings.Contains(b, "Site suspended") || !strings.Contains(b, "suspended by the operator") {
		t.Errorf("%s: not the suspension page: %s", what, b)
	}
	if w.Header().Get(upstreamHeader) != "" {
		t.Errorf("%s: a locked site was given an upstream", what)
	}
}

// ---- not served, anywhere ----

func TestAuthzLockedSiteIsSuspendedOnEveryRoute(t *testing.T) {
	ext.Register(&fakeProvider{domainsOK: true})
	defer ext.Reset()
	e := newEnv(t, nil)
	c := e.createSite(t, nil, map[string]string{"index.html": "phish"})
	edit := editIDFrom(t, c.EditURL)
	req := authed(httptest.NewRequest("POST", "/api/sites/"+edit+"/domains", strings.NewReader(`{"domain":"bank-login.example.org"}`)), c.EditPassword)
	if w := e.public(t, req); w.Code != 200 {
		t.Fatalf("add domain: %d %s", w.Code, w.Body)
	}
	lockSite(t, e, c.ID, "phishing")

	assertSuspended(t, "view host", e.internal(t, authzReq(c.ID+".sitebin.example", "/", "")))
	assertSuspended(t, "custom domain", e.internal(t, authzReq("bank-login.example.org", "/login", "")))
	pathReq := httptest.NewRequest("GET", "/internal/authz", nil)
	pathReq.Header.Set("X-Sitebin-View", c.ID)
	assertSuspended(t, "path view", e.internal(t, pathReq))
	// The reason is between the operator and the owner.
	if b := e.internal(t, authzReq(c.ID+".sitebin.example", "/", "")).Body.String(); strings.Contains(b, "phishing") {
		t.Error("the suspension page shows the lock's reason to visitors")
	}

	unlockSite(t, e, c.ID)
	if w := e.internal(t, authzReq(c.ID+".sitebin.example", "/", "")); w.Code != 200 {
		t.Fatalf("after unlock: %d", w.Code)
	}
}

// The lock comes before every other rule: a view password does not show a
// gate (whose cookie would then open it), and an expired site says it is
// suspended, not expired.
func TestAuthzLockComesFirst(t *testing.T) {
	e := newEnv(t, nil)
	prot := e.createSite(t, map[string]string{"view_password": "sesame"}, map[string]string{"index.html": "x"})
	past := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	old := e.createSite(t, map[string]string{"expires_at": past}, map[string]string{"index.html": "x"})
	lockSite(t, e, prot.ID, "")
	lockSite(t, e, old.ID, "")
	assertSuspended(t, "view-password site", e.internal(t, authzReq(prot.ID+".sitebin.example", "/", "")))
	assertSuspended(t, "expired site", e.internal(t, authzReq(old.ID+".sitebin.example", "/", "")))
}

func TestAuthzLockedContainerSiteGetsNoUpstream(t *testing.T) {
	rt := &fakeRuntime{}
	withContainers(t, rt)
	e := newEnv(t, nil)
	c := e.createSite(t, map[string]string{"mode": "container"}, nil)
	running(t, e, c.ID, ext.ContainerService{Name: "app", Host: "sb-" + c.ID + "-app", Domains: []ext.ContainerDomain{{Domain: "*", Port: 3000}}})
	host := c.ID + "." + e.cfg.ViewDomain
	if w := authzFor(t, e, host, nil); w.Code != 200 || w.Header().Get(upstreamHeader) == "" {
		t.Fatalf("running: %d %q", w.Code, w.Header().Get(upstreamHeader))
	}
	kicks := len(rt.kicked)
	lockSite(t, e, c.ID, "")
	assertSuspended(t, "container site", authzFor(t, e, host, nil))
	// The runtime is told at once, and sees the lock in the site it reads.
	if len(rt.kicked) != kicks+1 || rt.kicked[len(rt.kicked)-1] != c.ID {
		t.Errorf("runtime not kicked on lock: %v", rt.kicked)
	}
	cs, err := e.api.SiteService().ContainerSite(c.ID)
	if err != nil || !cs.Locked {
		t.Fatalf("ContainerSite.Locked = %v, %v", cs.Locked, err)
	}
	// An unlock hands the enabled project back as a restart.
	seq := cs.RestartSeq
	unlockSite(t, e, c.ID)
	cs, _ = e.api.SiteService().ContainerSite(c.ID)
	if cs.Locked || cs.RestartSeq != seq+1 {
		t.Errorf("after unlock: locked %v, seq %d (was %d)", cs.Locked, cs.RestartSeq, seq)
	}
}

func TestLockedSiteRefusesFormSubmissions(t *testing.T) {
	e, rs := formsEnv(t, nil)
	site, f := activeForm(t, e, store.Form{Captcha: true})
	lockSite(t, e, site.ViewID, "")
	w := submit(t, e, viewHost(site), f.Key, "name=Anna&password=hunter2", nil)
	if w.Code != 410 {
		t.Fatalf("submit to a locked site = %d, want 410", w.Code)
	}
	if n := rs.count(); n != 0 {
		t.Errorf("%d mail(s) sent from a locked site", n)
	}
	if w := get(t, e, viewHost(site), "/_sitebin/forms/"+f.Key+"/challenge", nil); w.Code != 404 {
		t.Errorf("captcha challenge on a locked site = %d, want 404", w.Code)
	}
}

// ---- frozen for the owner ----

// Every per-site route but the settings read is refused on a locked site,
// whatever the credential — and only once it has authenticated.
func TestLockedSiteRefusesEveryPerSiteRoute(t *testing.T) {
	p := &sessionProvider{
		fakeProvider: &fakeProvider{enabled: true, owner: "acct-1", domainsOK: true, bearer: map[string]string{"sbp_tok": "acct-1"}},
		sessions:     map[string]string{"owner-cookie": "acct-1"},
	}
	registerProvider(t, p)
	e := newEnv(t, map[string]string{"SITEBIN_FTP_ENABLED": "true"})
	c := e.createSite(t, map[string]string{"webdav": "true", "ftp": "true"}, map[string]string{"index.html": "phish", "a/b.txt": "b"})
	edit := editIDFrom(t, c.EditURL)
	lockSite(t, e, c.ID, "phishing")

	routes := []struct{ method, path, body string }{
		{"GET", "/api/sites/" + edit + "/download", ""},
		{"GET", "/api/sites/" + edit + "/content/index.html", ""},
		{"GET", "/api/sites/" + edit + "/dir", ""},
		{"PUT", "/api/sites/" + edit, `{"name":"clean"}`},
		{"DELETE", "/api/sites/" + edit, ""},
		{"POST", "/api/sites/" + edit + "/files", ""},
		{"DELETE", "/api/sites/" + edit + "/files/index.html", ""},
		{"POST", "/api/sites/" + edit + "/domains", `{"domain":"x.example.org"}`},
		{"DELETE", "/api/sites/" + edit + "/domains/x.example.org", ""},
		{"POST", "/api/sites/" + edit + "/containers/start", ""},
		{"GET", "/api/sites/" + edit + "/containers/app/logs", ""},
		{"GET", "/api/sites/" + edit + "/forms", ""},
		{"POST", "/api/sites/" + edit + "/forms", `{"name":"x","recipient":"a@example.com"}`},
		{"PUT", "/api/sites/" + edit + "/forms/abc", `{"name":"y"}`},
		{"DELETE", "/api/sites/" + edit + "/forms/abc", ""},
		{"POST", "/api/sites/" + edit + "/forms/abc/confirmation", ""},
	}
	creds := []struct {
		name string
		set  func(*http.Request)
	}{
		{"edit password", func(r *http.Request) { r.Header.Set("X-Edit-Password", c.EditPassword) }},
		{"account token", func(r *http.Request) { r.Header.Set("Authorization", "Bearer sbp_tok") }},
		{"owner session", func(r *http.Request) {
			r.AddCookie(&http.Cookie{Name: "sb_test", Value: "owner-cookie"})
			r.Header.Set("X-Sitebin-Session", "1")
			r.Header.Set("Sec-Fetch-Site", "same-origin")
		}},
	}
	for _, cr := range creds {
		for _, rt := range routes {
			var req *http.Request
			if rt.body != "" {
				req = httptest.NewRequest(rt.method, rt.path, strings.NewReader(rt.body))
				req.Header.Set("Content-Type", "application/json")
			} else {
				req = httptest.NewRequest(rt.method, rt.path, nil)
			}
			cr.set(req)
			w := e.public(t, req)
			if w.Code != 403 || !strings.Contains(w.Body.String(), "This site is locked by the operator: phishing") {
				t.Errorf("%s: %s %s = %d %s, want 403 with the lock", cr.name, rt.method, rt.path, w.Code, w.Body)
			}
		}
	}
	// A stranger is told nothing about the lock: the password is wrong first.
	req := authed(httptest.NewRequest("GET", "/api/sites/"+edit+"/download", nil), "wrong-password-000000")
	if w := e.public(t, req); w.Code != 401 {
		t.Errorf("wrong password on a locked site = %d, want 401", w.Code)
	}

	// Nothing changed underneath.
	site, err := e.st.ByViewID(c.ID)
	if err != nil {
		t.Fatalf("a locked site was deleted: %v", err)
	}
	if site.Meta.Name != "" {
		t.Error("a locked site was renamed")
	}
	if b, _ := e.st.ReadContentFile(site, "index.html"); string(b) != "phish" {
		t.Errorf("content changed: %q", b)
	}

	// WebDAV and FTP, for the password.
	if w := davReq(t, e, "GET", "/dav/"+edit+"/index.html", c.EditPassword, nil); w.Code != 403 {
		t.Errorf("WebDAV GET on a locked site = %d, want 403", w.Code)
	}
	if w := davReq(t, e, "PUT", "/dav/"+edit+"/index.html", c.EditPassword, strings.NewReader("clean")); w.Code != 403 {
		t.Errorf("WebDAV PUT on a locked site = %d, want 403", w.Code)
	}
	if _, _, _, err := e.api.FTPAuth(edit, c.EditPassword, "1.2.3.4"); err == nil || !strings.Contains(err.Error(), "locked") {
		t.Errorf("FTP login to a locked site = %v", err)
	}
}

// The settings read is how the owner learns of the lock: it answers, with the
// lock and without the file list.
func TestLockedSiteShowsTheLockToItsOwner(t *testing.T) {
	e := newEnv(t, nil)
	c := e.createSite(t, nil, map[string]string{"index.html": "x"})
	edit := editIDFrom(t, c.EditURL)

	var before map[string]any
	w := e.public(t, authed(httptest.NewRequest("GET", "/api/sites/"+edit, nil), c.EditPassword))
	json.Unmarshal(w.Body.Bytes(), &before)
	if v, present := before["locked"]; !present || v != nil {
		t.Errorf("an unlocked site's payload: locked = %v (present %v), want null", v, present)
	}

	lockSite(t, e, c.ID, "phishing")
	w = e.public(t, authed(httptest.NewRequest("GET", "/api/sites/"+edit, nil), c.EditPassword))
	if w.Code != 200 {
		t.Fatalf("GET a locked site = %d %s", w.Code, w.Body)
	}
	var got struct {
		Locked *struct {
			At     string `json:"at"`
			Reason string `json:"reason"`
			By     string `json:"by"`
		} `json:"locked"`
		Files []any `json:"files"`
	}
	json.Unmarshal(w.Body.Bytes(), &got)
	if got.Locked == nil || got.Locked.Reason != "phishing" || got.Locked.At == "" {
		t.Fatalf("locked = %+v", got.Locked)
	}
	if got.Locked.By != "" {
		t.Error("the payload says who locked the site; that is the operator's business")
	}
	if len(got.Files) != 0 {
		t.Errorf("a locked site's payload lists its files: %v", got.Files)
	}
}

func TestLockedSiteRefusesUploadTokens(t *testing.T) {
	e := newEnv(t, nil)
	c := e.createSite(t, nil, map[string]string{"index.html": "x"})
	edit := editIDFrom(t, c.EditURL)
	tok := uploadTokenFor(t, e, c)

	// Locked from another process (the CLI): the token is still in memory,
	// and the meta is what refuses it.
	site, _ := e.st.ByViewID(c.ID)
	if _, err := e.st.SetLock(site, &store.SiteLock{By: store.LockByAdmin}); err != nil {
		t.Fatal(err)
	}
	body, ct := uploadBody(t, nil, map[string]string{"index.html": "clean"})
	req := bearer(httptest.NewRequest("POST", "/api/sites/"+edit+"/files", body), tok)
	req.Header.Set("Content-Type", ct)
	if w := e.public(t, req); w.Code != 403 {
		t.Fatalf("upload with a token to a locked site = %d, want 403", w.Code)
	}
	put := bearer(httptest.NewRequest("PUT", "/dav/"+edit+"/index.html", strings.NewReader("clean")), tok)
	if w := e.public(t, put); w.Code != 403 {
		t.Fatalf("WebDAV with a token to a locked site = %d, want 403", w.Code)
	}

	// Locked through the seam, in this process: the token itself dies.
	e.st.SetLock(site, nil)
	tok = uploadTokenFor(t, e, c)
	lockSite(t, e, c.ID, "")
	unlockSite(t, e, c.ID)
	body, ct = uploadBody(t, nil, map[string]string{"index.html": "clean"})
	req = bearer(httptest.NewRequest("POST", "/api/sites/"+edit+"/files", body), tok)
	req.Header.Set("Content-Type", ct)
	if w := e.public(t, req); w.Code != 401 {
		t.Fatalf("a token issued before the lock, after the unlock = %d, want 401 (revoked)", w.Code)
	}
}

// ---- the seam ----

func TestSiteServiceLockSeam(t *testing.T) {
	e := newEnv(t, nil)
	c := e.createSite(t, nil, map[string]string{"index.html": "x"})
	svc := e.api.SiteService()
	lockSite(t, e, c.ID, " evidence ")

	info, ok := svc.Info(c.ID)
	if !ok || info.Locked == nil || info.Locked.Reason != "evidence" || info.Locked.By != ext.LockByAdmin || info.Locked.At.IsZero() {
		t.Fatalf("Info.Locked = %+v", info.Locked)
	}
	all, _ := svc.All()
	if len(all) != 1 || all[0].Locked == nil {
		t.Errorf("All does not carry the lock: %+v", all)
	}

	if err := svc.SetName(c.ID, "clean"); !errors.Is(err, ext.ErrSiteLocked) {
		t.Errorf("SetName on a locked site = %v, want ErrSiteLocked", err)
	}
	if _, err := svc.RotateEditPassword(c.ID); !errors.Is(err, ext.ErrSiteLocked) {
		t.Errorf("RotateEditPassword on a locked site = %v, want ErrSiteLocked", err)
	}
	if err := svc.Delete(c.ID); !errors.Is(err, ext.ErrSiteLocked) {
		t.Errorf("Delete on a locked site = %v, want ErrSiteLocked", err)
	}
	// A tier restamp leaves the hold as it found it.
	if err := svc.ApplyQuota(c.ID, ext.CreateGrant{MaxSiteBytes: 1, MaxExpiryDays: 1}); err != nil {
		t.Fatalf("ApplyQuota on a locked site = %v", err)
	}
	site, _ := e.st.ByViewID(c.ID)
	if site.Meta.QuotaBytes != 0 || site.Meta.ExpiresAt != nil || site.Meta.Name != "" {
		t.Errorf("a locked site changed: %+v", site.Meta)
	}
	// The old password still works after the refused rotation.
	if w := e.public(t, authed(httptest.NewRequest("GET", "/api/sites/"+editIDFrom(t, c.EditURL), nil), c.EditPassword)); w.Code != 200 {
		t.Errorf("GET with the original password = %d", w.Code)
	}

	// An account lock does not replace the operator's; ReleaseLock lifts only its own kind.
	if err := svc.SetLock(c.ID, &ext.SiteLock{Reason: "suspended", By: ext.LockByAccount}); err != nil {
		t.Fatal(err)
	}
	if info, _ := svc.Info(c.ID); info.Locked.By != ext.LockByAdmin {
		t.Errorf("account lock replaced the operator's: %+v", info.Locked)
	}
	if released, err := svc.ReleaseLock(c.ID, ext.LockByAccount); err != nil || released {
		t.Errorf("ReleaseLock(account) on an admin lock = %v, %v", released, err)
	}

	// The operator's takedown goes past the hold.
	if err := svc.ForceDelete(c.ID); err != nil {
		t.Fatalf("ForceDelete: %v", err)
	}
	if _, ok := svc.Info(c.ID); ok {
		t.Error("site survived ForceDelete")
	}
	if err := svc.SetLock(c.ID, &ext.SiteLock{}); !errors.Is(err, ext.ErrSiteGone) {
		t.Errorf("SetLock on a gone site = %v, want ErrSiteGone", err)
	}
	if _, err := svc.ReleaseLock(c.ID, ext.LockByAccount); !errors.Is(err, ext.ErrSiteGone) {
		t.Errorf("ReleaseLock on a gone site = %v, want ErrSiteGone", err)
	}
}

func TestSiteServiceUnlockedSiteUnchanged(t *testing.T) {
	e := newEnv(t, nil)
	c := e.createSite(t, nil, map[string]string{"index.html": "x"})
	svc := e.api.SiteService()
	if info, _ := svc.Info(c.ID); info.Locked != nil {
		t.Fatalf("an unlocked site reports a lock: %+v", info.Locked)
	}
	if err := svc.SetName(c.ID, "fine"); err != nil {
		t.Fatalf("SetName: %v", err)
	}
	if err := svc.Delete(c.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
}

// ---- MCP: the same rules through the adapter ----

func TestMCPLockedSiteRefusesEveryToolButGetSite(t *testing.T) {
	e := newEnv(t, nil)
	cs := mcpClient(t, e, nil)
	editID, pw := mcpCreate(t, cs, "<h1>phish</h1>")
	site, _ := e.st.ByEditID(editID)
	lockSite(t, e, site.ViewID, "phishing")

	ref := func(extra map[string]any) map[string]any {
		args := map[string]any{"edit_id": editID, "edit_password": pw}
		for k, v := range extra {
			args[k] = v
		}
		return args
	}
	calls := []struct {
		tool string
		args map[string]any
	}{
		{"update_site", ref(map[string]any{"settings": map[string]any{"name": "clean"}})},
		{"list_files", ref(nil)},
		{"read_file", ref(map[string]any{"path": "index.html"})},
		{"write_files", ref(map[string]any{"files": []any{map[string]any{"path": "index.html", "text": "clean"}}})},
		{"delete_file", ref(map[string]any{"path": "index.html"})},
		{"delete_site", ref(nil)},
		{"add_domain", ref(map[string]any{"domain": "x.example.org"})},
		{"remove_domain", ref(map[string]any{"domain": "x.example.org"})},
		{"download_site", ref(nil)},
		{"open_upload", ref(nil)},
		{"list_forms", ref(nil)},
		{"add_form", ref(map[string]any{"form": map[string]any{"name": "x", "recipient": "a@example.com"}})},
		{"update_form", ref(map[string]any{"key": "abc", "form": map[string]any{"name": "y"}})},
		{"remove_form", ref(map[string]any{"key": "abc"})},
		{"resend_form_confirmation", ref(map[string]any{"key": "abc"})},
	}
	for _, c := range calls {
		res, err := cs.CallTool(context.Background(), &sdk.CallToolParams{Name: c.tool, Arguments: c.args})
		if err != nil {
			// A schema refusal would not prove the lock; the call must reach the tool.
			t.Errorf("%s: %v", c.tool, err)
			continue
		}
		if !res.IsError || !strings.Contains(mcpText(res), "This site is locked by the operator: phishing") {
			t.Errorf("%s on a locked site: error=%v %s", c.tool, res.IsError, mcpText(res))
		}
	}
	if b, _ := e.st.ReadContentFile(site, "index.html"); string(b) != "<h1>phish</h1>" {
		t.Errorf("content changed through MCP: %q", b)
	}
	if _, err := e.st.ByEditID(editID); err != nil {
		t.Fatalf("a locked site was deleted through MCP: %v", err)
	}

	// get_site answers, with the lock and without the files.
	res := mcpCall(t, cs, "get_site", ref(nil))
	if res.IsError {
		t.Fatalf("get_site on a locked site: %s", mcpText(res))
	}
	m := res.StructuredContent.(map[string]any)
	lock, _ := m["locked"].(map[string]any)
	if lock == nil || lock["reason"] != "phishing" || lock["at"] == nil {
		t.Fatalf("get_site locked = %v", m["locked"])
	}
	if _, has := lock["by"]; has {
		t.Error("get_site says who locked the site")
	}
	if files, _ := m["files"].([]any); len(files) != 0 {
		t.Errorf("get_site lists a locked site's files: %v", files)
	}
}

// An unlocked site's result carries no "locked" key at all, so a client that
// cached the output schema before the field existed keeps working.
func TestMCPUnlockedResultHasNoLockedKey(t *testing.T) {
	e := newEnv(t, nil)
	cs := mcpClient(t, e, nil)
	editID, pw := mcpCreate(t, cs, "x")
	res := mcpCall(t, cs, "get_site", map[string]any{"edit_id": editID, "edit_password": pw})
	if _, has := res.StructuredContent.(map[string]any)["locked"]; has {
		t.Fatalf("an unlocked site's result carries locked: %s", mcpText(res))
	}
}

func TestMCPListSitesCarriesTheLock(t *testing.T) {
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
	lockedID, _ := mcpCreate(t, cs, "a")
	openID, _ := mcpCreate(t, cs, "b")
	locked, _ := e.st.ByEditID(lockedID)
	open, _ := e.st.ByEditID(openID)
	p.owned["acct-1"] = []string{locked.ViewID, open.ViewID}
	lockSite(t, e, locked.ViewID, "")

	res := mcpCall(t, cs, "list_sites", map[string]any{})
	if res.IsError {
		t.Fatalf("list_sites: %s", mcpText(res))
	}
	raw, _ := json.Marshal(res.StructuredContent)
	var out struct {
		Sites []map[string]any `json:"sites"`
	}
	json.Unmarshal(raw, &out)
	seen := 0
	for _, s := range out.Sites {
		_, has := s["locked"]
		switch s["edit_id"] {
		case lockedID:
			seen++
			if !has {
				t.Errorf("the locked site's row has no lock: %v", s)
			}
		case openID:
			seen++
			if has {
				t.Errorf("the open site's row has a lock: %v", s)
			}
		}
	}
	if seen != 2 {
		t.Fatalf("list_sites rows: %s", raw)
	}
}
