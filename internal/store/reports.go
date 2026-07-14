package store

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// Report is a single abuse/takedown report filed against a site.
type Report struct {
	Time    time.Time `json:"time"`
	Target  string    `json:"target"`            // what the reporter submitted (URL/domain/id)
	ViewID  string    `json:"view_id,omitempty"` // resolved site, if any
	Reason  string    `json:"reason"`
	Details string    `json:"details,omitempty"`
	IP      string    `json:"ip,omitempty"`
}

func (s *Store) reportsDir() string { return filepath.Join(s.root, "reports") }

// AddReport persists a report (one JSON file per report).
func (s *Store) AddReport(r Report) error {
	if err := os.MkdirAll(s.reportsDir(), 0o755); err != nil {
		return err
	}
	if r.Time.IsZero() {
		r.Time = time.Now().UTC()
	}
	var buf [6]byte
	rand.Read(buf[:])
	name := fmt.Sprintf("%d-%s.json", r.Time.UnixNano(), hex.EncodeToString(buf[:]))
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(s.reportsDir(), name), b, 0o644)
}

// ListReports returns all reports, newest first.
func (s *Store) ListReports() ([]Report, error) {
	entries, err := os.ReadDir(s.reportsDir())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []Report
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		b, err := os.ReadFile(filepath.Join(s.reportsDir(), e.Name()))
		if err != nil {
			continue
		}
		var r Report
		if json.Unmarshal(b, &r) == nil {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Time.After(out[j].Time) })
	return out, nil
}
