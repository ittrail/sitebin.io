package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ittrail/sitebin.io/internal/ext"
	"github.com/ittrail/sitebin.io/internal/store"
)

// postFiles uploads files to the site with its edit password.
func postFiles(t *testing.T, e *env, edit, pw string, files map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	body, ct := filesBody(t, nil, files, nil)
	req := httptest.NewRequest("POST", "/api/sites/"+edit+"/files", body)
	req.Header.Set("Content-Type", ct)
	return e.public(t, authed(req, pw))
}

func TestCreateStampsTheAccountCap(t *testing.T) {
	e := newEnv(t, nil)
	ext.Register(&fakeProvider{enabled: true, owner: "acct-1", grant: ext.CreateGrant{MaxAccountBytes: 5000}})
	defer ext.Reset()
	c := e.createSite(t, nil, map[string]string{"index.html": "hi"})
	site, _ := e.st.ByViewID(c.ID)
	if site.Meta.QuotaAccountBytes != 5000 {
		t.Fatalf("QuotaAccountBytes = %d, want 5000 from the grant", site.Meta.QuotaAccountBytes)
	}
}

func TestSiteServiceApplyQuotaCarriesTheAccountCap(t *testing.T) {
	e := newEnv(t, nil)
	c := e.createSite(t, nil, map[string]string{"index.html": "hi"})
	if err := (siteService{a: e.api}).ApplyQuota(c.ID, ext.CreateGrant{MaxAccountBytes: 7000}); err != nil {
		t.Fatal(err)
	}
	site, _ := e.st.ByViewID(c.ID)
	if site.Meta.QuotaAccountBytes != 7000 {
		t.Fatalf("QuotaAccountBytes = %d, want 7000 after a tier change", site.Meta.QuotaAccountBytes)
	}
}

// ownAs makes the site (by edit id) one of owner's, stamped with the account
// cap, and returns it.
func ownAs(t *testing.T, e *env, edit, owner string, accountCap int64) *store.Site {
	t.Helper()
	site, err := e.st.ByEditID(edit)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.st.Update(site, func(m *store.Meta) error {
		m.OwnerAccountID = owner
		m.QuotaAccountBytes = accountCap
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.SiteUsage(site); err != nil { // noted, as any measure does
		t.Fatal(err)
	}
	return site
}

// twoSitesOneAccount creates two sites of one account under a 200-byte
// account cap: the other one holds 150 bytes, so 50 are left for the first,
// which holds 1 byte ("x"). It returns the first's create response.
func twoSitesOneAccount(t *testing.T, e *env, fields map[string]string) createResp {
	t.Helper()
	c := e.createSite(t, fields, map[string]string{"index.html": "x"})
	other := e.createSite(t, nil, map[string]string{"big.bin": strings.Repeat("o", 150)})
	ownAs(t, e, editIDFrom(t, other.EditURL), "acct-1", 200)
	ownAs(t, e, editIDFrom(t, c.EditURL), "acct-1", 200)
	return c
}

func TestAPIUploadCountsTheAccount(t *testing.T) {
	e := newEnv(t, nil)
	c := twoSitesOneAccount(t, e, nil)
	edit := editIDFrom(t, c.EditURL)
	w := postFiles(t, e, edit, c.EditPassword, map[string]string{"more.txt": strings.Repeat("a", 60)})
	if w.Code != http.StatusRequestEntityTooLarge || !strings.Contains(w.Body.String(), "account storage") {
		t.Fatalf("upload past the account's room: %d %s", w.Code, w.Body)
	}
	if w := postFiles(t, e, edit, c.EditPassword, map[string]string{"ok.txt": strings.Repeat("a", 40)}); w.Code >= 300 {
		t.Fatalf("upload within the account's room: %d %s", w.Code, w.Body)
	}
}

func TestWebDAVPutCountsTheAccount(t *testing.T) {
	e := newEnv(t, nil)
	c := twoSitesOneAccount(t, e, map[string]string{"webdav": "true"})
	edit := editIDFrom(t, c.EditURL)
	base := "/dav/" + edit + "/"
	w := davReq(t, e, "PUT", base+"big.txt", c.EditPassword, strings.NewReader(strings.Repeat("a", 60)))
	if w.Code != http.StatusInsufficientStorage || !strings.Contains(w.Body.String(), "account storage") {
		t.Fatalf("PUT past the account's room: %d %s", w.Code, w.Body)
	}
	if w := davReq(t, e, "PUT", base+"ok.txt", c.EditPassword, strings.NewReader(strings.Repeat("a", 40))); w.Code >= 300 {
		t.Fatalf("PUT within the account's room: %d %s", w.Code, w.Body)
	}
}

func TestFTPCountsTheAccount(t *testing.T) {
	e := newEnv(t, map[string]string{"SITEBIN_FTP_ENABLED": "true"})
	c := twoSitesOneAccount(t, e, map[string]string{"ftp": "true"})
	edit := editIDFrom(t, c.EditURL)
	sess, err := e.api.FTPAuth(edit, c.EditPassword, "1.2.3.4")
	if err != nil {
		t.Fatal(err)
	}
	// The session's arithmetic is files + UsedOutside <= MaxBytes; the account
	// leaves 50 of the site's cap, so UsedOutside carries the difference.
	if sess.UsedOutside == nil || sess.MaxBytes-sess.UsedOutside() != 50 {
		t.Fatalf("MaxBytes %d - UsedOutside = %d, want the account's 50 left", sess.MaxBytes, sess.MaxBytes-sess.UsedOutside())
	}
}

func TestGitAllowanceCountsTheAccount(t *testing.T) {
	e := newEnv(t, nil)
	c := twoSitesOneAccount(t, e, nil)
	site, _ := e.st.ByEditID(editIDFrom(t, c.EditURL))
	allowance, refusal, err := e.api.gitAllowance(site)
	if err != nil || refusal != "" || allowance != 49 {
		t.Fatalf("allowance %d, refusal %q, err %v; want the account's 49 left", allowance, refusal, err)
	}
	other := e.createSite(t, nil, map[string]string{"fill.bin": strings.Repeat("f", 49)})
	ownAs(t, e, editIDFrom(t, other.EditURL), "acct-1", 200)
	_, refusal, _ = e.api.gitAllowance(site)
	if !strings.Contains(refusal, "your sites hold 200 B of the 200 B") {
		t.Fatalf("refusal %q, want the account's figures", refusal)
	}
}

func TestStoreErrorNamesTheAccount(t *testing.T) {
	w := httptest.NewRecorder()
	storeError(w, store.ErrAccountFull)
	if w.Code != 413 || !strings.Contains(w.Body.String(), "account storage limit exceeded") {
		t.Fatalf("ErrAccountFull: %d %s", w.Code, w.Body)
	}
	if err := (mcpOps{}).mcpError(store.ErrAccountFull); err == nil || !strings.Contains(err.Error(), "account's storage is full") {
		t.Fatalf("MCP: %v", err)
	}
}

// The runtime reads the account's figures from the site service, so the stop
// rule needs no store of its own.
func TestContainerSiteCarriesTheAccount(t *testing.T) {
	e := newEnv(t, nil)
	c := twoSitesOneAccount(t, e, nil)
	cs, err := (siteService{a: e.api}).ContainerSite(c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cs.AccountCap != 200 || cs.AccountOthers != 150 {
		t.Fatalf("AccountCap %d, AccountOthers %d; want 200 and 150", cs.AccountCap, cs.AccountOthers)
	}
}

// The owner is told the account's storage on the settings read; an admin or a
// holder of the edit password learns nothing about the owner's other sites.
func TestSettingsReadTellsOnlyTheOwnerTheAccount(t *testing.T) {
	e, _ := formsEnv(t, nil)
	f := registerSharing(t, ext.CreateGrant{Trusted: true, MaxAccountBytes: 5000})
	edit, pw, _ := sharedSite(t, e, f)
	other := e.createSite(t, nil, map[string]string{"big.bin": strings.Repeat("o", 300)})
	ownAs(t, e, editIDFrom(t, other.EditURL), "acct-1", 5000)

	usage := func(as func(*http.Request) *http.Request) map[string]any {
		t.Helper()
		w := e.public(t, as(bodyReq("GET", "/api/sites/"+edit, nil)))
		if w.Code != 200 {
			t.Fatalf("get = %d %s", w.Code, w.Body)
		}
		return payloadOf(t, w)["usage"].(map[string]any)
	}
	u := usage(func(r *http.Request) *http.Request { return bearer(r, "sbp_owner") })
	if u["account_max_bytes"] != float64(5000) || u["account_bytes"] != float64(300+len("<h1>shared</h1>")) {
		t.Fatalf("owner's usage = %v, want the account's 5000 cap and both sites' bytes", u)
	}
	for name, as := range map[string]func(*http.Request) *http.Request{
		"admin":    func(r *http.Request) *http.Request { return bearer(r, "sbp_admin") },
		"password": func(r *http.Request) *http.Request { return authed(r, pw) },
	} {
		if u := usage(as); u["account_max_bytes"] != nil || u["account_bytes"] != nil {
			t.Errorf("%s was told the owner's account: %v", name, u)
		}
	}
}
