package store

import (
	"errors"
	"io"
	"os"
	"path"
	"path/filepath"

	"github.com/ittrail/sitebin.io/internal/abuse"
)

// StagedFile is a write into one of a site's files that becomes visible
// only when it is closed: the bytes go to a temp file beside the target, and
// Close scans the result, settles the abuse guard's verdict in meta.json and
// only then renames it into place. It is how WebDAV and FTP — whose clients
// write through a file handle rather than hand the store a stream — keep the
// guard's promise that nothing it holds is ever served.
//
// It embeds the temp file, so it is an *os.File for everything but Close.
type StagedFile struct {
	*os.File
	s        *Store
	site     *Site
	rel      string // native, relative to the content root
	tmp      string
	lockHeld bool
	// dirty is set by any write. A handle opened read-write and never
	// written (WebDAV's PROPPATCH does this) is discarded on Close instead
	// of committing an identical copy.
	dirty  bool
	closed bool
}

// OpenStaged opens rel (native, relative to the content root, already
// validated as an upload path) for writing through a StagedFile. flag is the
// caller's os.OpenFile flag: without O_TRUNC the existing content is copied
// into the temp file first, so an append or a resumed transfer continues
// where the file ended. lockHeld says whether the caller already holds the
// site lock (the WebDAV handler does for its mutating methods), so Close
// must not take it again.
func (s *Store) OpenStaged(site *Site, rel string, flag int, perm os.FileMode, lockHeld bool) (*StagedFile, error) {
	root, err := openContent(site)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	exists := false
	if fi, err := root.Lstat(rel); err == nil {
		if !fi.Mode().IsRegular() {
			return nil, ErrBadPath
		}
		exists = true
		if flag&os.O_EXCL != 0 && flag&os.O_CREATE != 0 {
			return nil, os.ErrExist
		}
	} else if !os.IsNotExist(err) {
		return nil, ErrBadPath
	} else if flag&os.O_CREATE == 0 {
		return nil, os.ErrNotExist
	}
	if dir := filepath.Dir(rel); dir != "." {
		// The target's folder must exist, as for a plain open.
		if fi, err := root.Stat(dir); err != nil || !fi.IsDir() {
			return nil, os.ErrNotExist
		}
	}
	tmp := rel + ".sbtmp-" + randomSuffix()[:8]
	f, err := root.OpenFile(tmp, os.O_RDWR|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return nil, err
	}
	sf := &StagedFile{File: f, s: s, site: site, rel: rel, tmp: tmp, lockHeld: lockHeld,
		// a new file, or an existing one truncated, is a change even if
		// nothing is ever written to it
		dirty: !exists || flag&os.O_TRUNC != 0}
	if exists && flag&os.O_TRUNC == 0 {
		src, err := root.Open(rel)
		if err == nil {
			_, err = io.Copy(f, src)
			src.Close()
		}
		if err == nil && flag&os.O_APPEND == 0 {
			_, err = f.Seek(0, io.SeekStart)
		}
		if err != nil {
			f.Close()
			root.Remove(tmp)
			return nil, err
		}
	}
	return sf, nil
}

func (f *StagedFile) Write(p []byte) (int, error) { f.dirty = true; return f.File.Write(p) }
func (f *StagedFile) WriteAt(p []byte, off int64) (int, error) {
	f.dirty = true
	return f.File.WriteAt(p, off)
}
func (f *StagedFile) WriteString(s string) (int, error) { f.dirty = true; return f.File.WriteString(s) }
func (f *StagedFile) Truncate(size int64) error         { f.dirty = true; return f.File.Truncate(size) }

// ReadFrom is what io.Copy uses on an *os.File; without this override it
// would reach the embedded file's and the write would go unnoticed.
func (f *StagedFile) ReadFrom(r io.Reader) (int64, error) {
	f.dirty = true
	return f.File.ReadFrom(r)
}

// Close commits the file: scanned, settled, renamed into place. It returns a
// *HeldError when the content held the site (the file is in place, as
// evidence), ErrLocked when the site was locked while the file was written
// (the file is discarded), and ErrNotFound when the site is gone.
func (f *StagedFile) Close() error {
	if f.closed {
		return os.ErrClosed
	}
	f.closed = true
	if err := f.File.Close(); err != nil {
		f.discard()
		return err
	}
	if !f.dirty {
		f.discard()
		return nil
	}
	if !f.lockHeld {
		l := f.s.lockSite(f.site.ViewID)
		l.Lock()
		defer l.Unlock()
	}
	return f.s.commitStagedLocked(f.site, f.tmp, f.rel)
}

func (f *StagedFile) discard() {
	if root, err := openContent(f.site); err == nil {
		root.Remove(f.tmp)
		root.Close()
	}
}

// commitStagedLocked scans tmp as the file rel, settles the verdict and
// renames tmp to rel. The caller holds the site lock.
func (s *Store) commitStagedLocked(site *Site, tmp, rel string) error {
	root, err := openContent(site)
	if err != nil {
		return err
	}
	defer root.Close()
	meta, err := readMeta(site.dir)
	if err != nil {
		root.Remove(tmp)
		if os.IsNotExist(err) {
			return ErrNotFound
		}
		return err
	}
	// A lock placed while the file was being written: the transfer is
	// refused, as a replace that began before the lock is.
	if meta.Locked != nil {
		root.Remove(tmp)
		return ErrLocked
	}
	held, err := s.checkLocked(site, root, tmp, rel)
	if err != nil {
		root.Remove(tmp)
		return err
	}
	if err := root.Rename(tmp, rel); err != nil {
		root.Remove(tmp)
		return err
	}
	if held != nil {
		return held
	}
	return nil
}

// checkLocked scans the file at src in root as if it were at dst and
// settles the verdict. It returns the hold when the site is now held, and an
// error when the check itself failed. The caller holds the site lock.
func (s *Store) checkLocked(site *Site, root *os.Root, src, dst string) (*HeldError, error) {
	sc := s.newScan(filepath.ToSlash(dst))
	if sc == nil {
		return nil, nil
	}
	in, err := root.Open(src)
	if err != nil {
		return nil, err
	}
	_, err = feed(sc, in)
	in.Close()
	if err != nil {
		return nil, err
	}
	res := sc.Result()
	if len(res.Hits) == 0 {
		return nil, nil
	}
	ev, err := s.settleLocked(site, []abuse.Result{res}, FindingUpload, "")
	s.emit(ev)
	var held *HeldError
	if errors.As(err, &held) {
		return held, nil
	}
	return nil, err
}

// RenameChecked renames oldRel to newRel (native, relative to the content
// root, both already validated) the way WebDAV's MOVE and FTP's RNTO need
// it: a regular file whose extension changes is scanned as its new name
// first — kit.txt renamed to kit.html becomes active — and the verdict is
// settled before the rename. Directories, and files keeping their
// extension, are renamed as they are: their content was scanned under a
// name of the same kind when it was written.
func (s *Store) RenameChecked(site *Site, oldRel, newRel string, lockHeld bool) error {
	if !lockHeld {
		l := s.lockSite(site.ViewID)
		l.Lock()
		defer l.Unlock()
	}
	root, err := openContent(site)
	if err != nil {
		return err
	}
	defer root.Close()
	var held *HeldError
	if fi, err := root.Lstat(oldRel); err == nil && fi.Mode().IsRegular() &&
		path.Ext(filepath.ToSlash(oldRel)) != path.Ext(filepath.ToSlash(newRel)) {
		if held, err = s.checkLocked(site, root, oldRel, newRel); err != nil {
			return err
		}
	}
	if err := root.Rename(oldRel, newRel); err != nil {
		return err
	}
	if held != nil {
		return held
	}
	return nil
}
