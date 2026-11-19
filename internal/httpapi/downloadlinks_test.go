package httpapi

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/ittrail/sitebin.io/internal/ext"
	"github.com/ittrail/sitebin.io/internal/store"
)

type linkResp struct {
	URL       string    `json:"url"`
	ExpiresAt time.Time `json:"expires_at"`
}

// mintLink asks for a download link to path with the edit password.
func (e *env) mintLink(t *testing.T, edit, pw, path string) (*httptest.ResponseRecorder, linkResp) {
	t.Helper()
	w := e.call(t, "POST", "/api/sites/"+edit+"/download-link", pw, map[string]string{"path": path})
	var out linkResp
	json.Unmarshal(w.Body.Bytes(), &out)
	return w, out
}

// open GETs a link as the browser does: no credential of any kind.
func (e *env) open(t *testing.T, link string) *httptest.ResponseRecorder {
	t.Helper()
	return e.public(t, httptest.NewRequest("GET", link, nil))
}

func zipNames(t *testing.T, w *httptest.ResponseRecorder) []string {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(w.Body.Bytes()), int64(w.Body.Len()))
	if err != nil {
		t.Fatalf("not a zip: %v", err)
	}
	var out []string
	for _, f := range zr.File {
		out = append(out, f.Name)
	}
	return out
}

func TestDownloadLinkStreamsWithoutACredential(t *testing.T) {
	e := newEnv(t, nil)
	c := e.createSite(t, nil, map[string]string{"index.html": "hi"})
	edit := editIDFrom(t, c.EditURL)
	w, l := e.mintLink(t, edit, c.EditPassword, "/download")
	if w.Code != 200 || !strings.HasPrefix(l.URL, "/api/sites/"+edit+"/download?ticket=sbd_") {
		t.Fatalf("mint = %d %s", w.Code, w.Body)
	}
	if d := time.Until(l.ExpiresAt); d <= 0 || d > downloadLinkTTL {
		t.Errorf("expires in %v", d)
	}
	got := e.open(t, l.URL)
	if got.Code != 200 || got.Header().Get("Content-Type") != "application/zip" {
		t.Fatalf("open = %d %v %s", got.Code, got.Header(), got.Body)
	}
	if cd := got.Header().Get("Content-Disposition"); !strings.HasPrefix(cd, "attachment") || !strings.Contains(cd, c.ID+".zip") {
		t.Errorf("Content-Disposition = %q", cd)
	}
	if got.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("Cache-Control = %q", got.Header().Get("Cache-Control"))
	}
	if names := zipNames(t, got); len(names) != 1 || names[0] != "index.html" {
		t.Errorf("zip holds %v", names)
	}
	// Without a ticket the route still wants a credential.
	if w := e.open(t, "/api/sites/"+edit+"/download"); w.Code != 401 {
		t.Errorf("GET without ticket or credential = %d, want 401", w.Code)
	}
}

func TestDownloadLinkIsSingleUse(t *testing.T) {
	e := newEnv(t, nil)
	c := e.createSite(t, nil, map[string]string{"index.html": "hi"})
	edit := editIDFrom(t, c.EditURL)
	_, l := e.mintLink(t, edit, c.EditPassword, "/download")
	if w := e.open(t, l.URL); w.Code != 200 {
		t.Fatalf("first use = %d", w.Code)
	}
	if w := e.open(t, l.URL); w.Code != 401 {
		t.Errorf("second use = %d, want 401", w.Code)
	}
}

// A link opens exactly the request it was minted for: the same site, path
// and query. A misuse on a download route spends it; every other route never
// reads a ticket and wants its credential.
func TestDownloadLinkOpensOnlyItsRequest(t *testing.T) {
	e := newEnv(t, nil)
	c := e.createSite(t, nil, map[string]string{"index.html": "hi"})
	other := e.createSite(t, nil, map[string]string{"index.html": "other"})
	edit, otherEdit := editIDFrom(t, c.EditURL), editIDFrom(t, other.EditURL)
	for name, misuse := range map[string]func(ticket string) string{
		"another route": func(tk string) string { return "/api/sites/" + edit + "/git/archive?ticket=" + tk },
		"another site":  func(tk string) string { return "/api/sites/" + otherEdit + "/download?ticket=" + tk },
		"another query": func(tk string) string { return "/api/sites/" + edit + "/download?name=x&ticket=" + tk },
	} {
		_, l := e.mintLink(t, edit, c.EditPassword, "/download")
		u, _ := url.Parse(l.URL)
		tk := u.Query().Get("ticket")
		if w := e.open(t, misuse(tk)); w.Code != 401 {
			t.Errorf("%s: %d, want 401", name, w.Code)
		}
		if w := e.open(t, l.URL); w.Code != 401 {
			t.Errorf("%s: the link still opens after a misuse: %d", name, w.Code)
		}
	}
	_, l := e.mintLink(t, edit, c.EditPassword, "/download")
	u, _ := url.Parse(l.URL)
	for _, p := range []string{"/dir", "", "/content/index.html", "/backups"} {
		if w := e.open(t, "/api/sites/"+edit+p+"?ticket="+u.Query().Get("ticket")); w.Code != 401 {
			t.Errorf("a ticket on %q = %d, want 401", p, w.Code)
		}
	}
}

func TestDownloadLinkExpires(t *testing.T) {
	e := newEnv(t, nil)
	now := time.Now()
	e.api.downloads.now = func() time.Time { return now }
	c := e.createSite(t, nil, map[string]string{"index.html": "hi"})
	edit := editIDFrom(t, c.EditURL)
	_, l := e.mintLink(t, edit, c.EditPassword, "/download")
	now = now.Add(downloadLinkTTL + time.Second)
	if w := e.open(t, l.URL); w.Code != 401 {
		t.Errorf("an expired link = %d, want 401", w.Code)
	}
}

func TestDownloadLinkMintRefusesWhatIsNotADownload(t *testing.T) {
	e := newEnv(t, nil)
	c := e.createSite(t, nil, map[string]string{"index.html": "hi"})
	edit := editIDFrom(t, c.EditURL)
	for _, p := range []string{
		"", "/", "/dir", "/files", "/content/index.html", "/download/", "/download/../dir",
		"/backups//download", "/backups/a/b/download", "/backups/x/restore", "/git/blob?path=a",
		"https://evil.example/download", "//evil.example/download", "download",
		"/download?ticket=sbd_x", "/download#x",
	} {
		if w, _ := e.mintLink(t, edit, c.EditPassword, p); w.Code != 400 {
			t.Errorf("mint %q = %d %s, want 400", p, w.Code, w.Body)
		}
	}
	for _, p := range []string{"/download", "/download?name=Physio", "/backups/hourly-2026-11-19T11:07/download", "/git/archive", "/git/archive?ref=main"} {
		if w, _ := e.mintLink(t, edit, c.EditPassword, p); w.Code != 200 {
			t.Errorf("mint %q = %d %s, want 200", p, w.Code, w.Body)
		}
	}
}

func TestDownloadLinkMintNeedsACredential(t *testing.T) {
	e := newEnv(t, nil)
	c := e.createSite(t, nil, map[string]string{"index.html": "hi"})
	edit := editIDFrom(t, c.EditURL)
	if w, _ := e.mintLink(t, edit, "", "/download"); w.Code != 401 {
		t.Errorf("mint without a credential = %d", w.Code)
	}
	if w, _ := e.mintLink(t, edit, "wrong", "/download"); w.Code != 401 {
		t.Errorf("mint with a wrong password = %d", w.Code)
	}
	if w, _ := e.mintLink(t, edit, e.uploadToken(t, c.ID), "/download"); w.Code != 403 {
		t.Errorf("mint with an upload token = %d, want 403", w.Code)
	}
}

// A link minted with the edit password dies with that password.
func TestDownloadLinkFollowsTheEditPassword(t *testing.T) {
	e := newEnv(t, nil)
	c := e.createSite(t, nil, map[string]string{"index.html": "hi"})
	edit := editIDFrom(t, c.EditURL)
	_, l := e.mintLink(t, edit, c.EditPassword, "/download")
	if _, err := (siteService{a: e.api}).RotateEditPassword(c.ID); err != nil {
		t.Fatal(err)
	}
	if w := e.open(t, l.URL); w.Code != 401 {
		t.Errorf("a link from the old password = %d, want 401", w.Code)
	}
}

// An owner's link holds while the account owns the site.
func TestDownloadLinkOfAFormerOwnerIsRefused(t *testing.T) {
	ext.Register(&fakeProvider{enabled: true, owner: "acct-1"})
	defer ext.Reset()
	e := newEnv(t, nil)
	c := e.createSite(t, nil, map[string]string{"index.html": "x"})
	edit := editIDFrom(t, c.EditURL)
	link := func(account string) string {
		key := canonicalDownload("/api/sites/"+edit+"/download", url.Values{})
		tk, _, err := e.api.downloads.issue(c.ID, edit, key, actor{auth: "token", account: account, role: roleOwner}, "")
		if err != nil {
			t.Fatal(err)
		}
		return "/api/sites/" + edit + "/download?ticket=" + tk
	}
	before := link("acct-1")
	if _, err := e.own(t).TransferSite(c.ID, "acct-1", "acct-2", ext.CreateGrant{}, ext.ChangeBy{Account: "acct-2"}); err != nil {
		t.Fatal(err)
	}
	if w := e.open(t, before); w.Code != 401 {
		t.Errorf("the former owner's link = %d, want 401", w.Code)
	}
	if w := e.open(t, link("acct-2")); w.Code != 200 {
		t.Errorf("the new owner's link = %d, want 200", w.Code)
	}
}

func TestDownloadLinkOnALockedSiteIsRefused(t *testing.T) {
	e := newEnv(t, nil)
	c := e.createSite(t, nil, map[string]string{"index.html": "hi"})
	edit := editIDFrom(t, c.EditURL)
	_, l := e.mintLink(t, edit, c.EditPassword, "/download")
	site, _ := e.st.ByEditID(edit)
	if _, err := e.st.SetLock(site, &store.SiteLock{By: store.LockByAdmin}); err != nil {
		t.Fatal(err)
	}
	if w := e.open(t, l.URL); w.Code != 403 {
		t.Errorf("a link to a locked site = %d, want 403", w.Code)
	}
}

// The edit page's own case: the owner's session mints, the browser opens the
// link with nothing but the URL.
func TestDownloadLinkFromTheOwnersSession(t *testing.T) {
	e := newEnv(t, nil)
	edit := ownedSite(t, e)
	registerSessions(t)
	w := e.public(t, sessionReq("POST", "/api/sites/"+edit+"/download-link", `{"path":"/download?name=Physio Print"}`, "owner-cookie"))
	if w.Code != 200 {
		t.Fatalf("mint by session = %d %s", w.Code, w.Body)
	}
	var l linkResp
	json.Unmarshal(w.Body.Bytes(), &l)
	got := e.open(t, l.URL)
	if got.Code != 200 {
		t.Fatalf("open = %d %s", got.Code, got.Body)
	}
	if cd := got.Header().Get("Content-Disposition"); cd != `attachment; filename="Physio Print.zip"` {
		t.Errorf("Content-Disposition = %q", cd)
	}
	// A session without the marker header mints nothing: the CSRF boundary.
	req := sessionReq("POST", "/api/sites/"+edit+"/download-link", `{"path":"/download"}`, "owner-cookie")
	req.Header.Del("X-Sitebin-Session")
	if w := e.public(t, req); w.Code != 401 {
		t.Errorf("mint by a session without the marker = %d, want 401", w.Code)
	}
}

func TestBackupDownloadThroughALink(t *testing.T) {
	b := newBackupEnv(t)
	_, l := b.mintLink(t, b.editID, b.pw, "/backups/nightly-old/download")
	w := b.open(t, l.URL)
	if w.Code != 200 {
		t.Fatalf("open = %d %s", w.Code, w.Body)
	}
	if names := zipNames(t, w); len(names) != 2 {
		t.Errorf("zip holds %v", names)
	}
}

func TestGitArchiveThroughALink(t *testing.T) {
	e := newEnv(t, nil)
	g := newGitServer(t, e)
	edit, pw := gitSite(t, e, nil)
	cl := newGitClient(t)
	wd := cl.work()
	cl.commit(wd, "a.txt", []byte("a"))
	cl.mustGit(wd, "push", "-q", g.remote(edit, pw), "HEAD:main")
	_, l := e.mintLink(t, edit, pw, "/git/archive?ref=main")
	w := e.open(t, l.URL)
	if w.Code != 200 || !strings.HasPrefix(w.Body.String(), "PK") {
		t.Fatalf("open = %d %.100s", w.Code, w.Body)
	}
}

func TestDownloadNameIsAFileNameOnly(t *testing.T) {
	e := newEnv(t, nil)
	c := e.createSite(t, nil, map[string]string{"index.html": "hi"})
	edit := editIDFrom(t, c.EditURL)
	for name, want := range map[string]string{
		"Physio Print":           `attachment; filename="Physio Print.zip"`,
		"Übung":                  `attachment; filename*=utf-8''%C3%9Cbung.zip`,
		"../a\"b\r\nX-Evil: 1":   `attachment; filename="_a_b__X-Evil_ 1.zip"`,
		" .. ":                   `attachment; filename=` + c.ID + `.zip`,
		"":                       `attachment; filename=` + c.ID + `.zip`,
		strings.Repeat("x", 300): `attachment; filename=` + strings.Repeat("x", 100) + `.zip`,
	} {
		w := e.call(t, "GET", "/api/sites/"+edit+"/download?name="+url.QueryEscape(name), c.EditPassword, nil)
		if cd := w.Header().Get("Content-Disposition"); cd != want {
			t.Errorf("name %q: Content-Disposition = %q, want %q", name, cd, want)
		}
	}
}

func TestDownloadLinksAreBounded(t *testing.T) {
	e := newEnv(t, nil)
	c := e.createSite(t, nil, map[string]string{"index.html": "hi"})
	edit := editIDFrom(t, c.EditURL)
	_, first := e.mintLink(t, edit, c.EditPassword, "/download")
	for i := 0; i < downloadLinksPerSite; i++ {
		e.mintLink(t, edit, c.EditPassword, "/download")
	}
	if w := e.open(t, first.URL); w.Code != 401 {
		t.Errorf("the oldest of %d links = %d, want 401 (evicted)", downloadLinksPerSite+1, w.Code)
	}
	e.api.downloads.max = len(e.api.downloads.m)
	other := e.createSite(t, nil, map[string]string{"index.html": "x"})
	if w, _ := e.mintLink(t, editIDFrom(t, other.EditURL), other.EditPassword, "/download"); w.Code != 503 {
		t.Errorf("mint past the instance cap = %d, want 503", w.Code)
	}
}

// A download that fails halfway is cut off, never ended as if complete: a
// browser would keep a broken archive as a finished file.
func TestSiteDownloadThatFailsIsAborted(t *testing.T) {
	e := newEnv(t, nil)
	c := e.createSite(t, nil, map[string]string{"index.html": strings.Repeat("x", 200<<10)})
	site, _ := e.st.ByViewID(c.ID)
	defer func() {
		if p := recover(); p != http.ErrAbortHandler {
			t.Errorf("recovered %v, want http.ErrAbortHandler", p)
		}
	}()
	e.api.downloadSite(failingWriter{httptest.NewRecorder()}, httptest.NewRequest("GET", "/x", nil), site)
}

type failingWriter struct{ *httptest.ResponseRecorder }

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("connection reset") }
