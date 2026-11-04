package gitrepo

import (
	"context"
	"crypto/rand"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCollectSkipsUnchanged(t *testing.T) {
	NeedGit(t)
	r, dir := newBare(t)
	ctx := context.Background()
	if ran, err := r.Collect(ctx, dir); err != nil || ran {
		t.Fatalf("never pushed: ran=%v err=%v", ran, err)
	}
	if _, ok := LastPush(dir); ok {
		t.Fatal("LastPush before any push")
	}
	if err := MarkPushed(dir); err != nil {
		t.Fatal(err)
	}
	if _, ok := LastPush(dir); !ok {
		t.Fatal("no LastPush after MarkPushed")
	}
	if ran, err := r.Collect(ctx, dir); err != nil || !ran {
		t.Fatalf("first: ran=%v err=%v", ran, err)
	}
	if ran, err := r.Collect(ctx, dir); err != nil || ran {
		t.Fatalf("second: ran=%v err=%v", ran, err)
	}
	gcFi, err := os.Stat(filepath.Join(dir, "sitebin-gc"))
	if err != nil {
		t.Fatal(err)
	}
	// A push that landed while gc ran: later than the collection began.
	pushedAt := gcFi.ModTime().Add(time.Millisecond)
	if err := os.Chtimes(filepath.Join(dir, "sitebin-pushed"), pushedAt, pushedAt); err != nil {
		t.Fatal(err)
	}
	if ran, err := r.Collect(ctx, dir); err != nil || !ran {
		t.Fatalf("after new push: ran=%v err=%v", ran, err)
	}
}

func TestCollectShrinksAfterForcePush(t *testing.T) {
	NeedGit(t)
	r, dir := newBare(t)
	srv, _ := serveRepo(t, r, dir, 0)
	c := newClient(t)
	wd := filepath.Join(t.TempDir(), "w")
	c.mustGit(filepath.Dir(wd), "clone", srv.URL+"/repo.git", wd)
	blob := make([]byte, 1<<20)
	if _, err := rand.Read(blob); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wd, "big.bin"), blob, 0o644); err != nil {
		t.Fatal(err)
	}
	c.mustGit(wd, "add", "big.bin")
	c.mustGit(wd, "commit", "-m", "big")
	commitFile(t, c, wd, "a.txt", "2")
	commitFile(t, c, wd, "b.txt", "3")
	c.mustGit(wd, "push", "origin", "main")
	c.mustGit(wd, "checkout", "--orphan", "clean")
	c.mustGit(wd, "rm", "-rf", "--quiet", ".")
	commitFile(t, c, wd, "a.txt", "fresh")
	c.mustGit(wd, "push", "-f", "origin", "clean:main")
	if before, _ := dirSize(t, dir); before < 1<<20 {
		t.Fatalf("setup: repo is only %d bytes", before)
	}
	if err := MarkPushed(dir); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	// The sweep, minutes after the push: what the force push replaced is
	// younger than PruneAge and stays (as unreachable objects). Nothing on
	// disk is aged first: this is the timeline a server sees.
	if ran, err := r.Collect(ctx, dir); err != nil || !ran {
		t.Fatalf("first Collect: ran=%v err=%v", ran, err)
	}
	if mid, _ := dirSize(t, dir); mid < 1<<20 {
		t.Fatalf("the first collection already pruned (%d bytes): the timeline is not the one to test", mid)
	}

	// No sweep before PruneAge has passed collects again.
	r.now = func() time.Time { return time.Now().Add(PruneAge / 2) }
	if ran, err := r.Collect(ctx, dir); err != nil || ran {
		t.Fatalf("Collect half a day later: ran=%v err=%v", ran, err)
	}

	// A day later, no push in between: the runner's clock moves on (git
	// prunes by the cutoff the runner gives it), and once more is due.
	r.now = func() time.Time { return time.Now().Add(PruneAge + pruneSlack + time.Minute) }
	if ran, err := r.Collect(ctx, dir); err != nil || !ran {
		t.Fatalf("Collect a day later: ran=%v err=%v", ran, err)
	}
	if after, names := dirSize(t, dir); after >= 512<<10 {
		t.Fatalf("repo still %d bytes a day after the force push: %v", after, names)
	}

	// And then never again until the next push.
	r.now = func() time.Time { return time.Now().Add(10 * PruneAge) }
	if ran, err := r.Collect(ctx, dir); err != nil || ran {
		t.Fatalf("Collect after the follow-up: ran=%v err=%v", ran, err)
	}
}

func TestCollectDue(t *testing.T) {
	p := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	settled := p.Add(PruneAge + pruneSlack)
	for _, c := range []struct {
		name      string
		collected time.Time
		haveGC    bool
		now       time.Time
		want      bool
	}{
		{"never collected", time.Time{}, false, p.Add(time.Minute), true},
		{"pushed since the last collection", p.Add(-time.Hour), true, p.Add(time.Minute), true},
		{"collected minutes after the push, a day not passed", p.Add(10 * time.Minute), true, p.Add(PruneAge / 2), false},
		{"collected minutes after the push, a day passed", p.Add(10 * time.Minute), true, settled, true},
		{"collected minutes after the push, long after", p.Add(10 * time.Minute), true, p.Add(30 * PruneAge), true},
		{"the follow-up done", settled, true, settled.Add(time.Hour), false},
		{"the follow-up done late", p.Add(3 * PruneAge), true, p.Add(30 * PruneAge), false},
	} {
		if got := collectDue(p, c.collected, c.haveGC, c.now); got != c.want {
			t.Errorf("%s: collectDue = %v, want %v", c.name, got, c.want)
		}
	}
}

// gc is told to prune from the runner's clock, PruneAge back: the same as
// --prune=1.day.ago when the clock is the real one.
func TestPruneFlag(t *testing.T) {
	at := time.Date(2030, 1, 2, 3, 4, 5, 0, time.FixedZone("x", 2*3600))
	if got, want := pruneFlag(at), "--prune=2030-01-01 01:04:05 +0000"; got != want {
		t.Errorf("pruneFlag = %q, want %q", got, want)
	}
}

func TestCollectMarkerIsStampedAtStart(t *testing.T) {
	NeedGit(t)
	r, dir := newBare(t)
	if err := MarkPushed(dir); err != nil {
		t.Fatal(err)
	}
	before := time.Now()
	if ran, err := r.Collect(context.Background(), dir); err != nil || !ran {
		t.Fatalf("Collect: ran=%v err=%v", ran, err)
	}
	after := time.Now()
	fi, err := os.Stat(filepath.Join(dir, "sitebin-gc"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.ModTime().Before(before.Add(-time.Second)) || !fi.ModTime().Before(after) {
		t.Fatalf("gc marker %v not within [%v, %v)", fi.ModTime(), before, after)
	}
	if mid := before.Add(after.Sub(before) / 2); fi.ModTime().After(mid) {
		t.Fatalf("gc marker %v is stamped after gc finished, not at its start (%v..%v)", fi.ModTime(), before, after)
	}
}
