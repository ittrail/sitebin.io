package store

import (
	"archive/zip"
	"fmt"
	"io"
	"io/fs"
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
	if reservedTopLevel[strings.ToLower(segs[0])] {
		return "", ErrBadPath
	}
	for _, seg := range segs {
		if seg == "" || len(seg) > 255 {
			return "", ErrBadPath
		}
	}
	return c, nil
}

// usage returns the current byte and file count under dir (0s if missing).
func usage(dir string) (bytes int64, files int, err error) {
	err = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
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

// EffMaxFiles returns the site's effective file-count cap.
func (s *Store) EffMaxFiles(site *Site) int {
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
	dir := site.ContentDir()
	used, count, err := usage(dir)
	if err != nil {
		return err
	}
	maxBytes, maxFiles := s.EffMaxBytes(site), s.EffMaxFiles(site)
	dst := filepath.Join(dir, filepath.FromSlash(rel))
	var existing int64
	if fi, err := os.Lstat(dst); err == nil {
		if fi.IsDir() {
			return ErrBadPath
		}
		existing = fi.Size()
	} else if count+1 > maxFiles {
		return ErrTooManyFiles
	}
	budget := maxBytes - (used - existing)
	if budget < 0 {
		return ErrTooLarge
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fmt.Errorf("create dirs: %w", err)
	}
	tmp := dst + ".sbtmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("create file: %w", err)
	}
	_, err = io.Copy(f, io.LimitReader(r, budget+1))
	if err == nil {
		// detect budget overrun: LimitReader stops silently at budget+1
		if fi, serr := f.Stat(); serr == nil && fi.Size() > budget {
			err = ErrTooLarge
		}
	}
	if cerr := f.Close(); err == nil && cerr != nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("commit file: %w", err)
	}
	return nil
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

	root := site.ContentDir()
	dst := filepath.Join(root, filepath.FromSlash(rel))
	if fi, err := os.Lstat(dst); err != nil {
		if os.IsNotExist(err) {
			return ErrNotFound
		}
		return err
	} else if fi.IsDir() {
		return ErrBadPath
	}
	if err := os.Remove(dst); err != nil {
		return err
	}
	for d := filepath.Dir(dst); d != root && strings.HasPrefix(d, root); d = filepath.Dir(d) {
		if os.Remove(d) != nil { // fails when non-empty — that's the stop signal
			break
		}
	}
	return nil
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
		if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
			return err
		}
	}
	return nil
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
	p := filepath.Join(site.ContentDir(), filepath.FromSlash(rel))
	fi, err := os.Lstat(p)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if fi.IsDir() {
		return nil, ErrBadPath
	}
	if fi.Size() > MaxEditableBytes {
		return nil, ErrTooLarge
	}
	return os.ReadFile(p)
}

// ZipContent writes a zip archive of the site's content files to w.
func (s *Store) ZipContent(site *Site, w io.Writer) error {
	root := site.ContentDir()
	zw := zip.NewWriter(w)
	walkErr := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		zf, err := zw.Create(filepath.ToSlash(rel))
		if err != nil {
			return err
		}
		src, err := os.Open(p)
		if err != nil {
			return err
		}
		_, err = io.Copy(zf, src)
		src.Close()
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
		if d.IsDir() {
			return nil
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
