package gitrepo

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// RemoveLeftovers takes what an interrupted push leaves in the object store
// and nothing else.
func TestRemoveLeftovers(t *testing.T) {
	NeedGit(t)
	r, dir := newBare(t)
	gitOut(t, r, dir, "hash-object", "-t", "tree", "-w", "--stdin") // a real loose object
	before, _ := dirSize(t, dir)
	plant := func(rel string, n int) {
		t.Helper()
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, make([]byte, n), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	plant("objects/tmp_objdir-incoming-aB3dEf/pack/tmp_pack_x1y2z3", 3<<20)
	plant("objects/tmp_objdir-incoming-aB3dEf/ab/cdef", 100)
	plant("objects/incoming-Q1w2E3/pack/tmp_pack_q", 1<<20)
	plant("objects/pack/tmp_pack_Zz9", 2<<20)
	plant("objects/pack/tmp_idx_Zz9", 10)
	n, err := RemoveLeftovers(dir)
	if err != nil {
		t.Fatal(err)
	}
	if n != 4 {
		t.Errorf("removed %d entries, want 4", n)
	}
	if after, names := dirSize(t, dir); after != before {
		t.Errorf("repository is %d bytes after the removal, want %d as before: %v", after, before, names)
	}
	// The repository still works, its object included.
	gitOut(t, r, dir, "cat-file", "-e", "4b825dc642cb6eb9a060e54bf8d69288fbee4904")
	if n, err := RemoveLeftovers(dir); err != nil || n != 0 {
		t.Errorf("second removal: %d, %v", n, err)
	}
	if n, err := RemoveLeftovers(filepath.Join(t.TempDir(), "missing")); err != nil || n != 0 {
		t.Errorf("a missing repository: %d, %v", n, err)
	}
}

// RemoveLeftovers never follows a link out of the repository: a leftover
// that is a link goes as a link, and an objects/pack that is a link is not
// entered.
func TestRemoveLeftoversNeverFollowsLinks(t *testing.T) {
	NeedGit(t)
	_, dir := newBare(t)
	outside := t.TempDir()
	keep := filepath.Join(outside, "tmp_pack_keepme")
	if err := os.WriteFile(keep, []byte("not the repository's"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "objects", "tmp_objdir-incoming-link")); err != nil {
		t.Skipf("cannot create a symbolic link here: %v", err)
	}
	if _, err := RemoveLeftovers(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(dir, "objects", "tmp_objdir-incoming-link")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the leftover link is still there: %v", err)
	}
	if _, err := os.Stat(keep); err != nil {
		t.Fatalf("a file behind the link was removed: %v", err)
	}

	// objects/pack itself a link to outside: left alone.
	pack := filepath.Join(dir, "objects", "pack")
	if err := os.RemoveAll(pack); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, pack); err != nil {
		t.Fatal(err)
	}
	if _, err := RemoveLeftovers(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(keep); err != nil {
		t.Fatalf("a file behind a linked objects/pack was removed: %v", err)
	}
}
