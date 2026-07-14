package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// Stats is a site's lightweight view counter, stored as stats.json beside
// meta.json (kept separate so page views don't churn the metadata file).
type Stats struct {
	Views    int64      `json:"views"`
	LastSeen *time.Time `json:"last_seen"`
}

func statsPath(siteDir string) string { return filepath.Join(siteDir, "stats.json") }

// Stats returns the site's view stats (zero value if never viewed).
func (s *Store) Stats(site *Site) Stats {
	var st Stats
	if b, err := os.ReadFile(statsPath(site.dir)); err == nil {
		json.Unmarshal(b, &st)
	}
	return st
}

// RecordView increments the site's view counter and updates last_seen.
func (s *Store) RecordView(site *Site) {
	l := s.lockSite(site.ViewID)
	l.Lock()
	defer l.Unlock()

	var st Stats
	if b, err := os.ReadFile(statsPath(site.dir)); err == nil {
		json.Unmarshal(b, &st)
	}
	st.Views++
	now := time.Now().UTC()
	st.LastSeen = &now

	b, err := json.Marshal(st)
	if err != nil {
		return
	}
	tmp := statsPath(site.dir) + ".tmp"
	if os.WriteFile(tmp, b, 0o644) == nil {
		os.Rename(tmp, statsPath(site.dir))
	}
}
