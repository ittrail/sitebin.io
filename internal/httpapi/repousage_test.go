package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/ittrail/sitebin.io/internal/store"
)

// repoSite gives the site (by edit id) a repository of n bytes and a byte cap.
func repoSite(t *testing.T, e *env, edit string, n int, max int64) *store.Site {
	t.Helper()
	site, err := e.st.ByEditID(edit)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(site.GitDir(), "objects", "pack", "pack-1.pack")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, bytes.Repeat([]byte("g"), n), 0o644); err != nil {
		t.Fatal(err)
	}
	if max > 0 {
		if err := e.st.Update(site, func(m *store.Meta) error { m.QuotaBytes = max; return nil }); err != nil {
			t.Fatal(err)
		}
	}
	return site
}

func TestWebDAVPutCountsRepository(t *testing.T) {
	e := newEnv(t, nil)
	c := e.createSite(t, map[string]string{"webdav": "true"}, map[string]string{"index.html": "x"})
	edit := editIDFrom(t, c.EditURL)
	// 1 file byte + 100 repository bytes under a 120-byte cap: 19 left.
	repoSite(t, e, edit, 100, 120)
	base := "/dav/" + edit + "/"
	if w := davReq(t, e, "PUT", base+"big.txt", c.EditPassword, strings.NewReader(strings.Repeat("a", 30))); w.Code != http.StatusInsufficientStorage {
		t.Fatalf("PUT beside the repository over the cap: %d %s", w.Code, w.Body)
	}
	if w := davReq(t, e, "PUT", base+"ok.txt", c.EditPassword, strings.NewReader(strings.Repeat("a", 10))); w.Code >= 300 {
		t.Fatalf("PUT within the cap: %d %s", w.Code, w.Body)
	}
}

func TestFTPAuthReportsRepository(t *testing.T) {
	e := newEnv(t, map[string]string{"SITEBIN_FTP_ENABLED": "true"})
	c := e.createSite(t, map[string]string{"ftp": "true"}, map[string]string{"index.html": "x"})
	edit := editIDFrom(t, c.EditURL)
	site := repoSite(t, e, edit, 100, 120)
	sess, err := e.api.FTPAuth(edit, c.EditPassword, "1.2.3.4")
	if err != nil {
		t.Fatal(err)
	}
	if sess.UsedOutside == nil || sess.UsedOutside() != 100 {
		t.Fatalf("UsedOutside = %v, want the repository's 100 bytes", sess.UsedOutside != nil)
	}
	// Read at every check, not at login.
	p := filepath.Join(site.GitDir(), "objects", "pack", "pack-2.pack")
	os.WriteFile(p, bytes.Repeat([]byte("g"), 7), 0o644)
	if got := sess.UsedOutside(); got != 107 {
		t.Fatalf("UsedOutside after a push = %d, want 107", got)
	}
}

func TestGetSiteBytesIncludeRepository(t *testing.T) {
	e := newEnv(t, nil)
	c := e.createSite(t, nil, map[string]string{"index.html": strings.Repeat("f", 40)})
	edit := editIDFrom(t, c.EditURL)
	repoSite(t, e, edit, 60, 0)
	w := e.public(t, authed(httptest.NewRequest("GET", "/api/sites/"+edit, nil), c.EditPassword))
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	var got struct {
		Usage struct {
			Bytes    int64 `json:"bytes"`
			GitBytes int64 `json:"git_bytes"`
			Files    int   `json:"files"`
		} `json:"usage"`
	}
	json.Unmarshal(w.Body.Bytes(), &got)
	if got.Usage.Bytes != 100 || got.Usage.GitBytes != 60 || got.Usage.Files != 1 {
		t.Fatalf("usage = %+v, want bytes 100 (files 40 + repository 60), git_bytes 60, 1 file", got.Usage)
	}
}

func TestMCPGetSiteBytesIncludeRepository(t *testing.T) {
	e := newEnv(t, nil)
	cs := mcpClient(t, e, nil)
	editID, pw := mcpCreate(t, cs, strings.Repeat("f", 40))
	repoSite(t, e, editID, 60, 0)
	res := mcpCall(t, cs, "get_site", map[string]any{"edit_id": editID, "edit_password": pw})
	if res.IsError {
		t.Fatal(mcpText(res))
	}
	m := res.StructuredContent.(map[string]any)
	if m["bytes"] != float64(100) {
		t.Fatalf("bytes = %v, want 100 (files + repository)", m["bytes"])
	}
	var keys []string
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	// The pre-change key set: nothing new, git_bytes least of all.
	want := "bytes,edit_id,edit_url,entry_file,file_count,files,ftp_enabled,id,max_bytes,max_files,mode,spa_fallback,view_password_protected,view_url,webdav_enabled"
	if got := strings.Join(keys, ","); got != want {
		t.Fatalf("get_site keys changed: got %s, want %s", got, want)
	}
}
