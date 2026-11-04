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

// PruneAge is how long gc keeps an object that nothing reaches any more
// (git gc --prune=<PruneAge before the collection began>, i.e. 1.day.ago).
const PruneAge = 24 * time.Hour

// pruneSlack keeps the collection that follows a push by PruneAge clear of
// the cutoff's edge.
const pruneSlack = 10 * time.Minute

// Collect runs a full garbage collection on dir when one is due (collectDue),
// so history a force push replaced gives its bytes back. (gc --auto would do
// nothing below thousands of loose objects.)
func (r *Runner) Collect(ctx context.Context, dir string) (ran bool, err error) {
	pushed, ok := LastPush(dir)
	if !ok {
		return false, nil
	}
	var collected time.Time
	fi, err := os.Stat(filepath.Join(dir, gcMarker))
	if err == nil {
		collected = fi.ModTime()
	}
	// The marker carries the START of the collection: a push that lands while
	// gc runs is newer than it and is collected next time.
	start := r.now()
	if !collectDue(pushed, collected, err == nil, start) {
		return false, nil
	}
	_, over, err := r.runFor(ctx, GCTimeout, dir, 1<<20, "gc", pruneFlag(start), "--quiet")
	if err != nil {
		return false, err
	}
	if over {
		return false, errors.New("gitrepo: gc printed too much output")
	}
	return true, touchAt(filepath.Join(dir, gcMarker), start)
}

// pruneFlag is gc's --prune for a collection that begins at start: PruneAge
// back from the runner's clock rather than git's "1.day.ago" — the same
// cutoff, which a test can move.
func pruneFlag(start time.Time) string {
	return "--prune=" + start.Add(-PruneAge).UTC().Format("2006-01-02 15:04:05 -0700")
}

// collectDue decides whether a repository last pushed to at pushed, and last
// collected at collected (if haveGC), is collected now:
//
//   - pushed to since its last collection, or never collected: yes;
//   - collected, but that collection began within PruneAge of the push:
//     what the push replaced was still too young to prune then (the sweep
//     runs minutes after a push), so once more when PruneAge has passed
//     since the push. That collection begins after it, so it is the last;
//   - otherwise no: an unchanged repository costs nothing.
func collectDue(pushed, collected time.Time, haveGC bool, now time.Time) bool {
	if !haveGC || pushed.After(collected) {
		return true
	}
	settled := pushed.Add(PruneAge + pruneSlack)
	return collected.Before(settled) && !now.Before(settled)
}
