// Package gitrepo wraps the system git binary. Every git process it starts
// runs with a fixed environment and a fixed -c list, so a repository (which a
// user controls) cannot change how git behaves on the server.
package gitrepo

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

var (
	// ErrNoGit means the git binary was not found.
	ErrNoGit = errors.New("gitrepo: git binary not found")
	// ErrBusy means every git process slot stayed taken for the whole wait.
	ErrBusy = errors.New("gitrepo: too many git processes running")
	// ErrNotRepo means a directory holds no repository.
	ErrNotRepo = errors.New("gitrepo: not a git repository")
)

// acquireWait bounds how long acquire waits for a free slot.
const acquireWait = 5 * time.Second

// Options configure a Runner.
type Options struct {
	Bin          string // git binary name or path; empty means "git"
	StateDir     string // <data>/gitstate: empty config, home, empty hooks
	MaxProcesses int    // git processes at once; below 1 means 4
}

// Runner starts git processes.
type Runner struct {
	bin   string
	state string
	slots chan struct{}
}

// New finds the git binary and prepares the state directory.
func New(o Options) (*Runner, error) {
	if o.Bin == "" {
		o.Bin = "git"
	}
	bin, err := exec.LookPath(o.Bin)
	if err != nil {
		return nil, ErrNoGit
	}
	if o.MaxProcesses < 1 {
		o.MaxProcesses = 4
	}
	for _, d := range []string{o.StateDir, filepath.Join(o.StateDir, "home"), filepath.Join(o.StateDir, "empty-hooks"), filepath.Join(o.StateDir, "empty-template")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return nil, fmt.Errorf("gitrepo: %w", err)
		}
	}
	if err := os.WriteFile(filepath.Join(o.StateDir, "empty.gitconfig"), nil, 0o644); err != nil {
		return nil, fmt.Errorf("gitrepo: %w", err)
	}
	return &Runner{bin: bin, state: o.StateDir, slots: make(chan struct{}, o.MaxProcesses)}, nil
}

// command builds a git process running in dir. Its environment starts empty
// (only PATH is copied in) and the server's -c settings precede args.
func (r *Runner) command(ctx context.Context, dir string, args ...string) *exec.Cmd {
	return r.commandWith(ctx, dir, nil, nil, args...)
}

// commandWith is command plus extra -c settings ("key=value") and extra
// environment entries (such as GIT_PROTOCOL) for one call.
func (r *Runner) commandWith(ctx context.Context, dir string, extraC, extraEnv []string, args ...string) *exec.Cmd {
	full := []string{
		"-c", "core.hooksPath=" + filepath.Join(r.state, "empty-hooks"),
		"-c", "core.logAllRefUpdates=false",
		"-c", "receive.fsckObjects=true",
		"-c", "transfer.fsckObjects=true",
		"-c", "receive.autogc=false",
		"-c", "uploadpack.allowAnySHA1InWant=false",
		"-c", "pack.threads=1",
		"-c", "pack.windowMemory=64m",
		"-c", "core.packedGitLimit=128m",
	}
	for _, c := range extraC {
		full = append(full, "-c", c)
	}
	full = append(full, args...)
	cmd := exec.CommandContext(ctx, r.bin, full...)
	cmd.Dir = dir
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_ATTR_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=" + filepath.Join(r.state, "empty.gitconfig"),
		"HOME=" + filepath.Join(r.state, "home"),
		"GIT_TERMINAL_PROMPT=0",
	}
	cmd.Env = append(cmd.Env, extraEnv...)
	return cmd
}

// slotWaitKey carries a caller's own slot wait (WithSlotWait).
type slotWaitKey struct{}

// WithSlotWait lets the git calls made with ctx wait up to d for a process
// slot, instead of the five seconds a request gets. For bookkeeping that
// must not give up as easily: what a push changed is read after it, while
// other requests may hold every slot.
func WithSlotWait(ctx context.Context, d time.Duration) context.Context {
	return context.WithValue(ctx, slotWaitKey{}, d)
}

// acquire takes a process slot, waiting at most five seconds (or what
// WithSlotWait set, or until ctx ends) before it gives up with ErrBusy.
func (r *Runner) acquire(ctx context.Context) (release func(), err error) {
	wait := acquireWait
	if d, ok := ctx.Value(slotWaitKey{}).(time.Duration); ok && d > 0 {
		wait = d
	}
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case r.slots <- struct{}{}:
		return func() { <-r.slots }, nil
	case <-t.C:
		return nil, ErrBusy
	case <-ctx.Done():
		return nil, ErrBusy
	}
}

// Exists reports whether dir holds a git repository.
func Exists(dir string) bool {
	for _, n := range []string{"HEAD", "objects"} {
		if _, err := os.Stat(filepath.Join(dir, n)); err != nil {
			return false
		}
	}
	return true
}

// Init creates a bare repository with main as its default branch. An existing
// repository is left as it is.
func (r *Runner) Init(dir string) error {
	if Exists(dir) {
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	release, err := r.acquire(context.Background())
	if err != nil {
		return err
	}
	defer release()
	if out, err := r.command(context.Background(), dir, "init", "--bare", "--initial-branch=main", "--template="+filepath.Join(r.state, "empty-template"), ".").CombinedOutput(); err != nil {
		return fmt.Errorf("gitrepo: init: %w: %s", err, out)
	}
	return nil
}
