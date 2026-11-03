package gitrepo

import (
	"context"
	"crypto/rand"
	"io/fs"
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
	time.Sleep(20 * time.Millisecond)
	if err := MarkPushed(dir); err != nil {
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
	old := time.Now().Add(-48 * time.Hour)
	err := filepath.WalkDir(filepath.Join(dir, "objects"), func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		return os.Chtimes(p, old, old)
	})
	if err != nil {
		t.Fatal(err)
	}
	if ran, err := r.Collect(context.Background(), dir); err != nil || !ran {
		t.Fatalf("Collect: ran=%v err=%v", ran, err)
	}
	if after, names := dirSize(t, dir); after >= 512<<10 {
		t.Fatalf("repo still %d bytes after gc: %v", after, names)
	}
}
