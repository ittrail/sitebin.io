package analytics

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// What bounds the bytes analytics keeps on disk: lines stored as they came
// (no HTML escaping), a byte budget per source and site-day beside the line
// cap, and a free-space floor on the data volume.

// escapedLT is the JSON escape of "<": a backslash, then u003c.
var escapedLT = []byte{92, 'u', '0', '0', '3', 'c'}

// A beacon of nothing but "<" is stored with each "<" as one byte: never as
// the six of "\u003c", which would let a sender multiply what every
// field's bound allows on disk.
func TestRecorderStoresAngleBracketsAsTheyCame(t *testing.T) {
	e := newRecorderEnv(t)
	dir := e.site(t, testView)
	r := e.recorder(100)
	lt := func(n int) string { return strings.Repeat("<", n) }
	data := map[string]any{}
	for i := range 20 {
		data[fmt.Sprintf("%02d", i)+lt(48)] = lt(200)
	}
	h := scriptHit()
	h.Kind, h.Name, h.Data = "e", lt(50), data
	h.RawPath, h.RawQuery = "/"+lt(400), "utm_source="+lt(150)+"&utm_campaign="+lt(150)
	h.Referrer = "https://ref.example/" + lt(300)
	h.Title, h.Tag = lt(300), lt(80)
	r.Add(h)
	r.Flush()

	raw, err := os.ReadFile(DayFile(dir, "2026-11-04"))
	if err != nil {
		t.Fatal(err)
	}
	if i := bytes.Index(raw, escapedLT); i >= 0 {
		t.Fatalf("a < was stored escaped: ...%s", raw[max(0, i-60):i+20])
	}
	recs, err := ReadDay(dir, "2026-11-04")
	if err != nil || len(recs) != 1 {
		t.Fatalf("ReadDay = %d records, %v", len(recs), err)
	}
	rec := recs[0]
	fields := len(rec.P) + len(rec.R) + len(rec.Ti) + len(rec.Tg) + len(rec.N)
	for k, v := range rec.Q {
		fields += len(k) + len(v)
	}
	for k, v := range rec.D {
		fields += len(k) + len(v.(string))
	}
	if fields < 5000 {
		t.Fatalf("the sanitized fields hold %d bytes; the beacon did not fill them", fields)
	}
	if line := len(bytes.TrimSpace(raw)); float64(line) > 1.1*float64(fields) {
		t.Fatalf("the line is %d bytes for %d bytes of fields; want at most 1.1x", line, fields)
	}
}

// recorderWith is e.recorder with the byte budget, the free-space check and
// the clock set.
func (e *recorderEnv) recorderWith(dailyCap int, dailyBytes int64, free func() (uint64, error), now func() time.Time) *Recorder {
	return NewRecorder(RecorderConfig{
		DailyCap:   dailyCap,
		DailyBytes: dailyBytes,
		FreeSpace:  free,
		Salts:      NewSalts(e.data),
		WithSite:   e.withSite,
		Now:        now,
		Log:        slog.New(slog.DiscardHandler),
	})
}

// lineBytes is the size of the line a hit is stored as, newline included.
func lineBytes(t *testing.T, e *recorderEnv, h Hit) int64 {
	t.Helper()
	_, line, ok := e.recorder(100).record(h)
	if !ok {
		t.Fatal("the hit is not recorded")
	}
	return int64(len(line)) + 1
}

func TestRecorderDailyByteBudgetIsPerSource(t *testing.T) {
	e := newRecorderEnv(t)
	dir := e.site(t, testView)
	over := func() string {
		b, _ := os.ReadFile(filepath.Join(dir, "analytics", "events", "2026-11-04.over"))
		return strings.TrimSpace(string(b))
	}
	// Room for two lines of each source, far below the line cap.
	budget := 2*max(lineBytes(t, e, pageHit()), lineBytes(t, e, scriptHit())) + 10
	r := e.recorderWith(1000, budget, nil, nil)
	for range 4 {
		r.Add(scriptHit())
	}
	for range 3 {
		r.Add(pageHit())
	}
	r.Flush()
	if n := sourceCounts(t, dir, "2026-11-04"); n["js"] != 2 || n["srv"] != 2 || over() != "3" {
		t.Fatalf("records = %v, .over = %q; want js 2, srv 2, over 3", n, over())
	}
	// A restarted recorder counts the bytes already in the file.
	r2 := e.recorderWith(1000, budget, nil, nil)
	r2.Add(pageHit())
	r2.Add(scriptHit())
	r2.Flush()
	if n := sourceCounts(t, dir, "2026-11-04"); n["js"] != 2 || n["srv"] != 2 || over() != "5" {
		t.Fatalf("after restart: records = %v, .over = %q; want js 2, srv 2, over 5", n, over())
	}
	// Imported lines count against neither budget.
	big := `{"t":1,"k":"p","s":"um","p":"/` + strings.Repeat("x", int(budget)) + `"}` + "\n"
	if err := os.WriteFile(DayFile(dir, "2026-11-05"), []byte(strings.Repeat(big, 3)), 0o644); err != nil {
		t.Fatal(err)
	}
	h := pageHit()
	h.Time = h.Time.Add(24 * time.Hour)
	r2.Add(h)
	r2.Flush()
	if n := sourceCounts(t, dir, "2026-11-05"); n["um"] != 3 || n["srv"] != 1 {
		t.Fatalf("next day: records = %v; want um 3, srv 1", n)
	}
}

func TestCountDayBytesPerClass(t *testing.T) {
	path := filepath.Join(t.TempDir(), "d.jsonl")
	srv := `{"t":1,"k":"p","s":"srv"}` + "\n"
	js := `{"t":2,"k":"p","s":"js"}` + "\n"
	um := `{"t":3,"k":"p","s":"um"}` + "\n"
	cut := `{"t":4,"k":"p","s":"js","p":"/cut`
	if err := os.WriteFile(path, []byte(srv+js+um+srv+cut), 0o644); err != nil {
		t.Fatal(err)
	}
	s, j, isCut, err := countDay(path)
	if err != nil || !isCut {
		t.Fatalf("countDay: cut %v, %v", isCut, err)
	}
	if want := (classUse{lines: 3, bytes: int64(2*len(srv) + len(cut))}); s != want {
		t.Errorf("srv = %+v, want %+v (a cut line counts as the server's)", s, want)
	}
	if want := (classUse{lines: 1, bytes: int64(len(js))}); j != want {
		t.Errorf("js = %+v, want %+v", j, want)
	}
}

func TestRecorderStopsBelowTheFreeSpaceFloor(t *testing.T) {
	e := newRecorderEnv(t)
	dir := e.site(t, testView)
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	free := uint64(defaultMinFree - 1)
	asked := 0
	r := e.recorderWith(100, 0, func() (uint64, error) { asked++; return free, nil }, func() time.Time { return now })

	r.Add(pageHit())
	r.Add(pageHit())
	r.Flush()
	if _, err := os.Stat(filepath.Join(dir, "analytics")); !os.IsNotExist(err) {
		t.Fatalf("records were written below the floor: %v", err)
	}
	if r.floorDropped != 2 || asked != 1 {
		t.Fatalf("dropped %d, asked %d; want 2, 1", r.floorDropped, asked)
	}

	// The answer is kept for 30 seconds...
	free = defaultMinFree
	now = now.Add(29 * time.Second)
	r.Add(pageHit())
	r.Flush()
	if r.floorDropped != 3 || asked != 1 {
		t.Fatalf("within 30 s: dropped %d, asked %d; want 3, 1", r.floorDropped, asked)
	}
	// ...then asked again, and writing resumes.
	now = now.Add(2 * time.Second)
	r.Add(pageHit())
	r.Flush()
	if asked != 2 || len(readLines(t, DayFile(dir, "2026-11-04"))) != 1 {
		t.Fatalf("after 30 s: asked %d; want 2 and one line written", asked)
	}
	if _, err := os.Stat(filepath.Join(dir, "analytics", "events", "2026-11-04.over")); !os.IsNotExist(err) {
		t.Error("records dropped for the floor were counted in .over, a write to the full disk")
	}

	// A free space that cannot be read stops nothing.
	r2 := e.recorderWith(100, 0, func() (uint64, error) { return 0, errors.New("statfs failed") }, func() time.Time { return now })
	r2.Add(pageHit())
	r2.Flush()
	if n := len(readLines(t, DayFile(dir, "2026-11-04"))); n != 2 {
		t.Fatalf("with an unreadable free space: %d lines, want 2", n)
	}
}

func TestRecorderFloorIsSettable(t *testing.T) {
	e := newRecorderEnv(t)
	dir := e.site(t, testView)
	r := NewRecorder(RecorderConfig{
		Salts: NewSalts(e.data), WithSite: e.withSite, Log: slog.New(slog.DiscardHandler),
		FreeSpace: func() (uint64, error) { return 5 << 20, nil }, MinFree: 1 << 20,
	})
	r.Add(pageHit())
	r.Flush()
	if n := len(readLines(t, DayFile(dir, "2026-11-04"))); n != 1 {
		t.Fatalf("5 MiB free over a 1 MiB floor: %d lines, want 1", n)
	}
}

func TestRecorderLogsFloorDropsAtMostOnceAMinute(t *testing.T) {
	e := newRecorderEnv(t)
	e.site(t, testView)
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	logs := &countingHandler{}
	r := NewRecorder(RecorderConfig{
		Salts: NewSalts(e.data), WithSite: e.withSite, Log: slog.New(logs),
		FreeSpace: func() (uint64, error) { return 0, nil }, Now: func() time.Time { return now },
	})
	const msg = "analytics: the data volume is almost full, records dropped"
	for range 3 {
		r.Add(pageHit())
		r.Flush()
		now = now.Add(20 * time.Second)
	}
	if n := logs.count(msg); n != 1 {
		t.Fatalf("logged %d times in 60 s, want 1", n)
	}
	r.Add(pageHit())
	r.Flush()
	if n := logs.count(msg); n != 2 {
		t.Fatalf("logged %d times after a minute, want 2", n)
	}
}

// countingHandler counts log records by message.
type countingHandler struct {
	mu   sync.Mutex
	msgs map[string]int
}

func (h *countingHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *countingHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.msgs == nil {
		h.msgs = map[string]int{}
	}
	h.msgs[r.Message]++
	return nil
}
func (h *countingHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *countingHandler) WithGroup(string) slog.Handler      { return h }
func (h *countingHandler) count(msg string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.msgs[msg]
}

func TestDiskFreeAnswersForADirectory(t *testing.T) {
	n, err := DiskFree(t.TempDir())
	if errors.Is(err, errors.ErrUnsupported) {
		t.Skip("no free-space call on this platform")
	}
	if err != nil || n == 0 {
		t.Fatalf("DiskFree = %d, %v", n, err)
	}
	if _, err := DiskFree(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Error("DiskFree of a missing directory answered")
	}
}

// A day file over the read limit is refused with ErrDayTooLarge instead of
// being read into memory.
func TestParseDayRefusesAFileOverTheLimit(t *testing.T) {
	dir := t.TempDir()
	path := DayFile(dir, "2026-11-04")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	line := `{"t":1,"k":"p","s":"srv","p":"/"}` + "\n"
	if err := os.WriteFile(path, []byte(strings.Repeat(line, 10)), 0o644); err != nil {
		t.Fatal(err)
	}
	defer func(old int64) { maxDayFile = old }(maxDayFile)
	maxDayFile = int64(10 * len(line))
	if d, err := parseDay(path, int64(10*len(line))); err != nil || len(d.recs) != 10 {
		t.Fatalf("at the limit: %v", err)
	}
	maxDayFile--
	_, err := parseDay(path, int64(10*len(line)))
	if !errors.Is(err, ErrDayTooLarge) {
		t.Fatalf("over the limit: err = %v, want ErrDayTooLarge", err)
	}
	// Through the engine too, for a closed day (summary) and today (raw).
	en := NewEngine(64 << 20)
	en.now = func() time.Time { return time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC) }
	if _, err := en.summary(dir, "2026-11-04", time.UTC); !errors.Is(err, ErrDayTooLarge) {
		t.Fatalf("summary: err = %v, want ErrDayTooLarge", err)
	}
	if _, _, err := en.day(dir, "2026-11-04"); !errors.Is(err, ErrDayTooLarge) {
		t.Fatalf("day: err = %v, want ErrDayTooLarge", err)
	}
}
