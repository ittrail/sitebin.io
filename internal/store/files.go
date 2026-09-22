package store

import (
	"archive/zip"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// FileInfo describes one stored user file.
type FileInfo struct {
	Path string `json:"path"` // slash-separated, relative to the content root
	Size int64  `json:"size"`
}

// reservedTopLevel are names uploads may not use at the content root:
// _sitebin is the backend route prefix on site origins, _raw holds viewer-mode
// originals, and meta.json is guarded against confusion even though it lives
// one level above files/.
var reservedTopLevel = map[string]bool{"_sitebin": true, rawDirName: true, "meta.json": true}

// CleanRelPath validates and normalizes an upload path. It returns a
// slash-separated relative path that cannot escape the content root.
func CleanRelPath(p string) (string, error) {
	if p == "" || len(p) > 1024 {
		return "", ErrBadPath
	}
	for _, r := range p {
		if r < 0x20 || r == 0x7f {
			return "", ErrBadPath
		}
	}
	if strings.Contains(p, `\`) || strings.HasPrefix(p, "/") {
		return "", ErrBadPath
	}
	if len(p) >= 2 && p[1] == ':' {
		return "", ErrBadPath
	}
	c := path.Clean(p)
	if c == "." || c == ".." || strings.HasPrefix(c, "../") || strings.HasPrefix(c, "/") {
		return "", ErrBadPath
	}
	segs := strings.Split(c, "/")
	first := strings.ToLower(segs[0])
	if reservedTopLevel[first] || strings.HasPrefix(first, ".sitebin-") {
		return "", ErrBadPath
	}
	for _, seg := range segs {
		if seg == "" || len(seg) > 255 {
			return "", ErrBadPath
		}
	}
	return c, nil
}

// HumanBytes renders a byte count for people: "512 B", "3.4 MB", "120 GB".
// The one copy the CLI and the dashboard share.
func HumanBytes(n int64) string {
	if n < 1024 {
		return fmt.Sprintf("%d B", n)
	}
	const units = "KMGT"
	f := float64(n)
	i := -1
	for f >= 1024 && i < len(units)-1 {
		f /= 1024
		i++
	}
	if f >= 100 {
		return fmt.Sprintf("%.0f %cB", f, units[i])
	}
	return fmt.Sprintf("%.1f %cB", f, units[i])
}

// openContent opens the site's content directory as an os.Root, creating it
// if it is missing. Every file operation on a site's content goes through one.
//
// The reason is containers: a container site mounts the project's folders
// into code Sitebin does not control, and that code can create a symlink such
// as app/x -> /data. Resolving through an os.Root confines every path to the
// content directory with openat, so a link that leaves it is an error rather
// than a way into the instance's secret or another customer's site — and,
// unlike checking each component with Lstat first, it cannot be raced by a
// container swapping a directory for a link between the check and the use.
func openContent(site *Site) (*os.Root, error) {
	dir := site.ContentDir()
	r, err := os.OpenRoot(dir)
	if err != nil && os.IsNotExist(err) {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
		r, err = os.OpenRoot(dir)
	}
	return r, err
}

// OpenContentRoot is openContent for the file surfaces outside the store
// (WebDAV, FTP). The caller closes it.
func OpenContentRoot(site *Site) (*os.Root, error) { return openContent(site) }

// usage returns the current byte and file count under dir (0s if missing).
// Only regular files count: a symlink or a socket a container left behind is
// not content anyone uploaded, and nothing Sitebin serves.
func usage(dir string) (bytes int64, files int, err error) {
	err = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.Type().IsRegular() {
			return nil
		}
		// Sitebin's own markers are not the user's files and must not eat into
		// the quota they paid for: a 200-file tier would otherwise allow 199.
		if d.Name() == SPAMarker || d.Name() == TrustedMarker {
			return nil
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		bytes += fi.Size()
		files++
		return nil
	})
	if os.IsNotExist(err) {
		return 0, 0, nil
	}
	return bytes, files, err
}

// Usage reports the site's current content size and file count.
func (s *Store) Usage(site *Site) (int64, int, error) {
	return usage(site.ContentDir())
}

// EffMaxBytes returns the site's effective storage cap: its stamped per-site
// quota (from the owner's tier) if set, else the instance global.
func (s *Store) EffMaxBytes(site *Site) int64 {
	if site.Meta.QuotaBytes > 0 {
		return site.Meta.QuotaBytes
	}
	return s.maxSiteBytes
}

// EffMaxFiles returns the site's effective file-count cap. A container site
// has none: installing a project's dependencies writes tens of thousands of
// files, and the byte cap already bounds what the count would protect.
func (s *Store) EffMaxFiles(site *Site) int {
	if site.Meta.Mode == ModeContainer {
		return math.MaxInt
	}
	if site.Meta.QuotaFiles > 0 {
		return site.Meta.QuotaFiles
	}
	return s.maxFiles
}

// SaveFile stores one file at relPath inside the site's content root,
// enforcing the per-site byte and file-count limits.
func (s *Store) SaveFile(site *Site, relPath string, r io.Reader) error {
	rel, err := CleanRelPath(relPath)
	if err != nil {
		return err
	}
	l := s.lockSite(site.ViewID)
	l.Lock()
	defer l.Unlock()
	return s.saveFileLocked(site, rel, r)
}

func (s *Store) saveFileLocked(site *Site, rel string, r io.Reader) error {
	used, count, err := usage(site.ContentDir())
	if err != nil {
		return err
	}
	if _, _, err := s.writeFileLocked(site, rel, r, used, count, s.EffMaxBytes(site), s.EffMaxFiles(site)); err != nil {
		return err
	}
	return s.renewExpiryLocked(site)
}

// writeFileLocked writes one file against a budget the CALLER has measured:
// used and count are the site's current totals, maxBytes and maxFiles its
// caps. It returns the bytes written and the size of the file it replaced
// (0 for a new one), so a caller writing many files can keep the totals
// current without walking the site again. The caller holds the site lock.
func (s *Store) writeFileLocked(site *Site, rel string, r io.Reader, used int64, count int, maxBytes int64, maxFiles int) (written, existing int64, err error) {
	root, err := openContent(site)
	if err != nil {
		return 0, 0, err
	}
	defer root.Close()
	dst := filepath.FromSlash(rel)
	if fi, err := root.Lstat(dst); err == nil {
		if fi.IsDir() {
			return 0, 0, ErrBadPath
		}
		existing = fi.Size()
	} else if !os.IsNotExist(err) {
		// Anything but "not there" — above all a parent that is a link out of
		// the site — is a path this site may not write.
		return 0, 0, ErrBadPath
	} else if count+1 > maxFiles {
		return 0, 0, ErrTooManyFiles
	}
	budget := maxBytes - (used - existing)
	if budget < 0 {
		return 0, existing, ErrTooLarge
	}
	if dir := filepath.Dir(dst); dir != "." {
		if err := root.MkdirAll(dir, 0o755); err != nil {
			return 0, existing, fmt.Errorf("create dirs: %w", err)
		}
	}
	tmp := dst + ".sbtmp"
	f, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return 0, existing, fmt.Errorf("create file: %w", err)
	}
	written, err = io.Copy(f, io.LimitReader(r, budget+1))
	if err == nil && written > budget {
		// LimitReader stops silently at budget+1: past the budget by one is
		// past the budget.
		err = ErrTooLarge
	}
	if cerr := f.Close(); err == nil && cerr != nil {
		err = cerr
	}
	if err != nil {
		root.Remove(tmp)
		return 0, existing, err
	}
	if err := root.Rename(tmp, dst); err != nil {
		root.Remove(tmp)
		return 0, existing, fmt.Errorf("commit file: %w", err)
	}
	return written, existing, nil
}

// DeleteFile removes one file and prunes now-empty parent directories.
func (s *Store) DeleteFile(site *Site, relPath string) error {
	rel, err := CleanRelPath(relPath)
	if err != nil {
		return err
	}
	l := s.lockSite(site.ViewID)
	l.Lock()
	defer l.Unlock()

	root, err := openContent(site)
	if err != nil {
		return err
	}
	defer root.Close()
	dst := filepath.FromSlash(rel)
	if fi, err := root.Lstat(dst); err != nil {
		if os.IsNotExist(err) {
			return ErrNotFound
		}
		return ErrBadPath
	} else if fi.IsDir() {
		return ErrBadPath
	}
	if err := root.Remove(dst); err != nil {
		return err
	}
	for d := filepath.Dir(dst); d != "."; d = filepath.Dir(d) {
		if root.Remove(d) != nil { // fails when non-empty — that's the stop signal
			break
		}
	}
	return s.renewExpiryLocked(site)
}

// ClearFiles wipes the site's content root (used for replace-all uploads).
func (s *Store) ClearFiles(site *Site) error {
	l := s.lockSite(site.ViewID)
	l.Lock()
	defer l.Unlock()

	dir := site.ContentDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, e := range entries {
		// Sitebin's own markers survive a replace. They are not the caller's
		// files, and dropping them would silently change how the site is served
		// — a replace upload would strip the trust marker and harden the site
		// until the next cleanup sweep put it back, which for a site that
		// deploys on every push means breaking itself on every push.
		if e.Name() == SPAMarker || e.Name() == TrustedMarker {
			continue
		}
		if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
			return err
		}
	}
	return s.renewExpiryLocked(site)
}

// MaxEditableBytes caps files that can be read for in-browser editing.
const MaxEditableBytes = 2 << 20

// ReadContentFile returns the bytes of a content file (for the in-browser
// editor). Files larger than MaxEditableBytes are refused.
func (s *Store) ReadContentFile(site *Site, relPath string) ([]byte, error) {
	rel, err := CleanRelPath(relPath)
	if err != nil {
		return nil, err
	}
	root, err := openContent(site)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	p := filepath.FromSlash(rel)
	fi, err := root.Lstat(p)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotFound
		}
		return nil, ErrBadPath
	}
	if !fi.Mode().IsRegular() {
		return nil, ErrBadPath
	}
	if fi.Size() > MaxEditableBytes {
		return nil, ErrTooLarge
	}
	return root.ReadFile(p)
}

// ZipContent writes a zip archive of the site's content files to w.
func (s *Store) ZipContent(site *Site, w io.Writer) error {
	dir := site.ContentDir()
	root, err := openContent(site)
	if err != nil {
		return err
	}
	defer root.Close()
	zw := zip.NewWriter(w)
	walkErr := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		// Regular files only: a symlink is never followed out of the site,
		// and a socket or pipe has no bytes to archive.
		if !d.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		src, err := root.Open(rel)
		if err != nil {
			return nil // vanished or swapped for something else since the walk saw it
		}
		defer src.Close()
		if fi, err := src.Stat(); err != nil || !fi.Mode().IsRegular() {
			return nil
		}
		zf, err := zw.Create(filepath.ToSlash(rel))
		if err != nil {
			return err
		}
		_, err = io.Copy(zf, src)
		return err
	})
	if closeErr := zw.Close(); walkErr == nil {
		return closeErr
	}
	return walkErr
}

// ListFiles returns the site's user files sorted by path.
func (s *Store) ListFiles(site *Site) ([]FileInfo, error) {
	root := site.ContentDir()
	var out []FileInfo
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.Type().IsRegular() {
			return nil // directories, and links or sockets a container left
		}
		if d.Name() == SPAMarker || d.Name() == TrustedMarker {
			return nil // internal marker, not a user file
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		out = append(out, FileInfo{Path: filepath.ToSlash(rel), Size: fi.Size()})
		return nil
	})
	if os.IsNotExist(err) {
		return []FileInfo{}, nil
	}
	if err != nil {
		return nil, err
	}
	if out == nil {
		out = []FileInfo{}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}
