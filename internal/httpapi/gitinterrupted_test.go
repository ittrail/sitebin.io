package httpapi

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/ittrail/sitebin.io/internal/gitrepo"
)

// A push that git started and that did not end cleanly — here the client
// hangs up halfway through its pack — marks the repository, so the sweep
// collects what it may have left; it records nothing and changes no ref.
func TestGitInterruptedPushIsMarked(t *testing.T) {
	e := newEnv(t, nil)
	g := newGitServer(t, e)
	edit, pw := gitSite(t, e, nil)
	site := siteByEdit(t, e, edit)
	cl := newGitClient(t)
	cl.mustGit(t.TempDir(), "ls-remote", g.remote(edit, pw)) // creates the repository, unmarked
	if _, ok := gitrepo.LastPush(site.GitDir()); ok {
		t.Fatal("a fresh repository is marked")
	}

	wd := cl.work()
	sha := cl.commit(wd, "big.bin", randomBytes(t, 5<<20))
	pack := packOf(t, cl, wd, sha)
	cmds := pktLine(zeroSHA+" "+sha+" refs/heads/main\x00report-status\n") + "0000"
	body := &throttled{head: append([]byte(cmds), pack[:64<<10]...), rest: pack[64<<10:], gate: make(chan struct{})}
	ctx, hangUp := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		req, _ := http.NewRequestWithContext(ctx, "POST", g.url("/git/"+edit+".git/git-receive-pack"), body)
		req.SetBasicAuth("drop", pw)
		req.Header.Set("Content-Type", "application/x-git-receive-pack-request")
		if resp, err := http.DefaultClient.Do(req); err == nil {
			resp.Body.Close()
		}
	}()
	waitReceiving(t, site, body)
	hangUp()

	deadline := time.Now().Add(20 * time.Second)
	for {
		if _, ok := gitrepo.LastPush(site.GitDir()); ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the interrupted push did not mark the repository")
		}
		time.Sleep(20 * time.Millisecond)
	}
	// The server is done with it; let the client's body writer, still
	// waiting on the gate, run out against the closed connection.
	close(body.gate)
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("the client never returned")
	}
	if n := len(gitPushes(t, e, site)); n != 0 {
		t.Errorf("%d git-push entries for an interrupted push", n)
	}
	if refs, err := e.api.git.RefTargets(context.Background(), site.GitDir()); err != nil || len(refs) != 0 {
		t.Errorf("refs after the interrupted push: %v, %v", refs, err)
	}
}
