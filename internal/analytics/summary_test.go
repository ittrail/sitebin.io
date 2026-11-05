package analytics

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math/rand/v2"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"time"
)

// summaryFixture writes three Vienna days around the end of summer time
// (2026-10-25 has 25 hours). Sessions are drawn from small pools, so values
// repeat, visitors come back after a gap, some visits start with an event
// or carry their attribution on a later record, and some are imported
// visits; a visitor crosses midnight, and one is seen in both 02:00 hours.
func summaryFixture(t *testing.T, vienna *time.Location) string {
	t.Helper()
	rng := rand.New(rand.NewPCG(10, 25))
	pick := func(xs ...string) string { return xs[rng.IntN(len(xs))] }
	from := time.Date(2026, 10, 24, 0, 0, 0, 0, vienna)
	span := time.Date(2026, 10, 27, 0, 0, 0, 0, vienna).Sub(from)
	var recs []Record
	for i := range 150 {
		at := from.Add(time.Duration(rng.Int64N(int64(span))))
		v := fmt.Sprint("v", rng.IntN(30))
		imported := rng.IntN(8) == 0
		attrAt := rng.IntN(3) // the record carrying the attribution
		for j := range 1 + rng.IntN(6) {
			r := Record{
				T: at.UnixMilli(), K: "p", S: "srv", V: v,
				P:  pick("/", "/a", "/b", "/blog/post", "/pricing"),
				Ti: pick("", "Home", "A", "Pricing"),
				Tg: pick("", "", "promo"),
				A:  pick("example.com", "example.com/shop"),
				B:  pick("Chrome", "Firefox", "Safari"), O: pick("Windows", "macOS", "iOS"),
				Dv: pick("desktop", "mobile"), Sc: pick("1920x1080", "390x844"),
				L: pick("de-AT", "en-US"), C: pick("AT", "DE", ""), Rg: pick("", "AT-9"), Ct: pick("", "Vienna"),
			}
			if rng.IntN(4) == 0 {
				r.K, r.N = "e", pick("signup", "click", "x")
				r.D = map[string]any{"plan": pick("pro", "free"), "seats": float64(rng.IntN(3))}
				if rng.IntN(2) == 0 {
					r.D["trial"] = true
				}
			}
			if j == attrAt {
				switch rng.IntN(5) {
				case 0:
					r.R = pick("www.google.com/", "news.ycombinator.com/item")
				case 1:
					r.Q = map[string]string{"utm_source": pick("meta", "newsletter"), "utm_medium": pick("email", "cpc"), "utm_term": pick("a", "b"), "utm_content": pick("c1", "c2")}
				case 2:
					r.Ck, r.Q = []string{pick("gclid", "fbclid")}, map[string]string{"ref": pick("hn", "x")}
				case 3:
					r.R, r.Q = "l.facebook.com/", map[string]string{"utm_campaign": "fall", "ref": "fb"}
				}
			}
			if imported {
				r.S, r.Vi = "um", fmt.Sprint("visit", i)
			}
			recs = append(recs, r)
			at = at.Add(time.Duration(rng.IntN(40)) * time.Minute) // some gaps end the visit
		}
	}
	recs = append(recs,
		pv("2026-10-24T23:58:00+02:00", "night", "/late", ref("www.google.com/")),
		pv("2026-10-25T00:03:00+02:00", "night", "/later"), // the next day's file: another visit
		pv("2026-10-25T02:30:00+02:00", "dst", "/first-two"),
		pv("2026-10-25T02:30:00+01:00", "dst", "/second-two"), // the repeated hour, 60 minutes later
	)
	dir := t.TempDir()
	writeRecords(t, dir, vienna, recs)
	return dir
}

// sameAnswers runs every query of the engine on qu with both engines and
// requires the same answers: the overview in each unit with the previous
// period, every table, and the event data of each event. It reports the
// dimensions that had rows.
func sameAnswers(t *testing.T, raw, sum *Engine, dir string, qu Query) (withRows int) {
	t.Helper()
	for _, unit := range []string{"hour", "day", "month"} {
		qu.Unit = unit
		want, err := raw.Overview(dir, qu, true)
		if err != nil {
			t.Fatal(err)
		}
		got, err := sum.Overview(dir, qu, true)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("overview by %s:\n got %+v\nwant %+v", unit, got, want)
		}
	}
	for _, dim := range append(slices.Clone(Dims), "query:ref") {
		want, wantTotal, err := raw.Table(dir, qu, dim, 0, 0)
		if err != nil {
			t.Fatal(err)
		}
		got, total, err := sum.Table(dir, qu, dim, 0, 0)
		if err != nil {
			t.Fatal(err)
		}
		if total != wantTotal {
			t.Fatalf("%s: total %d, want %d", dim, total, wantTotal)
		}
		sameRows(t, dim, got, want)
		if len(want) > 0 {
			withRows++
		}
	}
	for _, name := range []string{"signup", "click", "x"} {
		want, err := raw.EventData(dir, qu, name)
		if err != nil {
			t.Fatal(err)
		}
		got, err := sum.EventData(dir, qu, name)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("event data of %s:\n got %+v\nwant %+v", name, got, want)
		}
	}
	return withRows
}

func parsedDays(e *Engine) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.items)
}

func TestSummaryEqualsRawScan(t *testing.T) {
	vienna, err := time.LoadLocation("Europe/Vienna")
	if err != nil {
		t.Fatal(err)
	}
	dir := summaryFixture(t, vienna)
	three := days(vienna, "2026-10-24", "2026-10-26")
	long := days(vienna, "2026-10-25", "2026-10-25")
	edges := Query{From: time.Date(2026, 10, 24, 12, 0, 0, 0, vienna), To: time.Date(2026, 10, 26, 12, 0, 0, 0, vienna), Loc: vienna}
	inUTC := days(time.UTC, "2026-10-24", "2026-10-26") // every Vienna file spans two UTC days
	raw := rawEngine(64 << 20)

	if n := sameAnswers(t, raw, newTestEngine(), dir, three); n != len(Dims)+1 {
		t.Fatalf("the fixture fills %d of %d tables", n, len(Dims)+1)
	}
	for _, qu := range []Query{long, edges, inUTC} {
		sameAnswers(t, raw, newTestEngine(), dir, qu)
	}

	var s Summary
	if got := readSummary(dir, "2026-10-25"); got == nil {
		t.Fatal("no summary of 2026-10-25 was stored")
	} else {
		s = *got
	}
	if len(s.Hours) != 25 {
		t.Fatalf("2026-10-25 has %d hours, want 25", len(s.Hours))
	}
	for i, h := range s.Hours {
		if want := time.Date(2026, 10, 25, 0, 0, 0, 0, vienna).Add(time.Duration(i) * time.Hour).UnixMilli(); h.T != want {
			t.Fatalf("hour %d starts at %v, want %v", i, time.UnixMilli(h.T), time.UnixMilli(want))
		}
	}
	if s.Hours[2].Views == 0 || s.Hours[3].Views == 0 {
		t.Fatalf("the two 02:00 hours = %+v, %+v; want the dst visitor's page view in each", s.Hours[2], s.Hours[3])
	}

	t.Run("whole days are answered without parsing a day file", func(t *testing.T) {
		for _, qu := range []Query{three, long} { // long: its neighbours are skipped through their summaries
			for _, unit := range []string{"hour", "day"} {
				qu.Unit = unit
				e := newTestEngine()
				if _, err := e.Overview(dir, qu, true); err != nil {
					t.Fatal(err)
				}
				for _, dim := range Dims {
					if _, _, err := e.Table(dir, qu, dim, 0, 0); err != nil {
						t.Fatal(err)
					}
				}
				if _, err := e.EventData(dir, qu, "signup"); err != nil {
					t.Fatal(err)
				}
				if n := parsedDays(e); n != 0 {
					t.Fatalf("%v .. %v by %s parsed %d day files", qu.From, qu.To, unit, n)
				}
			}
		}
	})

	t.Run("summaries built in another zone", func(t *testing.T) {
		// Built by a UTC query first, the summaries' hours are UTC days: the
		// Vienna hour series cannot use them and scans those days raw.
		for _, date := range []string{"2026-10-24", "2026-10-25", "2026-10-26"} {
			if err := os.Remove(summaryFile(dir, date)); err != nil {
				t.Fatal(err)
			}
		}
		for _, qu := range []Query{inUTC, three, long, edges} {
			sameAnswers(t, raw, newTestEngine(), dir, qu)
		}
		if s := readSummary(dir, "2026-10-25"); s == nil || len(s.Hours) != 48 {
			t.Fatalf("the UTC-built summary of 2026-10-25 = %+v; want 48 UTC hours", s)
		}
	})
}

func TestSummaryRebuiltWhenDayChanges(t *testing.T) {
	dir := t.TempDir()
	const date = "2026-11-02"
	writeDay(t, dir, date, []Record{pv("2026-11-02T10:00:00Z", "a", "/a")})
	path := DayFile(dir, date)
	qu := days(time.UTC, date, date)
	e := newTestEngine()
	answer := func() (views int, top string) {
		t.Helper()
		ov, err := e.Overview(dir, qu, false)
		if err != nil {
			t.Fatal(err)
		}
		rows, _, err := e.Table(dir, qu, "path", 0, 0)
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) > 0 {
			top = rows[0].Value
		}
		return ov.Totals.Views, top
	}
	stored := func() *Summary {
		t.Helper()
		s := readSummary(dir, date)
		if s == nil {
			t.Fatal("no summary stored")
		}
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if s.V != summaryVersion || s.Size != fi.Size() || s.Mtime != fi.ModTime().UnixNano() {
			t.Fatalf("summary v%d of %d bytes at %d; the file has %d bytes at %d", s.V, s.Size, s.Mtime, fi.Size(), fi.ModTime().UnixNano())
		}
		return s
	}
	if v, top := answer(); v != 1 || top != "/a" {
		t.Fatalf("first answer: %d views, top %q", v, top)
	}

	// The stored summary is what answers a closed day: one edited by hand,
	// with the file's size and mtime, is believed.
	s := stored()
	s.Totals.Views = 99
	if err := writeSummary(dir, date, s); err != nil {
		t.Fatal(err)
	}
	if v, _ := answer(); v != 99 {
		t.Fatalf("views = %d; the stored summary was not read", v)
	}

	t.Run("an appended record changes the size", func(t *testing.T) {
		appendRecord(t, path, pv("2026-11-02T11:00:00Z", "b", "/a"))
		if v, _ := answer(); v != 2 {
			t.Fatalf("views = %d after an append, want 2", v)
		}
		stored()
	})

	t.Run("a rewrite of the same size changes the mtime", func(t *testing.T) {
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		writeDay(t, dir, date, []Record{pv("2026-11-02T10:00:00Z", "a", "/z"), pv("2026-11-02T11:00:00Z", "b", "/z")})
		later := fi.ModTime().Add(time.Second)
		if err := os.Chtimes(path, later, later); err != nil {
			t.Fatal(err)
		}
		if v, top := answer(); v != 2 || top != "/z" {
			t.Fatalf("after a rewrite: %d views, top %q", v, top)
		}
		stored()
	})

	t.Run("another format version", func(t *testing.T) {
		s := stored()
		s.V, s.Totals.Views = summaryVersion+1, 99
		if err := writeSummary(dir, date, s); err != nil {
			t.Fatal(err)
		}
		if v, _ := answer(); v != 2 {
			t.Fatalf("views = %d from a summary of another version", v)
		}
		stored()
	})

	t.Run("a summary that does not parse", func(t *testing.T) {
		if err := os.WriteFile(summaryFile(dir, date), []byte(`{"v":1,"size":`), 0o644); err != nil {
			t.Fatal(err)
		}
		if v, _ := answer(); v != 2 {
			t.Fatalf("views = %d from a cut summary", v)
		}
		stored()
	})

	t.Run("Invalidate deletes it", func(t *testing.T) {
		// A rewrite in the same size and mtime tick is the key's blind spot,
		// which the writer closes with Invalidate.
		e.Invalidate(dir, date)
		if _, err := os.Stat(summaryFile(dir, date)); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("summary after Invalidate: %v", err)
		}
	})

	t.Run("a deleted day is not answered from its summary", func(t *testing.T) {
		if v, _ := answer(); v != 2 {
			t.Fatalf("views = %d after Invalidate, want the rebuilt 2", v)
		}
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if v, top := answer(); v != 0 || top != "" {
			t.Fatalf("after a delete: %d views, top %q", v, top)
		}
	})

	t.Run("today is never summarised", func(t *testing.T) {
		writeDay(t, dir, date, []Record{pv("2026-11-02T10:00:00Z", "a", "/a")})
		os.Remove(summaryFile(dir, date))
		today := newTestEngine()
		today.now = func() time.Time { return tm("2026-11-02T23:59:00Z") }
		if ov, err := today.Overview(dir, qu, false); err != nil || ov.Totals.Views != 1 {
			t.Fatalf("today: %+v, %v", ov.Totals, err)
		}
		if _, err := os.Stat(summaryFile(dir, date)); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("today's summary: %v", err)
		}
	})
}

// appendRecord appends r to the day file at path, as the recorder does.
func appendRecord(t *testing.T, path string, r Record) {
	t.Helper()
	line, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, werr := f.Write(append(line, '\n'))
	if err := errors.Join(werr, f.Close()); err != nil {
		t.Fatal(err)
	}
}

func TestSummaryOtherBucket(t *testing.T) {
	at := tm("2026-11-02T10:00:00Z")
	var recs []Record
	for i := range 1005 {
		ts := at.Add(time.Duration(i) * time.Second).UnixMilli()
		v := fmt.Sprint("v", i)
		recs = append(recs,
			Record{T: ts, K: "p", S: "srv", V: v, P: fmt.Sprintf("/p%04d", i)},
			Record{T: ts, K: "e", S: "srv", V: v, P: "/", N: fmt.Sprintf("n%04d", i)},
			Record{T: ts, K: "e", S: "srv", V: v, P: "/", N: "e", D: map[string]any{"v": fmt.Sprint(i), fmt.Sprintf("k%04d", i): true}},
		)
	}
	// /p0000 has a second visitor, so it leads; the rest tie and go by value.
	recs = append(recs, pv("2026-11-02T11:00:00Z", "w", "/p0000"))

	s := BuildSummary(recs, time.UTC)
	paths := s.Dims["path"]
	if len(paths) != 1001 {
		t.Fatalf("%d path rows, want 1000 and %s", len(paths), Other)
	}
	if paths[0] != (Row{Value: "/p0000", Visitors: 2, Views: 2}) || paths[999].Value != "/p0999" {
		t.Fatalf("kept rows %+v .. %+v", paths[0], paths[999])
	}
	if paths[1000] != (Row{Value: Other, Visitors: 5, Views: 5}) {
		t.Fatalf("other = %+v, want /p1000../p1004 summed", paths[1000])
	}
	// Events go by count: e, then the names by value.
	if len(s.Events) != 1001 || s.Events[0].Value != "e" || s.Events[1000] != (Row{Value: Other, Visitors: 6, Count: 6}) {
		t.Fatalf("events: %d rows, first %+v, last %+v", len(s.Events), s.Events[0], s.Events[len(s.Events)-1])
	}
	if _, ok := s.EventData["n1004"]; ok {
		t.Fatal("event data kept for an event folded into other")
	}
	data := s.EventData["e"]
	byKey := map[string][]ValueCount{}
	for _, kv := range data {
		byKey[kv.Key] = kv.Values
	}
	// Keys go by the events carrying them: v, then k0000..k0998.
	if len(data) != 1001 || byKey["k0998"] == nil || byKey["k0999"] != nil {
		t.Fatalf("%d data keys; k0998 %v, k0999 %v", len(data), byKey["k0998"], byKey["k0999"])
	}
	if o := byKey[Other]; !reflect.DeepEqual(o, []ValueCount{{Value: Other, Count: 6}}) {
		t.Fatalf("other key = %+v", o)
	}
	if v := byKey["v"]; len(v) != 1001 || v[1000] != (ValueCount{Value: Other, Count: 5}) {
		t.Fatalf("values of v: %d, last %+v", len(v), v[len(v)-1])
	}

	// Through the engine, (other) comes last whatever its count, and two
	// days add up.
	dir := t.TempDir()
	writeRecords(t, dir, time.UTC, recs)
	next := slices.Clone(recs)
	for i := range next {
		next[i].T += (24 * time.Hour).Milliseconds()
	}
	writeRecords(t, dir, time.UTC, next)
	rows, total, err := newTestEngine().Table(dir, days(time.UTC, "2026-11-02", "2026-11-03"), "path", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if total != 1001 || rows[0] != (Row{Value: "/p0000", Visitors: 4, Views: 4}) || rows[1000] != (Row{Value: Other, Visitors: 10, Views: 10}) {
		t.Fatalf("table: %d rows, first %+v, last %+v", total, rows[0], rows[len(rows)-1])
	}
	kvs, err := newTestEngine().EventData(dir, days(time.UTC, "2026-11-02", "2026-11-03"), "e")
	if err != nil {
		t.Fatal(err)
	}
	if last := kvs[len(kvs)-1]; last.Key != Other || last.Values[0].Count != 12 {
		t.Fatalf("event data ends with %+v", last)
	}
}

func TestSummaryNeverCreatesTheEventsDir(t *testing.T) {
	gone := filepath.Join(t.TempDir(), "deleted-site")
	if err := writeSummary(gone, "2026-11-02", &Summary{V: summaryVersion}); err == nil {
		t.Fatal("a summary was written for a site without an events directory")
	}
	if _, err := os.Stat(gone); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the site folder was recreated: %v", err)
	}
}
