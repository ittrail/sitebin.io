// Package snapshots reads the instance's backups — a Borg repository or a
// folder of `sitebin backup` archives — for the agent that runs beside the
// instance on its host, and is the client the instance asks that agent with.
//
// The instance never holds backup credentials: the agent does, on the host,
// and answers over a unix socket only the container mounts. See
// docs/superpowers/specs/2026-11-07-snapshots-and-restore-design.md.
package snapshots

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"
)

// ErrNotFound is a snapshot, or a file in one, that does not exist.
var ErrNotFound = errors.New("not found")

// Snapshot is one backup of the whole data directory.
type Snapshot struct {
	ID   string    `json:"id"`
	At   time.Time `json:"at"`
	Kind string    `json:"kind"`
}

// Item is one entry of a snapshot, its path relative to the data root.
type Item struct {
	Path    string
	Size    int64
	Mtime   time.Time
	Regular bool
}

// Source is where the snapshots are. Every id passed to it must be one its
// List returned; the sources check the form again before running anything.
type Source interface {
	// List answers every snapshot, newest first.
	List(ctx context.Context) ([]Snapshot, error)
	// Walk calls fn for every entry under prefix (data-root relative).
	Walk(ctx context.Context, id, prefix string, fn func(Item) error) error
	// Tar writes a tar of the entries at or under paths (all when nil).
	Tar(ctx context.Context, id string, paths []string, w io.Writer) error
	// ReadFile answers one regular file, refusing one larger than max.
	ReadFile(ctx context.Context, id, path string, max int64) ([]byte, error)
}

// KindOf names a snapshot's kind from its name, as sitebin-borg names them.
func KindOf(name string) string {
	for _, k := range []string{"hourly", "nightly", "predeploy"} {
		if strings.HasPrefix(name, k+"-") {
			return k
		}
	}
	return "archive"
}

var (
	viewIDRe     = regexp.MustCompile(`^[a-z2-7]{26}$`)
	snapshotIDRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
)

// ValidViewID reports whether s has the form of a site's view id.
func ValidViewID(s string) bool { return viewIDRe.MatchString(s) }

// ValidSnapshotID reports whether s is a name the sources accept: it becomes
// a file name and a command-line argument, so nothing that could be read as
// an option, a path or a Borg location.
func ValidSnapshotID(s string) bool { return snapshotIDRe.MatchString(s) }

// Fingerprinter sums a set of files to a value that changes when any file's
// path, size or modification second does. It is a set's sum — the XOR of one
// SHA-256 per file — so the order an archive lists its files in does not
// matter, and seconds only, so Borg's nanoseconds and a tar's seconds agree.
type Fingerprinter struct{ sum [sha256.Size]byte }

// Add counts one regular file, its path relative to the site's files/.
func (f *Fingerprinter) Add(path string, size int64, mtime time.Time) {
	d := sha256.Sum256(fmt.Appendf(nil, "%s\x00%d\x00%d", path, size, mtime.Unix()))
	for i := range f.sum {
		f.sum[i] ^= d[i]
	}
}

// Sum is the fingerprint, hex.
func (f *Fingerprinter) Sum() string { return hex.EncodeToString(f.sum[:]) }

// SiteSummary is what one snapshot holds of one site's files.
type SiteSummary struct {
	Files       int       `json:"files"`
	Bytes       int64     `json:"bytes"`
	Newest      time.Time `json:"newest"`
	Fingerprint string    `json:"fingerprint"`
}

type summing struct {
	SiteSummary
	fp   Fingerprinter
	meta bool
}

// Summarize walks a snapshot's sites/ once and answers, per site that has a
// meta.json in it, what its files/ holds.
func Summarize(ctx context.Context, src Source, id string) (map[string]SiteSummary, error) {
	acc := map[string]*summing{}
	err := src.Walk(ctx, id, "sites/", func(it Item) error {
		rest, ok := strings.CutPrefix(it.Path, "sites/")
		if !ok {
			return nil
		}
		view, sub, _ := strings.Cut(rest, "/")
		if !ValidViewID(view) || !it.Regular {
			return nil
		}
		s := acc[view]
		if s == nil {
			s = &summing{}
			acc[view] = s
		}
		if sub == "meta.json" {
			s.meta = true
			return nil
		}
		rel, ok := strings.CutPrefix(sub, "files/")
		if !ok || rel == "" {
			return nil
		}
		s.Files++
		s.Bytes += it.Size
		if it.Mtime.After(s.Newest) {
			s.Newest = it.Mtime
		}
		s.fp.Add(rel, it.Size, it.Mtime)
		return nil
	})
	if err != nil {
		return nil, err
	}
	out := make(map[string]SiteSummary, len(acc))
	for view, s := range acc {
		if !s.meta {
			continue
		}
		s.Fingerprint = s.fp.Sum()
		s.Newest = s.Newest.UTC()
		out[view] = s.SiteSummary
	}
	return out, nil
}

// Version is one state of a site's files: a run of snapshots, next to each
// other in time, that hold the same files. Snapshot is the run's newest, the
// one a restore reads.
type Version struct {
	Snapshot    string    `json:"snapshot"`
	At          time.Time `json:"at"`
	First       time.Time `json:"first"`
	Kind        string    `json:"kind"`
	Files       int       `json:"files"`
	Bytes       int64     `json:"bytes"`
	Fingerprint string    `json:"fingerprint"`
}

// Versions answers the versions of the site view in snaps (newest first),
// newest first. A snapshot not indexed yet is left out; one that does not
// hold the site ends a run.
func Versions(snaps []Snapshot, idx func(id string) (map[string]SiteSummary, bool), view string) []Version {
	var out []Version
	open := false
	for _, s := range snaps {
		m, ok := idx(s.ID)
		if !ok {
			continue
		}
		sum, has := m[view]
		if !has {
			open = false
			continue
		}
		if open && out[len(out)-1].Fingerprint == sum.Fingerprint {
			out[len(out)-1].First = s.At
			continue
		}
		out = append(out, Version{Snapshot: s.ID, At: s.At, First: s.At, Kind: s.Kind,
			Files: sum.Files, Bytes: sum.Bytes, Fingerprint: sum.Fingerprint})
		open = true
	}
	return out
}
