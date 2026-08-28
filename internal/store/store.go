// Package store is Sitebin's database: plain directories and files under the
// data root. One folder per site (named by view id) with a meta.json, plus
// symlink index directories for edit-id and custom-domain lookups.
package store

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/ittrail/sitebin.io/internal/auth"
	"github.com/ittrail/sitebin.io/internal/ids"
)

var (
	ErrNotFound      = errors.New("site not found")
	ErrDomainTaken   = errors.New("domain already in use")
	ErrBadDomain     = errors.New("invalid domain name")
	ErrBadPath       = errors.New("invalid file path")
	ErrTooLarge      = errors.New("site size limit exceeded")
	ErrTooManyFiles  = errors.New("file count limit exceeded")
	ErrTooManyDomain = errors.New("custom domain limit exceeded")
)

// maxDomainsPerSite bounds custom domains per site to prevent one site from
// forcing unbounded certificate issuance.
const maxDomainsPerSite = 20

const (
	ModeWebserver = "webserver"
	ModeViewer    = "viewer"

	sitesDirName  = "sites"
	editIndexName = "edit-index"
	domainIndex   = "domain-index"
	rawDirName    = "_raw"

	// SPAMarker is an empty file in a webserver site's files/ that a Caddy
	// file-matcher keys on to enable index.html fallback for unknown paths.
	SPAMarker = ".sitebin-spa"

	// TrustedMarker is an empty file in a site's files/ that a Caddy
	// file-matcher keys on to SKIP the strict content-security headers.
	//
	// It marks trust, not hardening, and that polarity is deliberate: a marker
	// that was never written, was lost in a restore, or was not updated on a
	// tier change leaves the site served more strictly than intended, which is
	// an annoyance. The opposite polarity would leave a phishing site served
	// with no protection at all.
	TrustedMarker = ".sitebin-trusted"
)

// WriteSPAMarker creates the SPA-fallback marker in the site's files/ dir.
// ReserveDomains adds domains that no site may claim as a custom domain. The
// instance's own view domain belongs here: without it a site could attach
// evil.<view domain> and take over an address the wildcard already answers for.
func (s *Store) ReserveDomains(domains ...string) {
	for _, d := range domains {
		d = strings.ToLower(strings.TrimSpace(d))
		if d != "" && !slices.Contains(s.reserved, d) {
			s.reserved = append(s.reserved, d)
		}
	}
}

func (s *Store) WriteSPAMarker(site *Site) error {
	f, err := os.OpenFile(filepath.Join(site.FilesDir(), SPAMarker), os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	return f.Close()
}

// RemoveSPAMarker removes the SPA marker from files/ and _raw/ (the latter in
// case a mode switch swept it there).
func (s *Store) RemoveSPAMarker(site *Site) {
	os.Remove(filepath.Join(site.FilesDir(), SPAMarker))
	os.Remove(filepath.Join(site.FilesDir(), rawDirName, SPAMarker))
}

// SetTrusted writes or removes the trust marker, so Caddy serves the site
// without (or with) the strict content-security headers. Removal also clears a
// copy under _raw/, which a viewer-mode switch can have swept there.
func (s *Store) SetTrusted(site *Site, trusted bool) error {
	path := filepath.Join(site.FilesDir(), TrustedMarker)
	if !trusted {
		os.Remove(path)
		os.Remove(filepath.Join(site.FilesDir(), rawDirName, TrustedMarker))
		return nil
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	return f.Close()
}

// Trusted reports whether the site currently carries the trust marker.
func (s *Store) Trusted(site *Site) bool {
	_, err := os.Stat(filepath.Join(site.FilesDir(), TrustedMarker))
	return err == nil
}

// Store provides all site persistence. Methods are safe for concurrent use.
type Store struct {
	root       string
	baseDomain string
	// reserved are domains no site may claim as a custom domain, because the
	// instance already serves something there. It starts as the base domain and
	// grows by ReserveDomains — a separate view domain has to be in it, or a
	// site could claim <anything>.<view domain> and shadow the view namespace.
	reserved     []string
	maxSiteBytes int64
	maxFiles     int

	mu    sync.Mutex
	locks map[string]*sync.Mutex // per view id
}

// Site is a handle to one site. Meta is a snapshot; Update refreshes it.
type Site struct {
	ViewID string
	EditID string
	Meta   Meta
	dir    string
}

func (s *Site) Dir() string      { return s.dir }
func (s *Site) FilesDir() string { return filepath.Join(s.dir, "files") }

// ContentDir is where the user's own files live: files/ in webserver mode,
// files/_raw in viewer mode (files/ then holds the generated wrapper).
func (s *Site) ContentDir() string {
	if s.Meta.Mode == ModeViewer {
		return filepath.Join(s.FilesDir(), rawDirName)
	}
	return s.FilesDir()
}

// New opens (creating if needed) a store rooted at dataDir.
func New(dataDir, baseDomain string, maxSiteBytes int64, maxFiles int) (*Store, error) {
	s := &Store{
		root:         dataDir,
		baseDomain:   baseDomain,
		reserved:     []string{baseDomain},
		maxSiteBytes: maxSiteBytes,
		maxFiles:     maxFiles,
		locks:        make(map[string]*sync.Mutex),
	}
	for _, d := range []string{s.sitesDir(), s.editIndexDir(), s.domainIndexDir()} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return nil, fmt.Errorf("init data dir: %w", err)
		}
	}
	return s, nil
}

func (s *Store) Root() string           { return s.root }
func (s *Store) sitesDir() string       { return filepath.Join(s.root, sitesDirName) }
func (s *Store) editIndexDir() string   { return filepath.Join(s.root, editIndexName) }
func (s *Store) domainIndexDir() string { return filepath.Join(s.root, domainIndex) }

func (s *Store) lockSite(viewID string) *sync.Mutex {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, ok := s.locks[viewID]
	if !ok {
		l = &sync.Mutex{}
		s.locks[viewID] = l
	}
	return l
}

// WithLock runs fn while holding the site's exclusive lock — the same lock
// used by SaveFile/Update/Delete. Callers that mutate a site's files outside
// the store's own methods (e.g. the WebDAV handler) use this to serialize
// with API writes.
func (s *Store) WithLock(viewID string, fn func() error) error {
	l := s.lockSite(viewID)
	l.Lock()
	defer l.Unlock()
	return fn()
}

// Create makes a new empty site and returns it with the plaintext edit
// password — the only time it is ever available.
func (s *Store) Create() (*Site, string, error) {
	var dir, viewID string
	for try := 0; ; try++ {
		viewID = ids.NewViewID()
		dir = filepath.Join(s.sitesDir(), viewID)
		if err := os.Mkdir(dir, 0o755); err == nil {
			break
		} else if !os.IsExist(err) || try >= 3 {
			return nil, "", fmt.Errorf("create site dir: %w", err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, "files"), 0o755); err != nil {
		return nil, "", fmt.Errorf("create files dir: %w", err)
	}

	editID := ids.NewEditID()
	pw := ids.NewEditPassword()
	now := time.Now().UTC()
	meta := Meta{
		ID:               viewID,
		EditID:           editID,
		EditPasswordHash: auth.HashPassword(pw),
		Mode:             ModeWebserver,
		CustomDomains:    []string{},
		EntryFile:        "index.html",
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	if err := writeMeta(dir, meta); err != nil {
		os.RemoveAll(dir)
		return nil, "", err
	}
	link := filepath.Join(s.editIndexDir(), editID)
	if err := makeLink(link, filepath.Join("..", sitesDirName, viewID), dir); err != nil {
		os.RemoveAll(dir)
		return nil, "", fmt.Errorf("index edit id: %w", err)
	}
	return &Site{ViewID: viewID, EditID: editID, Meta: meta, dir: dir}, pw, nil
}

// SetEditPassword generates a fresh edit password for the site, stores its
// hash, and returns the plaintext once. Used by the enterprise dashboard's
// "reset edit password" (ownership proves identity, so the old password is not
// required). The edit URL/id is unchanged.
func (s *Store) SetEditPassword(site *Site) (string, error) {
	pw := ids.NewEditPassword()
	hash := auth.HashPassword(pw)
	if err := s.Update(site, func(m *Meta) error {
		m.EditPasswordHash = hash
		return nil
	}); err != nil {
		return "", err
	}
	return pw, nil
}

// ByViewID loads a site by its view id (subdomain label / folder name).
func (s *Store) ByViewID(viewID string) (*Site, error) {
	if !ids.ValidID(viewID) {
		return nil, ErrNotFound
	}
	return s.load(filepath.Join(s.sitesDir(), viewID))
}

// ByEditID resolves the edit-index link and loads the site.
func (s *Store) ByEditID(editID string) (*Site, error) {
	if !ids.ValidID(editID) {
		return nil, ErrNotFound
	}
	return s.load(filepath.Join(s.editIndexDir(), editID))
}

// ByDomain resolves a custom-domain link and loads the site.
func (s *Store) ByDomain(domain string) (*Site, error) {
	domain, err := normalizeDomain(domain)
	if err != nil {
		return nil, ErrNotFound
	}
	return s.load(filepath.Join(s.domainIndexDir(), domain))
}

// load reads meta.json under dir (following index links) and returns a Site
// handle rooted at the real site directory.
func (s *Store) load(dir string) (*Site, error) {
	meta, err := readMeta(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if !ids.ValidID(meta.ID) {
		return nil, fmt.Errorf("corrupt meta in %s: bad id", dir)
	}
	return &Site{
		ViewID: meta.ID,
		EditID: meta.EditID,
		Meta:   meta,
		dir:    filepath.Join(s.sitesDir(), meta.ID),
	}, nil
}

// Update atomically mutates a site's metadata: it re-reads the current
// meta.json under the site lock, applies mutate, bumps updated_at, and writes
// it back. site.Meta is refreshed with the result.
func (s *Store) Update(site *Site, mutate func(*Meta) error) error {
	l := s.lockSite(site.ViewID)
	l.Lock()
	defer l.Unlock()

	meta, err := readMeta(site.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return ErrNotFound
		}
		return err
	}
	if err := mutate(&meta); err != nil {
		return err
	}
	meta.UpdatedAt = time.Now().UTC()
	if err := writeMeta(site.dir, meta); err != nil {
		return err
	}
	site.Meta = meta
	return nil
}

// Delete removes the site folder and all index links pointing at it.
func (s *Store) Delete(site *Site) error {
	l := s.lockSite(site.ViewID)
	l.Lock()
	defer l.Unlock()

	meta, err := readMeta(site.dir)
	if err == nil {
		for _, d := range meta.CustomDomains {
			os.Remove(filepath.Join(s.domainIndexDir(), d))
		}
	}
	os.Remove(filepath.Join(s.editIndexDir(), site.EditID))
	if err := os.RemoveAll(site.dir); err != nil {
		return fmt.Errorf("delete site: %w", err)
	}
	s.mu.Lock()
	delete(s.locks, site.ViewID)
	s.mu.Unlock()
	return nil
}

// AllSites iterates every site folder, yielding a Site handle. Used by the
// cleanup worker; tolerates and reports (skips) unreadable entries.
func (s *Store) AllSites() ([]*Site, error) {
	entries, err := os.ReadDir(s.sitesDir())
	if err != nil {
		return nil, err
	}
	var sites []*Site
	for _, e := range entries {
		if !e.IsDir() || !ids.ValidID(e.Name()) {
			continue
		}
		site, err := s.ByViewID(e.Name())
		if err != nil {
			continue
		}
		sites = append(sites, site)
	}
	return sites, nil
}
