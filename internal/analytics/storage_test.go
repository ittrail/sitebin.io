package analytics

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
	"time"
)

func touch(t *testing.T, path, body string, mtime time.Time) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
}

func names(t *testing.T, dir string) []string {
	t.Helper()
	es, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range es {
		out = append(out, e.Name())
	}
	sort.Strings(out)
	return out
}

func TestRetentionStart(t *testing.T) {
	kiri, _ := time.LoadLocation("Pacific/Kiritimati") // UTC+14
	vienna, _ := time.LoadLocation("Europe/Vienna")
	now := tm("2026-11-05T12:00:00Z")
	for _, tc := range []struct {
		loc  *time.Location
		days int
		want string
	}{
		{time.UTC, 10, "2026-10-26"},
		{nil, 10, "2026-10-26"},
		{kiri, 10, "2026-10-27"}, // already 6 November there
		{vienna, 760, "2024-10-06"},
		{time.UTC, 0, ""}, // keeps every day
		{vienna, 0, ""},
	} {
		if got := RetentionStart(now, tc.loc, tc.days); got != tc.want {
			t.Errorf("RetentionStart(%v, %d) = %q, want %q", tc.loc, tc.days, got, tc.want)
		}
	}
	// Across a DST change the dates are calendar days, not 24-hour steps.
	if got := RetentionStart(tm("2026-03-30T00:30:00Z"), vienna, 1); got != "2026-03-29" {
		t.Errorf("across the spring change = %q", got)
	}
}

func TestPurgeDays(t *testing.T) {
	dir := t.TempDir()
	now := tm("2026-11-05T12:00:00Z")
	fresh, stale := now.Add(-time.Minute), now.Add(-2*time.Hour)
	ev := eventsDir(dir)
	for _, f := range []struct {
		name  string
		mtime time.Time
	}{
		// before the start: every file of the day, and any temp file of one
		{"2026-10-25.jsonl", stale},
		{"2026-10-25.sum.json", stale},
		{"2026-10-25.over", stale},
		{"2026-10-25.over.tmp123", fresh},
		{"2026-10-25.sum.json.tmp456", fresh},
		{"2026-10-25.jsonl.tmp789", fresh},
		{"2026-09-01.sum.json", stale}, // a summary without its day file
		// the start and after: kept
		{"2026-10-26.jsonl", stale},
		{"2026-10-26.sum.json", stale},
		{"2026-10-26.over", stale},
		{"2026-11-05.jsonl", fresh},
		// a crashed write's temp file of a kept day goes once it is an hour old
		{"2026-11-04.over.tmp1", stale},
		{"2026-11-05.over.tmp2", fresh},
		{"2026-11-04.sum.json.tmp3", stale},
		// not a day's file
		{"notes.txt", stale},
	} {
		touch(t, filepath.Join(ev, f.name), "x", f.mtime)
	}
	n, err := PurgeDays(dir, "2026-10-26", now)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"2026-10-26.jsonl", "2026-10-26.over", "2026-10-26.sum.json", "2026-11-05.jsonl", "2026-11-05.over.tmp2", "notes.txt"}
	if got := names(t, ev); !reflect.DeepEqual(got, want) {
		t.Errorf("left = %v, want %v", got, want)
	}
	if n != 9 {
		t.Errorf("deleted %d, want 9", n)
	}
	// Nothing to purge: no events directory, and it is not created.
	empty := t.TempDir()
	if n, err := PurgeDays(empty, "2026-10-26", now); n != 0 || err != nil {
		t.Errorf("no events dir: %d, %v", n, err)
	}
	if _, err := os.Stat(eventsDir(empty)); !os.IsNotExist(err) {
		t.Error("PurgeDays created the events directory")
	}
}

func TestStorage(t *testing.T) {
	dir := t.TempDir()
	if b, first, err := Storage(dir); b != 0 || first != "" || err != nil {
		t.Fatalf("no analytics: %d %q %v", b, first, err)
	}
	now := time.Now()
	touch(t, DayFile(dir, "2026-11-04"), "12345", now)
	touch(t, DayFile(dir, "2026-10-30"), "123", now)
	touch(t, summaryFile(dir, "2026-10-01"), "1234567", now) // a summary is no day
	touch(t, overFile(dir, "2026-09-01"), "1", now)
	touch(t, filepath.Join(dir, "analytics", "imported.json"), "[]", now)
	b, first, err := Storage(dir)
	if err != nil {
		t.Fatal(err)
	}
	if b != 5+3+7+1+2 || first != "2026-10-30" {
		t.Errorf("Storage = %d bytes, first %q", b, first)
	}
}

func TestImported(t *testing.T) {
	dir := t.TempDir()
	got, err := Imported(dir)
	if err != nil || got == nil || len(got) != 0 {
		t.Fatalf("none imported = %#v, %v; want an empty list", got, err)
	}
	touch(t, filepath.Join(dir, "analytics", "imported.json"), `[{"from":"2025-01-01","to":"2026-10-31"},{"from":"2024-06-01","to":"2024-12-31"}]`, time.Now())
	got, err = Imported(dir)
	want := []ImportedRange{{From: "2025-01-01", To: "2026-10-31"}, {From: "2024-06-01", To: "2024-12-31"}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("Imported = %+v, %v", got, err)
	}
	touch(t, filepath.Join(dir, "analytics", "imported.json"), `{broken`, time.Now())
	if _, err := Imported(dir); err == nil {
		t.Error("a broken imported.json read without an error")
	}
}
