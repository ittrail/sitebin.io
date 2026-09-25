package store

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// replaceStagingPrefix names the staging directories of replace uploads. They
// sit inside the site's folder beside files/ — the same filesystem as the
// content — so committing one is a rename, not a copy.
const replaceStagingPrefix = ".replace-"

// staleStagingAge is how old a staging directory must be before BeginReplace
// treats it as left behind by a crash; an upload still writing into one is
// younger than that.
const staleStagingAge = time.Hour

var errReplaceFinished = errors.New("this replacement was already committed or aborted")

// Replacement stages a replace-all upload. What is written to it counts
// against the site's caps on its own — the content it replaces is going away —
// and nothing the site serves changes until Commit. A failed upload is Aborted
// and the site stays exactly as it was.
type Replacement struct {
	s        *Store
	site     *Site
	dir      string
	root     *os.Root
	used     int64
	count    int
	maxBytes int64
	maxFiles int
	finished bool
}

// BeginReplace starts a replacement of site's content.
func (s *Store) BeginReplace(site *Site) (*Replacement, error) {
	removeStaleStaging(site.Dir(), time.Now())
	dir, err := os.MkdirTemp(site.Dir(), replaceStagingPrefix+"*")
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		os.RemoveAll(dir)
		return nil, err
	}
	return &Replacement{
		s: s, site: site, dir: dir, root: root,
		maxBytes: s.EffMaxBytes(site), maxFiles: s.EffMaxFiles(site),
	}, nil
}

// removeStaleStaging deletes the staging directories crashed uploads left in
// siteDir. They count toward no quota, so nothing else would ever notice them.
func removeStaleStaging(siteDir string, now time.Time) {
	entries, err := os.ReadDir(siteDir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), replaceStagingPrefix) {
			continue
		}
		if fi, err := e.Info(); err == nil && now.Sub(fi.ModTime()) > staleStagingAge {
			os.RemoveAll(filepath.Join(siteDir, e.Name()))
		}
	}
}

// SaveFile stages one file at relPath, with SaveFile's path and budget rules.
func (r *Replacement) SaveFile(relPath string, rd io.Reader) error {
	if r.finished {
		return errReplaceFinished
	}
	rel, err := CleanRelPath(relPath)
	if err != nil {
		return err
	}
	written, existing, err := writeFileIn(r.root, rel, rd, r.used, r.count, r.maxBytes, r.maxFiles)
	if err != nil {
		return err
	}
	r.used += written - existing
	if existing == 0 {
		r.count++
	}
	return nil
}

// ExtractZip stages an archive's files, with ExtractZip's rules.
func (r *Replacement) ExtractZip(ra io.ReaderAt, size int64) error {
	if r.finished {
		return errReplaceFinished
	}
	entries, err := zipEntries(ra, size, r.maxFiles)
	if err != nil {
		return err
	}
	r.used, r.count, err = extractEntries(r.root, entries, r.used, r.count, r.maxBytes, r.maxFiles)
	return err
}

// Commit swaps the staged files in. Under the site lock it empties the content
// root — Sitebin's own markers excepted, as ClearFiles does — and moves each
// staged entry into it. The content directory itself is never renamed or
// replaced: a container site's bind mount points at that directory, and
// swapping it would leave the running container on a deleted one.
func (r *Replacement) Commit() error {
	if r.finished {
		return errReplaceFinished
	}
	r.finished = true
	r.root.Close()
	defer os.RemoveAll(r.dir)

	l := r.s.lockSite(r.site.ViewID)
	l.Lock()
	defer l.Unlock()
	dst := r.site.ContentDir()
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	if err := clearContentDir(dst); err != nil {
		return err
	}
	entries, err := os.ReadDir(r.dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if err := os.Rename(filepath.Join(r.dir, e.Name()), filepath.Join(dst, e.Name())); err != nil {
			return fmt.Errorf("commit replacement: %w", err)
		}
	}
	return r.s.renewExpiryLocked(r.site)
}

// Abort discards the staged files. It is harmless after Commit or a previous
// Abort, so callers defer it.
func (r *Replacement) Abort() {
	if r.finished {
		return
	}
	r.finished = true
	r.root.Close()
	os.RemoveAll(r.dir)
}
