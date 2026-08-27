package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"time"
)

// Stats is a site's lightweight view counter, stored as stats.json beside
// meta.json (kept separate so page views don't churn the metadata file).
type Stats struct {
	Views    int64      `json:"views"`
	LastSeen *time.Time `json:"last_seen"`
	// CSPViolations counts every content-security-policy report the site's
	// pages have produced. CSPBlocked holds the distinct destinations they
	// tried to reach, capped at MaxBlockedURIs — for spotting abuse the
	// destination is the evidence, the count is only volume.
	CSPViolations int      `json:"csp_violations,omitempty"`
	CSPBlocked    []string `json:"csp_blocked,omitempty"`
}

// MaxBlockedURIs caps the distinct blocked destinations kept per site. A
// hostile page controls both how many reports it sends and what is in them, so
// this list has to be bounded; past the cap only the counter moves.
const MaxBlockedURIs = 20

func statsPath(siteDir string) string { return filepath.Join(siteDir, "stats.json") }

// Stats returns the site's view stats (zero value if never viewed).
func (s *Store) Stats(site *Site) Stats {
	var st Stats
	if b, err := os.ReadFile(statsPath(site.dir)); err == nil {
		json.Unmarshal(b, &st)
	}
	return st
}

// RecordCSPViolation records n reports naming the given blocked destinations.
// Distinct destinations accumulate up to MaxBlockedURIs; the count always
// moves, so volume stays visible after the list is full.
func (s *Store) RecordCSPViolation(site *Site, n int, blocked []string) {
	l := s.lockSite(site.ViewID)
	l.Lock()
	defer l.Unlock()

	var st Stats
	if b, err := os.ReadFile(statsPath(site.dir)); err == nil {
		json.Unmarshal(b, &st)
	}
	st.CSPViolations += n
	for _, u := range blocked {
		if u == "" || len(st.CSPBlocked) >= MaxBlockedURIs {
			continue
		}
		if !slices.Contains(st.CSPBlocked, u) {
			st.CSPBlocked = append(st.CSPBlocked, u)
		}
	}
	s.writeStats(site, st)
}

// writeStats persists stats atomically. Callers hold the site lock.
func (s *Store) writeStats(site *Site, st Stats) {
	b, err := json.Marshal(st)
	if err != nil {
		return
	}
	tmp := statsPath(site.dir) + ".tmp"
	if os.WriteFile(tmp, b, 0o644) == nil {
		os.Rename(tmp, statsPath(site.dir))
	}
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
	s.writeStats(site, st)
}
