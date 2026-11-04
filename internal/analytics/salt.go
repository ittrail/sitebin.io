package analytics

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"
)

var saltDateRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

// Salts keeps the per-date random salts that visitor ids are hashed with.
// A salt is deleted two days after its date, so an id can no longer be tied
// to a person or to another day.
type Salts struct {
	dir   string
	mu    sync.Mutex
	cache map[string][]byte
}

// NewSalts keeps salts under <dataDir>/analytics/salts.
func NewSalts(dataDir string) *Salts {
	return &Salts{
		dir:   filepath.Join(dataDir, "analytics", "salts"),
		cache: map[string][]byte{},
	}
}

// For returns the salt of a date (YYYY-MM-DD), creating 32 random bytes on
// first use. It is safe for concurrent use.
func (s *Salts) For(date string) ([]byte, error) {
	if !saltDateRe.MatchString(date) {
		return nil, fmt.Errorf("analytics: bad salt date %q", date)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if b, ok := s.cache[date]; ok {
		return b, nil
	}
	path := filepath.Join(s.dir, date)
	if b, err := os.ReadFile(path); err == nil && len(b) == 32 {
		s.cache[date] = b
		return b, nil
	}
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return nil, err
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	// Temp file + rename, so no reader ever sees a half-written salt.
	tmp, err := os.CreateTemp(s.dir, ".tmp-")
	if err != nil {
		return nil, err
	}
	_, werr := tmp.Write(b)
	cerr := tmp.Close()
	if err := errors.Join(werr, cerr, os.Chmod(tmp.Name(), 0o600)); err != nil {
		os.Remove(tmp.Name())
		return nil, err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		os.Remove(tmp.Name())
		// Another process may have won the race where rename onto an
		// existing file fails: use its salt.
		if existing, rerr := os.ReadFile(path); rerr == nil && len(existing) == 32 {
			s.cache[date] = existing
			return existing, nil
		}
		return nil, err
	}
	s.cache[date] = b
	return b, nil
}

// Prune deletes the salts of every date before today(UTC) minus two days and
// drops them from the cache.
func (s *Salts) Prune(today time.Time) error {
	cutoff := today.UTC().AddDate(0, 0, -2).Format("2006-01-02")
	s.mu.Lock()
	defer s.mu.Unlock()
	for d := range s.cache {
		if d < cutoff {
			delete(s.cache, d)
		}
	}
	entries, err := os.ReadDir(s.dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var errs []error
	for _, e := range entries {
		n := e.Name()
		if !saltDateRe.MatchString(n) || n >= cutoff {
			continue
		}
		if err := os.Remove(filepath.Join(s.dir, n)); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// VisitorID is the first 16 hex characters of
// sha256(salt || 0 || viewID || 0 || ip || 0 || ua).
func VisitorID(salt []byte, viewID, ip, ua string) string {
	h := sha256.New()
	h.Write(salt)
	h.Write([]byte{0})
	h.Write([]byte(viewID))
	h.Write([]byte{0})
	h.Write([]byte(ip))
	h.Write([]byte{0})
	h.Write([]byte(ua))
	return hex.EncodeToString(h.Sum(nil))[:16]
}
