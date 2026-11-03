package gitrepo

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// GCTimeout bounds one garbage collection.
const GCTimeout = 10 * time.Minute

const (
	pushedMarker = "sitebin-pushed"
	gcMarker     = "sitebin-gc"
)

// touch creates path if needed and sets its mtime to now.
func touch(path string) error {
	now := time.Now()
	err := os.Chtimes(path, now, now)
	if err == nil || !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Chtimes(path, now, now)
}

// MarkPushed records that dir received a push. The marker lives inside the
// bare repository, where git ignores it.
func MarkPushed(dir string) error {
	return touch(filepath.Join(dir, pushedMarker))
}

// LastPush is when dir last received a push; false when it never has.
func LastPush(dir string) (time.Time, bool) {
	fi, err := os.Stat(filepath.Join(dir, pushedMarker))
	if err != nil {
		return time.Time{}, false
	}
	return fi.ModTime(), true
}

// Collect runs a full garbage collection on dir, but only when it was pushed
// to since its last one, so history a force push replaced gives its bytes
// back. (gc --auto would do nothing below thousands of loose objects.)
func (r *Runner) Collect(ctx context.Context, dir string) (ran bool, err error) {
	pushed, ok := LastPush(dir)
	if !ok {
		return false, nil
	}
	if fi, err := os.Stat(filepath.Join(dir, gcMarker)); err == nil && !pushed.After(fi.ModTime()) {
		return false, nil
	}
	if _, _, err := r.runFor(ctx, GCTimeout, dir, 1<<20, "gc", "--prune=1.day.ago", "--quiet"); err != nil {
		return false, err
	}
	return true, touch(filepath.Join(dir, gcMarker))
}
