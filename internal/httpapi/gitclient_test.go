package httpapi

import (
	"bytes"
	"crypto/rand"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/ittrail/sitebin.io/internal/gitrepo"
)

// A hermetic git client and a real HTTP server around a.Public(), for the
// Git endpoint's tests. The client's environment starts empty, like the
// server's (internal/gitrepo), so neither the developer's config nor a
// credential helper changes what a test sees.

// gitClient runs git as a user would, with no config of its own.
type gitClient struct {
	t   *testing.T
	env []string
}

func newGitClient(t *testing.T) *gitClient {
	t.Helper()
	home := t.TempDir()
	empty := filepath.Join(home, "empty.gitconfig")
	if err := os.WriteFile(empty, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + home,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=" + empty,
		"GIT_TERMINAL_PROMPT=0",
	}
	if runtime.GOOS == "windows" {
		env = append(env, "SYSTEMROOT="+os.Getenv("SYSTEMROOT"), "TEMP="+os.TempDir(), "TMP="+os.TempDir())
	}
	return &gitClient{t: t, env: env}
}

// git runs git in dir and returns its combined output.
func (c *gitClient) git(dir string, args ...string) (string, error) {
	c.t.Helper()
	full := append([]string{"-c", "user.name=t", "-c", "user.email=t@t", "-c", "init.defaultBranch=main", "-c", "credential.helper="}, args...)
	cmd := exec.Command("git", full...)
	cmd.Dir = dir
	cmd.Env = c.env
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (c *gitClient) mustGit(dir string, args ...string) string {
	c.t.Helper()
	out, err := c.git(dir, args...)
	if err != nil {
		c.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(out)
}

// failGit runs git and expects it to fail, returning its output.
func (c *gitClient) failGit(dir string, args ...string) string {
	c.t.Helper()
	out, err := c.git(dir, args...)
	if err == nil {
		c.t.Fatalf("git %v succeeded, want a failure\n%s", args, out)
	}
	return out
}

// work makes a fresh working repository with main as its branch.
func (c *gitClient) work() string {
	c.t.Helper()
	wd := c.t.TempDir()
	c.mustGit(wd, "init", "-q")
	return wd
}

// commit writes name with content into wd and commits it; it returns the
// new commit's sha.
func (c *gitClient) commit(wd, name string, content []byte) string {
	c.t.Helper()
	if err := os.WriteFile(filepath.Join(wd, name), content, 0o644); err != nil {
		c.t.Fatal(err)
	}
	c.mustGit(wd, "add", name)
	c.mustGit(wd, "commit", "-q", "-m", "add "+name)
	return c.mustGit(wd, "rev-parse", "HEAD")
}

// randomBytes is n incompressible bytes, so a pack is as big as its content.
func randomBytes(t *testing.T, n int) []byte {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return b
}

// gitServer is a real HTTP server around the API's public handler, as
// Caddy proxies it: every request arrives with Host set to host (the base
// domain unless a test says otherwise).
type gitServer struct {
	srv *httptest.Server

	mu   sync.Mutex
	host string
}

// newGitServer starts the server. Call it after any provider is registered:
// the handler is built once, at the first request.
func newGitServer(t *testing.T, e *env) *gitServer {
	t.Helper()
	gitrepo.NeedGit(t)
	if e.api.git == nil {
		t.Fatal("the API has no git runner")
	}
	g := &gitServer{host: e.cfg.BaseDomain}
	var once sync.Once
	var h http.Handler
	g.srv = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		once.Do(func() { h = e.api.Public() })
		g.mu.Lock()
		r.Host = g.host
		g.mu.Unlock()
		h.ServeHTTP(w, r)
	}))
	// A handler that panics is recovered by net/http and only logged, and
	// Go's own client retries on a fresh connection: the log is where it
	// shows. (The panic that matters here — net/http's "invalid concurrent
	// Body.Read call" — fires on the connection's next request.)
	errLog := &lockedBuffer{}
	g.srv.Config.ErrorLog = log.New(errLog, "", 0)
	t.Cleanup(func() {
		if s := errLog.String(); strings.Contains(s, "panic") {
			t.Errorf("the server panicked:\n%s", s)
		}
	})
	g.srv.Start()
	t.Cleanup(g.srv.Close)
	return g
}

// lockedBuffer is a bytes.Buffer the server's goroutines can share.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// setHost changes the Host every later request carries.
func (g *gitServer) setHost(h string) {
	g.mu.Lock()
	g.host = h
	g.mu.Unlock()
}

// remote is the repository URL of the site with edit id edit, carrying pw
// as the Basic password (the username is not checked).
func (g *gitServer) remote(edit, pw string) string {
	u, err := url.Parse(g.srv.URL)
	if err != nil {
		panic(err)
	}
	u.User = url.UserPassword("drop", pw)
	u.Path = "/git/" + edit + ".git"
	return u.String()
}

// url is path on the server, without credentials.
func (g *gitServer) url(path string) string { return g.srv.URL + path }
