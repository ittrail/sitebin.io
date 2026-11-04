package httpapi

import (
	"strings"
	"testing"
)

// Two pushes from one client within the merge window are two entries, each
// with its own refs and bytes, and the Git tab's last push is the newer one.
// (Merged, the second's detail was folded into the first's and could be cut
// off, and last_push showed the older refs.)
func TestGitPushesAreNeverMerged(t *testing.T) {
	e := newEnv(t, nil)
	g := newGitServer(t, e)
	edit, pw := gitSite(t, e, nil)
	site := siteByEdit(t, e, edit)
	cl := newGitClient(t)
	remote := g.remote(edit, pw)
	wd := cl.work()
	first := cl.commit(wd, "a.txt", []byte("a"))
	cl.mustGit(wd, "push", "-q", remote, "HEAD:refs/heads/one")
	second := cl.commit(wd, "b.txt", []byte("b"))
	cl.mustGit(wd, "push", "-q", remote, "HEAD:refs/heads/two")

	ps := gitPushes(t, e, site)
	if len(ps) != 2 {
		t.Fatalf("%d git-push entries for two pushes, want 2: %+v", len(ps), ps)
	}
	for i, want := range []string{"refs/heads/one " + first[:7] + " created", "refs/heads/two " + second[:7] + " created"} {
		if ps[i].Times() != 1 || !strings.Contains(ps[i].Detail, want) {
			t.Errorf("entry %d = %+v, want its own detail %q", i, ps[i], want)
		}
	}
	if strings.Contains(ps[0].Detail, "refs/heads/two") {
		t.Errorf("the first entry carries the second push: %q", ps[0].Detail)
	}
	in := gitInfo(t, e, edit, pw)
	if in.LastPush == nil || !strings.Contains(in.LastPush.Detail, "refs/heads/two") || strings.Contains(in.LastPush.Detail, "refs/heads/one") {
		t.Errorf("last_push = %+v, want the newer push", in.LastPush)
	}
}
