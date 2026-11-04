package analytics

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	testView = "aaaaaaaaaaaaaaaaaaaaaaaaaa"
	testIP   = "203.0.113.77"
	testUA   = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36 RecorderTestAgent/9"
)

// recorderEnv is a data dir with a sites/ folder and a WithSite that, like
// store.WithStatsLock, calls fn only while the site's meta.json exists.
type recorderEnv struct {
	data, sites string
}

func newRecorderEnv(t *testing.T) *recorderEnv {
	t.Helper()
	data := t.TempDir()
	return &recorderEnv{data: data, sites: filepath.Join(data, "sites")}
}

func (e *recorderEnv) withSite(viewID string, fn func(siteDir string) error) (bool, error) {
	dir := filepath.Join(e.sites, viewID)
	if _, err := os.Stat(filepath.Join(dir, "meta.json")); err != nil {
		return false, nil
	}
	return true, fn(dir)
}

func (e *recorderEnv) site(t *testing.T, viewID string) string {
	t.Helper()
	dir := filepath.Join(e.sites, viewID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func (e *recorderEnv) recorder(dailyCap int) *Recorder {
	return NewRecorder(RecorderConfig{
		DailyCap: dailyCap,
		Salts:    NewSalts(e.data),
		WithSite: e.withSite,
		Log:      slog.New(slog.DiscardHandler),
	})
}

func pageHit() Hit {
	return Hit{
		Time:     time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC),
		ViewID:   testView,
		Kind:     "p",
		Source:   "srv",
		Address:  "shop.example",
		RawPath:  "/pricing?utm_source=news&secret=1",
		RawQuery: "utm_source=news&secret=1",
		IP:       testIP,
		UA:       testUA,
	}
}

func readLines(t *testing.T, path string) []string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
}

func TestRecorderWritesOneLinePerHit(t *testing.T) {
	e := newRecorderEnv(t)
	dir := e.site(t, testView)
	r := e.recorder(100)
	if !r.Add(pageHit()) || !r.Add(pageHit()) {
		t.Fatal("Add refused with an empty buffer")
	}
	r.Flush()

	path := DayFile(dir, "2026-11-04")
	if want := filepath.Join(dir, "analytics", "events", "2026-11-04.jsonl"); path != want {
		t.Fatalf("DayFile = %q, want %q", path, want)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), testIP) || strings.Contains(string(raw), "RecorderTestAgent") || strings.Contains(string(raw), testUA) {
		t.Fatalf("the raw line holds the IP or the UA: %s", raw)
	}
	lines := readLines(t, path)
	if len(lines) != 2 {
		t.Fatalf("lines = %d, want 2: %q", len(lines), lines)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &m); err != nil {
		t.Fatal(err)
	}
	if m["s"] != "srv" || m["k"] != "p" {
		t.Fatalf("s/k = %v/%v", m["s"], m["k"])
	}
	if v, _ := m["v"].(string); len(v) != 16 {
		t.Fatalf("v = %v, want 16 hex characters", m["v"])
	}
	for _, k := range []string{"ip", "ua"} {
		if _, ok := m[k]; ok {
			t.Fatalf("record has key %q", k)
		}
	}
	if m["p"] != "/pricing" || m["a"] != "shop.example" || m["b"] != "Chrome" || m["o"] != "Windows" || m["dv"] != "desktop" {
		t.Fatalf("record = %v", m)
	}
	if q, _ := m["q"].(map[string]any); len(q) != 1 || q["utm_source"] != "news" {
		t.Fatalf("q = %v, want only utm_source", m["q"])
	}
	if m["t"] != float64(time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC).UnixMilli()) {
		t.Fatalf("t = %v", m["t"])
	}

	recs, err := ReadDay(dir, "2026-11-04")
	if err != nil || len(recs) != 2 {
		t.Fatalf("ReadDay = %d records, %v", len(recs), err)
	}
	if recs[0].V != recs[1].V {
		t.Fatal("the same visitor on the same day must keep its id")
	}
}

func TestRecorderUsesSiteTimezoneForTheDayFile(t *testing.T) {
	vienna, err := time.LoadLocation("Europe/Vienna")
	if err != nil {
		t.Fatal(err)
	}
	when := time.Date(2026, 11, 4, 23, 30, 0, 0, time.UTC)
	if got := LocalDate(when, vienna); got != "2026-11-05" {
		t.Fatalf("LocalDate(Vienna) = %s", got)
	}
	if got := LocalDate(when, nil); got != "2026-11-04" {
		t.Fatalf("LocalDate(nil) = %s, want UTC", got)
	}

	e := newRecorderEnv(t)
	dir := e.site(t, testView)
	r := e.recorder(100)
	h := pageHit()
	h.Time, h.TZ = when, vienna
	r.Add(h)
	r.Flush()
	if _, err := os.Stat(DayFile(dir, "2026-11-05")); err != nil {
		t.Fatalf("the Vienna day file is missing: %v", err)
	}
	if _, err := os.Stat(DayFile(dir, "2026-11-04")); !os.IsNotExist(err) {
		t.Fatalf("the UTC day file exists: %v", err)
	}
}

func TestRecorderDailyCapCountsOver(t *testing.T) {
	e := newRecorderEnv(t)
	dir := e.site(t, testView)
	over := filepath.Join(dir, "analytics", "events", "2026-11-04.over")

	r := e.recorder(3)
	for range 5 {
		r.Add(pageHit())
	}
	r.Flush()
	if n := len(readLines(t, DayFile(dir, "2026-11-04"))); n != 3 {
		t.Fatalf("lines = %d, want 3", n)
	}
	if b, err := os.ReadFile(over); err != nil || strings.TrimSpace(string(b)) != "2" {
		t.Fatalf(".over = %q, %v; want 2", b, err)
	}

	// The count adds up across flushes...
	r.Add(pageHit())
	r.Flush()
	if b, _ := os.ReadFile(over); strings.TrimSpace(string(b)) != "3" {
		t.Fatalf(".over = %q, want 3", b)
	}
	// ...and a restarted recorder counts the lines already in the file.
	r2 := e.recorder(3)
	r2.Add(pageHit())
	r2.Flush()
	if n := len(readLines(t, DayFile(dir, "2026-11-04"))); n != 3 {
		t.Fatalf("lines after restart = %d, want 3", n)
	}
	if b, _ := os.ReadFile(over); strings.TrimSpace(string(b)) != "4" {
		t.Fatalf(".over after restart = %q, want 4", b)
	}
	// Another day starts from zero.
	h := pageHit()
	h.Time = h.Time.Add(24 * time.Hour)
	r2.Add(h)
	r2.Flush()
	if n := len(readLines(t, DayFile(dir, "2026-11-05"))); n != 1 {
		t.Fatalf("next day lines = %d, want 1", n)
	}
}

func TestRecorderCapSeesLinesWrittenByAnotherWriter(t *testing.T) {
	e := newRecorderEnv(t)
	dir := e.site(t, testView)
	r := e.recorder(3)
	r.Add(pageHit())
	r.Flush()
	// An import rewrites the day file under the stats lock; the recorder's
	// running count must not miss it.
	path := DayFile(dir, "2026-11-04")
	line := readLines(t, path)[0]
	if err := os.WriteFile(path, []byte(line+"\n"+line+"\n"+line+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r.Add(pageHit())
	r.Flush()
	if n := len(readLines(t, path)); n != 3 {
		t.Fatalf("lines = %d, want 3", n)
	}
}

func TestRecorderNeverGluesOntoAHalfWrittenLine(t *testing.T) {
	e := newRecorderEnv(t)
	dir := e.site(t, testView)
	path := DayFile(dir, "2026-11-04")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"t":1,"k":"p","p":"/ok"}`+"\n"+`{"t":2,"k":"p","p":"/cut`), 0o644); err != nil {
		t.Fatal(err)
	}
	r := e.recorder(100)
	r.Add(pageHit())
	r.Flush()
	recs, err := ReadDay(dir, "2026-11-04")
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 2 || recs[0].P != "/ok" || recs[1].P != "/pricing" {
		t.Fatalf("records = %+v", recs)
	}
}

func TestRecorderNeverRecreatesDeletedSite(t *testing.T) {
	e := newRecorderEnv(t)
	dir := e.site(t, testView)
	r := e.recorder(100)
	r.Add(pageHit())
	// The site is deleted while its hit waits in the writer.
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	r.Flush()
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("the deleted site folder exists again: %v", err)
	}
	// A site that never existed gets no folder either.
	h := pageHit()
	h.ViewID = "bbbbbbbbbbbbbbbbbbbbbbbbbb"
	r.Add(h)
	r.Flush()
	if _, err := os.Stat(filepath.Join(e.sites, h.ViewID)); !os.IsNotExist(err) {
		t.Fatalf("a folder was created for an unknown site: %v", err)
	}
}

func TestRecorderDropsWhenFull(t *testing.T) {
	e := newRecorderEnv(t)
	e.site(t, testView)
	r := e.recorder(100)
	start := time.Now()
	accepted := 0
	for range 10000 {
		if r.Add(pageHit()) {
			accepted++
		}
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("10000 adds took %v", d)
	}
	if r.Dropped() <= 0 {
		t.Fatalf("Dropped = %d", r.Dropped())
	}
	if accepted != 8192 || r.Dropped() != int64(10000-accepted) {
		t.Fatalf("accepted %d, dropped %d", accepted, r.Dropped())
	}
}

func TestRecorderPageCheckFalseDrops(t *testing.T) {
	e := newRecorderEnv(t)
	dir := e.site(t, testView)
	r := e.recorder(100)
	var calls int
	var mu sync.Mutex
	check := func(ok bool) func() bool {
		return func() bool {
			mu.Lock()
			calls++
			mu.Unlock()
			return ok
		}
	}
	missing := pageHit()
	missing.RawPath = "/missing"
	missing.PageCheck = check(false)
	found := pageHit()
	found.PageCheck = check(true)
	r.Add(missing)
	r.Add(found)
	mu.Lock()
	if calls != 0 {
		t.Fatalf("Add ran the page check %d times; only the writer may", calls)
	}
	mu.Unlock()
	r.Flush()
	recs, err := ReadDay(dir, "2026-11-04")
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 || recs[0].P != "/pricing" {
		t.Fatalf("records = %+v", recs)
	}
}

func TestRecorderVrAlwaysEmpty(t *testing.T) {
	e := newRecorderEnv(t)
	dir := e.site(t, testView)
	r := e.recorder(100)
	r.Add(pageHit())
	ev := pageHit()
	ev.Kind, ev.Source, ev.Name = "e", "js", "signup"
	r.Add(ev)
	r.Flush()
	for _, line := range readLines(t, DayFile(dir, "2026-11-04")) {
		if strings.Contains(line, `"vr"`) {
			t.Fatalf("line carries vr: %s", line)
		}
	}
	recs, _ := ReadDay(dir, "2026-11-04")
	if len(recs) != 2 {
		t.Fatalf("records = %d", len(recs))
	}
	for _, rec := range recs {
		if rec.Vr != "" {
			t.Fatalf("vr = %q", rec.Vr)
		}
	}
}

func TestRecorderScriptEventFields(t *testing.T) {
	e := newRecorderEnv(t)
	dir := e.site(t, testView)
	g, err := OpenGeo("testdata/geo-test.mmdb")
	if err != nil {
		t.Fatal(err)
	}
	r := NewRecorder(RecorderConfig{
		DailyCap: 100, Salts: NewSalts(e.data), Geo: g, WithSite: e.withSite,
		Log: slog.New(slog.DiscardHandler),
	})
	h := Hit{
		Time: time.Date(2026, 11, 4, 9, 0, 0, 0, time.UTC), ViewID: testView,
		Kind: "e", Source: "js", Address: "shop.example", RawPath: "/buy#x",
		RawQuery: "gclid=SECRETCLICK&utm_medium=cpc", ExtraQuery: []string{"ref"},
		Referrer: "https://news.example/story?id=SECRETREF",
		IP:       "81.2.69.142",
		UA:       "Mozilla/5.0 (iPhone; CPU iPhone OS 18_1 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.1 Mobile/15E148 Safari/604.1",
		Language: "de-AT", Title: "Buy", Screen: "1024x1366", Tag: "spring",
		Name: "purchase", Data: map[string]any{"plan": "pro", "nested": map[string]any{"x": 1}},
		Own: []string{"shop.example"},
	}
	own := h
	own.Kind, own.Referrer = "p", "https://shop.example/home"
	bad := h
	bad.Name = ""
	r.Add(h)
	r.Add(own)
	r.Add(bad) // an event without a name is dropped
	r.Flush()

	raw, _ := os.ReadFile(DayFile(dir, "2026-11-04"))
	for _, secret := range []string{"SECRETCLICK", "SECRETREF", "81.2.69.142"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("stored %q: %s", secret, raw)
		}
	}
	recs, err := ReadDay(dir, "2026-11-04")
	if err != nil || len(recs) != 2 {
		t.Fatalf("ReadDay = %+v, %v", recs, err)
	}
	ev := recs[0]
	if ev.K != "e" || ev.S != "js" || ev.N != "purchase" || ev.D["plan"] != "pro" || ev.D["nested"] != nil {
		t.Fatalf("event = %+v", ev)
	}
	if ev.P != "/buy" || ev.R != "news.example/story" || ev.Q["utm_medium"] != "cpc" || len(ev.Ck) != 1 || ev.Ck[0] != "gclid" {
		t.Fatalf("event url fields = %+v", ev)
	}
	if ev.Ti != "Buy" || ev.Tg != "spring" || ev.L != "de-AT" || ev.Sc != "1024x1366" {
		t.Fatalf("event script fields = %+v", ev)
	}
	if ev.B != "Safari" || ev.O != "iOS" || ev.Dv != "tablet" {
		t.Fatalf("device = %q %q %q; a 1024 px wide iOS screen is a tablet", ev.B, ev.O, ev.Dv)
	}
	if ev.C != "GB" || ev.Rg != "GB-ENG" || ev.Ct != "London" {
		t.Fatalf("geo = %q %q %q", ev.C, ev.Rg, ev.Ct)
	}
	if pv := recs[1]; pv.K != "p" || pv.R != "" || pv.N != "" || pv.D != nil {
		t.Fatalf("page view = %+v; an own referrer and event fields must not be kept", pv)
	}
}

func TestReadDayMissingAndMalformed(t *testing.T) {
	dir := t.TempDir()
	recs, err := ReadDay(dir, "2026-11-04")
	if recs != nil || err != nil {
		t.Fatalf("missing day = %v, %v", recs, err)
	}
	path := DayFile(dir, "2026-11-04")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"t":1,"k":"p","p":"/a"}` + "\n" + "not json\n\n" + `{"t":2,"k":"e","n":"x"}` + "\n" + `{"t":3,`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	recs, err = ReadDay(dir, "2026-11-04")
	if err != nil || len(recs) != 2 || recs[0].P != "/a" || recs[1].N != "x" {
		t.Fatalf("ReadDay = %+v, %v", recs, err)
	}
	if _, err := ReadDay(dir, "../../etc"); err == nil {
		t.Fatal("a malformed date must be refused")
	}
}

func TestRecorderRunDrainsOnShutdown(t *testing.T) {
	e := newRecorderEnv(t)
	dir := e.site(t, testView)
	r := e.recorder(1000)
	ctx, cancel := context.WithCancel(context.Background())
	for range 300 {
		r.Add(pageHit())
	}
	cancel()
	done := make(chan struct{})
	go func() { r.Run(ctx); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after its context ended")
	}
	if n := len(readLines(t, DayFile(dir, "2026-11-04"))); n != 300 {
		t.Fatalf("lines after shutdown = %d, want 300", n)
	}
}

// TestRecorderConcurrentAdd is meant for go test -race: many request
// goroutines add while the writer runs, and Flush waits for the writer.
func TestRecorderConcurrentAdd(t *testing.T) {
	e := newRecorderEnv(t)
	dir := e.site(t, testView)
	r := e.recorder(100000)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { r.Run(ctx); close(done) }()

	const workers, each = 8, 250
	var wg sync.WaitGroup
	for w := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range each {
				h := pageHit()
				h.UA = testUA + string(rune('a'+w))
				h.Time = h.Time.Add(time.Duration(i) * time.Millisecond)
				r.Add(h)
			}
		}()
	}
	wg.Wait()
	r.Flush()
	if n := len(readLines(t, DayFile(dir, "2026-11-04"))); n != workers*each {
		t.Fatalf("lines = %d, want %d (dropped %d)", n, workers*each, r.Dropped())
	}
	cancel()
	<-done
	// Flush after Run has ended drains directly.
	r.Add(pageHit())
	r.Flush()
	if n := len(readLines(t, DayFile(dir, "2026-11-04"))); n != workers*each+1 {
		t.Fatalf("lines after Run ended = %d", n)
	}
}
