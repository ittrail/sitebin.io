package gitrepo

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
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

// Leftovers of a git that did not end: receive-pack's quarantine
// (objects/tmp_objdir-incoming-*, and objects/incoming-* from older git),
// half-written packs (objects/pack/tmp_*) and a killed repack's packs
// (objects/pack/.tmp-<pid>-pack-*). Git removes them itself when it ends
// normally, and a killed git cannot.
var (
	objectLeftovers = []string{"tmp_objdir-", "incoming-"}
	packLeftovers   = []string{"tmp_", ".tmp-"}
)

// pushKeep starts the .keep file receive-pack holds on a pushed pack until
// its refs are updated. A receive-pack killed in between leaves the pack kept,
// and gc never repacks or prunes a kept pack.
const pushKeep = "receive-pack "

// RemoveLeftovers deletes what a push that was killed left in dir's object
// store, and reports how many entries it removed. They count against the
// site's quota, and no later push or gc removes them: gc runs only on a
// changed repository, and git cleans up only its own temporary files.
//
// Call it only while no git process can write into dir — inside the site's
// registration (store.BeginGitWrite), which every push holds — or it would
// pull a running push's pack from under it. It stays strictly inside dir's
// objects/ and never follows a symbolic link: an objects/ or objects/pack/
// that is a link is left alone, and a link among the leftovers is removed as
// a link.
func RemoveLeftovers(dir string) (removed int, err error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return 0, nil
		}
		return 0, err
	}
	defer root.Close()
	pack := filepath.Join("objects", "pack")
	for _, sub := range []struct {
		dir      string
		prefixes []string
	}{{"objects", objectLeftovers}, {pack, packLeftovers}} {
		real, err := realDir(root, sub.dir)
		if err != nil || !real {
			return removed, err // objects/ that is not git's: not even pack/
		}
		n, err := removePrefixed(root, sub.dir, sub.prefixes)
		removed += n
		if err != nil {
			return removed, err
		}
	}
	n, err := unkeepPushes(root, pack)
	return removed + n, err
}

// unkeepPushes removes the .keep files a killed receive-pack left in root's
// pack directory, so gc can collect their packs again. A .keep anyone else
// wrote (an operator's, say) is left alone.
func unkeepPushes(root *os.Root, pack string) (int, error) {
	f, err := root.Open(pack)
	if err != nil {
		return 0, err
	}
	names, err := f.Readdirnames(-1)
	f.Close()
	if err != nil {
		return 0, err
	}
	removed := 0
	for _, name := range names {
		if !strings.HasPrefix(name, "pack-") || !strings.HasSuffix(name, ".keep") {
			continue
		}
		p := filepath.Join(pack, name)
		if fi, err := root.Lstat(p); err != nil || !fi.Mode().IsRegular() {
			continue
		}
		b, err := root.ReadFile(p)
		if err != nil || !strings.HasPrefix(string(b), pushKeep) {
			continue
		}
		if err := root.Remove(p); err != nil {
			return removed, err
		}
		removed++
	}
	return removed, nil
}

// realDir reports whether root's dir is a directory itself, not a link to
// one (and not missing).
func realDir(root *os.Root, dir string) (bool, error) {
	fi, err := root.Lstat(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	return fi.IsDir(), nil
}

// removePrefixed removes the entries of root's directory dir whose names
// begin with one of prefixes.
func removePrefixed(root *os.Root, dir string, prefixes []string) (int, error) {
	f, err := root.Open(dir)
	if err != nil {
		return 0, err
	}
	names, err := f.Readdirnames(-1)
	f.Close()
	if err != nil {
		return 0, err
	}
	removed := 0
	for _, name := range names {
		for _, p := range prefixes {
			if strings.HasPrefix(name, p) {
				if err := root.RemoveAll(filepath.Join(dir, name)); err != nil {
					return removed, err
				}
				removed++
				break
			}
		}
	}
	return removed, nil
}
