package ftp

import (
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/afero"
)

// rootFs is an afero.Fs confined to one directory by os.Root. It replaces
// afero.BasePathFs, whose confinement is lexical only: it cleans "../" away
// but follows a symlink wherever it points. A container site's folders are
// written by code Sitebin does not control, so a link planted there
// (app/x -> /data) would otherwise be an FTP path into the instance secret or
// another customer's site. os.Root resolves every component with openat and
// refuses one that leaves the directory.
//
// The root is opened per call; files opened through it stay valid after it
// is closed, so there is no session lifetime to manage.
type rootFs struct{ dir string }

func (f rootFs) name(n string) string {
	rel := strings.TrimPrefix(path.Clean("/"+filepath.ToSlash(n)), "/")
	if rel == "" {
		return "."
	}
	return filepath.FromSlash(rel)
}

func (f rootFs) with(fn func(*os.Root) error) error {
	r, err := os.OpenRoot(f.dir)
	if err != nil {
		return err
	}
	defer r.Close()
	return fn(r)
}

func (f rootFs) Name() string { return "rootFs" }

func (f rootFs) Create(name string) (afero.File, error) {
	return f.OpenFile(name, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o666)
}

func (f rootFs) Open(name string) (afero.File, error) {
	return f.OpenFile(name, os.O_RDONLY, 0)
}

func (f rootFs) OpenFile(name string, flag int, perm os.FileMode) (afero.File, error) {
	var file *os.File
	err := f.with(func(r *os.Root) error {
		var err error
		file, err = r.OpenFile(f.name(name), flag, perm)
		return err
	})
	if err != nil {
		return nil, err
	}
	return file, nil
}

func (f rootFs) Mkdir(name string, perm os.FileMode) error {
	return f.with(func(r *os.Root) error { return r.Mkdir(f.name(name), perm) })
}

func (f rootFs) MkdirAll(name string, perm os.FileMode) error {
	n := f.name(name)
	if n == "." {
		return nil
	}
	return f.with(func(r *os.Root) error { return r.MkdirAll(n, perm) })
}

func (f rootFs) Remove(name string) error {
	return f.with(func(r *os.Root) error { return r.Remove(f.name(name)) })
}

func (f rootFs) RemoveAll(name string) error {
	n := f.name(name)
	if n == "." {
		return os.ErrInvalid
	}
	return f.with(func(r *os.Root) error { return r.RemoveAll(n) })
}

func (f rootFs) Rename(oldname, newname string) error {
	return f.with(func(r *os.Root) error { return r.Rename(f.name(oldname), f.name(newname)) })
}

func (f rootFs) Stat(name string) (os.FileInfo, error) {
	var fi os.FileInfo
	err := f.with(func(r *os.Root) error {
		var err error
		fi, err = r.Stat(f.name(name))
		return err
	})
	return fi, err
}

func (f rootFs) Chmod(name string, mode os.FileMode) error {
	return f.with(func(r *os.Root) error { return r.Chmod(f.name(name), mode) })
}

func (f rootFs) Chown(name string, uid, gid int) error {
	return f.with(func(r *os.Root) error { return r.Chown(f.name(name), uid, gid) })
}

func (f rootFs) Chtimes(name string, atime, mtime time.Time) error {
	return f.with(func(r *os.Root) error { return r.Chtimes(f.name(name), atime, mtime) })
}
