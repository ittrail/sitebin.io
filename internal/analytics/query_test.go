package analytics

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"
)

// writeDay is ReadDay's inverse: it writes recs as the day file of date.
func writeDay(t *testing.T, siteDir, date string, recs []Record) {
	t.Helper()
	var b bytes.Buffer
	for _, r := range recs {
		line, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		b.Write(line)
		b.WriteByte('\n')
	}
	path := DayFile(siteDir, date)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

// writeRecords files each record under its local date in loc, as the
// recorder does.
func writeRecords(t *testing.T, siteDir string, loc *time.Location, recs []Record) {
	t.Helper()
	byDate := map[string][]Record{}
	var order []string
	for _, r := range recs {
		d := LocalDate(time.UnixMilli(r.T), loc)
		if _, ok := byDate[d]; !ok {
			order = append(order, d)
		}
		byDate[d] = append(byDate[d], r)
	}
	for _, d := range order {
		writeDay(t, siteDir, d, byDate[d])
	}
}

func tm(s string) time.Time {
	x, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return x
}

type opt func(*Record)

func q(k, v string) opt { return func(r *Record) { r.Q = map[string]string{k: v} } }
func ref(s string) opt  { return func(r *Record) { r.R = s } }
func cc(c string) opt   { return func(r *Record) { r.C = c } }
func ck(ids ...string) opt {
	return func(r *Record) { r.Ck = ids }
}
func data(d map[string]any) opt { return func(r *Record) { r.D = d } }
func visit(id string) opt       { return func(r *Record) { r.S = "um"; r.Vi = id } }
func also(f func(*Record)) opt  { return f }

func rec(kind, ts, v, path string, opts []opt) Record {
	r := Record{T: tm(ts).UnixMilli(), K: kind, S: "srv", V: v, P: path}
	for _, o := range opts {
		o(&r)
	}
	return r
}

func pv(ts, v, path string, opts ...opt) Record { return rec("p", ts, v, path, opts) }

func ev(ts, v, path, name string, opts ...opt) Record {
	r := rec("e", ts, v, path, opts)
	r.N = name
	return r
}

// days is the query over the local dates first..last, inclusive, in loc.
func days(loc *time.Location, first, last string, filters ...Filter) Query {
	f, err := time.ParseInLocation("2006-01-02", first, loc)
	if err != nil {
		panic(err)
	}
	l, err := time.ParseInLocation("2006-01-02", last, loc)
	if err != nil {
		panic(err)
	}
	return Query{From: f, To: time.Date(l.Year(), l.Month(), l.Day()+1, 0, 0, 0, 0, loc), Loc: loc, Filters: filters}
}

func newTestEngine() *Engine { return NewEngine(64 << 20) }

func overview(t *testing.T, dir string, qu Query, compare bool) Overview {
	t.Helper()
	ov, err := newTestEngine().Overview(dir, qu, compare)
	if err != nil {
		t.Fatal(err)
	}
	return ov
}

func table(t *testing.T, dir string, qu Query, dim string) []Row {
	t.Helper()
	rows, total, err := newTestEngine().Table(dir, qu, dim, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if total != len(rows) {
		t.Fatalf("%s: total %d with %d rows and no limit", dim, total, len(rows))
	}
	return rows
}

func sameRows(t *testing.T, what string, got, want []Row) {
	t.Helper()
	if len(want) == 0 && len(got) == 0 {
		return
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s rows:\n got %+v\nwant %+v", what, got, want)
	}
}

func TestOverviewMetrics(t *testing.T) {
	dir := t.TempDir()
	writeRecords(t, dir, time.UTC, []Record{
		pv("2026-11-02T10:00:00Z", "A", "/a"),
		pv("2026-11-02T10:05:00Z", "A", "/b"),
		pv("2026-11-02T10:10:00Z", "A", "/c"),
		pv("2026-11-02T10:41:00Z", "A", "/a"), // 31 minutes later: a second visit, a bounce
		pv("2026-11-02T11:00:00Z", "B", "/a"),
		pv("2026-11-02T11:02:00Z", "B", "/b"),
		ev("2026-11-02T11:03:00Z", "B", "/b", "signup"),
		pv("2026-11-02T12:00:00Z", "C", "/a"), // a bounce
	})
	if err := os.WriteFile(overFile(dir, "2026-11-02"), []byte("12"), 0o644); err != nil {
		t.Fatal(err)
	}
	ov := overview(t, dir, days(time.UTC, "2026-11-02", "2026-11-02"), false)
	// Durations run from a visit's first to its last page view: A's first
	// visit 600 s, its second 0, B's 120 s (the event does not extend it),
	// C's 0: 720 s over 4 visits.
	want := Totals{Views: 7, Visitors: 3, Visits: 4, BounceRate: 0.5, DurationS: 180}
	if ov.Totals != want {
		t.Fatalf("totals = %+v, want %+v", ov.Totals, want)
	}
	if ov.Previous != nil {
		t.Fatalf("previous = %+v without compare", ov.Previous)
	}
	if ov.OverCap != 12 {
		t.Fatalf("over_cap = %d, want 12", ov.OverCap)
	}
	if ov.Unit != "hour" || len(ov.Series) != 24 {
		t.Fatalf("unit %q with %d points, want hour with 24", ov.Unit, len(ov.Series))
	}
	wantViews := map[int][2]int{10: {4, 1}, 11: {2, 1}, 12: {1, 1}}
	for h, p := range ov.Series {
		if !p.T.Equal(tm("2026-11-02T00:00:00Z").Add(time.Duration(h) * time.Hour)) {
			t.Fatalf("point %d at %v", h, p.T)
		}
		if w := wantViews[h]; p.Views != w[0] || p.Visitors != w[1] {
			t.Fatalf("hour %d = %d views, %d visitors; want %v", h, p.Views, p.Visitors, w)
		}
	}
}

func TestVisitGapIsThirtyMinutes(t *testing.T) {
	dir := t.TempDir()
	writeRecords(t, dir, time.UTC, []Record{
		pv("2026-11-02T13:00:00Z", "D", "/a"),
		pv("2026-11-02T13:30:00Z", "D", "/b"), // exactly 30 minutes: the same visit
		pv("2026-11-02T14:00:01Z", "D", "/c"), // 30 min 1 s: a new one
	})
	ov := overview(t, dir, days(time.UTC, "2026-11-02", "2026-11-02"), false)
	if ov.Totals.Visits != 2 || ov.Totals.DurationS != 900 {
		t.Fatalf("totals = %+v, want 2 visits averaging 900 s", ov.Totals)
	}
}

func TestOverviewCompare(t *testing.T) {
	t.Run("utc", func(t *testing.T) {
		dir := t.TempDir()
		writeRecords(t, dir, time.UTC, []Record{
			pv("2026-10-31T23:59:00Z", "E", "/z"), // before the previous period
			pv("2026-11-01T09:00:00Z", "D", "/x"),
			pv("2026-11-01T09:01:00Z", "D", "/y"),
			pv("2026-11-02T10:00:00Z", "A", "/a"),
		})
		ov := overview(t, dir, days(time.UTC, "2026-11-02", "2026-11-02"), true)
		if ov.Totals != (Totals{Views: 1, Visitors: 1, Visits: 1, BounceRate: 1}) {
			t.Fatalf("totals = %+v", ov.Totals)
		}
		want := Totals{Views: 2, Visitors: 1, Visits: 1, DurationS: 60}
		if ov.Previous == nil || *ov.Previous != want {
			t.Fatalf("previous = %+v, want %+v", ov.Previous, want)
		}
		if len(ov.Series) != 24 {
			t.Fatalf("the series is the range's own: %d points", len(ov.Series))
		}
	})
	t.Run("the previous period of a 25-hour day is the local day before", func(t *testing.T) {
		vienna, err := time.LoadLocation("Europe/Vienna")
		if err != nil {
			t.Fatal(err)
		}
		dir := t.TempDir()
		writeRecords(t, dir, vienna, []Record{
			pv("2026-10-23T23:30:00+02:00", "X", "/before"), // 25 h back would reach it
			pv("2026-10-24T00:30:00+02:00", "Y", "/in"),
			pv("2026-10-25T12:00:00+01:00", "Z", "/now"),
		})
		ov := overview(t, dir, days(vienna, "2026-10-25", "2026-10-25"), true)
		if ov.Totals.Views != 1 || ov.Previous == nil || ov.Previous.Views != 1 {
			t.Fatalf("totals %+v, previous %+v: want 1 and 1 view", ov.Totals, ov.Previous)
		}
	})
}

func TestVisitorsAreDailySums(t *testing.T) {
	dir := t.TempDir()
	writeRecords(t, dir, time.UTC, []Record{
		pv("2026-11-01T10:00:00Z", "same", "/a"),
		pv("2026-11-01T10:01:00Z", "same", "/b"),
		pv("2026-11-02T10:00:00Z", "same", "/a"),
	})
	qu := days(time.UTC, "2026-11-01", "2026-11-02")
	ov := overview(t, dir, qu, false)
	if ov.Totals.Visitors != 2 || ov.Totals.Visits != 2 || ov.Totals.Views != 3 {
		t.Fatalf("totals = %+v, want 2 visitors (one per day)", ov.Totals)
	}
	qu.Unit = "day"
	ov = overview(t, dir, qu, false)
	if len(ov.Series) != 2 || ov.Series[0].Visitors != 1 || ov.Series[1].Visitors != 1 || ov.Series[0].Views != 2 {
		t.Fatalf("day series = %+v", ov.Series)
	}
	qu.Unit = "month"
	ov = overview(t, dir, qu, false)
	if ov.Unit != "month" || len(ov.Series) != 1 || ov.Series[0].Visitors != 2 || !ov.Series[0].T.Equal(tm("2026-11-01T00:00:00Z")) {
		t.Fatalf("month series = %+v, want one point with 2 visitors", ov.Series)
	}
	if rows := table(t, dir, qu, "path"); rows[0] != (Row{Value: "/a", Visitors: 2, Views: 2}) {
		t.Fatalf("path rows = %+v; /a has one visitor on each day", rows)
	}
}

// metaVisits writes two Meta visits, a Google one and a direct one.
func metaVisits(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	writeRecords(t, dir, time.UTC, []Record{
		// A Meta visit: two page views and an event without the parameter.
		pv("2026-11-02T10:00:00Z", "m", "/landing", q("utm_source", "meta"), ref("l.facebook.com/")),
		pv("2026-11-02T10:01:00Z", "m", "/pricing"),
		ev("2026-11-02T10:02:00Z", "m", "/pricing", "signup"),
		// A Google visit.
		pv("2026-11-02T10:00:00Z", "g", "/landing", q("utm_source", "google")),
		ev("2026-11-02T10:03:00Z", "g", "/landing", "signup"),
		ev("2026-11-02T10:04:00Z", "g", "/landing", "click"),
		pv("2026-11-02T10:05:00Z", "g", "/pricing"),
		// A direct visit.
		pv("2026-11-02T11:00:00Z", "n", "/landing"),
		ev("2026-11-02T11:01:00Z", "n", "/landing", "click"),
		// A Meta visit whose first record is an event: the attribution
		// comes from the first record carrying it.
		ev("2026-11-02T12:00:00Z", "e", "/landing", "x"),
		pv("2026-11-02T12:01:00Z", "e", "/landing", q("utm_source", "meta")),
	})
	return dir
}

func TestFilterVisitLevel(t *testing.T) {
	dir := metaVisits(t)
	qu := days(time.UTC, "2026-11-02", "2026-11-02", Filter{Dim: "utm_source", Value: "meta"})
	ov := overview(t, dir, qu, false)
	// m's visit: 60 s between its page views; e's: one page view, a bounce
	// of 0 s.
	want := Totals{Views: 3, Visitors: 2, Visits: 2, BounceRate: 0.5, DurationS: 30}
	if ov.Totals != want {
		t.Fatalf("totals = %+v, want %+v", ov.Totals, want)
	}
	sameRows(t, "event", table(t, dir, qu, "event"), []Row{
		{Value: "signup", Visitors: 1, Count: 1},
		{Value: "x", Visitors: 1, Count: 1},
	})
	sameRows(t, "path", table(t, dir, qu, "path"), []Row{
		{Value: "/landing", Visitors: 2, Views: 2},
		{Value: "/pricing", Visitors: 1, Views: 1},
	})
	// Two visit-level filters AND.
	qu.Filters = append(qu.Filters, Filter{Dim: "referrer", Value: "l.facebook.com/"})
	if ov := overview(t, dir, qu, false); ov.Totals.Visits != 1 || ov.Totals.Views != 2 {
		t.Fatalf("meta and facebook = %+v", ov.Totals)
	}
}

func TestFilterVisitAndRecordLevel(t *testing.T) {
	dir := metaVisits(t)
	qu := days(time.UTC, "2026-11-02", "2026-11-02", Filter{Dim: "path", Value: "/pricing"})
	// Alone, path=/pricing keeps m's and g's page view of it: each visit
	// holds one kept page view, so both are bounces of 0 s.
	if ov := overview(t, dir, qu, false); ov.Totals != (Totals{Views: 2, Visitors: 2, Visits: 2, BounceRate: 1}) {
		t.Fatalf("path=/pricing totals = %+v", ov.Totals)
	}
	qu.Filters = append(qu.Filters, Filter{Dim: "utm_source", Value: "meta"})
	if ov := overview(t, dir, qu, false); ov.Totals != (Totals{Views: 1, Visitors: 1, Visits: 1, BounceRate: 1}) {
		t.Fatalf("utm_source=meta and path=/pricing totals = %+v", ov.Totals)
	}
	sameRows(t, "event", table(t, dir, qu, "event"), []Row{{Value: "signup", Visitors: 1, Count: 1}})
	sameRows(t, "path", table(t, dir, qu, "path"), []Row{{Value: "/pricing", Visitors: 1, Views: 1}})
	sameRows(t, "entry", table(t, dir, qu, "entry"), []Row{{Value: "/landing", Visitors: 1, Views: 1}})
}

func TestFilterRecordLevel(t *testing.T) {
	dir := t.TempDir()
	writeRecords(t, dir, time.UTC, []Record{
		pv("2026-11-02T10:00:00Z", "a", "/a"),
		pv("2026-11-02T10:01:00Z", "a", "/b"),
		pv("2026-11-02T10:02:00Z", "a", "/a"),
		pv("2026-11-02T10:00:00Z", "b", "/b"),
		pv("2026-11-02T10:00:00Z", "c", "/a"),
		ev("2026-11-02T10:01:00Z", "c", "/a", "click"),
		ev("2026-11-02T10:02:00Z", "c", "/b", "other"),
	})
	qu := days(time.UTC, "2026-11-02", "2026-11-02", Filter{Dim: "path", Value: "/a"})
	ov := overview(t, dir, qu, false)
	// Views and visitors count /a; visits are those holding /a, measured on
	// their /a page views as Umami does: a's (two, 120 s apart) and c's (one,
	// a bounce of 0 s).
	want := Totals{Views: 3, Visitors: 2, Visits: 2, BounceRate: 0.5, DurationS: 60}
	if ov.Totals != want {
		t.Fatalf("totals = %+v, want %+v", ov.Totals, want)
	}
	sameRows(t, "path", table(t, dir, qu, "path"), []Row{{Value: "/a", Visitors: 2, Views: 3}})
	sameRows(t, "event", table(t, dir, qu, "event"), []Row{{Value: "click", Visitors: 1, Count: 1}})
	sameRows(t, "entry", table(t, dir, qu, "entry"), []Row{{Value: "/a", Visitors: 2, Views: 3}})

	qu.Filters = []Filter{{Dim: "event", Value: "click"}}
	// c's visit holds the click but no kept page view: no bounce, no duration.
	if ov := overview(t, dir, qu, false); ov.Totals != (Totals{Visitors: 1, Visits: 1}) {
		t.Fatalf("event=click totals = %+v", ov.Totals)
	}
}

func TestRecordFilterMeasuresKeptPageViews(t *testing.T) {
	dir := t.TempDir()
	writeRecords(t, dir, time.UTC, []Record{
		pv("2026-11-02T10:00:00Z", "a", "/a"),
		pv("2026-11-02T10:05:00Z", "a", "/b"),
		pv("2026-11-02T10:20:00Z", "a", "/a"),
		pv("2026-11-02T10:30:00Z", "a", "/c"),
		pv("2026-11-02T11:00:00Z", "b", "/b"),
		ev("2026-11-02T11:10:00Z", "b", "/b", "x"), // never extends a duration
	})
	for _, tc := range []struct {
		filters []Filter
		want    Totals
	}{
		// a: 4 page views over 1800 s; b: a bounce of 0 s.
		{nil, Totals{Views: 5, Visitors: 2, Visits: 2, BounceRate: 0.5, DurationS: 900}},
		// a's /a page views are 1200 s apart; b holds no /a.
		{[]Filter{{Dim: "path", Value: "/a"}}, Totals{Views: 2, Visitors: 1, Visits: 1, DurationS: 1200}},
		// One /b page view in each visit: two bounces.
		{[]Filter{{Dim: "path", Value: "/b"}}, Totals{Views: 2, Visitors: 2, Visits: 2, BounceRate: 1}},
	} {
		qu := days(time.UTC, "2026-11-02", "2026-11-02", tc.filters...)
		if ov := overview(t, dir, qu, false); ov.Totals != tc.want {
			t.Fatalf("%v: totals = %+v, want %+v", tc.filters, ov.Totals, tc.want)
		}
	}
}

// TestQueryReadsNeighbouringDays: day files are named in the zone the site
// had when they were written. After a zone change a query in the new zone
// still finds the records near midnight in the neighbouring day's file.
func TestQueryReadsNeighbouringDays(t *testing.T) {
	vienna, err := time.LoadLocation("Europe/Vienna")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	writeRecords(t, dir, time.UTC, []Record{
		pv("2026-11-02T23:30:00Z", "late", "/late"), // the UTC file 2026-11-02; 00:30 on 2026-11-03 in Vienna
		ev("2026-11-02T23:40:00Z", "late", "/late", "late-ev", data(map[string]any{"k": "v"})),
		pv("2026-11-03T12:00:00Z", "noon", "/noon"),
	})
	if err := os.WriteFile(overFile(dir, "2026-11-02"), []byte("7"), 0o644); err != nil {
		t.Fatal(err)
	}
	e := newTestEngine()
	nov3 := days(vienna, "2026-11-03", "2026-11-03")
	ov, err := e.Overview(dir, nov3, false)
	if err != nil || ov.Totals.Views != 2 {
		t.Fatalf("Vienna 2026-11-03 = %+v, %v; want both page views", ov.Totals, err)
	}
	if ov.OverCap != 0 {
		t.Fatalf("over_cap = %d: a neighbouring day's count is not the range's", ov.OverCap)
	}
	if ov, err := e.Overview(dir, days(vienna, "2026-11-02", "2026-11-02"), false); err != nil || ov.Totals.Views != 0 || ov.OverCap != 7 {
		t.Fatalf("Vienna 2026-11-02 = %+v over %d, %v; want nothing, over 7", ov.Totals, ov.OverCap, err)
	}
	if ov, err := e.Overview(dir, days(vienna, "2026-11-04", "2026-11-04"), true); err != nil || ov.Previous == nil || ov.Previous.Views != 2 {
		t.Fatalf("compare with 2026-11-03 = %+v, %v", ov.Previous, err)
	}
	sameRows(t, "path", table(t, dir, nov3, "path"), []Row{{Value: "/late", Visitors: 1, Views: 1}, {Value: "/noon", Visitors: 1, Views: 1}})
	if kv, err := e.EventData(dir, nov3, "late-ev"); err != nil || len(kv) != 1 || kv[0].Key != "k" {
		t.Fatalf("event data = %+v, %v", kv, err)
	}
	rt, err := e.Realtime(dir, tm("2026-11-02T23:50:00Z"), vienna) // 00:50 in Vienna: the window is all on 2026-11-03
	if err != nil {
		t.Fatal(err)
	}
	sameRows(t, "realtime pages", rt.Pages, []Row{{Value: "/late", Visitors: 1, Views: 1}})
}

func TestTableEntryExitReferrerChannel(t *testing.T) {
	dir := t.TempDir()
	writeRecords(t, dir, time.UTC, []Record{
		pv("2026-11-02T10:00:00Z", "v1", "/home", ref("www.google.com/")),
		pv("2026-11-02T10:05:00Z", "v1", "/about"),
		pv("2026-11-02T10:00:00Z", "v2", "/about", also(func(r *Record) {
			r.Q = map[string]string{"utm_source": "newsletter", "utm_medium": "email"}
		})),
		pv("2026-11-02T10:00:00Z", "v3", "/home", ref("news.ycombinator.com/item")),
		pv("2026-11-02T10:01:00Z", "v3", "/blog"),
		pv("2026-11-02T10:02:00Z", "v3", "/home"),
		pv("2026-11-02T10:00:00Z", "v4", "/home", ck("gclid")),
		ev("2026-11-02T10:00:00Z", "v5", "/x", "ping"), // a visit without a page view
	})
	qu := days(time.UTC, "2026-11-02", "2026-11-02")
	sameRows(t, "entry", table(t, dir, qu, "entry"), []Row{
		{Value: "/home", Visitors: 3, Views: 6},
		{Value: "/about", Visitors: 1, Views: 1},
	})
	sameRows(t, "exit", table(t, dir, qu, "exit"), []Row{
		{Value: "/home", Visitors: 2, Views: 4},
		{Value: "/about", Visitors: 2, Views: 3},
	})
	sameRows(t, "referrer", table(t, dir, qu, "referrer"), []Row{
		{Value: DirectReferrer, Visitors: 3, Views: 2},
		{Value: "news.ycombinator.com/item", Visitors: 1, Views: 3},
		{Value: "www.google.com/", Visitors: 1, Views: 2},
	})
	channels := []Row{
		{Value: "Referral", Visitors: 1, Views: 3},
		{Value: "Organic search", Visitors: 1, Views: 2},
		{Value: "Email", Visitors: 1, Views: 1},
		{Value: "Paid search", Visitors: 1, Views: 1},
		{Value: "Direct", Visitors: 1},
	}
	sameRows(t, "channel", table(t, dir, qu, "channel"), channels)
	sameRows(t, "click_id", table(t, dir, qu, "click_id"), []Row{{Value: "gclid", Visitors: 1, Views: 1}})
	sameRows(t, "utm_source", table(t, dir, qu, "utm_source"), []Row{{Value: "newsletter", Visitors: 1, Views: 1}})
	sameRows(t, "query:utm_medium", table(t, dir, qu, "query:utm_medium"), []Row{{Value: "email", Visitors: 1, Views: 1}})

	rows, total, err := newTestEngine().Table(dir, qu, "channel", 2, 1)
	if err != nil || total != 5 {
		t.Fatalf("paged: total %d, err %v", total, err)
	}
	sameRows(t, "channel page 2", rows, channels[1:3])
	if rows, total, _ := newTestEngine().Table(dir, qu, "channel", 2, 9); len(rows) != 0 || rows == nil || total != 5 {
		t.Fatalf("past the end: %+v, %d", rows, total)
	}

	qu.Filters = []Filter{{Dim: "referrer", Value: DirectReferrer}}
	if ov := overview(t, dir, qu, false); ov.Totals.Visits != 3 || ov.Totals.Views != 2 {
		t.Fatalf("referrer=(direct) totals = %+v", ov.Totals)
	}
	qu.Filters = []Filter{{Dim: "channel", Value: "Paid search"}, {Dim: "click_id", Value: "gclid"}}
	if ov := overview(t, dir, qu, false); ov.Totals.Visits != 1 || ov.Totals.Views != 1 {
		t.Fatalf("paid search + gclid totals = %+v", ov.Totals)
	}
	qu.Filters = []Filter{{Dim: "exit", Value: "/about"}}
	if ov := overview(t, dir, qu, false); ov.Totals.Visits != 2 || ov.Totals.Views != 3 {
		t.Fatalf("exit=/about totals = %+v", ov.Totals)
	}
}

func TestTableEventCounts(t *testing.T) {
	dir := t.TempDir()
	writeRecords(t, dir, time.UTC, []Record{
		ev("2026-11-02T10:00:00Z", "v1", "/", "signup"),
		ev("2026-11-02T10:01:00Z", "v1", "/", "signup"),
		ev("2026-11-02T10:02:00Z", "v1", "/", "click"),
		ev("2026-11-02T10:00:00Z", "v2", "/", "signup"),
		pv("2026-11-02T10:00:00Z", "v3", "/"),
		ev("2026-11-03T10:00:00Z", "v1", "/", "signup"), // the next day: another visitor
	})
	sameRows(t, "event", table(t, dir, days(time.UTC, "2026-11-02", "2026-11-03"), "event"), []Row{
		{Value: "signup", Visitors: 3, Count: 4},
		{Value: "click", Visitors: 1, Count: 1},
	})

	t.Run("events sort by count, as Umami's do", func(t *testing.T) {
		dir := t.TempDir()
		var recs []Record
		for i := range 1000 {
			recs = append(recs, ev(tm("2026-11-02T10:00:00Z").Add(time.Duration(i)*time.Second).Format(time.RFC3339), "one", "/", "often"))
		}
		recs = append(recs, ev("2026-11-02T11:00:00Z", "x", "/", "shared"), ev("2026-11-02T11:00:00Z", "y", "/", "shared"))
		writeRecords(t, dir, time.UTC, recs)
		sameRows(t, "event", table(t, dir, days(time.UTC, "2026-11-02", "2026-11-02"), "event"), []Row{
			{Value: "often", Visitors: 1, Count: 1000},
			{Value: "shared", Visitors: 2, Count: 2},
		})
	})
}

func TestEventData(t *testing.T) {
	dir := t.TempDir()
	writeRecords(t, dir, time.UTC, []Record{
		ev("2026-11-02T10:00:00Z", "v1", "/", "signup", cc("AT"), data(map[string]any{"plan": "pro", "seats": 3, "trial": true})),
		ev("2026-11-02T10:00:00Z", "v2", "/", "signup", cc("AT"), data(map[string]any{"plan": "pro", "seats": 2.5})),
		ev("2026-11-02T10:00:00Z", "v3", "/", "signup", cc("DE"), data(map[string]any{"plan": "free"})),
		ev("2026-11-02T10:00:00Z", "v4", "/", "other", cc("AT"), data(map[string]any{"plan": "x"})),
	})
	e := newTestEngine()
	qu := days(time.UTC, "2026-11-02", "2026-11-02")
	got, err := e.EventData(dir, qu, "signup")
	if err != nil {
		t.Fatal(err)
	}
	want := []KeyValues{
		{Key: "plan", Values: []ValueCount{{Value: "pro", Count: 2}, {Value: "free", Count: 1}}},
		{Key: "seats", Values: []ValueCount{{Value: "2.5", Count: 1}, {Value: "3", Count: 1}}},
		{Key: "trial", Values: []ValueCount{{Value: "true", Count: 1}}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("event data:\n got %+v\nwant %+v", got, want)
	}
	qu.Filters = []Filter{{Dim: "country", Value: "DE"}}
	got, err = e.EventData(dir, qu, "signup")
	if err != nil || !reflect.DeepEqual(got, []KeyValues{{Key: "plan", Values: []ValueCount{{Value: "free", Count: 1}}}}) {
		t.Fatalf("filtered event data = %+v, %v", got, err)
	}
	qu.Filters = []Filter{{Dim: "data:seats", Value: "2.5"}}
	if ov, err := e.Overview(dir, qu, false); err != nil || ov.Totals.Visitors != 1 {
		t.Fatalf("data:seats=2.5 = %+v, %v", ov.Totals, err)
	}
	got, err = e.EventData(dir, qu, "nothing")
	if err != nil || got == nil || len(got) != 0 {
		t.Fatalf("no such event = %#v, %v", got, err)
	}
}

func TestRealtime(t *testing.T) {
	dir := t.TempDir()
	now := tm("2026-11-04T12:00:30Z")
	writeRecords(t, dir, time.UTC, []Record{
		pv("2026-11-04T11:29:30Z", "d", "/old"), // 31 minutes ago
		pv("2026-11-04T11:50:30Z", "c", "/blog", cc("AT"), ref("news.ycombinator.com/")),
		ev("2026-11-04T11:51:30Z", "c", "/blog", "signup", cc("AT")),
		pv("2026-11-04T11:56:30Z", "b", "/", cc("DE")),
		pv("2026-11-04T11:59:30Z", "a", "/", cc("AT"), ref("www.google.com/"), also(func(r *Record) { r.B = "Chrome"; r.Dv = "desktop" })),
		pv("2026-11-04T12:00:00Z", "a", "/pricing", cc("AT"), also(func(r *Record) { r.B = "Chrome"; r.Dv = "desktop" })),
		pv("2026-11-04T12:01:30Z", "f", "/future"),
	})
	rt, err := newTestEngine().Realtime(dir, now, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	if rt.Active != 2 {
		t.Fatalf("active = %d, want 2 (a and b in the last 5 minutes)", rt.Active)
	}
	if len(rt.PerMinute) != 30 || !rt.PerMinute[0].T.Equal(tm("2026-11-04T11:31:00Z")) || !rt.PerMinute[29].T.Equal(tm("2026-11-04T12:00:00Z")) {
		t.Fatalf("per minute: %d points, %v .. %v", len(rt.PerMinute), rt.PerMinute[0].T, rt.PerMinute[len(rt.PerMinute)-1].T)
	}
	got := map[string][2]int{}
	for _, p := range rt.PerMinute {
		if p.Views+p.Visitors > 0 {
			got[p.T.Format("15:04")] = [2]int{p.Views, p.Visitors}
		}
	}
	want := map[string][2]int{"11:50": {1, 1}, "11:51": {0, 1}, "11:56": {1, 1}, "11:59": {1, 1}, "12:00": {1, 1}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("per minute = %v, want %v", got, want)
	}
	sameRows(t, "pages", rt.Pages, []Row{
		{Value: "/", Visitors: 2, Views: 2},
		{Value: "/blog", Visitors: 1, Views: 1},
		{Value: "/pricing", Visitors: 1, Views: 1},
	})
	sameRows(t, "referrers", rt.Referrers, []Row{
		{Value: "www.google.com/", Visitors: 1, Views: 2},
		{Value: DirectReferrer, Visitors: 1, Views: 1},
		{Value: "news.ycombinator.com/", Visitors: 1, Views: 1},
	})
	sameRows(t, "countries", rt.Countries, []Row{
		{Value: "AT", Visitors: 2, Views: 3},
		{Value: "DE", Visitors: 1, Views: 1},
	})
	wantLatest := []Recent{
		{T: tm("2026-11-04T12:00:00Z"), K: "p", P: "/pricing", C: "AT", B: "Chrome", Dv: "desktop"},
		{T: tm("2026-11-04T11:59:30Z"), K: "p", P: "/", C: "AT", B: "Chrome", Dv: "desktop"},
		{T: tm("2026-11-04T11:56:30Z"), K: "p", P: "/", C: "DE"},
		{T: tm("2026-11-04T11:51:30Z"), K: "e", N: "signup", C: "AT"},
		{T: tm("2026-11-04T11:50:30Z"), K: "p", P: "/blog", C: "AT"},
	}
	if len(rt.Latest) != len(wantLatest) {
		t.Fatalf("latest = %+v", rt.Latest)
	}
	for i, w := range wantLatest {
		g := rt.Latest[i]
		if !g.T.Equal(w.T) || g.K != w.K || g.P != w.P || g.N != w.N || g.C != w.C || g.B != w.B || g.Dv != w.Dv {
			t.Fatalf("latest[%d] = %+v, want %+v", i, g, w)
		}
	}

	t.Run("at most 50 latest, newest first", func(t *testing.T) {
		dir := t.TempDir()
		var recs []Record
		for i := 59; i >= 0; i-- {
			recs = append(recs, pv(now.Add(-time.Duration(i)*10*time.Second).Format(time.RFC3339), fmt.Sprint("v", i), "/"))
		}
		writeRecords(t, dir, time.UTC, recs)
		rt, err := newTestEngine().Realtime(dir, now, time.UTC)
		if err != nil {
			t.Fatal(err)
		}
		if len(rt.Latest) != 50 || !rt.Latest[0].T.Equal(now) {
			t.Fatalf("latest: %d, first %v", len(rt.Latest), rt.Latest[0].T)
		}
		for i := 1; i < len(rt.Latest); i++ {
			if !rt.Latest[i].T.Before(rt.Latest[i-1].T) {
				t.Fatalf("latest not newest first at %d", i)
			}
		}
		if rt.Active != 31 { // now-300 s .. now, every 10 s
			t.Fatalf("active = %d, want 31", rt.Active)
		}
	})

	t.Run("the window reaches into yesterday's file", func(t *testing.T) {
		dir := t.TempDir()
		writeRecords(t, dir, time.UTC, []Record{
			pv("2026-11-03T23:50:00Z", "y", "/"),
			pv("2026-11-04T00:05:00Z", "z", "/"),
		})
		rt, err := newTestEngine().Realtime(dir, tm("2026-11-04T00:10:00Z"), time.UTC)
		if err != nil {
			t.Fatal(err)
		}
		sameRows(t, "pages", rt.Pages, []Row{{Value: "/", Visitors: 2, Views: 2}})
	})
}

// TestSeriesAcrossDST is Review Focus 3: a local day of 23 or 25 hours
// neither skips nor doubles an hour.
func TestSeriesAcrossDST(t *testing.T) {
	vienna, err := time.LoadLocation("Europe/Vienna")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		first, day, last string
		hours            int
		perDay           []int
	}{
		{"2026-03-28", "2026-03-29", "2026-03-30", 23, []int{48, 46, 48}},
		{"2026-10-24", "2026-10-25", "2026-10-26", 25, []int{48, 50, 48}},
	} {
		t.Run(tc.day, func(t *testing.T) {
			dir := t.TempDir()
			span := days(vienna, tc.first, tc.last)
			var recs []Record
			for at := span.From; at.Before(span.To); at = at.Add(30 * time.Minute) {
				recs = append(recs, Record{T: at.UnixMilli(), K: "p", S: "srv", V: "x", P: "/"})
			}
			writeRecords(t, dir, vienna, recs)

			day := days(vienna, tc.day, tc.day)
			ov := overview(t, dir, day, false)
			if ov.Unit != "hour" || len(ov.Series) != tc.hours {
				t.Fatalf("%s: unit %q with %d points, want hour with %d", tc.day, ov.Unit, len(ov.Series), tc.hours)
			}
			if !ov.Series[0].T.Equal(day.From) {
				t.Fatalf("first point %v, want %v", ov.Series[0].T, day.From)
			}
			for i, p := range ov.Series {
				if p.Views != 2 {
					t.Fatalf("hour %d (%v) has %d views, want 2", i, p.T, p.Views)
				}
				if i > 0 && p.T.Sub(ov.Series[i-1].T) != time.Hour {
					t.Fatalf("point %d at %v follows %v", i, p.T, ov.Series[i-1].T)
				}
				if p.T.Location().String() != "Europe/Vienna" || p.T.Minute() != 0 {
					t.Fatalf("point %d at %v is not a local hour", i, p.T)
				}
			}
			if ov.Totals.Views != 2*tc.hours {
				t.Fatalf("views = %d, want %d", ov.Totals.Views, 2*tc.hours)
			}

			span.Unit = "day"
			ov = overview(t, dir, span, false)
			if len(ov.Series) != 3 {
				t.Fatalf("day unit: %d points, want 3", len(ov.Series))
			}
			for i, p := range ov.Series {
				l := p.T.In(vienna)
				if l.Hour() != 0 || l.Minute() != 0 || l.Format("2006-01-02") != LocalDate(span.From.AddDate(0, 0, i), vienna) {
					t.Fatalf("day point %d at %v is not a local midnight", i, p.T)
				}
				if p.Views != tc.perDay[i] {
					t.Fatalf("day %d has %d views, want %d", i, p.Views, tc.perDay[i])
				}
			}
		})
	}
}

func TestAutoUnit(t *testing.T) {
	vienna, err := time.LoadLocation("Europe/Vienna")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	e := newTestEngine()
	for _, tc := range []struct {
		first, last string
		unit        string
		points      int
	}{
		{"2026-11-01", "2026-11-02", "hour", 48},
		{"2026-10-24", "2026-10-25", "hour", 49}, // two local days of 49 hours
		{"2026-11-01", "2026-11-03", "day", 3},
		{"2026-01-01", "2026-04-02", "day", 92},
		{"2026-01-01", "2026-04-03", "month", 4},
		{"2026-01-01", "2026-05-31", "month", 5},
	} {
		ov, err := e.Overview(dir, days(vienna, tc.first, tc.last), false)
		if err != nil {
			t.Fatal(err)
		}
		if ov.Unit != tc.unit || len(ov.Series) != tc.points {
			t.Fatalf("%s..%s: %q with %d points, want %q with %d", tc.first, tc.last, ov.Unit, len(ov.Series), tc.unit, tc.points)
		}
		if tc.unit == "month" && !ov.Series[1].T.Equal(time.Date(2026, 2, 1, 0, 0, 0, 0, vienna)) {
			t.Fatalf("second month starts %v", ov.Series[1].T)
		}
	}
}

func TestImportedVisitsUseVi(t *testing.T) {
	dir := t.TempDir()
	writeRecords(t, dir, time.UTC, []Record{
		pv("2026-11-02T10:00:00Z", "u1", "/a", visit("A")),
		pv("2026-11-02T10:50:00Z", "u1", "/b", visit("A")), // 50 minutes: still Umami's visit A
		pv("2026-11-02T10:52:00Z", "u1", "/c", visit("B")), // 2 minutes: Umami's next visit
	})
	qu := days(time.UTC, "2026-11-02", "2026-11-02")
	ov := overview(t, dir, qu, false)
	want := Totals{Views: 3, Visitors: 1, Visits: 2, BounceRate: 0.5, DurationS: 1500}
	if ov.Totals != want {
		t.Fatalf("totals = %+v, want %+v", ov.Totals, want)
	}
	sameRows(t, "entry", table(t, dir, qu, "entry"), []Row{{Value: "/a", Visitors: 1, Views: 2}, {Value: "/c", Visitors: 1, Views: 1}})
	sameRows(t, "exit", table(t, dir, qu, "exit"), []Row{{Value: "/b", Visitors: 1, Views: 2}, {Value: "/c", Visitors: 1, Views: 1}})
}

func TestQueryRefusesBadDimsAndUnits(t *testing.T) {
	dir := t.TempDir()
	e := newTestEngine()
	qu := days(time.UTC, "2026-11-02", "2026-11-02")
	for _, dim := range []string{"nope", "query:", "data:plan", "data:", "utm_nope", ""} {
		if _, _, err := e.Table(dir, qu, dim, 10, 0); !errors.Is(err, ErrBadQuery) {
			t.Fatalf("Table(%q) = %v, want ErrBadQuery", dim, err)
		}
	}
	for _, dim := range []string{"nope", "query:", "data:"} {
		bad := qu
		bad.Filters = []Filter{{Dim: dim, Value: "x"}}
		if _, err := e.Overview(dir, bad, false); !errors.Is(err, ErrBadQuery) {
			t.Fatalf("filter %q = %v, want ErrBadQuery", dim, err)
		}
	}
	bad := qu
	bad.Unit = "week"
	if _, err := e.Overview(dir, bad, false); !errors.Is(err, ErrBadQuery) {
		t.Fatalf("unit week = %v, want ErrBadQuery", err)
	}
	bad = qu
	bad.To = bad.From
	if _, err := e.Overview(dir, bad, false); !errors.Is(err, ErrBadQuery) {
		t.Fatalf("an empty range = %v, want ErrBadQuery", err)
	}
	for _, dim := range append(append([]string{}, Dims...), "query:ref") {
		if _, _, err := e.Table(dir, qu, dim, 10, 0); err != nil {
			t.Fatalf("Table(%q) = %v", dim, err)
		}
	}
}

func TestVisitLevel(t *testing.T) {
	visit := []string{"entry", "exit", "referrer", "channel", "utm_source", "utm_medium", "utm_campaign", "utm_term", "utm_content", "click_id", "country", "region", "city", "browser", "os", "device", "language", "screen", "address", "query:ref"}
	record := []string{"path", "title", "tag", "event", "data:plan"}
	for _, d := range visit {
		if !VisitLevel(d) {
			t.Fatalf("%s must be visit-level", d)
		}
	}
	for _, d := range record {
		if VisitLevel(d) {
			t.Fatalf("%s must be record-level", d)
		}
	}
}

func TestEngineCacheSeesChanges(t *testing.T) {
	dir := t.TempDir()
	e := newTestEngine()
	qu := days(time.UTC, "2026-11-02", "2026-11-02")
	writeDay(t, dir, "2026-11-02", []Record{pv("2026-11-02T10:00:00Z", "a", "/a")})
	path := DayFile(dir, "2026-11-02")
	first := func() string {
		t.Helper()
		rows, _, err := e.Table(dir, qu, "path", 1, 0)
		if err != nil || len(rows) != 1 {
			t.Fatalf("rows %+v, %v", rows, err)
		}
		return rows[0].Value
	}
	if first() != "/a" {
		t.Fatal("first read")
	}

	// An appended line is seen: the file's size is part of the key.
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	line, _ := json.Marshal(pv("2026-11-02T11:00:00Z", "b", "/a"))
	if _, err := f.Write(append(line, '\n')); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if ov, err := e.Overview(dir, qu, false); err != nil || ov.Totals.Views != 2 {
		t.Fatalf("after an append: %+v, %v", ov.Totals, err)
	}

	// A rewrite of the same size and mtime is the cache's blind spot, which
	// Invalidate closes.
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	writeDay(t, dir, "2026-11-02", []Record{pv("2026-11-02T10:00:00Z", "a", "/z"), pv("2026-11-02T11:00:00Z", "b", "/z")})
	if err := os.Chtimes(path, fi.ModTime(), fi.ModTime()); err != nil {
		t.Fatal(err)
	}
	if first() != "/a" {
		t.Fatal("the same size and mtime should have been answered from the cache")
	}
	e.Invalidate(dir, "2026-11-02")
	if first() != "/z" {
		t.Fatal("Invalidate did not drop the parsed day")
	}

	// A deleted day is no longer answered.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if ov, err := e.Overview(dir, qu, false); err != nil || ov.Totals.Views != 0 {
		t.Fatalf("after a delete: %+v, %v", ov.Totals, err)
	}
}

func TestEngineCacheIsBounded(t *testing.T) {
	dir := t.TempDir()
	var recs []Record
	for d := 1; d <= 4; d++ {
		for i := 0; i < 200; i++ {
			recs = append(recs, pv(fmt.Sprintf("2026-11-%02dT10:%02d:%02dZ", d, i/60, i%60), fmt.Sprint("v", i), "/p"))
		}
	}
	writeRecords(t, dir, time.UTC, recs)
	one, err := NewEngine(64<<20).day(dir, "2026-11-01")
	if err != nil || one == nil {
		t.Fatal(err)
	}
	e := NewEngine(one.size*2 + one.size/2) // room for two days
	qu := days(time.UTC, "2026-11-01", "2026-11-04")
	for range 2 {
		ov, err := e.Overview(dir, qu, false)
		if err != nil || ov.Totals.Views != 800 {
			t.Fatalf("totals %+v, %v", ov.Totals, err)
		}
	}
	e.mu.Lock()
	used, n := e.used, len(e.items)
	e.mu.Unlock()
	if used > e.max || n != 2 {
		t.Fatalf("cache holds %d days in %d bytes, max %d", n, used, e.max)
	}

	tiny := NewEngine(1) // nothing fits: every query parses, and is still right
	if ov, err := tiny.Overview(dir, qu, false); err != nil || ov.Totals.Views != 800 {
		t.Fatalf("tiny cache: %+v, %v", ov.Totals, err)
	}
	if len(tiny.items) != 0 || tiny.used != 0 {
		t.Fatalf("tiny cache holds %d days", len(tiny.items))
	}
}

func TestEngineConcurrentQueries(t *testing.T) {
	dir := t.TempDir()
	var recs []Record
	for i := 0; i < 500; i++ {
		recs = append(recs, pv(fmt.Sprintf("2026-11-02T10:%02d:%02dZ", i/60, i%60), fmt.Sprint("v", i%40), fmt.Sprint("/p", i%7)))
	}
	writeRecords(t, dir, time.UTC, recs)
	e := NewEngine(64 << 20)
	qu := days(time.UTC, "2026-11-02", "2026-11-02")
	var wg sync.WaitGroup
	errs := make(chan error, 32)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if i%4 == 0 {
				e.Invalidate(dir, "2026-11-02")
			}
			ov, err := e.Overview(dir, qu, i%2 == 0)
			if err == nil && (ov.Totals.Views != 500 || ov.Totals.Visitors != 40) {
				err = fmt.Errorf("totals %+v", ov.Totals)
			}
			if err == nil {
				var rows []Row
				rows, _, err = e.Table(dir, qu, "path", 0, 0)
				if err == nil && len(rows) != 7 {
					err = fmt.Errorf("rows %+v", rows)
				}
			}
			if err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
}

func TestQueryJSONShapes(t *testing.T) {
	b, err := json.Marshal(Overview{Totals: Totals{Views: 1}, Series: []Point{{T: tm("2026-11-02T10:00:00Z"), Views: 1}}, Unit: "hour"})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"totals":{"views":1,"visitors":0,"visits":0,"bounce_rate":0,"duration_s":0},"series":[{"t":"2026-11-02T10:00:00Z","views":1,"visitors":0}],"unit":"hour","over_cap":0}`
	if string(b) != want {
		t.Fatalf("overview JSON:\n got %s\nwant %s", b, want)
	}
	b, _ = json.Marshal([]Row{{Value: "/a", Visitors: 1, Views: 2}, {Value: "signup", Visitors: 1, Count: 3}})
	if want := `[{"value":"/a","visitors":1,"views":2},{"value":"signup","visitors":1,"views":0,"count":3}]`; string(b) != want {
		t.Fatalf("rows JSON:\n got %s\nwant %s", b, want)
	}
	b, _ = json.Marshal([]KeyValues{{Key: "plan", Values: []ValueCount{{Value: "pro", Count: 2}}}})
	if want := `[{"key":"plan","values":[{"value":"pro","count":2}]}]`; string(b) != want {
		t.Fatalf("event data JSON: %s", b)
	}
	b, _ = json.Marshal(Realtime{Latest: []Recent{{T: tm("2026-11-02T10:00:00Z"), K: "e", N: "signup", C: "AT"}}})
	if want := `{"active":0,"per_minute":null,"pages":null,"referrers":null,"countries":null,"latest":[{"t":"2026-11-02T10:00:00Z","k":"e","n":"signup","c":"AT","b":"","dv":""}]}`; string(b) != want {
		t.Fatalf("realtime JSON:\n got %s\nwant %s", b, want)
	}
}
