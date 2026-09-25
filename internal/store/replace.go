package store

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// A replace upload stages under the store's tmp/ directory — the one the zip
// spool already uses, on the same volume as sites/ — as
// tmp/replace-<viewID>-<random>. Nothing is written inside the site folder
// while the upload streams, so a Delete of the site cannot race a staging
// writer. Commit moves the whole staging directory into the site folder, as
// <site>/.replace-commit-<random>, under the site lock and only once it knows
// the site still exists; a crash in the middle of a commit is the only thing
// that leaves one of those behind.
const (
	tmpDirName           = "tmp"
	replaceTmpPrefix     = "replace-"
	replaceStagingPrefix = ".replace-"
	replaceCommitPrefix  = ".replace-commit-"
)

// staleStagingAge is how old a staging directory must be before BeginReplace
// treats it as left behind by a crash; an upload still writing into one is
// younger than that (one request must finish within the public server's
// 10-minute read timeout).
const staleStagingAge = time.Hour

// ErrReplaceBusy is BeginReplace refusing a second replacement of a site while
// one is still in flight. Each stages up to the site's full cap on the data
// volume, so overlapping replaces of one site must not pile up.
var ErrReplaceBusy = errors.New("another replace of this site is still running")

var (
	errReplaceFinished = errors.New("this replacement was already committed or aborted")
	errReplaceFailed   = errors.New("this replacement had a failed write and cannot be committed")
)

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
	// failed records that a write returned an error: whatever the caller does
	// next, a replacement missing a file is never committed.
	failed   bool
	finished bool
}

func (s *Store) tmpDir() string { return filepath.Join(s.root, tmpDirName) }

// claimReplace marks a replacement of viewID as in flight; it reports false
// when one already is.
func (s *Store) claimReplace(viewID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.replacing[viewID] {
		return false
	}
	s.replacing[viewID] = true
	return true
}

func (s *Store) releaseReplace(viewID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.replacing, viewID)
}

// BeginReplace starts a replacement of site's content. Only one replacement
// of a site may be in flight at a time — each can stage the site's whole cap
// on the data volume — so a second is ErrReplaceBusy until the first is
// committed or aborted.
func (s *Store) BeginReplace(site *Site) (*Replacement, error) {
	if !s.claimReplace(site.ViewID) {
		return nil, ErrReplaceBusy
	}
	// Released on every way out that does not hand a Replacement back —
	// an error and a panic alike — or the site would answer 409 until a
	// restart.
	begun := false
	defer func() {
		if !begun {
			s.releaseReplace(site.ViewID)
		}
	}()
	tmp := s.tmpDir()
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		return nil, err
	}
	now := time.Now()
	removeStaleStaging(tmp, replaceTmpPrefix, now)
	removeStaleStaging(site.Dir(), replaceStagingPrefix, now)
	dir, err := os.MkdirTemp(tmp, replaceTmpPrefix+site.ViewID+"-*")
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		os.RemoveAll(dir)
		return nil, err
	}
	rep := &Replacement{
		s: s, site: site, dir: dir, root: root,
		maxBytes: s.EffMaxBytes(site), maxFiles: s.EffMaxFiles(site),
	}
	begun = true
	return rep, nil
}

// commitRename is os.Rename, swappable so a test can make one of Commit's
// renames fail.
var commitRename = os.Rename

// removeStaleStaging deletes the staging directories named prefix* in dir that
// are older than staleStagingAge: under tmp/ the uploads crashes left (of any
// site), inside a site folder the commits a crash interrupted. They count
// toward no quota, so nothing else would ever notice them.
func removeStaleStaging(dir, prefix string, now time.Time) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), prefix) {
			continue
		}
		if fi, err := e.Info(); err == nil && now.Sub(fi.ModTime()) > staleStagingAge {
			os.RemoveAll(filepath.Join(dir, e.Name()))
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
		r.failed = true
		return err
	}
	written, existing, err := writeFileIn(r.root, rel, rd, r.used, r.count, r.maxBytes, r.maxFiles)
	if err != nil {
		r.failed = true
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
		r.failed = true
		return err
	}
	r.used, r.count, err = extractEntries(r.root, entries, r.used, r.count, r.maxBytes, r.maxFiles)
	if err != nil {
		r.failed = true
	}
	return err
}

// Commit swaps the staged files in, all under the site lock, and in an order
// that touches the live content last:
//
//  1. a replacement with a failed write is refused;
//  2. a site whose meta.json is gone (deleted while the upload streamed) is
//     ErrNotFound — its folder is not brought back;
//  3. the staging directory is renamed into the site folder as one unit; if
//     that fails (a mount that cannot rename across), the site is untouched;
//  4. only then is the content root emptied — Sitebin's own markers excepted,
//     as ClearFiles does — and each staged entry renamed into it. Should one
//     of those steps fail, the part of the upload not yet in place stays in
//     the site's .replace-commit-* directory (the error names it; the stale
//     sweep removes it after staleStagingAge) instead of being thrown away.
//
// The content directory itself is never renamed or replaced: a container
// site's bind mounts point INTO it (files/<folder>), and swapping it would
// leave the running containers on a deleted tree.
func (r *Replacement) Commit() error {
	if r.finished {
		return errReplaceFinished
	}
	r.finished = true
	// Released last, once the lock is: a new replacement of the site cannot
	// begin until this commit, however it ends, is completely over.
	defer r.s.releaseReplace(r.site.ViewID)
	// Closed before any rename: Windows refuses to move a directory with an
	// open handle, and nothing is written to the staging tree from here on.
	r.root.Close()
	// Whatever happens below, the staging directory does not outlive Commit;
	// once it has been moved this is a no-op.
	defer os.RemoveAll(r.dir)

	l := r.s.lockSite(r.site.ViewID)
	l.Lock()
	defer l.Unlock()

	if r.failed {
		return errReplaceFailed
	}
	meta, err := readMeta(r.site.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return ErrNotFound
		}
		return err
	}
	// The mode decides where the content lives; take it as it is now, not as
	// it was when the upload began.
	r.site.Meta = meta

	moved := filepath.Join(r.site.dir, replaceCommitPrefix+randomSuffix())
	if err := commitRename(r.dir, moved); err != nil {
		return fmt.Errorf("commit replacement: %w", err)
	}
	// Until the live content is touched, the moved staging directory goes
	// when Commit does. Once clearing has begun it is the only copy of the
	// part of the upload not yet in place, so a failure from there on keeps
	// it — the stale sweep removes it after staleStagingAge.
	keep := false
	defer func() {
		if !keep {
			os.RemoveAll(moved)
		}
	}()
	entries, err := os.ReadDir(moved)
	if err != nil {
		return fmt.Errorf("commit replacement: %w", err)
	}

	// The site folder is known to exist; the content directory (and, in
	// viewer mode, files/ above it) is recreated if it went missing, but
	// never the site folder itself.
	dst := r.site.ContentDir()
	for _, d := range []string{r.site.FilesDir(), dst} {
		if err := os.Mkdir(d, 0o755); err != nil && !errors.Is(err, fs.ErrExist) {
			return err
		}
	}
	keep = true
	if err := clearContentDir(dst); err != nil {
		return fmt.Errorf("commit replacement: %w (the upload is kept in %s)", err, moved)
	}
	for _, e := range entries {
		if err := commitRename(filepath.Join(moved, e.Name()), filepath.Join(dst, e.Name())); err != nil {
			return fmt.Errorf("commit replacement: %w (the rest of the upload is kept in %s)", err, moved)
		}
	}
	keep = false
	return r.s.renewExpiryLocked(r.site)
}

// randomSuffix names a commit's directory inside the site folder.
func randomSuffix() string {
	var b [8]byte
	rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// Abort discards the staged files and lets the next replacement of the site
// begin. It is harmless after Commit or a previous Abort, so callers defer it.
func (r *Replacement) Abort() {
	if r.finished {
		return
	}
	r.finished = true
	r.root.Close()
	os.RemoveAll(r.dir)
	r.s.releaseReplace(r.site.ViewID)
}
