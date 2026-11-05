package analytics

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/ittrail/sitebin.io/internal/store"
)

func pathStep(glob string) store.AnalyticsStep {
	return store.AnalyticsStep{Type: "path", Value: glob}
}

func eventStep(name string) store.AnalyticsStep {
	return store.AnalyticsStep{Type: "event", Value: name}
}

func funnel(id string, window int, steps ...store.AnalyticsStep) store.AnalyticsFunnel {
	return store.AnalyticsFunnel{ID: id, Name: "Funnel " + id, Steps: steps, WindowMinutes: window}
}

// funnels answers qu's funnels with an engine whose fixture days are
// closed and with one that never summarises, and requires both to agree:
// a funnel never comes from a day summary.
func funnels(t *testing.T, dir string, qu Query, fs ...store.AnalyticsFunnel) []FunnelResult {
	t.Helper()
	got, err := newTestEngine().Funnels(dir, qu, fs)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := rawEngine(64<<20).Funnels(dir, qu, fs)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, raw) {
		t.Fatalf("closed days answer\n %+v\na raw engine\n %+v", got, raw)
	}
	return got
}

// goals is funnels' twin for goals.
func goals(t *testing.T, dir string, qu Query, gs []store.AnalyticsGoal) []GoalResult {
	t.Helper()
	got, err := newTestEngine().Goals(dir, qu, gs)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := rawEngine(64<<20).Goals(dir, qu, gs)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, raw) {
		t.Fatalf("closed days answer\n %+v\na raw engine\n %+v", got, raw)
	}
	return got
}

// reached is the number of visitors at each step.
func reached(r FunnelResult) []int {
	out := make([]int, len(r.Steps))
	for i, s := range r.Steps {
		out[i] = s.Visitors
	}
	return out
}

func wantReached(t *testing.T, what string, r FunnelResult, want ...int) {
	t.Helper()
	if got := reached(r); !reflect.DeepEqual(got, want) {
		t.Fatalf("%s: visitors per step = %v, want %v", what, got, want)
	}
}

func TestFunnelWindowFromPreviousStep(t *testing.T) {
	dir := t.TempDir()
	writeRecords(t, dir, time.UTC, []Record{
		// A: each step 50 minutes after the one before, the last 100
		// minutes after the first.
		pv("2026-11-02T10:00:00Z", "A", "/a"),
		pv("2026-11-02T10:50:00Z", "A", "/b"),
		ev("2026-11-02T11:40:00Z", "A", "/b", "done"),
		// B: the third step 70 minutes after the second.
		pv("2026-11-02T10:00:00Z", "B", "/a"),
		pv("2026-11-02T10:50:00Z", "B", "/b"),
		ev("2026-11-02T12:00:00Z", "B", "/b", "done"),
		// C: the second step exactly 60 minutes after the first, the third
		// 61 after the second.
		pv("2026-11-02T10:00:00Z", "C", "/a"),
		pv("2026-11-02T11:00:00Z", "C", "/b"),
		ev("2026-11-02T12:01:00Z", "C", "/b", "done"),
		// D: the second step before the first.
		pv("2026-11-02T09:00:00Z", "D", "/b"),
		pv("2026-11-02T09:10:00Z", "D", "/a"),
		// E: one page view matching both steps of the second funnel.
		pv("2026-11-02T14:00:00Z", "E", "/x/y"),
	})
	qu := days(time.UTC, "2026-11-02", "2026-11-02")
	got := funnels(t, dir, qu,
		funnel("f", 60, pathStep("/a"), pathStep("/b"), eventStep("done")),
		funnel("self", 60, pathStep("/x/*"), pathStep("/x/y")),
	)
	want := []FunnelResult{
		{ID: "f", Name: "Funnel f", Steps: []StepResult{
			{Visitors: 4, Conversion: 1},
			{Visitors: 3, DropOff: 1, Conversion: 0.75},
			{Visitors: 1, DropOff: 2, Conversion: 0.25},
		}},
		// A record never follows itself.
		{ID: "self", Name: "Funnel self", Steps: []StepResult{
			{Visitors: 1, Conversion: 1},
			{Visitors: 0, DropOff: 1, Conversion: 0},
		}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("funnels =\n %+v\nwant\n %+v", got, want)
	}
}

func TestFunnelAnyChainCounts(t *testing.T) {
	dir := t.TempDir()
	writeRecords(t, dir, time.UTC, []Record{
		// A: the first landing leads nowhere; one four hours later goes
		// all the way.
		pv("2026-11-02T09:00:00Z", "A", "/a"),
		pv("2026-11-02T13:00:00Z", "A", "/a"),
		pv("2026-11-02T13:05:00Z", "A", "/b"),
		ev("2026-11-02T13:10:00Z", "A", "/b", "done"),
		// B: the first chain stops at the second step, 140 minutes before
		// the third; a later second step leads to it.
		pv("2026-11-02T09:00:00Z", "B", "/a"),
		pv("2026-11-02T09:10:00Z", "B", "/b"),
		pv("2026-11-02T11:00:00Z", "B", "/a"),
		pv("2026-11-02T11:20:00Z", "B", "/b"),
		ev("2026-11-02T11:30:00Z", "B", "/b", "done"),
	})
	got := funnels(t, dir, days(time.UTC, "2026-11-02", "2026-11-02"),
		funnel("f", 60, pathStep("/a"), pathStep("/b"), eventStep("done")))
	wantReached(t, "any chain", got[0], 2, 2, 2)
	if s := got[0].Steps[2]; s.DropOff != 0 || s.Conversion != 1 {
		t.Fatalf("third step = %+v, want no drop-off and a conversion of 1", s)
	}
}

// TestFunnelMorgenfertig is the operator's own funnel on morgenfertig.at,
// "Landingpage → Anfrage": a landing page, the form begun, the form sent.
func TestFunnelMorgenfertig(t *testing.T) {
	vienna, err := time.LoadLocation("Europe/Vienna")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	writeRecords(t, dir, vienna, []Record{
		// m1, from a Meta ad: lands, begins the form, sends it.
		pv("2026-11-02T09:00:00+01:00", "m1", "/app-entwickeln-lassen/", q("utm_source", "meta")),
		ev("2026-11-02T09:04:00+01:00", "m1", "/app-entwickeln-lassen/", "anfrage-start", data(map[string]any{"quelle": "meta"})),
		ev("2026-11-02T09:09:00+01:00", "m1", "/app-entwickeln-lassen/", "anfrage", data(map[string]any{"paket": "start"})),
		// m2, from Meta: lands on another landing page, begins, never sends.
		pv("2026-11-02T10:00:00+01:00", "m2", "/app-entwickeln-lassen/salzburg", q("utm_source", "meta")),
		ev("2026-11-02T10:02:00+01:00", "m2", "/app-entwickeln-lassen/salzburg", "anfrage-start"),
		// g1, from Google: sends 75 minutes after beginning.
		pv("2026-11-02T11:00:00+01:00", "g1", "/app-entwickeln-lassen/", q("utm_source", "google")),
		ev("2026-11-02T11:30:00+01:00", "g1", "/app-entwickeln-lassen/", "anfrage-start"),
		ev("2026-11-02T12:45:00+01:00", "g1", "/app-entwickeln-lassen/", "anfrage"),
		// h1 sends the form from the home page, never from a landing page.
		pv("2026-11-02T12:00:00+01:00", "h1", "/"),
		ev("2026-11-02T12:01:00+01:00", "h1", "/", "anfrage-start"),
		ev("2026-11-02T12:03:00+01:00", "h1", "/", "anfrage"),
		// b1 lands and rates; bewerten is no step.
		pv("2026-11-02T14:00:00+01:00", "b1", "/app-entwickeln-lassen/"),
		ev("2026-11-02T14:05:00+01:00", "b1", "/app-entwickeln-lassen/", "bewerten"),
	})
	f := store.AnalyticsFunnel{
		ID: "landingpage-anfrage", Name: "Landingpage → Anfrage", WindowMinutes: 60,
		Steps: []store.AnalyticsStep{pathStep("/app-entwickeln-lassen/*"), eventStep("anfrage-start"), eventStep("anfrage")},
	}
	day := days(vienna, "2026-11-02", "2026-11-02")

	got := funnels(t, dir, day, f)[0]
	if got.ID != f.ID || got.Name != f.Name {
		t.Fatalf("result names %q %q", got.ID, got.Name)
	}
	// m1, m2, g1 and b1 landed; m1, m2 and g1 began; m1 sent within the hour.
	want := []StepResult{
		{Visitors: 4, Conversion: 1},
		{Visitors: 3, DropOff: 1, Conversion: 0.75},
		{Visitors: 1, DropOff: 2, Conversion: 0.25},
	}
	if !reflect.DeepEqual(got.Steps, want) {
		t.Fatalf("steps = %+v, want %+v", got.Steps, want)
	}

	// The operator's own report: the Meta visits only.
	meta := day
	meta.Filters = []Filter{{Dim: "utm_source", Value: "meta"}}
	wantReached(t, "utm_source=meta", funnels(t, dir, meta, f)[0], 2, 2, 1)

	// A range from 09:05 leaves m1's landing and its begun form out: the
	// form it sent, in range, follows nothing.
	cut := day
	cut.From = tm("2026-11-02T09:05:00+01:00")
	wantReached(t, "from 09:05", funnels(t, dir, cut, f)[0], 3, 2, 0)
}

func TestFunnelDoesNotLinkAcrossDays(t *testing.T) {
	dir := t.TempDir()
	writeRecords(t, dir, time.UTC, []Record{
		// A lands ten minutes before midnight and finishes ten minutes
		// after: two visitor-days, even under the same id.
		pv("2026-11-02T23:50:00Z", "A", "/a"),
		ev("2026-11-03T00:10:00Z", "A", "/a", "done"),
		// B does both within the next day.
		pv("2026-11-03T00:20:00Z", "B", "/a"),
		ev("2026-11-03T00:30:00Z", "B", "/a", "done"),
	})
	got := funnels(t, dir, days(time.UTC, "2026-11-02", "2026-11-03"), funnel("f", 60, pathStep("/a"), eventStep("done")))
	wantReached(t, "across midnight", got[0], 2, 1)
}

func TestGoalsRate(t *testing.T) {
	dir := t.TempDir()
	writeRecords(t, dir, time.UTC, []Record{
		pv("2026-11-02T10:00:00Z", "A", "/pricing", cc("AT")),
		ev("2026-11-02T10:01:00Z", "A", "/pricing", "signup", cc("AT")),
		ev("2026-11-02T10:02:00Z", "A", "/pricing", "signup", cc("AT")),
		pv("2026-11-02T11:00:00Z", "B", "/pricing", cc("AT")),
		pv("2026-11-02T11:01:00Z", "B", "/pricing", cc("AT")),
		pv("2026-11-02T11:02:00Z", "B", "/pricing/team", cc("AT")),
		pv("2026-11-02T12:00:00Z", "C", "/pricing", cc("DE")),
		pv("2026-11-02T13:00:00Z", "D", "/", cc("DE")),
		// The same id the next day is another visitor.
		pv("2026-11-03T10:00:00Z", "A", "/", cc("DE")),
		ev("2026-11-03T10:01:00Z", "A", "/", "signup", cc("DE")),
	})
	gs := []store.AnalyticsGoal{
		{ID: "signup", Name: "Signed up", Type: "event", Value: "signup"},
		{ID: "pricing", Name: "Saw pricing", Type: "path", Value: "/pricing"},
		{ID: "plan", Name: "Saw a plan", Type: "path", Value: "/pricing/*"},
		{ID: "never", Name: "Never", Type: "event", Value: "never"},
		{ID: "bad", Name: "Not a glob", Type: "path", Value: "pricing"},
	}
	result := func(id string, completions, visitors int, rate float64) GoalResult {
		for _, g := range gs {
			if g.ID == id {
				return GoalResult{ID: id, Name: g.Name, Completions: completions, Visitors: visitors, Rate: rate}
			}
		}
		panic(id)
	}
	check := func(what string, got, want []GoalResult) {
		t.Helper()
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%s: goals =\n %+v\nwant\n %+v", what, got, want)
		}
	}
	both := days(time.UTC, "2026-11-02", "2026-11-03")

	// Five visitors: A, B, C and D on the 2nd, A again on the 3rd. The
	// signups carry the path /pricing, but a path goal counts page views.
	check("unfiltered", goals(t, dir, both, gs), []GoalResult{
		result("signup", 3, 2, 0.4),
		result("pricing", 4, 3, 0.6),
		result("plan", 1, 1, 0.2),
		result("never", 0, 0, 0),
		result("bad", 0, 0, 0),
	})

	// A visit-level filter keeps the Austrian visits: A on the 2nd and B.
	at := both
	at.Filters = []Filter{{Dim: "country", Value: "AT"}}
	check("country=AT", goals(t, dir, at, gs), []GoalResult{
		result("signup", 2, 1, 0.5),
		result("pricing", 3, 2, 1),
		result("plan", 1, 1, 0.5),
		result("never", 0, 0, 0),
		result("bad", 0, 0, 0),
	})

	// A record-level filter keeps the records on /pricing, the signups of
	// the 2nd among them: A, B and C.
	onPricing := both
	onPricing.Filters = []Filter{{Dim: "path", Value: "/pricing"}}
	check("path=/pricing", goals(t, dir, onPricing, gs), []GoalResult{
		result("signup", 2, 1, 1.0/3),
		result("pricing", 4, 3, 1),
		result("plan", 0, 0, 0),
		result("never", 0, 0, 0),
		result("bad", 0, 0, 0),
	})

	// No visitors: a rate of 0, never NaN.
	check("empty range", goals(t, dir, days(time.UTC, "2026-11-05", "2026-11-05"), gs[:1]), []GoalResult{
		result("signup", 0, 0, 0),
	})
}

func TestFunnelAndGoalJSON(t *testing.T) {
	b, err := json.Marshal([]FunnelResult{{ID: "f", Name: "F", Steps: []StepResult{{Visitors: 2, Conversion: 1}, {Visitors: 1, DropOff: 1, Conversion: 0.5}}}})
	if err != nil {
		t.Fatal(err)
	}
	if want := `[{"id":"f","name":"F","steps":[{"visitors":2,"drop_off":0,"conversion":1},{"visitors":1,"drop_off":1,"conversion":0.5}]}]`; string(b) != want {
		t.Fatalf("funnel JSON:\n got %s\nwant %s", b, want)
	}
	b, _ = json.Marshal([]GoalResult{{ID: "g", Name: "G", Completions: 3, Visitors: 2, Rate: 0.4}})
	if want := `[{"id":"g","name":"G","completions":3,"visitors":2,"rate":0.4}]`; string(b) != want {
		t.Fatalf("goal JSON:\n got %s\nwant %s", b, want)
	}

	// No definitions answer an empty list, not null.
	dir := t.TempDir()
	qu := days(time.UTC, "2026-11-02", "2026-11-02")
	fs, err := newTestEngine().Funnels(dir, qu, nil)
	if err != nil {
		t.Fatal(err)
	}
	gs, err := newTestEngine().Goals(dir, qu, nil)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := json.Marshal(fs); string(b) != "[]" {
		t.Fatalf("no funnels = %s", b)
	}
	if b, _ := json.Marshal(gs); string(b) != "[]" {
		t.Fatalf("no goals = %s", b)
	}
}
