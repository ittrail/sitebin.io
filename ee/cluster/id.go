//go:build ee

package cluster

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

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
	return id, nil
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
