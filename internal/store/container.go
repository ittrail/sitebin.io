package store

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// ComposeFile is the file at a container site's root that declares what runs.
const ComposeFile = "sitebin-container-compose.yaml"

// MaxComposeBytes bounds the compose file. A real one is a few hundred bytes;
// the bound keeps a hostile one from costing anything to read every tick.
const MaxComposeBytes = 64 << 10

// Observed container states, as the runtime reports them.
const (
	ContainerStarting = "starting"
	ContainerRunning  = "running"
	ContainerStopped  = "stopped"
	ContainerError    = "error"
)

// ContainerMeta is a container site's state. It has two halves with two
// writers, and keeping them apart is what lets the runtime be a reconciler
// rather than a command queue:
//
//   - desired state (Enabled, RestartSeq) is written by the core — the API
//     handlers — and never by the runtime;
//   - observed state (everything else) is written only by the runtime,
//     through ext.SiteService.SetContainerState.
//
// The runtime re-applies a project whenever the compose file's hash or
// RestartSeq differs from what it last applied, which is how every write path
// — WebDAV and FTP included — restarts a project without telling anyone.
type ContainerMeta struct {
	Enabled    bool `json:"enabled"`
	RestartSeq int  `json:"restart_seq,omitempty"`

	Status      string             `json:"status,omitempty"`
	Message     string             `json:"message,omitempty"`
	AppliedHash string             `json:"applied_hash,omitempty"`
	AppliedSeq  int                `json:"applied_seq,omitempty"`
	Services    []ContainerService `json:"services,omitempty"`
	ObservedAt  *time.Time         `json:"observed_at,omitempty"`
}

// ContainerService is one service as it was last applied.
type ContainerService struct {
	Name    string            `json:"name"`
	Image   string            `json:"image"`
	Egress  bool              `json:"egress,omitempty"`
	Volumes []string          `json:"volumes,omitempty"`
	Domains []ContainerDomain `json:"domains,omitempty"`
	// State is Docker's word for the container: running, restarting, exited…
	State string `json:"state,omitempty"`
	// Host is the name the service answers to on its project network, and
	// what Caddy proxies to.
	Host string `json:"host,omitempty"`
}

// ContainerDomain maps a host onto a port inside the service. Domain is "*"
// for the site's own address.
type ContainerDomain struct {
	Domain string `json:"domain"`
	Port   int    `json:"port"`
}

// DefaultDomain is the compose file's name for the site's own address.
const DefaultDomain = "*"

// Upstream answers where a request for host goes: "<service host>:<port>".
// viewHost is the site's own address, which the "*" mapping answers for.
// ok=false means nothing is mapped to host, or the project is not running —
// and a container site is never served any other way.
func (m *ContainerMeta) Upstream(host, viewHost string) (string, bool) {
	if m == nil || !m.Enabled || m.Status != ContainerRunning {
		return "", false
	}
	host = strings.ToLower(host)
	want := host
	if viewHost != "" && host == strings.ToLower(viewHost) {
		want = DefaultDomain
	}
	for _, s := range m.Services {
		if s.Host == "" {
			continue
		}
		for _, d := range s.Domains {
			if d.Domain == want {
				return s.Host + ":" + strconv.Itoa(d.Port), true
			}
		}
	}
	return "", false
}

// ReadCompose returns the site's compose file. ErrNotFound when there is
// none; ErrTooLarge past MaxComposeBytes; ErrBadPath when it is not a regular
// file (a container can have replaced it with a link).
func (s *Store) ReadCompose(site *Site) ([]byte, error) {
	root, err := os.OpenRoot(site.FilesDir())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	defer root.Close()
	fi, err := root.Lstat(ComposeFile)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, ErrBadPath
	}
	if fi.Size() > MaxComposeBytes {
		return nil, ErrTooLarge
	}
	return root.ReadFile(ComposeFile)
}

// ValidVolumeFolder reports whether name can be a volume: one path segment
// the store would accept as an upload, and not a hidden name (Sitebin's own
// markers are hidden, and so is nothing a customer means to mount).
func ValidVolumeFolder(name string) bool {
	if name == "" || strings.ContainsAny(name, `/\`) || strings.HasPrefix(name, ".") {
		return false
	}
	c, err := CleanRelPath(name)
	return err == nil && c == name
}

// PrepareVolume makes sure the root folder name exists as a real directory
// and returns its path relative to the data root, slash-separated
// ("sites/<id>/files/<name>"), which the runtime turns into a mount.
//
// It is checked to be a directory and NOT a link immediately before the
// runtime mounts it: Docker resolves a bind source on the host, so a folder a
// container had swapped for a link to / would mount the host's root.
func (s *Store) PrepareVolume(site *Site, name string) (string, error) {
	if !ValidVolumeFolder(name) {
		return "", fmt.Errorf("%w: %q is not a folder name", ErrBadPath, name)
	}
	root, err := openContent(site)
	if err != nil {
		return "", err
	}
	defer root.Close()
	if err := root.Mkdir(name, 0o755); err != nil && !os.IsExist(err) {
		return "", err
	}
	fi, err := root.Lstat(name)
	if err != nil {
		return "", err
	}
	if !fi.IsDir() || fi.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("%w: %q is not a folder", ErrBadPath, name)
	}
	rel, err := filepath.Rel(s.root, filepath.Join(site.FilesDir(), name))
	if err != nil {
		return "", err
	}
	return filepath.ToSlash(rel), nil
}

// PurgeSymlinks removes every symlink in the site's content tree and returns
// how many it removed. Leaving container mode calls it after the containers
// are gone: Caddy's file server follows links, and only containers can have
// made one.
func (s *Store) PurgeSymlinks(site *Site) (int, error) {
	n := 0
	err := filepath.WalkDir(site.FilesDir(), func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if d.Type()&fs.ModeSymlink != 0 {
			if err := os.Remove(p); err != nil {
				return err
			}
			n++
		}
		return nil
	})
	return n, err
}
