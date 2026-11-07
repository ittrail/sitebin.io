//go:build ee

package cluster

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"syscall"

	"github.com/ittrail/sitebin.io/internal/ids"
)

// IDFile is the cluster id's path below the data directory.
const IDFile = "cluster/id"

// ErrInvalidID reports an existing id file whose content is not a cluster id.
// The file is left alone: replacing it would silently change the instance's
// identity.
var ErrInvalidID = errors.New("cluster: id file does not hold a valid cluster id")

var idPattern = regexp.MustCompile(`^[a-z2-7]{26}$`)

// LoadOrCreateID returns the instance's cluster id, creating it on first use.
// It is safe against concurrent callers and processes: the id is written to a
// temp file and hard-linked to its final name, which fails if another caller
// got there first — then that caller's id is read and returned.
func LoadOrCreateID(dataDir string) (string, error) {
	path := filepath.Join(dataDir, filepath.FromSlash(IDFile))
	if id, err := readID(path); err == nil || !errors.Is(err, os.ErrNotExist) {
		return id, err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	id := ids.New()
	tmp, err := os.CreateTemp(dir, "id-*.tmp")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	_, werr := tmp.WriteString(id + "\n")
	if werr == nil {
		werr = tmp.Sync()
	}
	if cerr := tmp.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return "", werr
	}
	if err := os.Chmod(tmp.Name(), 0o600); err != nil && !errors.Is(err, os.ErrPermission) {
		return "", err
	}
	if err := os.Link(tmp.Name(), path); err != nil {
		if existing, rerr := readID(path); rerr == nil || !errors.Is(rerr, os.ErrNotExist) {
			return existing, rerr
		}
		return "", err
	}
	// The file's bytes are synced; its directory entry is not until the
	// directory is. A power loss right after the first start could otherwise
	// drop the entry, and the next start would mint a NEW id while the stack
	// has bound the licence to this one. MkdirAll may have just created the
	// cluster directory itself, so its parent is synced too.
	if err := syncDir(dir); err != nil {
		return "", fmt.Errorf("cluster: created %s but could not sync its directory: %w", path, err)
	}
	if err := syncDir(filepath.Dir(dir)); err != nil {
		return "", fmt.Errorf("cluster: created %s but could not sync %s: %w", path, filepath.Dir(dir), err)
	}
	return id, nil
}

// syncDir fsyncs a directory so the entries in it survive a power loss. Windows
// cannot sync a directory (FlushFileBuffers refuses a directory handle), so
// there it is best effort and its error is ignored; NTFS journals the entry.
// The same holds for a filesystem that does not support syncing a directory
// at all (EINVAL, ENOTSUP: some FUSE and network mounts): there is nothing
// more to do, and failing would only cost the id on every first start.
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		if runtime.GOOS == "windows" {
			return nil
		}
		return err
	}
	err = d.Sync()
	if cerr := d.Close(); err == nil {
		err = cerr
	}
	if runtime.GOOS == "windows" || errors.Is(err, syscall.EINVAL) || errors.Is(err, errors.ErrUnsupported) {
		return nil
	}
	return err
}

func readID(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	id := strings.TrimSpace(string(b))
	if !idPattern.MatchString(id) {
		return "", fmt.Errorf("%w: %s", ErrInvalidID, path)
	}
	return id, nil
}
