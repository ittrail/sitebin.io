package httpapi

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ittrail/sitebin.io/internal/analytics"
	"github.com/ittrail/sitebin.io/internal/store"
)

// The Umami import through the internal listener. See "Umami import, per
// site" in docs/superpowers/specs/2026-11-04-site-analytics-design.md.

// importPost sends the parts, in order, as the CLI does: events and reports
// as files, the rest as fields.
func (e *env) importPost(t *testing.T, parts ...[2]string) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for _, p := range parts {
		if p[0] == "events" || p[0] == "reports" {
			w, err := mw.CreateFormFile(p[0], p[0]+".json")
			if err != nil {
				t.Fatal(err)
			}
			w.Write([]byte(p[1]))
			continue
		}
		mw.WriteField(p[0], p[1])
	}
	mw.Close()
	req := httptest.NewRequest("POST", "/internal/analytics/import", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	return e.internal(t, req)
}

func umamiExport(t *testing.T, evs ...analytics.UmamiEvent) string {
	t.Helper()
	var b strings.Builder
	for _, ev := range evs {
		line, err := json.Marshal(ev)
		if err != nil {
			t.Fatal(err)
		}
		b.Write(line)
		b.WriteByte('\n')
	}
	return b.String()
}

func umamiPage(at time.Time, session, path string) analytics.UmamiEvent {
	return analytics.UmamiEvent{CreatedAt: at, EventType: 1, Hostname: "morgenfertig.at", URLPath: path,
		SessionID: session, VisitID: "visit-" + session, Browser: "chrome", OS: "Windows 10", Device: "desktop", Country: "AT"}
}

const operatorReportsJSON = `[
 {"type":"funnel","name":"Landingpage → Anfrage","parameters":{"steps":[{"type":"path","value":"/app-entwickeln-lassen/*"},{"type":"event","value":"anfrage-start"},{"type":"event","value":"anfrage"}],"window":60}},
 {"type":"funnel","name":"Formular begonnen → gesendet","parameters":{"steps":[{"type":"event","value":"anfrage-start"},{"type":"event","value":"anfrage"}],"window":60}},
 {"type":"goal","name":"Anfrage gesendet","parameters":{"type":"event","value":"anfrage"}},
 {"type":"goal","name":"Formular begonnen","parameters":{"type":"event","value":"anfrage-start"}}
]`

func TestInternalImportRoute(t *testing.T) {
	e := newEnv(t, nil)
	c := e.createSite(t, nil, map[string]string{"index.html": "x"})
	edit := editIDFrom(t, c.EditURL)
	site, _ := e.st.ByViewID(c.ID)
	gen := e.api.generatedAddress(site)
	if w := e.analyticsCall(t, "PUT", edit, c.EditPassword, "", map[string]any{"enabled": true, "query": []string{"ref"}}); w.Code != 200 {
		t.Fatalf("PUT settings = %d %s", w.Code, w.Body)
	}
	now := time.Now()
	// Sitebin has measured since yesterday noon: the import stops there.
	own := daysAgo(now, 1, 12, 0)
	seedAnalytics(t, site, dateOf(own), pageView(own, "own", "/"))

	first := umamiPage(daysAgo(now, 3, 9, 0), "s1", "/app-entwickeln-lassen/ios")
	first.URLQuery, first.Gclid = "ref=newsletter&gclid=SECRET", "SECRET"
	first.ReferrerDomain, first.ReferrerPath = gen, "/" // the site's own address
	start := umamiPage(daysAgo(now, 3, 9, 5), "s1", "/kontakt")
	start.EventType, start.EventName, start.Data = 2, "anfrage-start", map[string]any{"seite": "/kontakt"}
	second := umamiPage(daysAgo(now, 2, 10, 0), "s2", "/")
	second.ReferrerDomain, second.ReferrerPath = "google.com", "/"
	late := umamiPage(daysAgo(now, 1, 13, 0), "s3", "/") // Sitebin counted this one itself
	export := umamiExport(t, first, start, second, late)

	w := e.importPost(t, [2]string{"site", site.ViewID}, [2]string{"reports", operatorReportsJSON}, [2]string{"events", export})
	var rep analytics.ImportReport
	decodeInto(t, w, &rep)
	want := analytics.ImportReport{
		Days: 2, Records: 3, SkippedAfterCut: 1,
		From: dateOf(daysAgo(now, 3, 0, 0)), To: dateOf(daysAgo(now, 2, 0, 0)),
		Funnels:        []string{"Landingpage → Anfrage", "Formular begonnen → gesendet"},
		Goals:          []string{"Anfrage gesendet", "Formular begonnen"},
		SkippedReports: []string{},
	}
	if !reflect.DeepEqual(rep, want) {
		t.Errorf("report = %+v\nwant %+v", rep, want)
	}

	recs, err := analytics.ReadDay(site.Dir(), dateOf(first.CreatedAt))
	if err != nil || len(recs) != 2 {
		t.Fatalf("day = %+v %v", recs, err)
	}
	if r := recs[0]; r.S != "um" || r.R != "" || !reflect.DeepEqual(r.Q, map[string]string{"ref": "newsletter"}) || !reflect.DeepEqual(r.Ck, []string{"gclid"}) {
		t.Errorf("imported view = %+v", r)
	}
	if b, _ := os.ReadFile(analytics.DayFile(site.Dir(), dateOf(first.CreatedAt))); bytes.Contains(b, []byte("SECRET")) {
		t.Error("a click id's value was stored")
	}

	// The state lists the range and the settings carry the reports; what
	// the owner set stays.
	s := e.analyticsState(t, edit, c.EditPassword)
	if !reflect.DeepEqual(s.Imported, []analytics.ImportedRange{{From: want.From, To: want.To}}) {
		t.Errorf("imported = %v", s.Imported)
	}
	if len(s.Settings.Funnels) != 2 || len(s.Settings.Goals) != 2 || !s.Settings.Enabled || !reflect.DeepEqual(s.Settings.Query, []string{"ref"}) {
		t.Errorf("settings = %+v", s.Settings)
	}

	// The dashboard counts the imported days beside Sitebin's own.
	var ov analytics.Overview
	decodeInto(t, e.analyticsCall(t, "GET", edit, c.EditPassword, "/overview?"+rangeOf(now, 3, 1), nil), &ov)
	if ov.Totals.Views != 3 || ov.Totals.Visitors != 3 {
		t.Errorf("totals = %+v, want 3 views by 3 visitors", ov.Totals)
	}
	var tab struct {
		Rows []analytics.Row `json:"rows"`
	}
	decodeInto(t, e.analyticsCall(t, "GET", edit, c.EditPassword, "/table?dim=referrer&"+rangeOf(now, 3, 1), nil), &tab)
	refs := map[string]int{}
	for _, row := range tab.Rows {
		refs[row.Value] = row.Views
	}
	if want := map[string]int{"(direct)": 2, "google.com/": 1}; !reflect.DeepEqual(refs, want) {
		t.Errorf("referrers = %v, want %v (never the site's own address)", refs, want)
	}

	// Again: the same files, the same range once, and the reports exist.
	day := dateOf(first.CreatedAt)
	before, _ := os.ReadFile(analytics.DayFile(site.Dir(), day))
	w = e.importPost(t, [2]string{"site", site.ViewID}, [2]string{"reports", operatorReportsJSON}, [2]string{"events", export})
	decodeInto(t, w, &rep)
	if after, _ := os.ReadFile(analytics.DayFile(site.Dir(), day)); !bytes.Equal(before, after) {
		t.Error("a second import changed the day file")
	}
	if len(rep.Funnels)+len(rep.Goals) != 0 || len(rep.SkippedReports) != 4 {
		t.Errorf("second import's reports: %+v", rep)
	}
	if s := e.analyticsState(t, edit, c.EditPassword); len(s.Imported) != 1 || len(s.Settings.Funnels) != 2 {
		t.Errorf("after a second import: imported %v, %d funnels", s.Imported, len(s.Settings.Funnels))
	}

	// The route is the internal listener's alone.
	req := httptest.NewRequest("POST", "/internal/analytics/import", strings.NewReader(""))
	if w := e.public(t, req); w.Code == 200 {
		t.Error("the public listener answers the import")
	}
}

// until moves the cut earlier, never later.
func TestInternalImportUntil(t *testing.T) {
	e := newEnv(t, nil)
	c := e.createSite(t, nil, map[string]string{"index.html": "x"})
	site, _ := e.st.ByViewID(c.ID)
	now := time.Now()
	own := daysAgo(now, 1, 12, 0)
	seedAnalytics(t, site, dateOf(own), pageView(own, "own", "/"))
	export := umamiExport(t, umamiPage(daysAgo(now, 3, 9, 0), "a", "/"), umamiPage(daysAgo(now, 2, 9, 0), "b", "/"), umamiPage(daysAgo(now, 1, 13, 0), "c", "/"))

	for until, wantRecords := range map[time.Time]int{
		daysAgo(now, 2, 0, 0): 1, // earlier than the first own record
		now:                   2, // later: the first own record still cuts
	} {
		w := e.importPost(t, [2]string{"site", site.ViewID}, [2]string{"until", until.Format(time.RFC3339)}, [2]string{"events", export})
		var rep analytics.ImportReport
		decodeInto(t, w, &rep)
		if rep.Records != wantRecords || rep.SkippedAfterCut != 3-wantRecords {
			t.Errorf("until %v: report = %+v", until, rep)
		}
	}
}

func TestInternalImportRefusals(t *testing.T) {
	e := newEnv(t, nil)
	c := e.createSite(t, nil, map[string]string{"index.html": "x"})
	site, _ := e.st.ByViewID(c.ID)
	export := umamiExport(t, umamiPage(daysAgo(time.Now(), 3, 9, 0), "a", "/"))

	for name, tc := range map[string]struct {
		parts [][2]string
		code  int
	}{
		"bad id":        {[][2]string{{"site", "../../etc"}, {"events", export}}, 400},
		"unknown site":  {[][2]string{{"site", strings.Repeat("a", 26)}, {"events", export}}, 404},
		"no events":     {[][2]string{{"site", site.ViewID}}, 400},
		"events first":  {[][2]string{{"events", export}, {"site", site.ViewID}}, 400},
		"bad until":     {[][2]string{{"site", site.ViewID}, {"until", "yesterday"}, {"events", export}}, 400},
		"bad reports":   {[][2]string{{"site", site.ViewID}, {"reports", "{"}, {"events", export}}, 400},
		"bad export":    {[][2]string{{"site", site.ViewID}, {"events", export + "{nope\n"}}, 400},
		"unknown field": {[][2]string{{"site", site.ViewID}, {"force", "1"}, {"events", export}}, 400},
		"not multipart": {nil, 400},
	} {
		var w *httptest.ResponseRecorder
		if tc.parts == nil {
			w = e.internal(t, httptest.NewRequest("POST", "/internal/analytics/import", strings.NewReader("{}")))
		} else {
			w = e.importPost(t, tc.parts...)
		}
		if w.Code != tc.code {
			t.Errorf("%s: %d %s, want %d", name, w.Code, w.Body, tc.code)
		}
	}
	if _, err := os.Stat(analytics.DayFile(site.Dir(), dateOf(daysAgo(time.Now(), 3, 0, 0)))); err == nil {
		t.Error("a refused import wrote a day")
	}

	// One import into a site at a time.
	e.api.imports.Store(site.ViewID, struct{}{})
	if w := e.importPost(t, [2]string{"site", site.ViewID}, [2]string{"events", export}); w.Code != 409 {
		t.Errorf("a second import: %d %s, want 409", w.Code, w.Body)
	}
	e.api.imports.Delete(site.ViewID)

	// A locked site is an evidence hold: nothing is imported into it.
	if _, err := e.st.SetLock(site, &store.SiteLock{By: store.LockByAdmin}); err != nil {
		t.Fatal(err)
	}
	w := e.importPost(t, [2]string{"site", site.ViewID}, [2]string{"events", export})
	if w.Code != 409 || refusalReason(t, w) != "locked" {
		t.Errorf("locked site: %d %s, want 409 locked", w.Code, w.Body)
	}
	if _, err := os.Stat(filepath.Join(site.Dir(), "analytics")); err == nil {
		t.Error("an import wrote into a locked site")
	}
}

func refusalReason(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Reason string `json:"reason"`
	}
	json.Unmarshal(w.Body.Bytes(), &body)
	return body.Reason
}

// The instance switch off refuses an import; a plan without analytics does
// not, since the operator moves sites and what is stored stays readable.
func TestInternalImportGate(t *testing.T) {
	off := newEnv(t, map[string]string{"SITEBIN_ANALYTICS": "false"})
	c := off.createSite(t, nil, map[string]string{"index.html": "x"})
	site, _ := off.st.ByViewID(c.ID)
	export := umamiExport(t, umamiPage(daysAgo(time.Now(), 3, 9, 0), "a", "/"))
	w := off.importPost(t, [2]string{"site", site.ViewID}, [2]string{"events", export})
	if w.Code != 409 || refusalReason(t, w) != "instance" {
		t.Errorf("instance off: %d %s, want 409 instance", w.Code, w.Body)
	}
	if _, err := os.Stat(filepath.Join(site.Dir(), "analytics")); err == nil {
		t.Error("an import wrote with the instance switch off")
	}

	e := newEnv(t, nil)
	c = e.createSite(t, nil, map[string]string{"index.html": "x"})
	site, _ = e.st.ByViewID(c.ID)
	no := false
	if err := e.st.SetQuotaAnalytics(site, &no); err != nil {
		t.Fatal(err)
	}
	if reason := e.api.analyticsReason(site); reason != analyticsReasonPlan {
		t.Fatalf("reason = %q, want plan", reason)
	}
	var rep analytics.ImportReport
	decodeInto(t, e.importPost(t, [2]string{"site", site.ViewID}, [2]string{"events", export}), &rep)
	if rep.Records != 1 {
		t.Errorf("plan without analytics: report %+v, want the record imported", rep)
	}
}

// A day file that cannot be read hides where Sitebin's own records begin:
// nothing is imported.
func TestInternalImportStopsWhenTheFirstOwnRecordIsUnknown(t *testing.T) {
	e := newEnv(t, nil)
	c := e.createSite(t, nil, map[string]string{"index.html": "x"})
	site, _ := e.st.ByViewID(c.ID)
	now := time.Now()
	if err := os.MkdirAll(analytics.DayFile(site.Dir(), dateOf(daysAgo(now, 5, 0, 0))), 0o755); err != nil {
		t.Fatal(err)
	}
	at := daysAgo(now, 3, 9, 0)
	w := e.importPost(t, [2]string{"site", site.ViewID}, [2]string{"events", umamiExport(t, umamiPage(at, "a", "/"))})
	if w.Code != 500 || !strings.Contains(w.Body.String(), "nothing imported") {
		t.Errorf("unreadable day file: %d %s, want 500", w.Code, w.Body)
	}
	if _, err := os.Stat(analytics.DayFile(site.Dir(), dateOf(at))); err == nil {
		t.Error("a day was imported although the first own record was unknown")
	}
	if imported, _ := analytics.Imported(site.Dir()); len(imported) != 0 {
		t.Errorf("imported = %v", imported)
	}
}

// A lock that comes while the export streams is seen where meta is written:
// the funnels and goals are not added.
func TestInternalImportLockedDuringTheImport(t *testing.T) {
	e := newEnv(t, nil)
	c := e.createSite(t, nil, map[string]string{"index.html": "x"})
	site, _ := e.st.ByViewID(c.ID)
	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	req := httptest.NewRequest("POST", "/internal/analytics/import", pr)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	done := make(chan *httptest.ResponseRecorder)
	go func() {
		w := httptest.NewRecorder()
		e.api.Internal().ServeHTTP(w, req)
		pr.CloseWithError(io.ErrUnexpectedEOF) // ends a body the handler stopped reading
		done <- w
	}()

	mw.WriteField("site", site.ViewID)
	rw, _ := mw.CreateFormFile("reports", "reports.json")
	rw.Write([]byte(operatorReportsJSON))
	ew, _ := mw.CreateFormFile("events", "events.jsonl")
	line := umamiExport(t, umamiPage(daysAgo(time.Now(), 3, 9, 0), "a", "/"))
	// Far more than any buffer holds: once this is written, the handler is
	// importing.
	if _, err := io.WriteString(ew, strings.Repeat(line, 20000)); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.SetLock(site, &store.SiteLock{By: store.LockByAdmin}); err != nil {
		t.Fatal(err)
	}
	mw.Close()
	pw.Close()
	w := <-done

	if w.Code != 409 || refusalReason(t, w) != "locked" {
		t.Errorf("locked during the import: %d %s, want 409 locked", w.Code, w.Body)
	}
	if got, _ := e.st.ByViewID(c.ID); got.Meta.Analytics != nil {
		t.Errorf("settings were written into a locked site: %+v", got.Meta.Analytics)
	}
}

// A day the import rewrites is dropped from the engine: its summary goes
// with the import, before any query could read it.
func TestInternalImportInvalidatesTheEngine(t *testing.T) {
	e := newEnv(t, nil)
	c := e.createSite(t, nil, map[string]string{"index.html": "x"})
	edit := editIDFrom(t, c.EditURL)
	site, _ := e.st.ByViewID(c.ID)
	now := time.Now()
	at := daysAgo(now, 3, 9, 0)
	day := dateOf(at)
	seedAnalytics(t, site, day, analytics.Record{T: at.UnixMilli(), K: "p", S: "um", V: "old", P: "/"})
	var ov analytics.Overview
	decodeInto(t, e.analyticsCall(t, "GET", edit, c.EditPassword, "/overview?"+rangeOf(now, 3, 3), nil), &ov)
	sum := strings.TrimSuffix(analytics.DayFile(site.Dir(), day), ".jsonl") + ".sum.json"
	if _, err := os.Stat(sum); err != nil {
		t.Fatalf("no summary to drop: %v", err)
	}
	export := umamiExport(t, umamiPage(at, "a", "/"), umamiPage(at.Add(time.Minute), "b", "/x"))
	decodeInto(t, e.importPost(t, [2]string{"site", site.ViewID}, [2]string{"events", export}), new(analytics.ImportReport))
	if _, err := os.Stat(sum); err == nil {
		t.Error("the rewritten day's summary stayed")
	}
	decodeInto(t, e.analyticsCall(t, "GET", edit, c.EditPassword, "/overview?"+rangeOf(now, 3, 3), nil), &ov)
	if ov.Totals.Views != 2 {
		t.Errorf("views = %d, want the import's 2", ov.Totals.Views)
	}
}
