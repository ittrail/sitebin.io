package httpapi

import (
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// entry_file "" is how the owner makes the file list the viewer's start page:
// accepted by the API (it used to be a 400), stored as "", and written into
// the wrapper as the list.
func TestEntryFileEmptyMeansTheList(t *testing.T) {
	e := newEnv(t, nil)
	c := e.createSite(t, nil, map[string]string{"report.pdf": "%PDF"})
	edit := editIDFrom(t, c.EditURL)
	put := func(body string) *httptest.ResponseRecorder {
		req := authed(httptest.NewRequest("PUT", "/api/sites/"+edit, strings.NewReader(body)), c.EditPassword)
		req.Header.Set("Content-Type", "application/json")
		return e.public(t, req)
	}

	if w := put(`{"mode":"viewer","entry_file":""}`); w.Code != 200 {
		t.Fatalf("PUT entry_file \"\": %d %s", w.Code, w.Body)
	}
	site, _ := e.st.ByViewID(c.ID)
	if site.Meta.EntryFile != "" {
		t.Errorf("stored entry = %q, want the list", site.Meta.EntryFile)
	}
	b, err := os.ReadFile(site.FilesDir() + "/index.html")
	if err != nil || !strings.Contains(string(b), `"entry":""`) {
		t.Errorf("wrapper does not open on the list: %v %s", err, b)
	}

	// Choosing a file again still works, and a bad path is still refused.
	if w := put(`{"entry_file":"report.pdf"}`); w.Code != 200 {
		t.Fatalf("PUT entry_file report.pdf: %d %s", w.Code, w.Body)
	}
	if site, _ = e.st.ByViewID(c.ID); site.Meta.EntryFile != "report.pdf" {
		t.Errorf("stored entry = %q", site.Meta.EntryFile)
	}
	if w := put(`{"entry_file":"../x"}`); w.Code != 400 {
		t.Errorf("bad entry path: %d, want 400", w.Code)
	}
}

// An agent may create an empty viewer site and send its one file later. The
// empty site must not save "" (the owner's list) on its own: its first lone
// file opens.
func TestEmptyViewerSiteOpensItsFirstLoneFile(t *testing.T) {
	e := newEnv(t, nil)
	c := e.createSite(t, map[string]string{"mode": "viewer"}, nil)
	edit := editIDFrom(t, c.EditURL)

	if w := e.public(t, authed(uploadReq(t, edit, "setup.exe", "MZ"), c.EditPassword)); w.Code != 200 {
		t.Fatalf("upload: %d %s", w.Code, w.Body)
	}
	site, _ := e.st.ByViewID(c.ID)
	if site.Meta.EntryFile != "setup.exe" {
		t.Errorf("entry after the first upload = %q, want the lone file", site.Meta.EntryFile)
	}
	b, err := os.ReadFile(site.FilesDir() + "/index.html")
	if err != nil || !strings.Contains(string(b), `"entry":"setup.exe"`) {
		t.Errorf("wrapper does not open the lone file: %v", err)
	}
}

// MCP reaches the same rule: update_site with entry_file "" opens the list.
func TestMCPEntryFileEmptyMeansTheList(t *testing.T) {
	e := newEnv(t, nil)
	cs := mcpClient(t, e, nil)
	editID, pw := mcpCreate(t, cs, "hi")
	res := mcpCall(t, cs, "update_site", map[string]any{
		"edit_id": editID, "edit_password": pw,
		"settings": map[string]any{"mode": "viewer", "entry_file": ""},
	})
	if res.IsError {
		t.Fatalf("update_site: %s", mcpText(res))
	}
	site, _ := e.st.ByEditID(editID)
	if site.Meta.EntryFile != "" {
		t.Errorf("stored entry = %q, want the list", site.Meta.EntryFile)
	}
	b, err := os.ReadFile(site.FilesDir() + "/index.html")
	if err != nil || !strings.Contains(string(b), `"entry":""`) {
		t.Errorf("wrapper does not open on the list: %v", err)
	}
}

// A form create may ask for the list too: entry_file present but empty, as
// a PUT may. Without it a one-file viewer site opens that file.
func TestCreateFormEntryFileEmptyMeansTheList(t *testing.T) {
	e := newEnv(t, nil)
	c := e.createSite(t, map[string]string{"mode": "viewer", "entry_file": ""}, map[string]string{"report.pdf": "%PDF"})
	site, _ := e.st.ByViewID(c.ID)
	if site.Meta.EntryFile != "" {
		t.Errorf("stored entry = %q, want the list asked for", site.Meta.EntryFile)
	}
	c = e.createSite(t, map[string]string{"mode": "viewer"}, map[string]string{"report.pdf": "%PDF"})
	if site, _ = e.st.ByViewID(c.ID); site.Meta.EntryFile != "report.pdf" {
		t.Errorf("without entry_file the lone file should open, got %q", site.Meta.EntryFile)
	}
}
