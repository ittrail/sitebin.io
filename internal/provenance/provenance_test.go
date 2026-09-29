package provenance

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var t0 = time.Date(2026, 9, 28, 8, 49, 0, 0, time.UTC)

func upload(at time.Time, ip string, files int) Entry {
	return Entry{Time: at, Action: ActionUpload, Surface: SurfaceAPI, Auth: AuthToken, Account: "acc1", IP: ip, UA: "curl/8", Files: files}
}

func TestRecordAppendsAndReads(t *testing.T) {
	p := filepath.Join(t.TempDir(), "provenance.jsonl")
	if got, err := Read(p); err != nil || got != nil {
		t.Fatalf("missing file: %v %v", got, err)
	}
	if err := Record(p, Entry{Time: t0, Action: ActionCreate, Surface: SurfaceUI, IP: "203.0.113.7"}); err != nil {
		t.Fatal(err)
	}
	if err := Record(p, upload(t0.Add(time.Minute), "198.51.100.1", 2)); err != nil {
		t.Fatal(err)
	}
	got, err := Read(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Action != ActionCreate || got[1].Files != 2 || got[1].IP != "198.51.100.1" {
		t.Fatalf("entries: %+v", got)
	}
	b, _ := os.ReadFile(p)
	if n := strings.Count(string(b), "\n"); n != 2 {
		t.Fatalf("want one JSON line per entry, got %d lines:\n%s", n, b)
	}
}

// A burst from one client is one line: a WebDAV sync of 500 files must not
// push the site's history out of the capped log.
func TestRecordMergesABurst(t *testing.T) {
	p := filepath.Join(t.TempDir(), "p.jsonl")
	for i := 0; i < 5; i++ {
		if err := Record(p, upload(t0.Add(time.Duration(i)*time.Minute), "198.51.100.1", 1)); err != nil {
			t.Fatal(err)
		}
	}
	got, _ := Read(p)
	if len(got) != 1 {
		t.Fatalf("want one merged line, got %d: %+v", len(got), got)
	}
	e := got[0]
	if e.Times() != 5 || e.Files != 5 || !e.Time.Equal(t0) || e.Last == nil || !e.Last.Equal(t0.Add(4*time.Minute)) {
		t.Fatalf("merged entry: %+v", e)
	}
	if !e.Latest().Equal(t0.Add(4 * time.Minute)) {
		t.Fatalf("latest: %v", e.Latest())
	}
}

func TestRecordDoesNotMergeAcrossDifferences(t *testing.T) {
	p := filepath.Join(t.TempDir(), "p.jsonl")
	Record(p, upload(t0, "198.51.100.1", 1))
	Record(p, upload(t0.Add(time.Minute), "198.51.100.2", 1))                         // another address
	Record(p, upload(t0.Add(time.Minute+MergeWindow+time.Second), "198.51.100.2", 1)) // too late
	other := upload(t0.Add(time.Minute+MergeWindow+2*time.Second), "198.51.100.2", 1)
	other.UA = "python-requests"
	Record(p, other) // another client
	got, _ := Read(p)
	if len(got) != 4 {
		t.Fatalf("want 4 lines, got %d: %+v", len(got), got)
	}
}

// Origin events and token mints always stand alone: "minted twice" is a
// different fact from "minted once".
func TestRecordNeverMergesOriginsOrMints(t *testing.T) {
	p := filepath.Join(t.TempDir(), "p.jsonl")
	for _, a := range []string{ActionTokenMint, ActionTokenMint, ActionSignup, ActionSiteCreate, ActionSiteCreate} {
		Record(p, Entry{Time: t0, Action: a, IP: "203.0.113.7"})
	}
	if got, _ := Read(p); len(got) != 5 {
		t.Fatalf("want 5 lines, got %d", len(got))
	}
}

func TestRecordCapsButKeepsTheOrigin(t *testing.T) {
	p := filepath.Join(t.TempDir(), "p.jsonl")
	Record(p, Entry{Time: t0, Action: ActionCreate, IP: "203.0.113.7"})
	for i := 0; i < MaxEntries+20; i++ {
		// distinct addresses, so nothing merges
		Record(p, upload(t0.Add(time.Duration(i+1)*time.Second), "198.51.100."+itoa(i%250), 1))
	}
	got, _ := Read(p)
	if len(got) != MaxEntries {
		t.Fatalf("want %d lines, got %d", MaxEntries, len(got))
	}
	if got[0].Action != ActionCreate || got[0].IP != "203.0.113.7" {
		t.Fatalf("origin lost: %+v", got[0])
	}
	if last := got[len(got)-1]; !last.Time.Equal(t0.Add(time.Duration(MaxEntries+20) * time.Second)) {
		t.Fatalf("newest lost: %+v", last)
	}
}

func TestRecordCapsWithoutAnOrigin(t *testing.T) {
	p := filepath.Join(t.TempDir(), "p.jsonl")
	for i := 0; i < MaxEntries+5; i++ {
		Record(p, upload(t0.Add(time.Duration(i)*time.Second), "198.51.100."+itoa(i%250), 1))
	}
	got, _ := Read(p)
	if len(got) != MaxEntries || !got[0].Time.Equal(t0.Add(5*time.Second)) {
		t.Fatalf("got %d, first %v", len(got), got[0].Time)
	}
}

func TestPurge(t *testing.T) {
	p := filepath.Join(t.TempDir(), "p.jsonl")
	Record(p, Entry{Time: t0, Action: ActionCreate, IP: "203.0.113.7"})
	// merged burst whose LAST time is recent survives, although it began early
	Record(p, upload(t0.Add(time.Hour), "198.51.100.1", 1))
	Record(p, upload(t0.Add(time.Hour+5*time.Minute), "198.51.100.1", 1))
	Record(p, upload(t0.Add(48*time.Hour), "198.51.100.9", 1))

	n, err := Purge(p, t0.Add(time.Hour+time.Minute))
	if err != nil || n != 1 {
		t.Fatalf("purged %d, %v", n, err)
	}
	got, _ := Read(p)
	if len(got) != 2 || got[0].Times() != 2 {
		t.Fatalf("after purge: %+v", got)
	}
	// purging everything removes the file
	if n, err := Purge(p, t0.Add(100*time.Hour)); err != nil || n != 2 {
		t.Fatalf("purged %d, %v", n, err)
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatalf("empty log left behind: %v", err)
	}
	if n, err := Purge(p, t0); err != nil || n != 0 {
		t.Fatalf("purge of a missing log: %d %v", n, err)
	}
}

// A damaged line — a disk hiccup, a hand edit — costs that line, not the log.
func TestReadSkipsDamagedLines(t *testing.T) {
	p := filepath.Join(t.TempDir(), "p.jsonl")
	os.WriteFile(p, []byte(`{"t":"2026-09-28T08:49:00Z","action":"create","ip":"203.0.113.7"}
not json
{"t":"2026-09-28T08:50:00Z","action":"upload","ip":"203.0.113.7"}
`), 0o644)
	got, err := Read(p)
	if err != nil || len(got) != 2 {
		t.Fatalf("got %+v %v", got, err)
	}
}

func TestCleanUA(t *testing.T) {
	if got := CleanUA("Mozilla/5.0\r\nX-Evil: 1\x00"); got != "Mozilla/5.0X-Evil: 1" {
		t.Fatalf("control characters kept: %q", got)
	}
	long := strings.Repeat("é", 300)
	got := CleanUA(long)
	if len(got) > MaxUA || !strings.HasPrefix(long, got) {
		t.Fatalf("truncated to %d bytes, valid prefix: %v", len(got), strings.HasPrefix(long, got))
	}
	if CleanUA("\xff\xfeok") != "ok" {
		t.Fatalf("invalid UTF-8 kept: %q", CleanUA("\xff\xfeok"))
	}
}

func TestCleanDetail(t *testing.T) {
	if got := CleanDetail("a\nb"); got != "ab" {
		t.Fatalf("%q", got)
	}
	if got := CleanDetail(strings.Repeat("x", 500)); len(got) > MaxDetail {
		t.Fatalf("detail not capped: %d", len(got))
	}
}

func TestParseMatch(t *testing.T) {
	for _, tc := range []struct {
		q     string
		ok    bool
		match []string
		miss  []string
	}{
		{"203.0.113.7", true, []string{"203.0.113.7"}, []string{"203.0.113.70", "203.0.113.8", ""}},
		{" 203.0.113.0/24 ", true, []string{"203.0.113.7", "203.0.113.255"}, []string{"203.0.114.1", "2001:db8::1"}},
		{"2001:db8::/48", true, []string{"2001:db8::1", "2001:db8:0:ffff::1"}, []string{"2001:db9::1"}},
		{"2001:DB8::1", true, []string{"2001:db8::1"}, []string{"2001:db8::2"}},
		{"::ffff:203.0.113.7", true, []string{"203.0.113.7"}, nil},
		{"billy@example.com", false, nil, nil},
		{"203.0.113", false, nil, nil},
		{"", false, nil, nil},
	} {
		m, ok := ParseMatch(tc.q)
		if ok != tc.ok {
			t.Fatalf("%q: ok=%v", tc.q, ok)
		}
		for _, ip := range tc.match {
			if !m.Matches(ip) {
				t.Errorf("%q should match %q", tc.q, ip)
			}
		}
		for _, ip := range tc.miss {
			if m.Matches(ip) {
				t.Errorf("%q should not match %q", tc.q, ip)
			}
		}
	}
}

func TestNamesAddress(t *testing.T) {
	m, _ := ParseMatch("203.0.113.7")
	es := []Entry{{Action: ActionCreate, IP: "198.51.100.1"}, {Action: ActionUpload, IP: "203.0.113.7"}}
	if got := m.Filter(es); len(got) != 1 || got[0].Action != ActionUpload {
		t.Fatalf("filter: %+v", got)
	}
}

func itoa(i int) string {
	const d = "0123456789"
	if i < 10 {
		return d[i : i+1]
	}
	return itoa(i/10) + d[i%10:i%10+1]
}
