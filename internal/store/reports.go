package store

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Report is a single abuse/takedown report filed against a site.
//
// It never holds the reporter's address. Source is the address truncated to
// its /24 (IPv4) or /48 (IPv6): enough to see that twenty reports came from
// one network, not enough to name a person — and the files themselves are
// purged after ReportRetention by the cleanup sweep, which is the retention
// the privacy page promises.
type Report struct {
	Time    time.Time `json:"time"`
	Target  string    `json:"target"`            // what the reporter submitted (URL/domain/id)
	ViewID  string    `json:"view_id,omitempty"` // resolved site, if any
	Reason  string    `json:"reason"`
	Details string    `json:"details,omitempty"`
	Source  string    `json:"source,omitempty"` // truncated reporter network, see AnonymizeIP
}

const (
	// ReportRetention is how long a report is kept before the sweep purges
	// it. Fourteen days is what the privacy page says about logs, and a
	// report is a log entry someone typed.
	ReportRetention = 14 * 24 * time.Hour
)

// MaxReports bounds the reports directory. Reports are one file each and the
// endpoint is public, so a distributed sender could otherwise fill the data
// volume; past the cap the oldest 1% go first, in one batch. A variable only
// so a test can lower it.
var MaxReports = 10000

// AnonymizeIP truncates an address to its network: /24 for IPv4, /48 for
// IPv6. Anything that is not an address comes back empty rather than stored.
func AnonymizeIP(ip string) string {
	addr := net.ParseIP(strings.TrimSpace(ip))
	if addr == nil {
		return ""
	}
	if v4 := addr.To4(); v4 != nil {
		return v4.Mask(net.CIDRMask(24, 32)).String() + "/24"
	}
	return addr.Mask(net.CIDRMask(48, 128)).String() + "/48"
}

func (s *Store) reportsDir() string { return filepath.Join(s.root, "reports") }

// AddReport persists a report (one JSON file per report), evicting the oldest
// beyond MaxReports. The directory is listed once and the count kept in
// memory after that, so a flood of reports costs one file each, not one
// directory listing each.
func (s *Store) AddReport(r Report) error {
	if err := os.MkdirAll(s.reportsDir(), 0o755); err != nil {
		return err
	}
	if r.Time.IsZero() {
		r.Time = time.Now().UTC()
	}
	s.reportsMu.Lock()
	defer s.reportsMu.Unlock()
	if s.reportsN < 0 {
		s.reportsN = s.countReports()
	}
	if s.reportsN >= MaxReports {
		// evict in a batch, so the listing is not repeated for every report
		// once the cap is reached
		s.evictReports(MaxReports - MaxReports/100)
		s.reportsN = s.countReports()
	}
	s.reportsN++
	var buf [6]byte
	rand.Read(buf[:])
	name := fmt.Sprintf("%d-%s.json", r.Time.UnixNano(), hex.EncodeToString(buf[:]))
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(s.reportsDir(), name), b, 0o644)
}

// countReports lists the directory once.
func (s *Store) countReports() int {
	entries, err := os.ReadDir(s.reportsDir())
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range entries {
		if !e.IsDir() {
			n++
		}
	}
	return n
}

// reportTime reads the timestamp a report file's name carries.
func reportTime(name string) (time.Time, bool) {
	nanos, _, ok := strings.Cut(name, "-")
	if !ok {
		return time.Time{}, false
	}
	n, err := strconv.ParseInt(nanos, 10, 64)
	if err != nil {
		return time.Time{}, false
	}
	return time.Unix(0, n), true
}

// evictReports removes the oldest report files until at most keep remain.
// File names start with the report's nanosecond timestamp, so name order is
// time order and no file has to be opened.
func (s *Store) evictReports(keep int) {
	entries, err := os.ReadDir(s.reportsDir())
	if err != nil || len(entries) <= keep {
		return
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for _, n := range names[:len(names)-keep] {
		os.Remove(filepath.Join(s.reportsDir(), n))
	}
}

// PurgeReports deletes reports filed before the given time and returns how
// many went.
func (s *Store) PurgeReports(before time.Time) (int, error) {
	entries, err := os.ReadDir(s.reportsDir())
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	n := 0
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if t, ok := reportTime(e.Name()); ok && t.Before(before) {
			if os.Remove(filepath.Join(s.reportsDir(), e.Name())) == nil {
				n++
			}
		}
	}
	s.reportsMu.Lock()
	s.reportsN = -1 // recount on the next write
	s.reportsMu.Unlock()
	return n, nil
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
