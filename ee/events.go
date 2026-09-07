//go:build ee

package ee

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// seenEvents is the record of webhook deliveries already applied: one empty
// file per event under <data>/billing-events, named by a hash of provider and
// event id, so the filesystem's O_EXCL is the atomic "first time?" test and
// the record survives a restart. Entries older than seenEventsTTL are pruned
// as new ones are written — a replay is only possible inside the signature
// window, so a week is generous.
type seenEvents struct {
	dir string
	mu  sync.Mutex
	n   int
}

const seenEventsTTL = 7 * 24 * time.Hour

func newSeenEvents(dataDir string) *seenEvents {
	return &seenEvents{dir: filepath.Join(dataDir, "billing-events")}
}

// first reports whether provider's event id has not been seen before, and
// records it. A record that cannot be written counts as seen-before: an
// unrecordable event applied twice is the failure mode this exists to stop.
func (s *seenEvents) first(provider, id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return false
	}
	sum := sha256.Sum256([]byte(provider + ":" + id))
	f, err := os.OpenFile(filepath.Join(s.dir, hex.EncodeToString(sum[:])), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return false // exists, or cannot be recorded
	}
	f.Close()
	s.n++
	if s.n%100 == 1 {
		s.prune()
	}
	return true
}

func (s *seenEvents) prune() {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-seenEventsTTL)
	for _, e := range entries {
		if info, err := e.Info(); err == nil && info.ModTime().Before(cutoff) {
			os.Remove(filepath.Join(s.dir, e.Name()))
		}
	}
}
