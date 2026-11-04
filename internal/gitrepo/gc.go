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

// touchAt creates path if needed and sets its mtime to t.
func touchAt(path string, t time.Time) error {
	now := t
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
	return touchAt(filepath.Join(dir, pushedMarker), time.Now())
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
	// The marker carries the START of the collection: a push that lands while
	// gc runs is newer than it and is collected next time.
	start := time.Now()
	_, over, err := r.runFor(ctx, GCTimeout, dir, 1<<20, "gc", "--prune=1.day.ago", "--quiet")
	if err != nil {
		return false, err
	}
	if over {
		return false, errors.New("gitrepo: gc printed too much output")
	}
	return true, touchAt(filepath.Join(dir, gcMarker), start)
}
