package httpapi

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ittrail/sitebin.io/internal/gitrepo"
	"github.com/ittrail/sitebin.io/internal/provenance"
	"github.com/ittrail/sitebin.io/internal/store"
)

// ---- the JSON read routes of a site's repository, and its deletion ----

type gitInfoResp struct {
	Available     bool    `json:"available"`
	Writable      bool    `json:"writable"`
	Reason        string  `json:"reason"`
	URL           string  `json:"url"`
	Exists        bool    `json:"exists"`
	Bytes         int64   `json:"bytes"`
	DefaultBranch string  `json:"default_branch"`
	ExpiresAt     *string `json:"expires_at"`
	LastPush      *struct {
		At     time.Time `json:"at"`
		Detail string    `json:"detail"`
		Auth   string    `json:"auth"`
	} `json:"last_push"`
	Credentials struct {
		Password bool `json:"password"`
		Token    bool `json:"token"`
		OAuth    any  `json:"oauth"`
	} `json:"credentials"`
}

func gitGet(t *testing.T, e *env, edit, pw, path string) *httptest.ResponseRecorder {
	t.Helper()
	return e.call(t, "GET", "/api/sites/"+edit+"/git"+path, pw, nil)
}

func gitInfo(t *testing.T, e *env, edit, pw string) gitInfoResp {
	t.Helper()
	w := gitGet(t, e, edit, pw, "")
	if w.Code != 200 {
		t.Fatalf("GET git = %d %s", w.Code, w.Body)
	}
	var out gitInfoResp
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestGitInfoNoRepo(t *testing.T) {
	e := newEnv(t, nil)
	edit, pw := gitSite(t, e, nil)
	in := gitInfo(t, e, edit, pw)
	if !in.Available || !in.Writable || in.Reason != "" || in.Exists || in.Bytes != 0 || in.LastPush != nil || in.ExpiresAt != nil {
		t.Errorf("info = %+v", in)
	}
	if want := "http://sitebin.example/git/" + edit + ".git"; in.URL != want {
		t.Errorf("url = %q, want %q", in.URL, want)
	}
	if !in.Credentials.Password || in.Credentials.Token || in.Credentials.OAuth != nil {
		t.Errorf("credentials = %+v", in.Credentials)
	}
	for _, p := range []string{"/refs", "/log", "/tree", "/blob?path=a", "/archive", "/commit/" + strings.Repeat("a", 40)} {
		if w := gitGet(t, e, edit, pw, p); w.Code != 404 {
			t.Errorf("GET git%s without a repository = %d, want 404", p, w.Code)
		}
	}
}

func TestGitInfoAfterPush(t *testing.T) {
	e := newEnv(t, nil)
	g := newGitServer(t, e)
	edit, pw := gitSite(t, e, nil)
	cl := newGitClient(t)
	wd := cl.work()
	cl.mustGit(wd, "checkout", "-q", "-b", "trunk")
	cl.commit(wd, "a.txt", []byte("a"))
	before := time.Now().Add(-2 * time.Second)
	cl.mustGit(wd, "push", "-q", g.remote(edit, pw), "HEAD:trunk")

	in := gitInfo(t, e, edit, pw)
	if !in.Exists || in.Bytes <= 0 || in.DefaultBranch == "" {
		t.Errorf("info = %+v", in)
	}
	if in.LastPush == nil || in.LastPush.At.Before(before) {
		t.Fatalf("last_push = %+v", in.LastPush)
	}
	if in.LastPush.Auth != provenance.AuthPassword || !strings.Contains(in.LastPush.Detail, "refs/heads/trunk") {
		t.Errorf("last_push = %+v, want the push's auth and detail", in.LastPush)
	}
}

func TestGitInfoReasonPlan(t *testing.T) {
	e := newEnv(t, nil)
	g := newGitServer(t, e)
	edit, pw := gitSite(t, e, nil)
	cl := newGitClient(t)
	wd := cl.work()
	pushed(t, cl, wd, g.remote(edit, pw), "a.txt", []byte("a"))
	no := false
	if err := e.st.SetQuotaGit(siteByEdit(t, e, edit), &no); err != nil {
		t.Fatal(err)
	}
	in := gitInfo(t, e, edit, pw)
	if !in.Available || in.Writable || in.Reason != gitReasonPlan || !in.Exists {
		t.Errorf("downgraded with a repository: %+v", in)
	}
	if w := gitGet(t, e, edit, pw, "/refs"); w.Code != 200 {
		t.Errorf("refs on a read-only repository = %d", w.Code)
	}

	edit2, pw2 := gitSite(t, e, nil)
	if err := e.st.SetQuotaGit(siteByEdit(t, e, edit2), &no); err != nil {
		t.Fatal(err)
	}
	in = gitInfo(t, e, edit2, pw2)
	if in.Available || in.Writable || in.Reason != gitReasonPlan || in.Exists {
		t.Errorf("no Git, no repository: %+v", in)
	}
}

func TestGitInfoUnavailable(t *testing.T) {
	e := newEnv(t, nil)
	edit, pw := gitSite(t, e, nil)
	e.api.git = nil
	in := gitInfo(t, e, edit, pw)
	if in.Available || in.Writable || in.Reason != gitReasonUnavailable {
		t.Errorf("info = %+v", in)
	}
	if w := gitGet(t, e, edit, pw, "/refs"); w.Code != 404 {
		t.Errorf("refs without a git binary = %d, want 404", w.Code)
	}
}

func TestGitInfoExpiresAt(t *testing.T) {
	e := newEnv(t, nil)
	edit, pw := gitSite(t, e, nil)
	at := time.Now().Add(48 * time.Hour).UTC().Truncate(time.Second)
	if err := e.st.Update(siteByEdit(t, e, edit), func(m *store.Meta) error { m.ExpiresAt = &at; return nil }); err != nil {
		t.Fatal(err)
	}
	in := gitInfo(t, e, edit, pw)
	if in.ExpiresAt == nil {
		t.Fatal("expires_at is null")
	}
	if got, err := time.Parse(time.RFC3339, *in.ExpiresAt); err != nil || !got.Equal(at) {
		t.Errorf("expires_at = %q, want %v", *in.ExpiresAt, at)
	}
}

func TestGitLogPagingStable(t *testing.T) {
	e := newEnv(t, nil)
	g := newGitServer(t, e)
	edit, pw := gitSite(t, e, nil)
	cl := newGitClient(t)
	wd := cl.work()
	remote := g.remote(edit, pw)
	for i := 0; i < 35; i++ {
		cl.commit(wd, "f"+string(rune('a'+i%26))+string(rune('a'+i/26))+".txt", []byte{byte(i)})
	}
	cl.mustGit(wd, "push", "-q", remote, "HEAD:main")

	type page struct {
		SHA      string `json:"sha"`
		Commits  []any  `json:"commits"`
		NextSkip *int   `json:"next_skip"`
	}
	read := func(path string) page {
		w := gitGet(t, e, edit, pw, path)
		if w.Code != 200 {
			t.Fatalf("GET git%s = %d %s", path, w.Code, w.Body)
		}
		var p page
		if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil {
			t.Fatal(err)
		}
		return p
	}
	p1 := read("/log")
	if len(p1.Commits) != gitrepo.LogPage || p1.NextSkip == nil || *p1.NextSkip != 30 || len(p1.SHA) != 40 {
		t.Fatalf("page 1 = %d commits, next %v, sha %q", len(p1.Commits), p1.NextSkip, p1.SHA)
	}
	cl.commit(wd, "late.txt", []byte("late"))
	cl.mustGit(wd, "push", "-q", remote, "HEAD:main")
	p2 := read("/log?ref=" + p1.SHA + "&skip=30")
	if len(p2.Commits) != 5 || p2.NextSkip != nil || p2.SHA != p1.SHA {
		t.Errorf("page 2 = %d commits, next %v, sha %q (want 5, null, %s)", len(p2.Commits), p2.NextSkip, p2.SHA, p1.SHA)
	}
	if w := gitGet(t, e, edit, pw, "/log?ref=nope"); w.Code != 404 || !strings.Contains(w.Body.String(), `"not found"`) {
		t.Errorf("bad ref = %d %s", w.Code, w.Body)
	}
}

func TestGitTreeAndBlob(t *testing.T) {
	e := newEnv(t, nil)
	g := newGitServer(t, e)
	edit, pw := gitSite(t, e, nil)
	cl := newGitClient(t)
	wd := cl.work()
	sha := cl.commit(wd, "a.txt", []byte("hello"))
	cl.mustGit(wd, "push", "-q", g.remote(edit, pw), "HEAD:main")

	w := gitGet(t, e, edit, pw, "/tree?ref=main")
	if w.Code != 200 {
		t.Fatalf("tree = %d %s", w.Code, w.Body)
	}
	var tr struct {
		SHA     string `json:"sha"`
		Entries []struct {
			Name string `json:"name"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &tr); err != nil {
		t.Fatal(err)
	}
	if tr.SHA != sha || len(tr.Entries) != 1 || tr.Entries[0].Name != "a.txt" {
		t.Errorf("tree = %+v, want a.txt at %s", tr, sha)
	}
	w = gitGet(t, e, edit, pw, "/blob?ref="+sha+"&path=a.txt")
	var b struct {
		Size int64  `json:"size"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &b); err != nil || w.Code != 200 || b.Text != "hello" || b.Size != 5 {
		t.Errorf("blob = %d %s", w.Code, w.Body)
	}
	if w = gitGet(t, e, edit, pw, "/blob?ref="+sha+"&path=missing.txt"); w.Code != 404 {
		t.Errorf("missing blob = %d, want 404", w.Code)
	}
	if w = gitGet(t, e, edit, pw, "/blob?ref="+sha+"&path=../x"); w.Code != 404 {
		t.Errorf("bad path = %d, want 404", w.Code)
	}
	w = gitGet(t, e, edit, pw, "/commit/"+sha)
	var c struct {
		SHA   string `json:"sha"`
		Files []struct {
			Path string `json:"path"`
		} `json:"files"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &c); err != nil || w.Code != 200 || c.SHA != sha || len(c.Files) != 1 {
		t.Errorf("commit = %d %s", w.Code, w.Body)
	}
	if w = gitGet(t, e, edit, pw, "/commit/not-a-sha"); w.Code != 404 {
		t.Errorf("commit with a bad sha = %d, want 404", w.Code)
	}
	w = gitGet(t, e, edit, pw, "/refs")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"refs"`) || !strings.Contains(w.Body.String(), `"main"`) {
		t.Errorf("refs = %d %s", w.Code, w.Body)
	}
}

func TestGitDownloadsAreAttachments(t *testing.T) {
	e := newEnv(t, nil)
	g := newGitServer(t, e)
	edit, pw := gitSite(t, e, nil)
	cl := newGitClient(t)
	wd := cl.work()
	sha := cl.commit(wd, "a.txt", []byte("<script>alert(1)</script>"))
	cl.mustGit(wd, "push", "-q", g.remote(edit, pw), "HEAD:main")

	for _, c := range []struct{ path, name string }{
		{"/blob?ref=main&path=a.txt&download=1", "a.txt"},
		{"/archive?ref=main", siteByEdit(t, e, edit).ViewID + "-" + sha[:7] + ".zip"},
	} {
		w := gitGet(t, e, edit, pw, c.path)
		if w.Code != 200 {
			t.Fatalf("%s = %d %s", c.path, w.Code, w.Body)
		}
		h := w.Header()
		if cd := h.Get("Content-Disposition"); !strings.HasPrefix(cd, "attachment") || !strings.Contains(cd, c.name) {
			t.Errorf("%s: Content-Disposition = %q, want an attachment of %s", c.path, cd, c.name)
		}
		if h.Get("Content-Type") != "application/octet-stream" || h.Get("X-Content-Type-Options") != "nosniff" || h.Get("Content-Security-Policy") != "sandbox" {
			t.Errorf("%s: headers = %v", c.path, h)
		}
	}
	w := gitGet(t, e, edit, pw, "/blob?ref=main&path=a.txt&download=1")
	if w.Body.String() != "<script>alert(1)</script>" {
		t.Errorf("download body = %q", w.Body)
	}
	if !strings.HasPrefix(gitGet(t, e, edit, pw, "/archive?ref=main").Body.String(), "PK") {
		t.Error("archive is not a ZIP")
	}
}

func TestGitDownloadFilenameIsSafe(t *testing.T) {
	cd := attachmentDisposition("a\"b\r\nSet-Cookie: xü.txt")
	if strings.ContainsAny(cd, "\r\n") || strings.Count(cd, `"`)%2 != 0 {
		t.Errorf("unsafe disposition %q", cd)
	}
	if !strings.HasPrefix(cd, "attachment;") {
		t.Errorf("disposition %q", cd)
	}
}

func TestGitReadRoutesRefuseLocked(t *testing.T) {
	e := newEnv(t, nil)
	g := newGitServer(t, e)
	edit, pw := gitSite(t, e, nil)
	cl := newGitClient(t)
	wd := cl.work()
	sha := pushed(t, cl, wd, g.remote(edit, pw), "a.txt", []byte("a"))
	site := siteByEdit(t, e, edit)
	if _, err := e.st.SetLock(site, &store.SiteLock{At: time.Now(), By: store.LockByAdmin, Reason: "under review"}); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"", "/refs", "/log", "/commit/" + sha, "/tree", "/blob?path=a.txt", "/archive"} {
		if w := gitGet(t, e, edit, pw, p); w.Code != 403 {
			t.Errorf("GET git%s on a locked site = %d, want 403", p, w.Code)
		}
	}
	if w := e.call(t, "DELETE", "/api/sites/"+edit+"/git", pw, nil); w.Code != 403 {
		t.Errorf("DELETE on a locked site = %d, want 403", w.Code)
	}
	if !gitrepo.Exists(site.GitDir()) {
		t.Error("a locked site's repository was deleted")
	}
}

func TestGitDeleteRepo(t *testing.T) {
	e := newEnv(t, nil)
	g := newGitServer(t, e)
	edit, pw := gitSite(t, e, nil)
	cl := newGitClient(t)
	wd := cl.work()
	remote := g.remote(edit, pw)
	pushed(t, cl, wd, remote, "a.txt", []byte("a"))
	site := siteByEdit(t, e, edit)

	if w := e.call(t, "DELETE", "/api/sites/"+edit+"/git", "wrong", nil); w.Code != 401 && w.Code != 403 {
		t.Errorf("DELETE with a wrong password = %d", w.Code)
	}
	if w := e.call(t, "DELETE", "/api/sites/"+edit+"/git", pw, nil); w.Code != 204 {
		t.Fatalf("DELETE = %d %s", w.Code, w.Body)
	}
	if gitrepo.Exists(site.GitDir()) {
		t.Error("the repository is still there")
	}
	if in := gitInfo(t, e, edit, pw); in.Exists || in.Bytes != 0 {
		t.Errorf("info after delete = %+v", in)
	}
	var found bool
	es, err := e.st.Provenance(site)
	if err != nil {
		t.Fatal(err)
	}
	for _, en := range es {
		found = found || en.Action == provenance.ActionGitDelete
	}
	if !found {
		t.Error("no git-delete provenance entry")
	}
	// Deleting again is not an error, and the next push re-creates it.
	if w := e.call(t, "DELETE", "/api/sites/"+edit+"/git", pw, nil); w.Code != 204 {
		t.Errorf("second DELETE = %d", w.Code)
	}
	cl.commit(wd, "b.txt", []byte("b"))
	cl.mustGit(wd, "push", "-q", "-f", remote, "HEAD:main")
	if in := gitInfo(t, e, edit, pw); !in.Exists || in.Bytes <= 0 {
		t.Errorf("info after the next push = %+v", in)
	}
}

func TestGitReadRoutesRefuseOAuth(t *testing.T) {
	e := newEnv(t, oauthEnv())
	f := newGitFake(t)
	f.oauthEverywhere = true
	edit, _ := ownedSiteWithPassword(t, e)
	for _, p := range []string{"", "/refs", "/log", "/tree", "/archive"} {
		req := httptest.NewRequest("GET", "/api/sites/"+edit+"/git"+p, nil)
		req.Header.Set("Authorization", "Bearer "+jwtWrite)
		if w := e.public(t, req); w.Code != 401 {
			t.Errorf("GET git%s with an OAuth token = %d, want 401", p, w.Code)
		}
	}
	req := httptest.NewRequest("DELETE", "/api/sites/"+edit+"/git", nil)
	req.Header.Set("Authorization", "Bearer "+jwtWrite)
	if w := e.public(t, req); w.Code != 401 {
		t.Errorf("DELETE git with an OAuth token = %d, want 401", w.Code)
	}
	// The account's own API token is let in.
	req = httptest.NewRequest("GET", "/api/sites/"+edit+"/git", nil)
	req.Header.Set("Authorization", "Bearer sbp_owner")
	w := e.public(t, req)
	if w.Code != 200 {
		t.Fatalf("GET git with the account token = %d %s", w.Code, w.Body)
	}
	var in gitInfoResp
	json.Unmarshal(w.Body.Bytes(), &in)
	if !in.Credentials.Token {
		t.Errorf("credentials.token = false on an accounts instance")
	}
}

func TestGitEmptyDownloadHasHeaders(t *testing.T) {
	e := newEnv(t, nil)
	g := newGitServer(t, e)
	edit, pw := gitSite(t, e, nil)
	cl := newGitClient(t)
	wd := cl.work()
	cl.commit(wd, "empty.txt", []byte{})
	cl.mustGit(wd, "push", "-q", g.remote(edit, pw), "HEAD:main")
	w := gitGet(t, e, edit, pw, "/blob?ref=main&path=empty.txt&download=1")
	if w.Code != 200 || w.Body.Len() != 0 {
		t.Fatalf("empty download = %d, %d bytes", w.Code, w.Body.Len())
	}
	h := w.Header()
	if !strings.HasPrefix(h.Get("Content-Disposition"), "attachment") || !strings.Contains(h.Get("Content-Disposition"), "empty.txt") ||
		h.Get("Content-Type") != "application/octet-stream" || h.Get("X-Content-Type-Options") != "nosniff" || h.Get("Content-Security-Policy") != "sandbox" {
		t.Errorf("headers = %v", h)
	}
}

func TestGitLogBadSkip(t *testing.T) {
	e := newEnv(t, nil)
	g := newGitServer(t, e)
	edit, pw := gitSite(t, e, nil)
	cl := newGitClient(t)
	pushed(t, cl, cl.work(), g.remote(edit, pw), "a.txt", []byte("a"))
	for _, v := range []string{"x", "-1", "99999999999999999999999"} {
		w := gitGet(t, e, edit, pw, "/log?skip="+v)
		if w.Code != 400 || !strings.Contains(w.Body.String(), `"bad skip"`) {
			t.Errorf("skip=%s = %d %s", v, w.Code, w.Body)
		}
	}
}

// A broken repository answers a generic 500: git's message, with its
// filesystem paths, never reaches the client.
func TestGitCorruptRepoDoesNotLeakPaths(t *testing.T) {
	e := newEnv(t, nil)
	g := newGitServer(t, e)
	edit, pw := gitSite(t, e, nil)
	cl := newGitClient(t)
	wd := cl.work()
	pushed(t, cl, wd, g.remote(edit, pw), "a.txt", []byte("a"))
	site := siteByEdit(t, e, edit)
	dir := site.GitDir()
	// Corrupt the repository: garbage in the branch ref and in the HEAD file.
	if err := os.WriteFile(filepath.Join(dir, "refs", "heads", "main"), []byte("garbage\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(filepath.Join(dir, "packed-refs"))
	var saw500 bool
	for _, p := range []string{"/refs", "/log?ref=main", "/tree?ref=main", "/log"} {
		w := gitGet(t, e, edit, pw, p)
		body := w.Body.String()
		if strings.Contains(body, dir) || strings.Contains(body, filepath.ToSlash(dir)) || strings.Contains(body, "fatal:") {
			t.Errorf("GET git%s leaked: %s", p, body)
		}
		if w.Code == 500 {
			saw500 = true
			if !strings.Contains(body, msgGitInternal) {
				t.Errorf("GET git%s = 500 %s, want the generic message", p, body)
			}
		}
	}
	if !saw500 {
		t.Error("no route answered 500 on the corrupt repository; the fixture does not exercise the generic error")
	}
}

func TestGetSiteGitAvailable(t *testing.T) {
	read := func(e *env, edit, pw string) (bool, bool) {
		t.Helper()
		w := e.call(t, "GET", "/api/sites/"+edit, pw, nil)
		if w.Code != 200 {
			t.Fatalf("GET site = %d %s", w.Code, w.Body)
		}
		var raw map[string]json.RawMessage
		json.Unmarshal(w.Body.Bytes(), &raw)
		v, ok := raw["git_available"]
		return ok, string(v) == "true"
	}
	e := newEnv(t, nil)
	edit, pw := gitSite(t, e, nil)
	if present, on := read(e, edit, pw); !present || !on {
		t.Errorf("git_available present=%v value=%v, want true", present, on)
	}
	e.api.cfg.GitEnabled = false
	if present, on := read(e, edit, pw); !present || on {
		t.Errorf("with Git disabled: present=%v value=%v, want present and false", present, on)
	}
}
