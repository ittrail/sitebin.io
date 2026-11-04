package gitrepo

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRefTargetsSnapshotsEveryRef(t *testing.T) {
	NeedGit(t)
	r, dir := newBare(t)
	ctx := context.Background()

	got, err := r.RefTargets(ctx, dir)
	if err != nil || len(got) != 0 {
		t.Fatalf("empty repository: %v, %v", got, err)
	}

	srv, _ := serveRepo(t, r, dir, 0)
	c := newClient(t)
	wd := t.TempDir()
	c.mustGit(wd, "init", "-q")
	one := commitFile(t, c, wd, "a.txt", "a")
	c.mustGit(wd, "tag", "-a", "-m", "v1", "v1")
	tag := c.mustGit(wd, "rev-parse", "v1")
	c.mustGit(wd, "push", "-q", srv.URL+"/repo.git", "HEAD:main", "HEAD:refs/heads/side", "v1")

	got, err = r.RefTargets(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"refs/heads/main": one, "refs/heads/side": one, "refs/tags/v1": tag}
	if len(got) != len(want) {
		t.Fatalf("refs = %v, want %v", got, want)
	}
	for ref, sha := range want {
		if got[ref] != sha {
			t.Errorf("%s = %q, want %q (an annotated tag is its tag object)", ref, got[ref], sha)
		}
	}
}

func TestIsAncestor(t *testing.T) {
	NeedGit(t)
	r, dir := newBare(t)
	ctx := context.Background()
	srv, _ := serveRepo(t, r, dir, 0)
	c := newClient(t)
	wd := t.TempDir()
	c.mustGit(wd, "init", "-q")
	one := commitFile(t, c, wd, "a.txt", "a")
	two := commitFile(t, c, wd, "b.txt", "b")
	c.mustGit(wd, "reset", "-q", "--hard", one)
	other := commitFile(t, c, wd, "c.txt", "c")
	c.mustGit(wd, "push", "-q", srv.URL+"/repo.git", two+":refs/heads/main", other+":refs/heads/other")

	for _, tc := range []struct {
		old, new string
		want     bool
	}{
		{one, two, true},    // a fast-forward
		{two, other, false}, // history replaced
		{two, one, false},   // a rewind
		{one, one, true},
	} {
		got, err := r.IsAncestor(ctx, dir, tc.old, tc.new)
		if err != nil || got != tc.want {
			t.Errorf("IsAncestor(%.7s, %.7s) = %v, %v, want %v", tc.old, tc.new, got, err, tc.want)
		}
	}
	for _, bad := range [][2]string{{"--output=x", two}, {one, "HEAD"}, {"", two}, {one, "main~1"}} {
		if _, err := r.IsAncestor(ctx, dir, bad[0], bad[1]); !errors.Is(err, ErrBadRef) {
			t.Errorf("IsAncestor(%q, %q) err = %v, want ErrBadRef", bad[0], bad[1], err)
		}
	}
	// A sha the repository does not hold is an error, never "forced".
	if _, err := r.IsAncestor(ctx, dir, "1111111111111111111111111111111111111111", two); err == nil {
		t.Error("an unknown commit answered without an error")
	}
}

// Bookkeeping after a push may wait longer for a slot than a request does.
func TestWithSlotWait(t *testing.T) {
	NeedGit(t)
	r, dir := newBareMax(t, 1)
	ctx := context.Background()
	hold, err := r.acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}

	// A short wait gives up as soon as it says.
	start := time.Now()
	if _, err := r.RefTargets(WithSlotWait(ctx, 100*time.Millisecond), dir); !errors.Is(err, ErrBusy) {
		t.Fatalf("short wait: %v, want ErrBusy", err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("a 100 ms wait took %v", d)
	}

	// A long one outlasts the default five seconds: the slot comes free
	// after them, and the call still gets it.
	done := make(chan error, 1)
	go func() {
		_, err := r.RefTargets(WithSlotWait(ctx, 30*time.Second), dir)
		done <- err
	}()
	time.Sleep(acquireWait + 500*time.Millisecond)
	select {
	case err := <-done:
		t.Fatalf("returned before the slot came free: %v", err)
	default:
	}
	hold()
	if err := <-done; err != nil {
		t.Fatalf("long wait: %v", err)
	}
}
