package analytics

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/ittrail/sitebin.io/internal/store"
)

// The Umami import. See "Umami import, per site" in
// docs/superpowers/specs/2026-11-04-site-analytics-design.md.

func umamiAt(t *testing.T, s string) time.Time {
	t.Helper()
	at, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatal(err)
	}
	return at
}

func TestConvertUmamiMapsNames(t *testing.T) {
	for _, c := range []struct {
		browser, os, device string
		b, o, dv            string
	}{
		{"chrome", "Windows 10", "desktop", "Chrome", "Windows", "desktop"},
		{"ios", "iOS", "mobile", "Safari", "iOS", "mobile"},
		{"crios", "iOS", "tablet", "Chrome (iOS)", "iOS", "tablet"},
		{"edge-chromium", "Windows 7", "laptop", "Edge", "Windows", "laptop"},
		{"safari", "Mac OS", "laptop", "Safari", "macOS", "laptop"},
		{"samsung", "Android OS", "mobile", "Samsung Internet", "Android", "mobile"},
		{"firefox", "Linux", "desktop", "Firefox", "Linux", "desktop"},
		{"fxios", "iOS", "mobile", "Firefox (iOS)", "iOS", "mobile"},
		{"edge-ios", "iOS", "mobile", "Edge", "iOS", "mobile"},
		{"opera", "Chrome OS", "laptop", "Opera", "ChromeOS", "laptop"},
		{"yandexbrowser", "Android OS", "mobile", "Yandex", "Android", "mobile"},
		{"chromium-webview", "Android OS", "mobile", "Android WebView", "Android", "mobile"},
		{"instagram", "Android OS", "mobile", "Android WebView", "Android", "mobile"},
		{"instagram", "iOS", "mobile", "Other", "iOS", "mobile"},
		{"ios-webview", "iOS", "mobile", "Other", "iOS", "mobile"},
		{"netfront", "Sun OS", "smarttv", "Other", "Other", ""},
		{"", "", "", "Other", "Other", ""},
	} {
		ev := UmamiEvent{CreatedAt: umamiAt(t, "2026-03-02T10:00:00Z"), EventType: 1, URLPath: "/", SessionID: "s1", Browser: c.browser, OS: c.os, Device: c.device}
		_, rec := ConvertUmami(ev, time.UTC, nil, nil)
		if rec.B != c.b || rec.O != c.o || rec.Dv != c.dv {
			t.Errorf("%q/%q/%q -> %q/%q/%q, want %q/%q/%q", c.browser, c.os, c.device, rec.B, rec.O, rec.Dv, c.b, c.o, c.dv)
		}
	}

	vienna, _ := time.LoadLocation("Europe/Vienna")
	ev := UmamiEvent{
		CreatedAt:      umamiAt(t, "2026-03-01T23:30:00Z"), // 00:30 on the 2nd in Vienna
		EventType:      1,
		Hostname:       "www.morgenfertig.at",
		URLPath:        "/app-entwickeln-lassen/ios",
		URLQuery:       "utm_source=meta&ref=newsletter&gclid=SECRET&lang=de",
		ReferrerDomain: "morgenfertig.at", // Umami drops the www. of a referrer
		ReferrerPath:   "/",
		PageTitle:      "App entwickeln lassen",
		Tag:            "b",
		VisitID:        "a0e1b2c3-0000-4000-8000-000000000001",
		SessionID:      "5e55e55e-0000-4000-8000-000000000001",
		UTMSource:      "meta",
		UTMCampaign:    "herbst",
		Gclid:          "SECRET",
		Browser:        "chrome",
		OS:             "Mac OS",
		Device:         "laptop",
		Screen:         "1440x900",
		Language:       "de-AT",
		Country:        "AT",
		Region:         "AT-9",
		City:           "Vienna",
	}
	own := []string{"x7k2m9p4q8r3s6t1v5w0y2z4ab.sitebin.app", "www.morgenfertig.at"}
	date, rec := ConvertUmami(ev, vienna, own, []string{"ref"})
	if date != "2026-03-02" {
		t.Errorf("date = %q, want the local date 2026-03-02", date)
	}
	want := Record{
		T: ev.CreatedAt.UnixMilli(), K: "p", S: "um", A: "www.morgenfertig.at", P: "/app-entwickeln-lassen/ios",
		Ti: "App entwickeln lassen", Tg: "b", Vi: ev.VisitID,
		Q:  map[string]string{"utm_source": "meta", "utm_campaign": "herbst", "ref": "newsletter"},
		Ck: []string{"gclid"},
		B:  "Chrome", O: "macOS", Dv: "laptop", Sc: "1440x900", L: "de-AT", C: "AT", Rg: "AT-9", Ct: "Vienna",
	}
	if len(rec.V) != 16 {
		t.Errorf("v = %q, want 16 hex characters", rec.V)
	}
	want.V = rec.V
	if !reflect.DeepEqual(rec, want) {
		t.Errorf("record =\n%+v\nwant\n%+v", rec, want)
	}
	line, _ := json.Marshal(rec)
	if bytes.Contains(line, []byte("SECRET")) {
		t.Errorf("a click id's value was kept: %s", line)
	}

	// The visitor id is per session and local day.
	same := ev
	same.CreatedAt = ev.CreatedAt.Add(time.Hour)
	if _, r := ConvertUmami(same, vienna, own, nil); r.V != rec.V {
		t.Error("the same session on the same local day has another visitor id")
	}
	next := ev
	next.CreatedAt = ev.CreatedAt.Add(24 * time.Hour)
	if _, r := ConvertUmami(next, vienna, own, nil); r.V == rec.V {
		t.Error("the same session on the next day keeps its visitor id")
	}
	other := ev
	other.SessionID = "5e55e55e-0000-4000-8000-000000000002"
	if _, r := ConvertUmami(other, vienna, own, nil); r.V == rec.V {
		t.Error("another session has the same visitor id")
	}

	// A referrer from the host the event was recorded on is internal too,
	// even when that host is not (yet) one of the site's addresses.
	self := ev
	self.ReferrerDomain = "morgenfertig.at"
	if _, r := ConvertUmami(self, vienna, nil, nil); r.R != "" {
		t.Errorf("r = %q for a referrer from the event's own host", r.R)
	}
	// A www. on either side does not make a referrer external.
	www := ev
	www.Hostname, www.ReferrerDomain = "morgenfertig.at", "www.morgenfertig.at"
	if _, r := ConvertUmami(www, vienna, []string{"morgenfertig.at"}, nil); r.R != "" {
		t.Errorf("r = %q for the site's own host with www.", r.R)
	}
	// An external referrer is host and path, never its query.
	ext := ev
	ext.ReferrerDomain, ext.ReferrerPath = "google.com", "/search?q=app"
	if _, r := ConvertUmami(ext, vienna, own, nil); r.R != "google.com/search" {
		t.Errorf("r = %q, want google.com/search", r.R)
	}

	// An event: its name and bounded data.
	e := ev
	e.EventType, e.EventName = 2, "anfrage"
	e.Data = map[string]any{"seite": "/kontakt", "budget": 5000.0, "eilig": true, "liste": []any{1.0}}
	_, er := ConvertUmami(e, vienna, own, nil)
	if er.K != "e" || er.N != "anfrage" || !reflect.DeepEqual(er.D, map[string]any{"seite": "/kontakt", "budget": 5000.0, "eilig": true}) {
		t.Errorf("event = k %q n %q d %v", er.K, er.N, er.D)
	}

	// Fields with no Clean function of their own are bounded too.
	long := ev
	long.Hostname = strings.Repeat("h", 300)
	long.VisitID = strings.Repeat("v", 100)
	long.Region, long.City = strings.Repeat("ö", 150), strings.Repeat("ß", 150)
	if _, r := ConvertUmami(long, vienna, nil, nil); len(r.A) != 253 || len(r.Vi) != 64 ||
		utf8.RuneCountInString(r.Rg) != 100 || utf8.RuneCountInString(r.Ct) != 100 || !utf8.ValidString(r.Rg+r.Ct) {
		t.Errorf("bounded: a %d, vi %d, rg %d runes, ct %d runes", len(r.A), len(r.Vi), utf8.RuneCountInString(r.Rg), utf8.RuneCountInString(r.Ct))
	}

	// Neither a page view nor a named event: nothing to import.
	for _, bad := range []UmamiEvent{{EventType: 3, SessionID: "s"}, {EventType: 2, SessionID: "s"}} {
		bad.CreatedAt = ev.CreatedAt
		if _, r := ConvertUmami(bad, vienna, nil, nil); r.K != "" {
			t.Errorf("type %d name %q converted to k %q", bad.EventType, bad.EventName, r.K)
		}
	}
}

// umamiLine is one export line.
func umamiLine(t *testing.T, ev UmamiEvent) string {
	t.Helper()
	b, err := json.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}
	return string(b) + "\n"
}

func umamiView(at time.Time, session, path string) UmamiEvent {
	return UmamiEvent{CreatedAt: at, EventType: 1, Hostname: "example.at", URLPath: path, SessionID: session, VisitID: "visit-" + session, Browser: "chrome", OS: "Windows 10", Device: "desktop"}
}

func importInto(t *testing.T, e *recorderEnv, dir, export string, cut time.Time, retention int, now time.Time) (ImportReport, error) {
	t.Helper()
	return Import(dir, time.UTC, []string{"example.at"}, nil, strings.NewReader(export), cut, retention, now,
		func(fn func(string) error) (bool, error) { return e.withSite(filepath.Base(dir), fn) })
}

// dayFiles reads every file under the site's events folder, by name.
func dayFiles(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	es, err := os.ReadDir(eventsDir(dir))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range es {
		b, err := os.ReadFile(filepath.Join(eventsDir(dir), e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		out[e.Name()] = string(b)
	}
	return out
}

func TestImportIdempotent(t *testing.T) {
	e := newRecorderEnv(t)
	dir := e.site(t, testView)
	now := umamiAt(t, "2026-03-10T12:00:00Z")
	d1 := umamiAt(t, "2026-03-01T09:00:00Z")
	// An own record at the same instant as an imported one: the order of
	// equal times must not depend on the run.
	touch(t, DayFile(dir, "2026-03-02"), `{"t":`+itoa(d1.Add(24*time.Hour).UnixMilli())+`,"k":"p","s":"srv","v":"own"}`+"\n", now)
	export := umamiLine(t, umamiView(d1, "a", "/")) +
		umamiLine(t, umamiView(d1, "b", "/x")) + // same t, input order kept
		umamiLine(t, umamiView(d1.Add(time.Minute), "a", "/y")) +
		umamiLine(t, umamiView(d1.Add(24*time.Hour), "c", "/"))
	// Many lines at one instant, as a burst of events can be, interleaved
	// with as many at another: an unstable sort would shuffle them.
	burst := d1.Add(3 * time.Hour)
	for i := range 64 {
		export += umamiLine(t, umamiView(burst, "d", "/burst/"+itoa(int64(i))))
		export += umamiLine(t, umamiView(burst.Add(-time.Hour), "d", "/before/"+itoa(int64(i))))
	}

	rep1, err := importInto(t, e, dir, export, time.Time{}, 0, now)
	if err != nil {
		t.Fatal(err)
	}
	first := dayFiles(t, dir)
	rep2, err := importInto(t, e, dir, export, time.Time{}, 0, now)
	if err != nil {
		t.Fatal(err)
	}
	if again := dayFiles(t, dir); !reflect.DeepEqual(first, again) {
		t.Errorf("a second run changed the files:\n%v\n%v", first, again)
	}
	if !reflect.DeepEqual(rep1, rep2) {
		t.Errorf("reports differ: %+v / %+v", rep1, rep2)
	}
	want := ImportReport{Days: 2, Records: 132, From: "2026-03-01", To: "2026-03-02"}
	if !reflect.DeepEqual(rep1, want) {
		t.Errorf("report = %+v, want %+v", rep1, want)
	}
	recs, _ := ReadDay(dir, "2026-03-01")
	if len(recs) != 131 || recs[0].P != "/" || recs[1].P != "/x" || recs[2].P != "/y" {
		t.Fatalf("day 1 = %+v", recs)
	}
	for i := range 64 {
		if p := recs[3+i].P; p != "/before/"+itoa(int64(i)) {
			t.Fatalf("line %d at one instant is %s: equal times lost their order", i, p)
		}
		if p := recs[67+i].P; p != "/burst/"+itoa(int64(i)) {
			t.Fatalf("line %d of the burst is %s: equal times lost their order", i, p)
		}
	}
	recs, _ = ReadDay(dir, "2026-03-02")
	if len(recs) != 2 || recs[0].S != "srv" || recs[1].S != "um" {
		t.Errorf("day 2 = %+v, want the own record first at an equal time", recs)
	}
	for name := range first {
		if strings.Contains(name, ".tmp") {
			t.Errorf("a temp file stayed: %s", name)
		}
	}
}

func itoa(n int64) string { b, _ := json.Marshal(n); return string(b) }

func TestImportKeepsOwnRecordsAndCutsAtFirstOwn(t *testing.T) {
	e := newRecorderEnv(t)
	dir := e.site(t, testView)
	now := umamiAt(t, "2026-03-10T12:00:00Z")
	day := umamiAt(t, "2026-03-05T00:00:00Z")
	at := func(h, m int) time.Time { return day.Add(time.Duration(h)*time.Hour + time.Duration(m)*time.Minute) }
	ms := func(x time.Time) string { return itoa(x.UnixMilli()) }

	// What a site that switched over holds: an earlier import's lines, and
	// from 12:00 its own. The own lines are kept byte for byte, however
	// they are written.
	srvLine := `{"s":"srv","t":` + ms(at(12, 0)) + `,"k":"p","p":"/","v":"own1"}`
	jsLine := `{"t":` + ms(at(13, 0)) + `,"k":"e","s":"js","n":"anfrage","d":{"n":1.50},"v":"own2"}`
	touch(t, DayFile(dir, "2026-03-05"), `{"t":`+ms(at(8, 0))+`,"k":"p","s":"um","v":"old"}`+"\n"+srvLine+"\n"+jsLine+"\n", now)
	touch(t, DayFile(dir, "2026-03-04"), `{"t":`+ms(at(-14, 0))+`,"k":"p","s":"um","v":"old"}`+"\n", now)
	// A later day's own record is not the first.
	touch(t, DayFile(dir, "2026-03-06"), `{"t":`+ms(at(30, 0))+`,"k":"p","s":"srv","v":"own3"}`+"\n", now)

	first, ok, err := FirstOwnRecord(dir)
	if err != nil || !ok || !first.Equal(at(12, 0)) {
		t.Fatalf("FirstOwnRecord = %v %v %v, want %v", first, ok, err, at(12, 0))
	}

	export := umamiLine(t, umamiView(at(-14, 0), "a", "/eins")) +
		umamiLine(t, umamiView(at(9, 0), "a", "/zwei")) +
		umamiLine(t, umamiView(at(11, 59), "b", "/drei")) +
		umamiLine(t, umamiView(at(12, 0), "b", "/vier")) + // Sitebin measured from here on
		umamiLine(t, umamiView(at(14, 0), "c", "/fuenf"))
	rep, err := importInto(t, e, dir, export, first, 0, now)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Records != 3 || rep.SkippedAfterCut != 2 || rep.Days != 2 || rep.From != "2026-03-04" || rep.To != "2026-03-05" {
		t.Errorf("report = %+v", rep)
	}
	files := dayFiles(t, dir)
	lines := strings.Split(strings.TrimSuffix(files["2026-03-05.jsonl"], "\n"), "\n")
	if len(lines) != 4 || lines[2] != srvLine || lines[3] != jsLine {
		t.Fatalf("day = %q", lines)
	}
	recs, _ := ReadDay(dir, "2026-03-05")
	if recs[0].P != "/zwei" || recs[1].P != "/drei" || recs[0].S != "um" {
		t.Errorf("imported = %+v", recs[:2])
	}
	if strings.Contains(files["2026-03-04.jsonl"], `"old"`) || !strings.Contains(files["2026-03-04.jsonl"], "/eins") {
		t.Errorf("the earlier import's line was not replaced: %s", files["2026-03-04.jsonl"])
	}
	if strings.Contains(files["2026-03-05.jsonl"], `"old"`) {
		t.Error("the earlier import's line stayed beside the new one")
	}

	// No own record yet: no cut from them.
	empty := e.site(t, "bbbbbbbbbbbbbbbbbbbbbbbbbb")
	if _, ok, err := FirstOwnRecord(empty); ok || err != nil {
		t.Errorf("a site with no day file: %v %v", ok, err)
	}
	touch(t, DayFile(empty, "2026-03-01"), `{"t":1,"k":"p","s":"um"}`+"\n", now)
	if _, ok, err := FirstOwnRecord(empty); ok || err != nil {
		t.Errorf("an imported record counted as the site's own: %v %v", ok, err)
	}
}

// What cannot be read is an error, never "no own records": the import would
// then run past the site's first own record and count twice.
func TestFirstOwnRecordRefusesWhatItCannotRead(t *testing.T) {
	e := newRecorderEnv(t)
	now := umamiAt(t, "2026-03-10T12:00:00Z")

	// A day file that cannot be read (a folder where the file should be).
	dir := e.site(t, testView)
	if err := os.MkdirAll(DayFile(dir, "2026-03-04"), 0o755); err != nil {
		t.Fatal(err)
	}
	touch(t, DayFile(dir, "2026-03-05"), `{"t":5,"k":"p","s":"srv"}`+"\n", now)
	if _, ok, err := FirstOwnRecord(dir); err == nil || ok {
		t.Errorf("an unreadable day file: ok %v, err %v", ok, err)
	}

	// A link where a day file should be is not followed (no file surface
	// follows a link out of a site).
	linked := e.site(t, "dddddddddddddddddddddddddd")
	target := filepath.Join(t.TempDir(), "elsewhere.jsonl")
	touch(t, target, `{"t":5,"k":"p","s":"srv"}`+"\n", now)
	if err := os.MkdirAll(eventsDir(linked), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, DayFile(linked, "2026-03-04")); err != nil {
		t.Logf("no symlink on this system (%v): the link case is not checked", err)
	} else if _, ok, err := FirstOwnRecord(linked); err == nil || ok {
		t.Errorf("a linked day file: ok %v, err %v", ok, err)
	}

	// An events folder that cannot be listed.
	other := e.site(t, "bbbbbbbbbbbbbbbbbbbbbbbbbb")
	touch(t, eventsDir(other), "not a folder", now)
	if _, ok, err := FirstOwnRecord(other); err == nil || ok {
		t.Errorf("an unlistable events folder: ok %v, err %v", ok, err)
	}

	// A day file that is not regular, but past the first own record, is
	// never read: the scan stops before it.
	late := e.site(t, "cccccccccccccccccccccccccc")
	touch(t, DayFile(late, "2026-03-01"), `{"t":`+itoa(umamiAt(t, "2026-03-01T09:00:00Z").UnixMilli())+`,"k":"p","s":"srv"}`+"\n", now)
	if err := os.MkdirAll(DayFile(late, "2026-03-09"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := FirstOwnRecord(late); err != nil || !ok {
		t.Errorf("a folder after the first own record: ok %v, err %v", ok, err)
	}
}

// The recorder and an import share a day: the recorder's lines survive the
// rewrite as they were, imported lines count against neither cap, and the
// recorder counts its own lines right after the file changed under it.
func TestRecorderAndImportShareADay(t *testing.T) {
	e := newRecorderEnv(t)
	dir := e.site(t, testView)
	r := e.recorder(3)
	for range 2 {
		r.Add(pageHit())
	}
	r.Flush()
	day := "2026-11-04"
	before := readLines(t, DayFile(dir, day))

	var export strings.Builder
	at := umamiAt(t, "2026-11-04T08:00:00Z")
	for i := range 5 {
		export.WriteString(umamiLine(t, umamiView(at.Add(time.Duration(i)*time.Minute), "u", "/import")))
	}
	if _, err := importInto(t, e, dir, export.String(), time.Time{}, 0, umamiAt(t, "2026-11-05T00:00:00Z")); err != nil {
		t.Fatal(err)
	}
	if srv, js, _, err := countDay(DayFile(dir, day)); err != nil || srv != 2 || js != 0 {
		t.Fatalf("after the import: srv %d, js %d, %v; want 2, 0", srv, js, err)
	}

	for range 2 {
		r.Add(pageHit())
	}
	r.Flush()
	if n := sourceCounts(t, dir, day); n["srv"] != 3 || n["um"] != 5 {
		t.Errorf("records = %v; want srv 3 (the cap), um 5", n)
	}
	got := strings.Join(readLines(t, DayFile(dir, day)), "\n")
	for _, l := range before {
		if !strings.Contains(got, l) {
			t.Errorf("a recorded line did not survive the import: %s", l)
		}
	}
	if b, err := os.ReadFile(overFile(dir, day)); err != nil || strings.TrimSpace(string(b)) != "1" {
		t.Errorf("over the cap = %q %v, want 1", b, err)
	}
}

// A site that moved its zone east can hold an earlier own record in a later
// day file; the scan stops only where no later file can.
func TestFirstOwnRecordAcrossAZoneChange(t *testing.T) {
	e := newRecorderEnv(t)
	dir := e.site(t, testView)
	now := umamiAt(t, "2026-03-10T12:00:00Z")
	late := umamiAt(t, "2026-03-05T23:00:00Z")  // 2026-03-05 in UTC
	early := umamiAt(t, "2026-03-05T11:00:00Z") // 2026-03-06 01:00 in UTC+14
	touch(t, DayFile(dir, "2026-03-05"), `{"t":`+itoa(late.UnixMilli())+`,"k":"p","s":"srv"}`+"\n", now)
	touch(t, DayFile(dir, "2026-03-06"), `{"t":`+itoa(early.UnixMilli())+`,"k":"p","s":"js"}`+"\n", now)
	touch(t, DayFile(dir, "2026-03-08"), `{"t":`+itoa(early.Add(-time.Hour).UnixMilli())+`,"k":"p","s":"srv"}`+"\n", now) // cannot be: past any zone
	if got, ok, err := FirstOwnRecord(dir); err != nil || !ok || !got.Equal(early) {
		t.Errorf("FirstOwnRecord = %v %v %v, want %v", got, ok, err, early)
	}
}

func TestImportSkipsBeyondRetention(t *testing.T) {
	e := newRecorderEnv(t)
	dir := e.site(t, testView)
	now := umamiAt(t, "2026-03-31T12:00:00Z")
	export := umamiLine(t, umamiView(umamiAt(t, "2026-02-20T10:00:00Z"), "a", "/")) +
		umamiLine(t, umamiView(umamiAt(t, "2026-03-01T10:00:00Z"), "a", "/")) + // the last day 30 days keep
		umamiLine(t, umamiView(umamiAt(t, "2026-03-20T10:00:00Z"), "b", "/"))
	rep, err := importInto(t, e, dir, export, time.Time{}, 30, now)
	if err != nil {
		t.Fatal(err)
	}
	if rep.SkippedRetention != 1 || rep.Records != 2 || rep.From != "2026-03-01" || rep.To != "2026-03-20" {
		t.Errorf("report = %+v", rep)
	}
	if _, err := os.Stat(DayFile(dir, "2026-02-20")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a day past the retention was written: %v", err)
	}
}

// An export that is not sorted by time imports as the sorted one does, even
// when a day comes back after it was written.
func TestImportUnsortedExport(t *testing.T) {
	now := umamiAt(t, "2026-03-10T12:00:00Z")
	d1, d2 := umamiAt(t, "2026-03-01T09:00:00Z"), umamiAt(t, "2026-03-02T09:00:00Z")
	a1, a2 := umamiView(d1, "a", "/1a"), umamiView(d1.Add(time.Minute), "a", "/1b")
	b1, b2 := umamiView(d2, "b", "/2a"), umamiView(d2.Add(time.Minute), "b", "/2b")
	// writes counts the day files written: once a date goes backwards, the
	// rest is held to the end, so no date is written more than twice.
	var writes int
	run := func(order ...UmamiEvent) (map[string]string, ImportReport) {
		e := newRecorderEnv(t)
		dir := e.site(t, testView)
		touch(t, DayFile(dir, "2026-03-01"), `{"t":1,"k":"p","s":"um","v":"old"}`+"\n", now)
		var b strings.Builder
		for _, ev := range order {
			b.WriteString(umamiLine(t, ev))
		}
		writes = 0
		rep, err := Import(dir, time.UTC, nil, nil, strings.NewReader(b.String()), time.Time{}, 0, now,
			func(fn func(string) error) (bool, error) { writes++; return e.withSite(testView, fn) })
		if err != nil {
			t.Fatal(err)
		}
		return dayFiles(t, dir), rep
	}
	want, wantRep := run(a1, a2, b1, b2)
	if writes != 2 {
		t.Errorf("a sorted export wrote %d days, want 2", writes)
	}
	a3, b3 := umamiView(d1.Add(2*time.Minute), "a", "/1c"), umamiView(d2.Add(2*time.Minute), "b", "/2c")
	wantMore, _ := run(a1, a2, a3, b1, b2, b3)
	for _, c := range []struct {
		order  []UmamiEvent
		want   map[string]string
		writes int
	}{
		{[]UmamiEvent{a1, b1, a2, b2}, want, 3}, // day 1 written, then back
		{[]UmamiEvent{b1, a1, b2, a2}, want, 2},
		{[]UmamiEvent{b1, b2, a1, a2}, want, 2},
		{[]UmamiEvent{a1, b1, a2, b2, a3, b3}, wantMore, 3}, // back and forth
	} {
		got, rep := run(c.order...)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("unsorted import gave\n%v\nwant\n%v", got, c.want)
		}
		if len(c.order) == 4 && !reflect.DeepEqual(rep, wantRep) {
			t.Errorf("report = %+v, want %+v", rep, wantRep)
		}
		if writes != c.writes {
			t.Errorf("%d day writes, want %d", writes, c.writes)
		}
	}
}

// A line as the runbook's SQL makes Umami 3's Postgres write it (rendered
// there from literals): nulls, a numeric(19,4), microseconds, typed data.
const postgresLine = `{"created_at" : "2026-10-17T10:15:30.123456+00:00", "event_type" : 2, "event_name" : "anfrage", "hostname" : "www.morgenfertig.at", "url_path" : "/kontakt", "url_query" : "utm_source=meta&gclid=XYZ", "referrer_domain" : "morgenfertig.at", "referrer_path" : "/", "page_title" : "Kontakt \"jetzt\" \\ sofort", "tag" : null, "visit_id" : "a0e1b2c3-0000-4000-8000-000000000001", "session_id" : "5e55e55e-0000-4000-8000-000000000001", "utm_source" : "meta", "utm_medium" : null, "utm_campaign" : null, "utm_content" : null, "utm_term" : null, "gclid" : "XYZ", "fbclid" : null, "msclkid" : null, "ttclid" : null, "li_fat_id" : null, "twclid" : null, "browser" : "crios", "os" : "iOS", "device" : "mobile", "screen" : "390x844", "language" : "de-AT", "country" : "AT", "region" : "AT-9", "city" : "Vienna", "event_data" : { "budget" : 5000.0000, "eilig" : true, "seite" : "/kontakt", "am" : "2026-10-17T10:00:00+00:00" }}`

func TestImportReadsPostgresOutput(t *testing.T) {
	e := newRecorderEnv(t)
	dir := e.site(t, testView)
	rep, err := importInto(t, e, dir, postgresLine+"\n", time.Time{}, 0, umamiAt(t, "2026-11-05T12:00:00Z"))
	if err != nil || rep.Records != 1 {
		t.Fatalf("import: %+v %v", rep, err)
	}
	recs, _ := ReadDay(dir, "2026-10-17")
	if len(recs) != 1 {
		t.Fatalf("day = %+v", recs)
	}
	r := recs[0]
	r.V = ""
	want := Record{
		T: umamiAt(t, "2026-10-17T10:15:30.123Z").UnixMilli(), K: "e", S: "um", A: "www.morgenfertig.at", P: "/kontakt",
		Ti: `Kontakt "jetzt" \ sofort`, Q: map[string]string{"utm_source": "meta"}, Ck: []string{"gclid"},
		N: "anfrage", D: map[string]any{"budget": 5000.0, "eilig": true, "seite": "/kontakt", "am": "2026-10-17T10:00:00+00:00"},
		Vi: "a0e1b2c3-0000-4000-8000-000000000001", B: "Chrome (iOS)", O: "iOS", Dv: "mobile", Sc: "390x844", L: "de-AT",
		C: "AT", Rg: "AT-9", Ct: "Vienna",
	}
	if !reflect.DeepEqual(r, want) {
		t.Errorf("record =\n%+v\nwant\n%+v", r, want)
	}
	if b, _ := os.ReadFile(DayFile(dir, "2026-10-17")); bytes.Contains(b, []byte("XYZ")) {
		t.Error("the click id's value was stored")
	}
}

func TestImportNeverCreatesADeletedSite(t *testing.T) {
	e := newRecorderEnv(t)
	dir := filepath.Join(e.sites, testView) // no meta.json: deleted
	export := umamiLine(t, umamiView(umamiAt(t, "2026-03-01T09:00:00Z"), "a", "/"))
	_, err := importInto(t, e, dir, export, time.Time{}, 0, umamiAt(t, "2026-03-10T12:00:00Z"))
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("err = %v, want store.ErrNotFound", err)
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the deleted site's folder was created: %v", err)
	}
}

func TestImportRefusesABadLine(t *testing.T) {
	e := newRecorderEnv(t)
	dir := e.site(t, testView)
	now := umamiAt(t, "2026-03-10T12:00:00Z")
	good := umamiLine(t, umamiView(umamiAt(t, "2026-03-01T09:00:00Z"), "a", "/"))
	for name, export := range map[string]string{
		"not json":     good + "{nope\n",
		"no time":      good + `{"event_type":1,"url_path":"/"}` + "\n",
		"unknown type": good + `{"created_at":"2026-03-01T10:00:00Z","event_type":3,"url_path":"/"}` + "\n",
	} {
		_, err := importInto(t, e, dir, export, time.Time{}, 0, now)
		if !errors.Is(err, ErrBadExport) || !strings.Contains(err.Error(), "line 2") {
			t.Errorf("%s: err = %v, want ErrBadExport naming line 2", name, err)
		}
	}
}

func TestAddImported(t *testing.T) {
	dir := t.TempDir()
	for _, r := range []ImportedRange{
		{"2026-01-01", "2026-01-31"},
		{"2026-03-01", "2026-03-31"},
		{"2026-01-01", "2026-01-31"}, // the same import again
		{"2026-02-15", "2026-03-05"}, // overlaps the March range only
	} {
		if err := AddImported(dir, r); err != nil {
			t.Fatal(err)
		}
	}
	got, err := Imported(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := []ImportedRange{{"2026-01-01", "2026-01-31"}, {"2026-02-15", "2026-03-31"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("imported = %v, want %v", got, want)
	}
	if es, _ := os.ReadDir(filepath.Join(dir, "analytics")); len(es) != 1 {
		t.Errorf("analytics/ holds %d entries, want imported.json alone", len(es))
	}
}

// The operator's funnels and goals in Umami (morgenfertig).
var operatorReports = []UmamiReport{
	{Type: "funnel", Name: "Landingpage → Anfrage", Parameters: json.RawMessage(`{"steps":[{"type":"path","value":"/app-entwickeln-lassen/*"},{"type":"event","value":"anfrage-start"},{"type":"event","value":"anfrage"}],"window":60}`)},
	{Type: "funnel", Name: "Formular begonnen → gesendet", Parameters: json.RawMessage(`{"steps":[{"type":"event","value":"anfrage-start"},{"type":"event","value":"anfrage"}],"window":60}`)},
	{Type: "goal", Name: "Anfrage gesendet", Parameters: json.RawMessage(`{"type":"event","value":"anfrage"}`)},
	{Type: "goal", Name: "Formular begonnen", Parameters: json.RawMessage(`{"type":"event","value":"anfrage-start"}`)},
	{Type: "retention", Name: "Wiederkehr", Parameters: json.RawMessage(`{}`)},
}

func TestImportReportsMergeByName(t *testing.T) {
	mine := store.AnalyticsGoal{ID: "mine", Name: "Anfrage gesendet", Type: "path", Value: "/danke"}
	start := store.Analytics{Enabled: true, Mode: "script", Query: []string{"ref"}, Goals: []store.AnalyticsGoal{mine}}
	out, funnels, goals, skipped := MergeUmamiReports(start, operatorReports)

	if !reflect.DeepEqual(funnels, []string{"Landingpage → Anfrage", "Formular begonnen → gesendet"}) {
		t.Errorf("funnels added = %q", funnels)
	}
	if !reflect.DeepEqual(goals, []string{"Formular begonnen"}) {
		t.Errorf("goals added = %q", goals)
	}
	if len(skipped) != 2 || !strings.HasPrefix(skipped[0], "Anfrage gesendet (goal)") || !strings.HasPrefix(skipped[1], "Wiederkehr (retention)") {
		t.Errorf("skipped = %q", skipped)
	}
	if len(out.Funnels) != 2 || len(out.Goals) != 2 || out.Goals[0] != mine {
		t.Fatalf("settings = %+v", out)
	}
	f := out.Funnels[0]
	if f.Name != "Landingpage → Anfrage" || f.WindowMinutes != 60 || !reflect.DeepEqual(f.Steps, []store.AnalyticsStep{
		{Type: "path", Value: "/app-entwickeln-lassen/*"}, {Type: "event", Value: "anfrage-start"}, {Type: "event", Value: "anfrage"},
	}) {
		t.Errorf("funnel = %+v", f)
	}
	if g := out.Goals[1]; g.Name != "Formular begonnen" || g.Type != "event" || g.Value != "anfrage-start" {
		t.Errorf("goal = %+v", g)
	}
	for _, id := range []string{out.Funnels[0].ID, out.Funnels[1].ID, out.Goals[1].ID} {
		if !strings.HasPrefix(id, "um-") || len(id) != 11 {
			t.Errorf("id %q, want um- and 8 hex", id)
		}
	}
	if out.Funnels[0].ID == out.Funnels[1].ID {
		t.Error("two funnels share an id")
	}
	if !out.Enabled || out.Mode != "script" || !reflect.DeepEqual(out.Query, []string{"ref"}) {
		t.Errorf("the other settings changed: %+v", out)
	}
	if _, err := out.Validate(nil); err != nil {
		t.Errorf("merged settings do not validate: %v", err)
	}
	if len(start.Goals) != 1 {
		t.Error("the settings passed in were changed")
	}

	// Again: every name exists now, nothing is added.
	again, funnels, goals, skipped := MergeUmamiReports(out, operatorReports)
	if len(funnels)+len(goals) != 0 || len(skipped) != 5 || !reflect.DeepEqual(again, out) {
		t.Errorf("second merge added %q %q, skipped %q", funnels, goals, skipped)
	}

	// The limits hold: a site with 20 funnels gets none.
	full := store.Analytics{}
	for i := range 20 {
		full.Funnels = append(full.Funnels, store.AnalyticsFunnel{ID: "f" + itoa(int64(i)), Name: "F" + itoa(int64(i)), WindowMinutes: 60,
			Steps: []store.AnalyticsStep{{Type: "event", Value: "a"}, {Type: "event", Value: "b"}}})
	}
	if _, funnels, _, skipped := MergeUmamiReports(full, operatorReports[:2]); len(funnels) != 0 || len(skipped) != 2 {
		t.Errorf("over the limit: added %q, skipped %q", funnels, skipped)
	}
	// A report Validate refuses is skipped with the reason.
	bad := []UmamiReport{{Type: "funnel", Name: "Kurz", Parameters: json.RawMessage(`{"steps":[{"type":"event","value":"a"}],"window":60}`)}}
	if _, funnels, _, skipped := MergeUmamiReports(store.Analytics{}, bad); len(funnels) != 0 || len(skipped) != 1 || !strings.Contains(skipped[0], "steps") {
		t.Errorf("one-step funnel: added %q, skipped %q", funnels, skipped)
	}
}
