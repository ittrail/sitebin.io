package gitrepo

import (
	"context"
	"errors"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newRunner(t *testing.T, max int) (*Runner, string) {
	t.Helper()
	bin := NeedGit(t)
	state := filepath.Join(t.TempDir(), "gitstate")
	r, err := New(Options{Bin: bin, StateDir: state, MaxProcesses: max})
	if err != nil {
		t.Fatal(err)
	}
	return r, state
}

func gitOut(t *testing.T, r *Runner, dir string, args ...string) string {
	t.Helper()
	out, err := r.command(context.Background(), dir, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestInitCreatesBareMainRepo(t *testing.T) {
	r, _ := newRunner(t, 2)
	dir := filepath.Join(t.TempDir(), "git")
	if err := r.Init(dir); err != nil {
		t.Fatal(err)
	}
	if !Exists(dir) {
		t.Fatal("Exists is false after Init")
	}
	if got := gitOut(t, r, dir, "symbolic-ref", "HEAD"); got != "refs/heads/main" {
		t.Errorf("HEAD = %q", got)
	}
}

func TestInitIsIdempotent(t *testing.T) {
	r, _ := newRunner(t, 2)
	dir := filepath.Join(t.TempDir(), "git")
	if err := r.Init(dir); err != nil {
		t.Fatal(err)
	}
	gitOut(t, r, dir, "hash-object", "-t", "tree", "-w", "--stdin") // writes the empty tree
	commit := commitEmpty(t, r, dir)
	gitOut(t, r, dir, "update-ref", "refs/heads/main", commit)
	if err := r.Init(dir); err != nil {
		t.Fatalf("second Init: %v", err)
	}
	if got := gitOut(t, r, dir, "rev-parse", "refs/heads/main"); got != commit {
		t.Errorf("main = %q, want %q", got, commit)
	}
}

func commitEmpty(t *testing.T, r *Runner, dir string) string {
	t.Helper()
	cmd := r.command(context.Background(), dir, "commit-tree", "4b825dc642cb6eb9a060e54bf8d69288fbee4904", "-m", "x")
	cmd.Env = append(cmd.Env,
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("commit-tree: %v\n%s", err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestCommandEnvIsFixed(t *testing.T) {
	r, state := newRunner(t, 1)
	t.Setenv("GIT_DIR", "/evil")
	t.Setenv("GIT_CONFIG_PARAMETERS", "'core.hooksPath'='/x'")
	cmd := r.command(context.Background(), t.TempDir(), "status")
	for _, e := range cmd.Env {
		if strings.HasPrefix(e, "GIT_DIR=") || strings.HasPrefix(e, "GIT_CONFIG_PARAMETERS=") {
			t.Errorf("environment leaks %q", e)
		}
	}
	want := []string{"GIT_CONFIG_NOSYSTEM=1", "GIT_ATTR_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0",
		"GIT_CONFIG_GLOBAL=" + filepath.Join(state, "empty.gitconfig"),
		"HOME=" + filepath.Join(state, "home")}
	for _, w := range want {
		found := false
		for _, e := range cmd.Env {
			if e == w {
				found = true
			}
		}
		if !found {
			t.Errorf("environment lacks %q: %v", w, cmd.Env)
		}
	}
}

func TestCommandArgsCarryServerConfig(t *testing.T) {
	r, state := newRunner(t, 1)
	cmd := r.command(context.Background(), t.TempDir(), "status")
	args := strings.Join(cmd.Args, "\x00")
	for _, w := range []string{"core.hooksPath=" + filepath.Join(state, "empty-hooks"), "receive.fsckObjects=true"} {
		i := strings.Index(args, "-c\x00"+w)
		if i < 0 {
			t.Errorf("args lack -c %s: %v", w, cmd.Args)
			continue
		}
		if j := strings.Index(args, "\x00status"); j < i {
			t.Errorf("-c %s comes after the subcommand", w)
		}
	}
}

func TestAcquireBusyAfterWait(t *testing.T) {
	r, _ := newRunner(t, 1)
	release, err := r.acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := r.acquire(ctx); !errors.Is(err, ErrBusy) {
		t.Errorf("err = %v, want ErrBusy", err)
	}
}

func TestNewWithoutGit(t *testing.T) {
	if _, err := New(Options{Bin: "definitely-not-git", StateDir: t.TempDir()}); !errors.Is(err, ErrNoGit) {
		t.Errorf("err = %v, want ErrNoGit", err)
	}
}

// A fresh repository holds no copy of git's default template (about 21 KB of
// sample hooks, and info/exclude, description): the bytes count against the
// site's quota.
func TestInitCopiesNoTemplate(t *testing.T) {
	r, _ := newRunner(t, 2)
	dir := filepath.Join(t.TempDir(), "git")
	if err := r.Init(dir); err != nil {
		t.Fatal(err)
	}
	var total int64
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if strings.HasSuffix(p, ".sample") {
			t.Errorf("template file copied: %s", p)
		}
		if !d.IsDir() {
			info, err := d.Info()
			if err != nil {
				return err
			}
			total += info.Size()
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if total > 4<<10 {
		t.Errorf("a fresh repository is %d bytes, want under 4 KiB", total)
	}
	// Still a working repository.
	gitOut(t, r, dir, "hash-object", "-t", "tree", "-w", "--stdin")
}
