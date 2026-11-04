package httpapi

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ittrail/sitebin.io/internal/gitrepo"
	"github.com/ittrail/sitebin.io/internal/store"
)

// flakyInspector stands in for the runner in the push bookkeeping, failing
// the queries a test names: the slot contention it models is not something
// a test can produce on demand.
type flakyInspector struct {
	r           *gitrepo.Runner
	refsErr     error
	ancestorErr error
}

func (f flakyInspector) RefTargets(ctx context.Context, dir string) (map[string]string, error) {
	if f.refsErr != nil {
		return nil, f.refsErr
	}
	return f.r.RefTargets(ctx, dir)
}

func (f flakyInspector) IsAncestor(ctx context.Context, dir, old, new string) (bool, error) {
	if f.ancestorErr != nil {
		return false, f.ancestorErr
	}
	return f.r.IsAncestor(ctx, dir, old, new)
}

// A push git finished but whose refs cannot be read afterwards is still
// recorded — as what was asked, never as what happened — and still marked
// for the sweep's collection, since its bytes may have landed.
func TestGitPushUnverifiedRecordsRequested(t *testing.T) {
	e := newEnv(t, nil)
	g := newGitServer(t, e)
	edit, pw := gitSite(t, e, nil)
	site := siteByEdit(t, e, edit)
	cl := newGitClient(t)
	remote := g.remote(edit, pw)
	wd := cl.work()
	first := pushed(t, cl, wd, remote, "a.txt", []byte("a"))

	e.api.gitInspect = flakyInspector{r: e.api.git, refsErr: gitrepo.ErrBusy}
	// The first push marked the repository: move that mark an hour back,
	// so only the second push can bring it to now.
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(filepath.Join(site.GitDir(), "sitebin-pushed"), old, old); err != nil {
		t.Fatal(err)
	}
	marked := time.Now()
	second := cl.commit(wd, "b.txt", []byte("b"))
	cl.mustGit(wd, "-c", "http.userAgent=unverified", "push", "-q", remote, "HEAD:main", "HEAD:refs/heads/side")

	ps := gitPushes(t, e, site)
	en := ps[len(ps)-1]
	if en.UA != "unverified" {
		t.Fatalf("no entry for the unverified push: %+v", ps)
	}
	if !strings.HasPrefix(en.Detail, "requested, not verified: ") {
		t.Errorf("detail = %q, want it labelled requested, not verified", en.Detail)
	}
	wantOutput(t, en.Detail, "refs/heads/main "+first[:7]+" to "+second[:7], "the requested update")
	wantOutput(t, en.Detail, "refs/heads/side create "+second[:7], "the requested creation")
	if strings.Contains(en.Detail, "..") || strings.Contains(en.Detail, " created") {
		t.Errorf("an unverified update in the applied form: %q", en.Detail)
	}
	if at, ok := gitrepo.LastPush(site.GitDir()); !ok || at.Before(marked.Truncate(time.Millisecond)) {
		t.Errorf("LastPush = %v, %v, want at or after %v: an unverified push is still marked", at, ok, marked)
	}
}

// A force check that fails says so; it never passes for a fast-forward.
func TestGitPushForcedUnknown(t *testing.T) {
	e := newEnv(t, nil)
	g := newGitServer(t, e)
	edit, pw := gitSite(t, e, nil)
	site := siteByEdit(t, e, edit)
	cl := newGitClient(t)
	remote := g.remote(edit, pw)
	wd := cl.work()
	first := pushed(t, cl, wd, remote, "a.txt", []byte("a"))

	e.api.gitInspect = flakyInspector{r: e.api.git, ancestorErr: gitrepo.ErrBusy}
	second := cl.commit(wd, "b.txt", []byte("b"))
	cl.mustGit(wd, "-c", "http.userAgent=unknown", "push", "-q", remote, "HEAD:main")
	ps := gitPushes(t, e, site)
	en := ps[len(ps)-1]
	want := "refs/heads/main " + first[:7] + ".." + second[:7] + " (forced: unknown)"
	if en.UA != "unknown" || !strings.Contains(en.Detail, want) {
		t.Errorf("entry = %+v, want detail with %q", en, want)
	}
}

// countingInspector is flakyInspector that also notes each query's
// deadline and how often the force check ran.
type countingInspector struct {
	flakyInspector
	mu        sync.Mutex
	deadlines []time.Time
	ancestors int
}

func (c *countingInspector) note(ctx context.Context) {
	d, ok := ctx.Deadline()
	if !ok {
		d = time.Time{}
	}
	c.mu.Lock()
	c.deadlines = append(c.deadlines, d)
	c.mu.Unlock()
}

func (c *countingInspector) RefTargets(ctx context.Context, dir string) (map[string]string, error) {
	c.note(ctx)
	return c.flakyInspector.RefTargets(ctx, dir)
}

func (c *countingInspector) IsAncestor(ctx context.Context, dir, old, new string) (bool, error) {
	c.note(ctx)
	c.mu.Lock()
	c.ancestors++
	c.mu.Unlock()
	return c.flakyInspector.IsAncestor(ctx, dir, old, new)
}

// The bookkeeping after a push has one deadline for all its queries, and
// stops asking for a slot after the first force check that found none: the
// refs after it are "(forced: unknown)" without waiting again.
func TestGitPushBookkeepingHasOneDeadline(t *testing.T) {
	e := newEnv(t, nil)
	g := newGitServer(t, e)
	edit, pw := gitSite(t, e, nil)
	site := siteByEdit(t, e, edit)
	cl := newGitClient(t)
	remote := g.remote(edit, pw)
	wd := cl.work()
	first := cl.commit(wd, "a.txt", []byte("a"))
	cl.mustGit(wd, "push", "-q", remote, "HEAD:main", "HEAD:refs/heads/side", "HEAD:refs/heads/third")

	ins := &countingInspector{flakyInspector: flakyInspector{r: e.api.git, ancestorErr: gitrepo.ErrBusy}}
	e.api.gitInspect = ins
	second := cl.commit(wd, "b.txt", []byte("b"))
	start := time.Now()
	cl.mustGit(wd, "-c", "http.userAgent=busy", "push", "-q", remote, "HEAD:main", "HEAD:refs/heads/side", "HEAD:refs/heads/third")

	if ins.ancestors != 1 {
		t.Errorf("force checks = %d after the first found no slot, want 1", ins.ancestors)
	}
	if len(ins.deadlines) < 2 {
		t.Fatalf("queries = %d", len(ins.deadlines))
	}
	for i, d := range ins.deadlines {
		if d.IsZero() || d != ins.deadlines[0] {
			t.Errorf("query %d has deadline %v, want the bookkeeping's one %v", i, d, ins.deadlines[0])
		}
	}
	if d := ins.deadlines[0]; d.After(start.Add(postPushBookkeeping + 5*time.Second)) {
		t.Errorf("deadline %v is more than %v after the push began (%v)", d, postPushBookkeeping, start)
	}
	ps := gitPushes(t, e, site)
	en := ps[len(ps)-1]
	if en.UA != "busy" {
		t.Fatalf("no entry for the push: %+v", ps)
	}
	for _, ref := range []string{"main", "side", "third"} {
		wantOutput(t, en.Detail, "refs/heads/"+ref+" "+first[:7]+".."+second[:7]+" (forced: unknown)", ref)
	}
}

// What a push POST answers when it cannot go ahead: Git shows only the
// status there, but the guarantees are the POST's, not the advertisement's.
func TestGitPushPostGuarantees(t *testing.T) {
	e := newEnv(t, nil)
	g := newGitServer(t, e)
	cl := newGitClient(t)
	create := pktLine(zeroSHA+" "+strings.Repeat("1", 40)+" refs/heads/main\x00report-status\n") + "0000" + emptyPack()

	t.Run("registration held", func(t *testing.T) {
		edit, pw := gitSite(t, e, nil)
		cl.mustGit(t.TempDir(), "ls-remote", g.remote(edit, pw)) // creates the repository
		end, err := e.st.BeginGitWrite(siteByEdit(t, e, edit), nil)
		if err != nil {
			t.Fatal(err)
		}
		code, out := postRPC(t, g, edit, pw, "git-receive-pack", strings.NewReader(create), nil)
		end()
		if code != http.StatusConflict || !strings.Contains(out, msgGitRepoBusy) {
			t.Errorf("push POST beside a registration = %d %q, want 409 %q", code, out, msgGitRepoBusy)
		}
	})

	t.Run("full site", func(t *testing.T) {
		edit, pw := gitSite(t, e, map[string]string{"index.html": "0123456789"})
		site := siteByEdit(t, e, edit)
		if err := e.st.Update(site, func(m *store.Meta) error { m.QuotaBytes = 10; return nil }); err != nil {
			t.Fatal(err)
		}
		code, out := postRPC(t, g, edit, pw, "git-receive-pack", strings.NewReader(create), nil)
		if code != http.StatusInsufficientStorage || !strings.Contains(out, "storage full: 10 B of 10 B used") {
			t.Errorf("push POST to a full site = %d %q, want 507 storage full", code, out)
		}
		if gitrepo.Exists(site.GitDir()) {
			t.Error("a push POST to a full site created the repository")
		}
	})

	t.Run("stopped before any output", func(t *testing.T) {
		edit, pw := gitSite(t, e, nil)
		site := siteByEdit(t, e, edit)
		cl.mustGit(t.TempDir(), "ls-remote", g.remote(edit, pw))
		wd := cl.work()
		sha := cl.commit(wd, "big.bin", randomBytes(t, 1<<20))
		pack := packOf(t, cl, wd, sha)
		// report-status only: no side band, so receive-pack writes nothing
		// before it has the whole pack.
		cmds := pktLine(zeroSHA+" "+sha+" refs/heads/main\x00report-status\n") + "0000"
		body := &throttled{head: append([]byte(cmds), pack[:16<<10]...), rest: pack[16<<10:], gate: make(chan struct{})}

		type answer struct {
			code int
			body string
		}
		done := make(chan answer, 1)
		go func() {
			req, _ := http.NewRequest("POST", g.url("/git/"+edit+".git/git-receive-pack"), body)
			req.SetBasicAuth("drop", pw)
			req.Header.Set("Content-Type", "application/x-git-receive-pack-request")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				done <- answer{0, err.Error()}
				return
			}
			b, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			done <- answer{resp.StatusCode, string(b)}
		}()
		waitReceiving(t, site, body)

		// A lock cancels the push before git has said anything.
		if _, err := e.st.SetLock(site, &store.SiteLock{At: time.Now(), By: store.LockByAdmin}); err != nil {
			t.Fatal(err)
		}
		var a answer
		select {
		case a = <-done:
		case <-time.After(30 * time.Second):
			t.Fatal("the stopped push never answered")
		}
		close(body.gate)
		if a.code != http.StatusConflict || !strings.Contains(a.body, msgGitStopped) {
			t.Errorf("stopped push = %d %q, want 409 %q", a.code, a.body, msgGitStopped)
		}
		if n := len(gitPushes(t, e, site)); n != 0 {
			t.Errorf("%d git-push entries for a stopped push", n)
		}
		if refs, err := e.api.git.RefTargets(context.Background(), site.GitDir()); err != nil || len(refs) != 0 {
			t.Errorf("refs after the stopped push: %v, %v", refs, err)
		}
	})
}

// waitReceiving trickles body until git is receiving the pack: its
// quarantine directory (objects/tmp_objdir-incoming-*) exists.
//
// It never lets the last chunk go. A loopback connection buffers megabytes,
// so the trickle runs ahead of git: a 1 MiB pack was wholly on its way
// before git had read its header. A git holding the whole pack can then
// finish between two looks; on CI's runner it did, every time, and the push
// it was meant to stop answered 200. Held back, git waits in its quarantine
// until it is seen and the test lets the rest go (or cuts the push off).
func waitReceiving(t *testing.T, site *store.Site, body *throttled) {
	t.Helper()
	// Taken before the first receive: until then Read never changes rest.
	ticks := (len(body.rest)+throttleChunk-1)/throttleChunk - 1
	deadline := time.Now().Add(20 * time.Second)
	for {
		if m, _ := filepath.Glob(filepath.Join(site.GitDir(), "objects", "tmp_objdir-incoming-*")); len(m) > 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the push never started receiving")
		}
		if ticks <= 0 {
			time.Sleep(20 * time.Millisecond)
			continue
		}
		select {
		case body.gate <- struct{}{}:
			ticks--
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// A git failure that is the server's — a damaged repository — is a 500 and
// an error in the log; one the request caused stays a 400.
func TestGitServerFaultIs500(t *testing.T) {
	e := newEnv(t, nil)
	g := newGitServer(t, e)
	edit, pw := gitSite(t, e, nil)
	site := siteByEdit(t, e, edit)
	cl := newGitClient(t)
	sha := pushed(t, cl, cl.work(), g.remote(edit, pw), "a.txt", []byte("a"))

	// A request cut short: git reads to the end of its input and gives up.
	cut := pktLine("want " + sha + "\n")
	if code, out := postRPC(t, g, edit, pw, "git-upload-pack", strings.NewReader(cut), nil); code != http.StatusBadRequest {
		t.Errorf("a truncated fetch = %d %q, want 400", code, out)
	}

	// The repository is damaged: nothing the request did.
	if err := os.WriteFile(filepath.Join(site.GitDir(), "HEAD"), []byte("not a ref\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	full := pktLine("want "+sha+"\n") + "0000" + pktLine("done\n")
	if code, out := postRPC(t, g, edit, pw, "git-upload-pack", strings.NewReader(full), nil); code != http.StatusInternalServerError {
		t.Errorf("a fetch from a damaged repository = %d %q, want 500", code, out)
	}
	create := pktLine(zeroSHA+" "+sha+" refs/heads/x\x00report-status\n") + "0000" + emptyPack()
	if code, out := postRPC(t, g, edit, pw, "git-receive-pack", strings.NewReader(create), nil); code != http.StatusInternalServerError {
		t.Errorf("a push to a damaged repository = %d %q, want 500", code, out)
	}
}

func TestGitClientFault(t *testing.T) {
	for err, want := range map[error]bool{
		gitrepo.ErrBadRequest: true,
		errors.New("gitrepo: git-upload-pack: exit status 128: fatal: the remote end hung up unexpectedly"):        true,
		errors.New("gitrepo: git-upload-pack: exit status 128: fatal: protocol error: bad line length 9"):          true,
		errors.New("gitrepo: git-upload-pack: exit status 128: fatal: '.' does not appear to be a git repository"): false,
		errors.New("gitrepo: git-receive-pack: exit status 128: fatal: write error: No space left on device"):      false,
	} {
		if got := gitClientFault(err); got != want {
			t.Errorf("gitClientFault(%v) = %v, want %v", err, got, want)
		}
	}
}
