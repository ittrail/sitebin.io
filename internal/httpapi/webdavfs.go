package httpapi

import (
	"context"
	"os"
	"path"
	"strings"

	"golang.org/x/net/webdav"

	"github.com/ittrail/sitebin.io/internal/store"
)

// siteFS is the webdav.FileSystem every DAV method goes through. It applies
// the store's path rules to every name it is handed, so no method — present
// or future — can reach a reserved name, whatever the handler above it
// remembered to check.
//
// It exists because of LOCK: x/net/webdav creates a missing lock target with
// O_CREATE (a lock-null resource, which desktop clients rely on before their
// first PUT), and the handler's per-method validation did not list LOCK. One
// request could therefore write /.sitebin-trusted into the content root and
// switch the anti-phishing headers off for the site. Validating at the
// filesystem is the fix that cannot be forgotten per method.
//
// It also enforces the file-count cap on creation, for the same reason: a
// created lock-null resource is a file the quota has to count, and PUT is not
// the only way to make one.
type siteFS struct {
	dir      webdav.Dir
	st       *store.Store
	site     *store.Site
	maxFiles int
}

func newSiteFS(st *store.Store, site *store.Site) *siteFS {
	return &siteFS{dir: webdav.Dir(site.ContentDir()), st: st, site: site, maxFiles: st.EffMaxFiles(site)}
}

// check validates a DAV name. The root itself is always allowed; anything else
// has to be a path the store would accept as an upload.
func (f *siteFS) check(name string) error {
	rel := strings.TrimPrefix(path.Clean("/"+name), "/")
	if rel == "" {
		return nil
	}
	if _, err := store.CleanRelPath(rel); err != nil {
		// os.ErrPermission maps to a 4xx in every x/net/webdav handler; the
		// store's own sentinel would come out as a 500.
		return os.ErrPermission
	}
	return nil
}

func (f *siteFS) Mkdir(ctx context.Context, name string, perm os.FileMode) error {
	if err := f.check(name); err != nil {
		return err
	}
	return f.dir.Mkdir(ctx, name, perm)
}

func (f *siteFS) OpenFile(ctx context.Context, name string, flag int, perm os.FileMode) (webdav.File, error) {
	if err := f.check(name); err != nil {
		return nil, err
	}
	if flag&os.O_CREATE != 0 {
		if _, err := f.dir.Stat(ctx, name); err != nil && os.IsNotExist(err) {
			_, count, uerr := f.st.Usage(f.site)
			if uerr != nil {
				return nil, uerr
			}
			if count+1 > f.maxFiles {
				return nil, store.ErrTooManyFiles
			}
		}
	}
	return f.dir.OpenFile(ctx, name, flag, perm)
}

func (f *siteFS) RemoveAll(ctx context.Context, name string) error {
	if err := f.check(name); err != nil {
		return err
	}
	return f.dir.RemoveAll(ctx, name)
}

func (f *siteFS) Rename(ctx context.Context, oldName, newName string) error {
	if err := f.check(oldName); err != nil {
		return err
	}
	if err := f.check(newName); err != nil {
		return err
	}
	return f.dir.Rename(ctx, oldName, newName)
}

func (f *siteFS) Stat(ctx context.Context, name string) (os.FileInfo, error) {
	if err := f.check(name); err != nil {
		return nil, err
	}
	return f.dir.Stat(ctx, name)
}
