package httpapi

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
)

type dirResp struct {
	Path    string `json:"path"`
	Entries []struct {
		Name string `json:"name"`
		Dir  bool   `json:"dir"`
		Size int64  `json:"size"`
	} `json:"entries"`
	Truncated bool `json:"truncated"`
}

func getDir(t *testing.T, e *env, edit, pw, path string) (int, dirResp) {
	t.Helper()
	req := httptest.NewRequest("GET", "/api/sites/"+edit+"/dir?path="+path, nil)
	if pw != "" {
		authed(req, pw)
	}
	w := e.public(t, req)
	var out dirResp
	json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

func TestDirListsOneFolderAtATime(t *testing.T) {
	e := newEnv(t, nil)
	c := e.createSite(t, nil, map[string]string{"index.html": "x", "assets/app.js": "js", "assets/img/logo.png": "png"})
	edit := editIDFrom(t, c.EditURL)

	code, d := getDir(t, e, edit, c.EditPassword, "")
	if code != 200 || d.Path != "" || len(d.Entries) != 2 ||
		d.Entries[0].Name != "assets" || !d.Entries[0].Dir ||
		d.Entries[1].Name != "index.html" || d.Entries[1].Dir || d.Entries[1].Size != 1 {
		t.Fatalf("root: %d %+v", code, d)
	}
	code, d = getDir(t, e, edit, c.EditPassword, "assets/")
	if code != 200 || d.Path != "assets" || len(d.Entries) != 2 || d.Entries[0].Name != "img" || d.Entries[1].Name != "app.js" {
		t.Fatalf("assets: %d %+v", code, d)
	}
}

func TestDirRefusesBadMissingAndUnauthenticated(t *testing.T) {
	e := newEnv(t, nil)
	c := e.createSite(t, nil, map[string]string{"index.html": "x"})
	edit := editIDFrom(t, c.EditURL)
	if code, _ := getDir(t, e, edit, c.EditPassword, "../x"); code != 400 {
		t.Errorf("traversal: %d, want 400", code)
	}
	if code, _ := getDir(t, e, edit, c.EditPassword, "nope"); code != 404 {
		t.Errorf("missing folder: %d, want 404", code)
	}
	if code, _ := getDir(t, e, edit, "", ""); code != 401 {
		t.Errorf("no credential: %d, want 401", code)
	}
	tok := uploadTokenFor(t, e, c)
	req := bearer(httptest.NewRequest("GET", "/api/sites/"+edit+"/dir", nil), tok)
	if w := e.public(t, req); w.Code != 403 {
		t.Errorf("upload token: %d, want 403", w.Code)
	}
}
