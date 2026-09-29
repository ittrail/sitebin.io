package httpapi

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"

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
//
// And it resolves every name through an os.Root, never webdav.Dir, which
// follows symlinks: a container site's folders are written by code Sitebin
// does not control, and a link it plants (app/x -> /data) must not become a
// WebDAV path into the instance secret or another customer's site.
//
// Every write goes through a store.StagedFile and every rename through
// store.RenameChecked, so the abuse guard settles its verdict before
// anything a client wrote becomes visible (docs/superpowers/specs/2026-09-29-abuse-detection.md).
type siteFS struct {
	st       *store.Store
	site     *store.Site
	maxFiles int
	// lockHeld says the handler holds the site lock for this request (its
	// mutating methods); LOCK does not, and its staged file takes it.
	lockHeld bool

	mu sync.Mutex
	// held is the abuse guard's hold on this request, answered as a 403
	// with its message whatever status the DAV library would have chosen.
	held *store.HeldError
}

func newSiteFS(st *store.Store, site *store.Site, lockHeld bool) *siteFS {
	return &siteFS{st: st, site: site, maxFiles: st.EffMaxFiles(site), lockHeld: lockHeld}
}

// noteHeld remembers a hold for the response.
func (f *siteFS) noteHeld(err error) {
	var h *store.HeldError
	if errors.As(err, &h) {
		f.mu.Lock()
		f.held = h
		f.mu.Unlock()
	}
}

func (f *siteFS) heldErr() *store.HeldError {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.held
}

// stagedDAVFile is a staged write whose Close reports a hold to its siteFS.
type stagedDAVFile struct {
	*store.StagedFile
	fs *siteFS
}

func (s stagedDAVFile) Close() error {
	err := s.StagedFile.Close()
	s.fs.noteHeld(err)
	return err
}

// rootName maps a DAV name onto a name inside the content root.
func rootName(name string) string {
	rel := strings.TrimPrefix(path.Clean("/"+name), "/")
	if rel == "" {
		return "."
	}
	return filepath.FromSlash(rel)
}

// withRoot runs fn against the site's content directory as an os.Root. It is
// opened per call: files opened through it stay valid after it is closed.
func (f *siteFS) withRoot(fn func(*os.Root) error) error {
	r, err := store.OpenContentRoot(f.site)
	if err != nil {
		return err
	}
	defer r.Close()
	return fn(r)
}

// heldWriter answers a DAV request the abuse guard held with 403 and the
// hold's message. The DAV library picks its own status for a failed Close
// (405) or rename (403, silently), and neither says why; the guard's verdict
// is known before the library writes its status, so it is swapped in there.
type heldWriter struct {
	http.ResponseWriter
	fs      *siteFS
	wrote   bool
	swallow bool
}

func (w *heldWriter) WriteHeader(code int) {
	if w.wrote {
		return
	}
	w.wrote = true
	if h := w.fs.heldErr(); h != nil {
		w.swallow = true
		writeError(w.ResponseWriter, 403, h.Error())
		return
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *heldWriter) Write(b []byte) (int, error) {
	if !w.wrote {
		w.WriteHeader(http.StatusOK)
	}
	if w.swallow {
		return len(b), nil
	}
	return w.ResponseWriter.Write(b)
}

func (w *heldWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

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
	return f.withRoot(func(r *os.Root) error { return r.Mkdir(rootName(name), perm) })
}

func (f *siteFS) OpenFile(ctx context.Context, name string, flag int, perm os.FileMode) (webdav.File, error) {
	if err := f.check(name); err != nil {
		return nil, err
	}
	if flag&os.O_CREATE != 0 {
		if _, err := f.Stat(ctx, name); err != nil && os.IsNotExist(err) {
			_, count, uerr := f.st.Usage(f.site)
			if uerr != nil {
				return nil, uerr
			}
			if count+1 > f.maxFiles {
				return nil, store.ErrTooManyFiles
			}
		}
	}
	if flag&(os.O_WRONLY|os.O_RDWR) != 0 {
		sf, err := f.st.OpenStaged(f.site, rootName(name), flag, perm, f.lockHeld)
		if err != nil {
			if errors.Is(err, store.ErrBadPath) {
				return nil, os.ErrPermission
			}
			return nil, err
		}
		return stagedDAVFile{StagedFile: sf, fs: f}, nil
	}
	var file *os.File
	err := f.withRoot(func(r *os.Root) error {
		var err error
		file, err = r.OpenFile(rootName(name), flag, perm)
		return err
	})
	if err != nil {
		return nil, err
	}
	return file, nil
}

func (f *siteFS) RemoveAll(ctx context.Context, name string) error {
	if err := f.check(name); err != nil {
		return err
	}
	n := rootName(name)
	if n == "." {
		return os.ErrInvalid // the root itself, as webdav.Dir answers
	}
	return f.withRoot(func(r *os.Root) error { return r.RemoveAll(n) })
}

func (f *siteFS) Rename(ctx context.Context, oldName, newName string) error {
	if err := f.check(oldName); err != nil {
		return err
	}
	if err := f.check(newName); err != nil {
		return err
	}
	o, n := rootName(oldName), rootName(newName)
	if o == "." || n == "." {
		return os.ErrInvalid
	}
	err := f.st.RenameChecked(f.site, o, n, f.lockHeld)
	f.noteHeld(err)
	return err
}

func (f *siteFS) Stat(ctx context.Context, name string) (os.FileInfo, error) {
	if err := f.check(name); err != nil {
		return nil, err
	}
	var fi os.FileInfo
	err := f.withRoot(func(r *os.Root) error {
		var err error
		fi, err = r.Stat(rootName(name))
		return err
	})
	return fi, err
}
