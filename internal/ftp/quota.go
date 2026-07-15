package ftp

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/afero"

	"github.com/ittrail/sitebin.io/internal/store"
)

// errQuota is returned when a write would exceed the site's byte/file caps.
var errQuota = errors.New("site storage limit exceeded")

// quotaFs is the per-session filesystem handed to an FTP client. It is rooted
// at the site's content directory (via afero BasePathFs, which confines all
// paths), rejects reserved/invalid names on mutation via the store's path
// rules, and enforces the site's byte and file-count caps on writes.
type quotaFs struct {
	afero.Fs // BasePathFs rooted at root
	root     string
	maxBytes int64
	maxFiles int
}

func newQuotaFs(root string, maxBytes int64, maxFiles int) *quotaFs {
	base := afero.NewBasePathFs(afero.NewOsFs(), root)
	return &quotaFs{Fs: base, root: root, maxBytes: maxBytes, maxFiles: maxFiles}
}

// cleanName validates an FTP path against the store's rules (no traversal, no
// reserved top-level names). The root itself is allowed.
func cleanName(name string) (string, error) {
	rel := strings.TrimPrefix(filepath.ToSlash(name), "/")
	if rel == "" || rel == "." {
		return "", nil // the root directory
	}
	if _, err := store.CleanRelPath(rel); err != nil {
		return "", err
	}
	return rel, nil
}

// usage returns the current byte and file totals under root.
func (q *quotaFs) usage() (bytes int64, files int) {
	filepath.WalkDir(q.root, func(_ string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if info, e := d.Info(); e == nil {
			bytes += info.Size()
			files++
		}
		return nil
	})
	return
}

func (q *quotaFs) Create(name string) (afero.File, error) {
	return q.OpenFile(name, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
}

func (q *quotaFs) OpenFile(name string, flag int, perm os.FileMode) (afero.File, error) {
	writing := flag&(os.O_WRONLY|os.O_RDWR|os.O_CREATE|os.O_APPEND) != 0
	if !writing {
		return q.Fs.OpenFile(name, flag, perm)
	}
	rel, err := cleanName(name)
	if err != nil || rel == "" {
		return nil, errQuota // reject writes to the root / invalid names
	}
	used, count := q.usage()
	var existing int64
	if fi, err := q.Fs.Stat(name); err == nil {
		existing = fi.Size()
	} else if count+1 > q.maxFiles {
		return nil, errQuota
	}
	f, err := q.Fs.OpenFile(name, flag, perm)
	if err != nil {
		return nil, err
	}
	return &quotaFile{File: f, remaining: q.maxBytes - (used - existing)}, nil
}

func (q *quotaFs) Mkdir(name string, perm os.FileMode) error {
	if _, err := cleanName(name); err != nil {
		return err
	}
	return q.Fs.Mkdir(name, perm)
}

func (q *quotaFs) MkdirAll(path string, perm os.FileMode) error {
	if _, err := cleanName(path); err != nil {
		return err
	}
	return q.Fs.MkdirAll(path, perm)
}

func (q *quotaFs) Rename(oldname, newname string) error {
	if _, err := cleanName(oldname); err != nil {
		return err
	}
	if _, err := cleanName(newname); err != nil {
		return err
	}
	return q.Fs.Rename(oldname, newname)
}

func (q *quotaFs) Remove(name string) error {
	if _, err := cleanName(name); err != nil {
		return err
	}
	return q.Fs.Remove(name)
}

func (q *quotaFs) RemoveAll(path string) error {
	if _, err := cleanName(path); err != nil {
		return err
	}
	return q.Fs.RemoveAll(path)
}

// quotaFile aborts a transfer that would exceed the site's byte budget.
type quotaFile struct {
	afero.File
	remaining int64
}

func (f *quotaFile) Write(p []byte) (int, error) {
	if int64(len(p)) > f.remaining {
		return 0, errQuota
	}
	n, err := f.File.Write(p)
	f.remaining -= int64(n)
	return n, err
}

func (f *quotaFile) WriteString(s string) (int, error) { return f.Write([]byte(s)) }

func (f *quotaFile) WriteAt(p []byte, off int64) (int, error) {
	if int64(len(p)) > f.remaining {
		return 0, errQuota
	}
	n, err := f.File.WriteAt(p, off)
	f.remaining -= int64(n)
	return n, err
}
