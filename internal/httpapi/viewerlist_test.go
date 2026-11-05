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
