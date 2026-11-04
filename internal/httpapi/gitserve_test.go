package httpapi

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha1"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ittrail/sitebin.io/internal/ext"
	"github.com/ittrail/sitebin.io/internal/gitrepo"
	"github.com/ittrail/sitebin.io/internal/mcp"
	"github.com/ittrail/sitebin.io/internal/provenance"
	"github.com/ittrail/sitebin.io/internal/store"
)

// ---- the Git endpoint: /git/<edit-id>.git on the base domain ----
//
// Every test here drives a real git client against a real HTTP server
// around a.Public(), and skips where no git is installed.

const zeroSHA = "0000000000000000000000000000000000000000"

// gitSite creates a community site and returns its edit id and password.
func gitSite(t *testing.T, e *env, files map[string]string) (edit, pw string) {
	t.Helper()
	if files == nil {
		files = map[string]string{"index.html": "x"}
	}
	c := e.createSite(t, nil, files)
	return editIDFrom(t, c.EditURL), c.EditPassword
}

// pushed makes a commit adding name and pushes it to main.
func pushed(t *testing.T, cl *gitClient, wd, remote, name string, content []byte) string {
	t.Helper()
	sha := cl.commit(wd, name, content)
	cl.mustGit(wd, "push", "-q", remote, "HEAD:main")
	return sha
}

// gitPushes returns the site's git-push provenance entries.
func gitPushes(t *testing.T, e *env, site *store.Site) []provenance.Entry {
	t.Helper()
	es, err := e.st.Provenance(site)
	if err != nil {
		t.Fatal(err)
	}
	var out []provenance.Entry
	for _, en := range es {
		if en.Action == provenance.ActionGitPush {
			out = append(out, en)
		}
	}
	return out
}

func wantOutput(t *testing.T, out, want, what string) {
	t.Helper()
	if !strings.Contains(out, want) {
		t.Fatalf("%s: output lacks %q:\n%s", what, want, out)
	}
}

// The community build, no provider: push and clone with the edit password.
func TestGitPushCloneWithEditPassword(t *testing.T) {
	e := newEnv(t, nil)
	g := newGitServer(t, e)
	edit, pw := gitSite(t, e, nil)
	site := siteByEdit(t, e, edit)
	cl := newGitClient(t)
	tmp := t.TempDir()
	remote := g.remote(edit, pw)

	if gitrepo.Exists(site.GitDir()) {
		t.Fatal("a repository before the first request")
	}
	// An empty repository clones: the first authorised request creates it.
	out := cl.mustGit(tmp, "clone", remote, filepath.Join(tmp, "empty"))
	wantOutput(t, out, "empty repository", "clone of a new site")
	if !gitrepo.Exists(site.GitDir()) {
		t.Fatal("the first clone created no repository")
	}

	wd := cl.work()
	sha := pushed(t, cl, wd, remote, "hello.txt", []byte("hello from git\n"))

	for _, proto := range []string{"0", "2"} {
		dir := filepath.Join(tmp, "clone-v"+proto)
		cl.mustGit(tmp, "-c", "protocol.version="+proto, "clone", "-q", remote, dir)
		if got := cl.mustGit(dir, "rev-parse", "HEAD"); got != sha {
			t.Errorf("protocol v%s: cloned HEAD = %s, want %s", proto, got, sha)
		}
		b, err := os.ReadFile(filepath.Join(dir, "hello.txt"))
		if err != nil || string(b) != "hello from git\n" {
			t.Errorf("protocol v%s: cloned file = %q, %v", proto, b, err)
		}
	}
	// The files a site serves are untouched by a push.
	if _, err := os.Stat(filepath.Join(site.ContentDir(), "hello.txt")); err == nil {
		t.Error("a pushed file appeared among the site's files")
	}

	// A wrong password is a challenge, and Git gives up.
	out = cl.failGit(tmp, "ls-remote", g.remote(edit, "wrong-password-0123456"))
	wantOutput(t, out, "Authentication failed", "a wrong password")
}

// The endpoint answers on the base domain only: never on a site's own
// address, a custom domain or anything else that reaches the backend.
func TestGitRefusesOtherHost(t *testing.T) {
	e := newEnv(t, nil)
	g := newGitServer(t, e)
	edit, pw := gitSite(t, e, nil)
	site := siteByEdit(t, e, edit)
	cl := newGitClient(t)
	tmp := t.TempDir()

	for _, host := range []string{site.ViewID + ".sitebin.example", "custom.example", "sitebin.example.evil.test", "evil.test"} {
		g.setHost(host)
		out := cl.failGit(tmp, "ls-remote", g.remote(edit, pw))
		wantOutput(t, out, "not found", "Host "+host)
		r := httptest.NewRequest("GET", "/git/"+edit+".git/info/refs?service=git-upload-pack", nil)
		r.Host = host
		r.SetBasicAuth("drop", pw)
		if w := e.public(t, r); w.Code != http.StatusNotFound {
			t.Errorf("Host %s: %d, want 404", host, w.Code)
		}
	}
	if gitrepo.Exists(site.GitDir()) {
		t.Fatal("a request on another host created the repository")
	}
	// The base domain is the base domain in any case and with a port.
	g.setHost("SiteBin.Example:8443")
	cl.mustGit(tmp, "ls-remote", g.remote(edit, pw))
}

// The routes themselves: what is not a repository, a service or a credential.
func TestGitEndpointRouting(t *testing.T) {
	e := newEnv(t, nil)
	edit, pw := gitSite(t, e, nil)
	get := func(path string, basic bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", path, nil)
		if basic {
			r.SetBasicAuth("drop", pw)
		}
		return e.public(t, r)
	}
	if w := get("/git/"+edit+".git/info/refs?service=git-upload-pack", false); w.Code != 401 || w.Header().Get("WWW-Authenticate") != gitBasicChallenge {
		t.Errorf("no credential: %d %q, want 401 with the Basic challenge", w.Code, w.Header().Get("WWW-Authenticate"))
	}
	for path, want := range map[string]int{
		"/git/" + edit + "/info/refs?service=git-upload-pack":                        404, // no .git
		"/git/" + strings.Repeat("a", 26) + ".git/info/refs?service=git-upload-pack": 404, // unknown site
		"/git/" + edit + ".git.git/info/refs?service=git-upload-pack":                404,
		"/git/" + strings.ToUpper(edit) + ".git/info/refs?service=git-upload-pack":   404,
		"/git/" + edit + ".git/info/refs?service=git-nope":                           400,
		"/git/" + edit + ".git/info/refs":                                            400, // the dumb protocol
		"/git/" + edit + ".git/info/refs?service=git-upload-pack&service=git-nope":   200,
		"/git/" + edit + ".git/info/refs?service=git-receive-pack":                   200,
		"/git/" + edit + ".git/HEAD":                                                 404,
		"/git/" + edit + ".git/objects/info/packs":                                   404,
	} {
		if w := get(path, true); w.Code != want {
			t.Errorf("GET %s: %d, want %d (%s)", path, w.Code, want, w.Body)
		}
	}
	// Another method never reaches the endpoint (the catch-all answers it).
	for _, m := range []struct{ method, path string }{
		{"POST", "/git/" + edit + ".git/info/refs?service=git-upload-pack"},
		{"GET", "/git/" + edit + ".git/git-upload-pack"},
		{"GET", "/git/" + edit + ".git/git-receive-pack"},
		{"PUT", "/git/" + edit + ".git/git-receive-pack"},
	} {
		r := httptest.NewRequest(m.method, m.path, nil)
		r.SetBasicAuth("drop", pw)
		if w := e.public(t, r); w.Code != http.StatusNotFound && w.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s %s: %d, want 404 or 405", m.method, m.path, w.Code)
		}
	}
}

func TestGitLockedSiteRefusedReadsIncluded(t *testing.T) {
	e := newEnv(t, nil)
	g := newGitServer(t, e)
	edit, pw := gitSite(t, e, nil)
	cl := newGitClient(t)
	tmp := t.TempDir()
	remote := g.remote(edit, pw)
	wd := cl.work()
	sha := pushed(t, cl, wd, remote, "a.txt", []byte("a"))

	site := siteByEdit(t, e, edit)
	if _, err := e.st.SetLock(site, &store.SiteLock{At: time.Now(), By: store.LockByAdmin, Reason: "under review"}); err != nil {
		t.Fatal(err)
	}
	const want = "remote error: This site is locked by the operator: under review"
	for _, proto := range []string{"0", "2"} {
		out := cl.failGit(tmp, "-c", "protocol.version="+proto, "ls-remote", remote)
		wantOutput(t, out, want, "ls-remote v"+proto)
		out = cl.failGit(tmp, "-c", "protocol.version="+proto, "clone", remote, filepath.Join(tmp, "c"+proto))
		wantOutput(t, out, want, "clone v"+proto)
	}
	cl.commit(wd, "b.txt", []byte("b"))
	out := cl.failGit(wd, "push", remote, "HEAD:main")
	wantOutput(t, out, want, "push")

	// The repository is frozen as it was.
	if _, err := e.st.SetLock(site, nil); err != nil {
		t.Fatal(err)
	}
	if got := cl.stdoutGit(tmp, "ls-remote", remote, "refs/heads/main"); !strings.HasPrefix(got, sha) {
		t.Errorf("after the lock: main = %q, want %s", got, sha)
	}
}

func TestGitPlanWithoutGitReadOnly(t *testing.T) {
	e := newEnv(t, nil)
	g := newGitServer(t, e)
	cl := newGitClient(t)
	tmp := t.TempDir()
	no := false
	hint := e.api.apiAccountHint()

	// A site that has a repository and then loses Git: clone, yes; push, no.
	edit, pw := gitSite(t, e, nil)
	remote := g.remote(edit, pw)
	wd := cl.work()
	sha := pushed(t, cl, wd, remote, "a.txt", []byte("kept"))
	if err := e.st.SetQuotaGit(siteByEdit(t, e, edit), &no); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(tmp, "clone")
	cl.mustGit(tmp, "clone", "-q", remote, dir)
	if got := cl.mustGit(dir, "rev-parse", "HEAD"); got != sha {
		t.Errorf("read-only clone HEAD = %s, want %s", got, sha)
	}
	cl.commit(wd, "b.txt", []byte("refused"))
	out := cl.failGit(wd, "push", remote, "HEAD:main")
	wantOutput(t, out, "fatal: remote error:", "push on a plan without Git")
	wantOutput(t, out, hint, "push on a plan without Git")
	if got := cl.stdoutGit(tmp, "ls-remote", remote, "refs/heads/main"); !strings.HasPrefix(got, sha) {
		t.Errorf("after the refused push: main = %q, want %s", got, sha)
	}

	// A site without a repository has none to read and none is created.
	edit2, pw2 := gitSite(t, e, nil)
	site2 := siteByEdit(t, e, edit2)
	if err := e.st.SetQuotaGit(site2, &no); err != nil {
		t.Fatal(err)
	}
	out = cl.failGit(tmp, "clone", g.remote(edit2, pw2), filepath.Join(tmp, "none"))
	wantOutput(t, out, "fatal: remote error:", "clone on a plan without Git, no repository")
	wantOutput(t, out, hint, "clone on a plan without Git, no repository")
	if gitrepo.Exists(site2.GitDir()) {
		t.Error("a site whose plan has no Git got a repository")
	}
}

func TestGitStorageFullRefusal(t *testing.T) {
	e := newEnv(t, nil)
	g := newGitServer(t, e)
	edit, pw := gitSite(t, e, map[string]string{"index.html": "0123456789"})
	site := siteByEdit(t, e, edit)
	if err := e.st.Update(site, func(m *store.Meta) error { m.QuotaBytes = 10; return nil }); err != nil {
		t.Fatal(err)
	}
	cl := newGitClient(t)
	wd := cl.work()
	cl.commit(wd, "a.txt", []byte("a"))
	out := cl.failGit(wd, "push", g.remote(edit, pw), "HEAD:main")
	wantOutput(t, out, "remote error: storage full: 10 B of 10 B used", "push to a full site")
	// Refused before Git ran: not even the repository was created.
	if gitrepo.Exists(siteByEdit(t, e, edit).GitDir()) {
		t.Error("a refused push created the repository")
	}
	// Reads are not about storage.
	cl.mustGit(wd, "ls-remote", g.remote(edit, pw))
}

func TestGitAllowanceIsRemainingQuota(t *testing.T) {
	e := newEnv(t, nil)
	g := newGitServer(t, e)
	edit, pw := gitSite(t, e, map[string]string{"big.bin": strings.Repeat("a", 900<<10)})
	site := siteByEdit(t, e, edit)
	if err := e.st.Update(site, func(m *store.Meta) error { m.QuotaBytes = 1 << 20; return nil }); err != nil {
		t.Fatal(err)
	}
	cl := newGitClient(t)
	remote := g.remote(edit, pw)
	wd := cl.work()
	cl.commit(wd, "big.bin", randomBytes(t, 300<<10))
	out := cl.failGit(wd, "push", remote, "HEAD:main")
	wantOutput(t, out, "maximum allowed size", "a push larger than the quota left")
	if got := cl.stdoutGit(wd, "ls-remote", remote); got != "" {
		t.Errorf("refs after the refused push: %q", got)
	}
	if n := len(gitPushes(t, e, site)); n != 0 {
		t.Errorf("%d git-push entries for a refused push", n)
	}
	if _, ok := gitrepo.LastPush(site.GitDir()); ok {
		t.Error("a refused push was marked as pushed")
	}

	// What is left of the quota is still the allowance: a small push fits.
	wd2 := cl.work()
	pushed(t, cl, wd2, remote, "small.bin", randomBytes(t, 10<<10))
}

func TestGitSecondPushBusy(t *testing.T) {
	e := newEnv(t, nil)
	g := newGitServer(t, e)
	edit, pw := gitSite(t, e, nil)
	cl := newGitClient(t)
	remote := g.remote(edit, pw)
	wd := cl.work()
	pushed(t, cl, wd, remote, "a.txt", []byte("a"))

	// Something holds the site's registration: a push, a first clone
	// creating the repository, the sweep's collection. Which one, the
	// endpoint cannot tell, so it says so neutrally.
	end, err := e.st.BeginGitWrite(siteByEdit(t, e, edit), nil)
	if err != nil {
		t.Fatal(err)
	}
	cl.commit(wd, "b.txt", []byte("b"))
	out := cl.failGit(wd, "push", remote, "HEAD:main")
	wantOutput(t, out, "remote error: "+msgGitRepoBusy, "a push beside a running push")
	if strings.Contains(out, "another push") {
		t.Errorf("the refusal names a push it cannot know of:\n%s", out)
	}
	// Fetches run beside a push.
	cl.mustGit(wd, "ls-remote", remote)
	end()
	cl.mustGit(wd, "push", "-q", remote, "HEAD:main")

	// A first clone that must create the repository while the site is
	// registered is told the same — not that a push is running.
	edit2, pw2 := gitSite(t, e, nil)
	end2, err := e.st.BeginGitWrite(siteByEdit(t, e, edit2), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer end2()
	tmp := t.TempDir()
	out = cl.failGit(tmp, "clone", g.remote(edit2, pw2), filepath.Join(tmp, "c"))
	wantOutput(t, out, "remote error: "+msgGitRepoBusy, "a first clone beside a registration")
}

// blockingWriter is a ResponseWriter whose first Write waits for release:
// it holds a git process slot for as long as a test needs.
type blockingWriter struct {
	h       http.Header
	entered chan struct{}
	release chan struct{}
	first   bool
}

func (b *blockingWriter) Header() http.Header { return b.h }
func (b *blockingWriter) WriteHeader(int)     {}
func (b *blockingWriter) Write(p []byte) (int, error) {
	if !b.first {
		b.first = true
		close(b.entered)
		<-b.release
	}
	return len(p), nil
}

func TestGitInstanceBusyIsReadable(t *testing.T) {
	e := newEnv(t, map[string]string{"SITEBIN_GIT_MAX_PROCESSES": "1"})
	g := newGitServer(t, e)
	edit, pw := gitSite(t, e, nil)
	site := siteByEdit(t, e, edit)
	cl := newGitClient(t)
	remote := g.remote(edit, pw)
	wd := cl.work()
	pushed(t, cl, wd, remote, "a.txt", []byte("a"))

	// The one process slot is taken.
	bw := &blockingWriter{h: http.Header{}, entered: make(chan struct{}), release: make(chan struct{})}
	held := make(chan error, 1)
	go func() {
		held <- e.api.git.AdvertiseRefs(context.Background(), site.GitDir(), gitrepo.UploadPack, "", bw)
	}()
	<-bw.entered

	// Each refusal waits out the runner's five seconds for a slot. (The
	// locked-site test shows refusals print under protocol v0 and v2 alike.)
	out := cl.failGit(wd, "ls-remote", remote)
	wantOutput(t, out, "remote error: instance busy, try again in a minute", "ls-remote with every slot taken")
	// An RPC cannot carry a remote error: a plain 503.
	req, _ := http.NewRequest("POST", g.url("/git/"+edit+".git/git-upload-pack"), strings.NewReader("0000"))
	req.SetBasicAuth("drop", pw)
	req.Header.Set("Content-Type", "application/x-git-upload-pack-request")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable || !strings.Contains(string(body), "instance busy") {
		t.Errorf("upload-pack with every slot taken: %d %q, want 503 instance busy", resp.StatusCode, body)
	}

	close(bw.release)
	if err := <-held; err != nil {
		t.Fatal(err)
	}
	cl.mustGit(wd, "ls-remote", remote)
}

func TestGitPushRecordsProvenance(t *testing.T) {
	e := newEnv(t, nil)
	g := newGitServer(t, e)
	edit, pw := gitSite(t, e, nil)
	site := siteByEdit(t, e, edit)
	cl := newGitClient(t)
	remote := g.remote(edit, pw)
	wd := cl.work()
	short := func(s string) string { return s[:7] }

	// Each push with its own user agent, so the entries stay apart.
	push := func(ua string, args ...string) provenance.Entry {
		t.Helper()
		cl.mustGit(wd, append([]string{"-c", "http.userAgent=" + ua, "push", "-q", remote}, args...)...)
		ps := gitPushes(t, e, site)
		if len(ps) == 0 {
			t.Fatalf("%s: no git-push entry", ua)
		}
		last := ps[len(ps)-1]
		if last.UA != ua {
			t.Fatalf("%s: newest entry is %+v", ua, last)
		}
		return last
	}

	first := cl.commit(wd, "a.txt", []byte("a"))
	en := push("push-create", "HEAD:main")
	if en.Surface != provenance.SurfaceGit || en.Auth != provenance.AuthPassword || en.Account != "" {
		t.Errorf("entry = %+v, want surface git, auth password, no account", en)
	}
	wantOutput(t, en.Detail, "refs/heads/main "+short(first)+" created", "a created branch")
	wantOutput(t, en.Detail, " received", "bytes received")
	if at, ok := gitrepo.LastPush(site.GitDir()); !ok || time.Since(at) > time.Minute {
		t.Errorf("LastPush = %v, %v after a push", at, ok)
	}

	second := cl.commit(wd, "b.txt", []byte("b"))
	en = push("push-ff", "HEAD:main")
	wantOutput(t, en.Detail, "refs/heads/main "+short(first)+".."+short(second), "a fast-forward")
	if strings.Contains(en.Detail, "forced") || strings.Contains(en.Detail, "created") {
		t.Errorf("a fast-forward marked: %q", en.Detail)
	}

	cl.mustGit(wd, "reset", "-q", "--hard", first)
	third := cl.commit(wd, "c.txt", []byte("c"))
	en = push("push-force", "--force", "HEAD:main")
	wantOutput(t, en.Detail, "refs/heads/main "+short(second)+".."+short(third)+" forced", "a force push")

	push("push-branch", "HEAD:refs/heads/side")
	en = push("push-delete", ":refs/heads/side")
	wantOutput(t, en.Detail, "refs/heads/side "+short(third)+" deleted", "a deleted branch")
}

// emptyPack is a pack with no objects, as git sends with a push that needs
// none.
func emptyPack() string {
	var b bytes.Buffer
	b.WriteString("PACK")
	_ = binary.Write(&b, binary.BigEndian, uint32(2))
	_ = binary.Write(&b, binary.BigEndian, uint32(0))
	sum := sha1.Sum(b.Bytes())
	b.Write(sum[:])
	return b.String()
}

func pktLine(s string) string { return fmt.Sprintf("%04x%s", len(s)+4, s) }

// postRPC sends a raw smart HTTP request.
func postRPC(t *testing.T, g *gitServer, edit, pw, svc string, body io.Reader, hdr map[string]string) (int, string) {
	t.Helper()
	req, err := http.NewRequest("POST", g.url("/git/"+edit+".git/"+svc), body)
	if err != nil {
		t.Fatal(err)
	}
	req.SetBasicAuth("drop", pw)
	req.Header.Set("Content-Type", "application/x-"+svc+"-request")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// Git refuses a ref update in-band and exits 0: what it refused is not
// recorded, and the repository is not marked as pushed.
func TestGitRefusedPushRecordsNothing(t *testing.T) {
	e := newEnv(t, nil)
	g := newGitServer(t, e)
	edit, pw := gitSite(t, e, nil)
	site := siteByEdit(t, e, edit)
	cl := newGitClient(t)
	cl.mustGit(t.TempDir(), "ls-remote", g.remote(edit, pw)) // creates the repository

	stale := strings.Repeat("1", 40)
	body := pktLine(stale+" "+strings.Repeat("2", 40)+" refs/heads/x\x00report-status\n") + "0000" + emptyPack()
	code, out := postRPC(t, g, edit, pw, "git-receive-pack", strings.NewReader(body), nil)
	if code != 200 || !strings.Contains(out, "ng refs/heads/x") {
		t.Fatalf("stale update: %d %q, want git's in-band ng", code, out)
	}
	// Deleting a ref that does not exist changes nothing either.
	body = pktLine(stale+" "+zeroSHA+" refs/heads/nothing\x00report-status delete-refs\n") + "0000"
	if code, out := postRPC(t, g, edit, pw, "git-receive-pack", strings.NewReader(body), nil); code != 200 {
		t.Fatalf("delete of a missing ref: %d %q", code, out)
	}
	if n := len(gitPushes(t, e, site)); n != 0 {
		t.Errorf("%d git-push entries for refused updates", n)
	}
	if _, ok := gitrepo.LastPush(site.GitDir()); ok {
		t.Error("refused updates marked the repository as pushed")
	}
}

// Fetch request bodies are bounded, compressed or not: a gzip bomb of have
// lines is cut where its decompressed size passes the bound.
func TestGitFetchBodyIsBounded(t *testing.T) {
	e := newEnv(t, nil)
	g := newGitServer(t, e)
	edit, pw := gitSite(t, e, nil)
	cl := newGitClient(t)
	wd := cl.work()
	sha := pushed(t, cl, wd, g.remote(edit, pw), "a.txt", []byte("a"))

	var raw bytes.Buffer
	raw.WriteString(pktLine("want " + sha + "\n"))
	raw.WriteString("0000")
	have := pktLine("have " + strings.Repeat("3", 40) + "\n")
	for raw.Len() <= gitFetchBodyMax+len(have) {
		raw.WriteString(have)
	}
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	_, _ = zw.Write(raw.Bytes())
	_ = zw.Close()
	if gz.Len() >= gitFetchBodyMax {
		t.Fatalf("the bomb is %d bytes compressed", gz.Len())
	}

	code, out := postRPC(t, g, edit, pw, "git-upload-pack", &gz, map[string]string{"Content-Encoding": "gzip"})
	if code != http.StatusRequestEntityTooLarge {
		t.Errorf("gzip bomb: %d %q, want 413", code, out)
	}
	code, out = postRPC(t, g, edit, pw, "git-upload-pack", bytes.NewReader(raw.Bytes()), nil)
	if code != http.StatusRequestEntityTooLarge {
		t.Errorf("an uncompressed body over the bound: %d %q, want 413", code, out)
	}
	code, out = postRPC(t, g, edit, pw, "git-upload-pack", strings.NewReader("not gzip"), map[string]string{"Content-Encoding": "gzip"})
	if code != http.StatusBadRequest {
		t.Errorf("a body that is not gzip: %d %q, want 400", code, out)
	}
	// Normal fetches are unaffected.
	cl.mustGit(wd, "fetch", "-q", g.remote(edit, pw))
}

// A request whose body git stops reading part way — a malformed command
// section, a body cut at its bound — must not break the connection's next
// request. (In full-duplex mode net/http reads such a rest only after the
// handler, and one that reaches its end there panicked the next request:
// "invalid concurrent Body.Read call". Go's client retries on a fresh
// connection, so only the server's log shows it; newGitServer fails a test
// on a logged panic.)
func TestGitCutOffBodyKeepsServing(t *testing.T) {
	e := newEnv(t, nil)
	g := newGitServer(t, e)
	edit, pw := gitSite(t, e, nil)
	cl := newGitClient(t)
	cl.mustGit(t.TempDir(), "ls-remote", g.remote(edit, pw)) // creates the repository

	junk := strings.Repeat("j", 100<<10) // well under net/http's 256 KiB post-handler read
	for i := 0; i < 5; i++ {
		code, out := postRPC(t, g, edit, pw, "git-receive-pack", strings.NewReader(pktLine("garbage\n")+junk), nil)
		if code != http.StatusBadRequest {
			t.Fatalf("round %d: malformed push = %d %q, want 400", i, code, out)
		}
		req, _ := http.NewRequest("GET", g.url("/git/"+edit+".git/info/refs?service=git-upload-pack"), nil)
		req.SetBasicAuth("drop", pw)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("round %d: the request after a cut-off body: %v", i, err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("round %d: the request after a cut-off body = %d", i, resp.StatusCode)
		}
	}
}

// throttled hands out head at once, then rest only as gate allows: one
// chunk per receive, everything once gate is closed.
type throttled struct {
	head, rest []byte
	gate       chan struct{}
	open       bool
}

func (r *throttled) Read(p []byte) (int, error) {
	if len(r.head) > 0 {
		n := copy(p, r.head)
		r.head = r.head[n:]
		return n, nil
	}
	if len(r.rest) == 0 {
		return 0, io.EOF
	}
	if !r.open {
		if _, ok := <-r.gate; !ok {
			r.open = true
		}
	}
	n := copy(p, r.rest[:min(len(r.rest), 32<<10)])
	r.rest = r.rest[n:]
	return n, nil
}

// A delete that lands while a push streams cancels the push, waits for its
// git process and removes the whole site: no folder comes back.
func TestGitDeleteDuringPush(t *testing.T) {
	e := newEnv(t, nil)
	g := newGitServer(t, e)
	edit, pw := gitSite(t, e, nil)
	site := siteByEdit(t, e, edit)
	cl := newGitClient(t)
	remote := g.remote(edit, pw)
	cl.mustGit(t.TempDir(), "ls-remote", remote) // creates the repository

	wd := cl.work()
	sha := cl.commit(wd, "big.bin", randomBytes(t, 5<<20))
	pack := packOf(t, cl, wd, sha)
	cmds := pktLine(zeroSHA+" "+sha+" refs/heads/main\x00report-status\n") + "0000"
	body := &throttled{head: append([]byte(cmds), pack[:64<<10]...), rest: pack[64<<10:], gate: make(chan struct{})}

	pushDone := make(chan struct{})
	go func() {
		defer close(pushDone)
		req, _ := http.NewRequest("POST", g.url("/git/"+edit+".git/git-receive-pack"), body)
		req.SetBasicAuth("drop", pw)
		req.Header.Set("Content-Type", "application/x-git-receive-pack-request")
		if resp, err := http.DefaultClient.Do(req); err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}
	}()

	waitReceiving(t, site, body)

	req, _ := http.NewRequest("DELETE", g.url("/api/sites/"+edit), nil)
	req.Header.Set("X-Edit-Password", pw)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		t.Fatalf("delete during a push: %d", resp.StatusCode)
	}
	close(body.gate)
	select {
	case <-pushDone:
	case <-time.After(30 * time.Second):
		t.Fatal("the push never ended")
	}
	if _, err := os.Stat(site.Dir()); !os.IsNotExist(err) {
		t.Fatalf("the site folder after the delete: %v", err)
	}
}

// packOf builds the pack a push of sha would send.
func packOf(t *testing.T, cl *gitClient, wd, sha string) []byte {
	t.Helper()
	cmd := exec.Command("git", "pack-objects", "--revs", "--stdout", "-q")
	cmd.Dir = wd
	cmd.Env = cl.env
	cmd.Stdin = strings.NewReader(sha + "\n")
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// On an accounts instance an anonymous site has no Git, like no WebDAV or
// FTP: the edit password passes the gate and the site is refused after it.
func TestGitAnonymousOnAccountsInstanceRefused(t *testing.T) {
	e := newEnv(t, nil)
	registerProvider(t, &fakeProvider{enabled: true})
	g := newGitServer(t, e)
	edit, pw := gitSite(t, e, nil)
	site := siteByEdit(t, e, edit)
	if site.Meta.OwnerAccountID != "" {
		t.Fatal("the fixture site has an owner")
	}
	cl := newGitClient(t)
	wd := cl.work()
	out := cl.failGit(wd, "ls-remote", g.remote(edit, pw))
	wantOutput(t, out, "remote error: this site was created without an account", "ls-remote of an anonymous site")
	cl.commit(wd, "a.txt", []byte("a"))
	out = cl.failGit(wd, "push", g.remote(edit, pw), "HEAD:main")
	wantOutput(t, out, "remote error: this site was created without an account", "push to an anonymous site")
	if gitrepo.Exists(site.GitDir()) {
		t.Error("an anonymous site got a repository")
	}
}

// An owned site on an accounts instance: the owner's token pushes when the
// plan stamped Git; a site not stamped yet has none; a read-only OAuth grant
// is told it cannot push.
func TestGitOwnedSiteOnAccountsInstance(t *testing.T) {
	e := newEnv(t, oauthEnv())
	f := newGitFake(t)
	yes := true
	f.grant = ext.CreateGrant{Git: &yes}
	g := newGitServer(t, e)
	edit, pw := ownedSiteWithPassword(t, e)
	site := siteByEdit(t, e, edit)
	if site.Meta.OwnerAccountID != "acct-1" || site.Meta.QuotaGit == nil || !*site.Meta.QuotaGit {
		t.Fatalf("fixture: owner %q, git %v", site.Meta.OwnerAccountID, site.Meta.QuotaGit)
	}
	cl := newGitClient(t)
	wd := cl.work()
	pushed(t, cl, wd, g.remote(edit, "sbp_owner"), "a.txt", []byte("a"))
	ps := gitPushes(t, e, site)
	if len(ps) != 1 || ps[0].Auth != provenance.AuthToken || ps[0].Account != "acct-1" {
		t.Errorf("push entries = %+v, want one by acct-1's token", ps)
	}
	// The edit password works too, and another account's token does not.
	cl.mustGit(wd, "ls-remote", g.remote(edit, pw))
	out := cl.failGit(wd, "ls-remote", g.remote(edit, "sbp_other"))
	wantOutput(t, out, "Authentication failed", "another account's token")

	// A read-only grant fetches, and is told why it cannot push.
	cl.mustGit(wd, "ls-remote", g.remote(edit, jwtRead))
	cl.commit(wd, "b.txt", []byte("b"))
	out = cl.failGit(wd, "push", g.remote(edit, jwtRead), "HEAD:main")
	wantOutput(t, out, "remote error: "+msgGitNoWrite, "a read-only grant pushing")
	cl.mustGit(wd, "push", "-q", g.remote(edit, jwtWrite), "HEAD:main")

	// A site not stamped yet (nil) has no Git with accounts on.
	if err := e.st.SetQuotaGit(site, nil); err != nil {
		t.Fatal(err)
	}
	out = cl.failGit(wd, "push", g.remote(edit, "sbp_owner"), "HEAD:main")
	wantOutput(t, out, e.api.apiAccountHint(), "a push to an unstamped site")
}

// The instance's own switches: Git turned off, or no git binary.
func TestGitInstanceRefusals(t *testing.T) {
	t.Run("disabled", func(t *testing.T) {
		e := newEnv(t, map[string]string{"SITEBIN_GIT_ENABLED": "false"})
		if e.api.git != nil {
			t.Error("a runner on an instance with Git turned off")
		}
		edit, pw := gitSite(t, e, nil)
		r := httptest.NewRequest("GET", "/git/"+edit+".git/info/refs?service=git-upload-pack", nil)
		r.SetBasicAuth("drop", pw)
		w := e.public(t, r)
		if w.Code != 200 || !strings.Contains(w.Body.String(), "ERR "+msgGitDisabled) {
			t.Errorf("Git off: %d %q, want the refusal %q", w.Code, w.Body, msgGitDisabled)
		}
	})
	t.Run("unavailable", func(t *testing.T) {
		e := newEnv(t, nil)
		e.api.git = nil // as when no git binary was found at startup
		edit, pw := gitSite(t, e, nil)
		r := httptest.NewRequest("GET", "/git/"+edit+".git/info/refs?service=git-receive-pack", nil)
		r.SetBasicAuth("drop", pw)
		w := e.public(t, r)
		if w.Code != 200 || !strings.Contains(w.Body.String(), "ERR "+msgGitUnavailableHere) {
			t.Errorf("no git: %d %q, want the refusal %q", w.Code, w.Body, msgGitUnavailableHere)
		}
		// An RPC is a plain status: it cannot carry a remote error.
		r = httptest.NewRequest("POST", "/git/"+edit+".git/git-upload-pack", strings.NewReader("0000"))
		r.SetBasicAuth("drop", pw)
		r.Header.Set("Content-Type", "application/x-git-upload-pack-request")
		if w := e.public(t, r); w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), msgGitUnavailableHere) {
			t.Errorf("no git, RPC: %d %q, want 403", w.Code, w.Body)
		}
	})
}

// gitStateOf is the one statement of whether a site has Git, and why not.
func TestGitStateOf(t *testing.T) {
	yes, no := true, false
	e := newEnv(t, nil)
	gitrepo.NeedGit(t)
	edit, _ := gitSite(t, e, nil)
	stamp := func(v *bool) *store.Site {
		site := siteByEdit(t, e, edit)
		if err := e.st.SetQuotaGit(site, v); err != nil {
			t.Fatal(err)
		}
		return siteByEdit(t, e, edit)
	}
	want := func(site *store.Site, w gitState, what string) {
		t.Helper()
		if got := e.api.gitStateOf(site); got != w {
			t.Errorf("%s: %+v, want %+v", what, got, w)
		}
	}
	ok := gitState{Available: true, Writable: true}
	plan := gitState{Reason: "plan"}
	readOnly := gitState{Available: true, Reason: "plan"}

	// Community: nil (never stamped) or true is Git; false is not.
	want(stamp(nil), ok, "community, unstamped")
	want(stamp(&yes), ok, "community, stamped true")
	want(stamp(&no), plan, "community, stamped false, no repository")
	if err := e.api.git.Init(siteByEdit(t, e, edit).GitDir()); err != nil {
		t.Fatal(err)
	}
	want(stamp(&no), readOnly, "community, stamped false, a repository")

	site := stamp(&yes)
	if _, err := e.st.SetLock(site, &store.SiteLock{At: time.Now(), By: store.LockByAdmin}); err != nil {
		t.Fatal(err)
	}
	want(siteByEdit(t, e, edit), gitState{Reason: "locked"}, "locked")
	if _, err := e.st.SetLock(site, nil); err != nil {
		t.Fatal(err)
	}

	// Accounts on: an anonymous site has none; only true is Git.
	registerProvider(t, &fakeProvider{enabled: true})
	want(stamp(&yes), gitState{Reason: "anonymous"}, "accounts, anonymous site")
	if err := e.st.Update(site, func(m *store.Meta) error { m.OwnerAccountID = "acct-1"; return nil }); err != nil {
		t.Fatal(err)
	}
	want(stamp(nil), readOnly, "accounts, owned, unstamped, a repository")
	want(stamp(&yes), ok, "accounts, owned, stamped true")
	want(stamp(&no), readOnly, "accounts, owned, stamped false, a repository")

	// The instance's switches come first.
	saved := e.api.git
	e.api.git = nil
	want(stamp(&yes), gitState{Reason: "unavailable"}, "no git binary")
	e.api.git = saved
	e.api.cfg.GitEnabled = false
	want(stamp(&yes), gitState{Reason: "disabled"}, "Git turned off")
}

// The scope a grant lacks is said in words Git prints.
func TestGitScopeRefusalIsReadable(t *testing.T) {
	e := newEnv(t, oauthEnv())
	newGitFake(t)
	edit, _ := ownedSiteWithPassword(t, e)
	site := siteByEdit(t, e, edit)
	_, d := e.api.gitAuth(gitReq("OAUTH-USER", jwtRead), site, true)
	if d == nil || !d.refusal || d.challenge || d.msg != msgGitNoWrite {
		t.Fatalf("a read grant pushing: %+v, want the refusal %q", d, msgGitNoWrite)
	}
	_, d = e.api.gitAuth(gitReq("OAUTH-USER", jwtNone), site, false)
	if d == nil || !d.refusal || d.challenge || d.msg != msgGitNoRead {
		t.Fatalf("a grant without scopes fetching: %+v, want the refusal %q", d, msgGitNoRead)
	}
	if !strings.Contains(msgGitNoWrite, "write") || !strings.Contains(msgGitNoRead, "read") || mcp.ScopeWrite == "" {
		t.Fatal("the messages do not name what is missing")
	}
}

// What a push changed is read from the refs, not from what it asked.
func TestRefsChangedAndPushDetail(t *testing.T) {
	a, b, c := strings.Repeat("a", 40), strings.Repeat("b", 40), strings.Repeat("c", 40)
	before := map[string]string{"refs/heads/main": a, "refs/heads/gone": b, "refs/heads/kept": c}
	after := map[string]string{"refs/heads/main": b, "refs/heads/new": c, "refs/heads/kept": c}
	got := refsChanged(before, after, []gitrepo.RefUpdate{
		{Ref: "refs/heads/main", Old: a, New: b},        // updated
		{Ref: "refs/heads/new", Old: zeroSHA, New: c},   // created
		{Ref: "refs/heads/gone", Old: b, New: zeroSHA},  // deleted
		{Ref: "refs/heads/kept", Old: a, New: b},        // refused in-band: unchanged
		{Ref: "refs/heads/never", Old: a, New: zeroSHA}, // a delete of nothing
		{Ref: "refs/heads/other", Old: zeroSHA, New: a}, // asked, not there
	})
	want := []pushedRef{
		{ref: "refs/heads/main", old: a, new: b},
		{ref: "refs/heads/new", new: c},
		{ref: "refs/heads/gone", old: b},
	}
	if len(got) != len(want) {
		t.Fatalf("changed = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("changed[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}

	got[0].forced = true
	got = append(got, pushedRef{ref: "refs/tags/v1", new: a}, pushedRef{ref: "refs/tags/v2", new: b})
	d := pushDetail(got, 2048)
	wantDetail := "refs/heads/main aaaaaaa..bbbbbbb forced, refs/heads/new ccccccc created, refs/heads/gone bbbbbbb deleted, 2 more, 2.0 KB received"
	if d != wantDetail {
		t.Errorf("detail = %q, want %q", d, wantDetail)
	}
	if d := pushDetail([]pushedRef{{ref: "refs/heads/main", old: a, new: b, forcedUnknown: true}}, 10); d != "refs/heads/main aaaaaaa..bbbbbbb (forced: unknown), 10 B received" {
		t.Errorf("forced-unknown detail = %q", d)
	}
	d = requestedDetail([]gitrepo.RefUpdate{
		{Ref: "refs/heads/main", Old: a, New: b},
		{Ref: "refs/heads/new", Old: zeroSHA, New: c},
		{Ref: "refs/heads/gone", Old: b, New: zeroSHA},
		{Ref: "refs/heads/more", Old: a, New: c},
	}, 10)
	if d != "requested, not verified: refs/heads/main aaaaaaa to bbbbbbb, refs/heads/new create ccccccc, refs/heads/gone delete bbbbbbb, 1 more, 10 B received" {
		t.Errorf("requested detail = %q", d)
	}
}

// The wire bound is crossed while git is still answering: upload-pack with
// multi_ack_detailed acknowledges each have as it reads it. The bound's
// reader runs on ServeRPC's stdin goroutine, beside git's output going to
// the client, so it must never touch the ResponseWriter (http.MaxBytesReader
// does, when its limit is passed). Under -race this is a data race; without
// it, the connection must still be served cleanly and the next request work.
func TestGitFetchWireBoundWhileGitWrites(t *testing.T) {
	e := newEnv(t, nil)
	g := newGitServer(t, e)
	edit, pw := gitSite(t, e, nil)
	cl := newGitClient(t)
	sha := pushed(t, cl, cl.work(), g.remote(edit, pw), "a.txt", []byte("a"))

	var raw strings.Builder
	raw.WriteString(pktLine("want " + sha + " multi_ack_detailed\n"))
	raw.WriteString("0000")
	have := pktLine("have " + sha + "\n")
	for raw.Len() <= gitFetchBodyMax+len(have) {
		raw.WriteString(have)
	}
	code, out := postRPC(t, g, edit, pw, "git-upload-pack", strings.NewReader(raw.String()), nil)
	if code != 200 || !strings.Contains(out, "ACK "+sha+" common") {
		t.Fatalf("fetch over the wire bound = %d, %d bytes, want git's ACKs streaming", code, len(out))
	}
	// The connection the body was cut on is closed, cleanly: the next
	// request goes through.
	cl.mustGit(t.TempDir(), "ls-remote", g.remote(edit, pw))
}

// The bounds hold with no ResponseWriter in reach: fetchBody is given only
// the request, so whatever goroutine reads it can never touch the writer
// (http.MaxBytesReader would, through the server's own one).
func TestGitBodyBoundsHoldNoWriter(t *testing.T) {
	big := strings.Repeat("h", gitFetchBodyMax+1)
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	_, _ = io.WriteString(zw, big)
	_ = zw.Close()
	for name, req := range map[string]*http.Request{
		"plain": httptest.NewRequest("POST", "/git/x.git/git-upload-pack", strings.NewReader(big)),
		"gzip":  httptest.NewRequest("POST", "/git/x.git/git-upload-pack", bytes.NewReader(gz.Bytes())),
	} {
		if name == "gzip" {
			req.Header.Set("Content-Encoding", "gzip")
		}
		body, err := fetchBody(req)
		if err != nil {
			t.Fatal(err)
		}
		n, err := io.Copy(io.Discard, body)
		if !errors.Is(err, errBodyTooLarge) || n != gitFetchBodyMax || !body.over.Load() || body.failed.Load() {
			t.Errorf("%s: read %d, %v, over %v, failed %v; want the bound at %d", name, n, err, body.over.Load(), body.failed.Load(), gitFetchBodyMax)
		}
	}
	// A wire bound under a decoder shows as the decoder's own.
	wire := &boundedBody{r: strings.NewReader(big), left: 10}
	outer := &boundedBody{r: wire, left: 100}
	if _, err := io.Copy(io.Discard, outer); !errors.Is(err, errBodyTooLarge) || !outer.over.Load() || outer.failed.Load() {
		t.Errorf("a bound beneath: %v, over %v, failed %v", err, outer.over.Load(), outer.failed.Load())
	}
}

// A Git endpoint never redirects: git 2.54 prints "warning: redirecting to"
// after a 401 retry, but the server must answer no 3xx for anything a client
// may ask, dumb-protocol probes and the bare repository URL included.
func TestGitNeverRedirects(t *testing.T) {
	e := newEnv(t, nil)
	g := newGitServer(t, e)
	edit, pw := gitSite(t, e, nil)
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	base := g.srv.URL + "/git/" + edit + ".git"
	for _, tc := range []struct{ method, path string }{
		{"GET", "/info/refs?service=git-upload-pack"},
		{"GET", "/info/refs?service=git-receive-pack"},
		{"GET", "/info/refs"},
		{"GET", "/HEAD"},
		{"GET", "/objects/info/packs"},
		{"GET", ""},
		{"GET", "/"},
		{"POST", "/git-upload-pack"},
		{"POST", "/git-receive-pack"},
	} {
		for _, auth := range []bool{false, true} {
			req, err := http.NewRequest(tc.method, base+tc.path, nil)
			if err != nil {
				t.Fatal(err)
			}
			if auth {
				req.SetBasicAuth("drop", pw)
			}
			resp, err := client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if resp.StatusCode >= 300 && resp.StatusCode < 400 {
				t.Errorf("%s %s (auth %v) = %d, Location %q; a Git endpoint must not redirect", tc.method, tc.path, auth, resp.StatusCode, resp.Header.Get("Location"))
			}
		}
	}
}
