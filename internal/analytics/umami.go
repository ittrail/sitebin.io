package analytics

import (
	"bufio"
	"bytes"
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/ittrail/sitebin.io/internal/store"
)

// Importing a site's history from Umami. The running server does it (the
// recorder appends under an in-process lock), so it lives here as functions
// the internal import route calls. See "Umami import, per site" in
// docs/superpowers/specs/2026-11-04-site-analytics-design.md and the runbook
// in docs/analytics-umami-import.md.

// UmamiEvent is one line of the export: an Umami website_event joined with
// its session and its event_data (key → value, typed by data_type).
type UmamiEvent struct {
	CreatedAt      time.Time      `json:"created_at"`
	EventType      int            `json:"event_type"` // 1 page view, 2 custom event
	EventName      string         `json:"event_name"`
	Hostname       string         `json:"hostname"`
	URLPath        string         `json:"url_path"`
	URLQuery       string         `json:"url_query"`
	ReferrerDomain string         `json:"referrer_domain"`
	ReferrerPath   string         `json:"referrer_path"`
	PageTitle      string         `json:"page_title"`
	Tag            string         `json:"tag"`
	VisitID        string         `json:"visit_id"`
	SessionID      string         `json:"session_id"`
	UTMSource      string         `json:"utm_source"`
	UTMMedium      string         `json:"utm_medium"`
	UTMCampaign    string         `json:"utm_campaign"`
	UTMContent     string         `json:"utm_content"`
	UTMTerm        string         `json:"utm_term"`
	Gclid          string         `json:"gclid"`
	Fbclid         string         `json:"fbclid"`
	Msclkid        string         `json:"msclkid"`
	Ttclid         string         `json:"ttclid"`
	LiFatID        string         `json:"li_fat_id"`
	Twclid         string         `json:"twclid"`
	Browser        string         `json:"browser"`
	OS             string         `json:"os"`
	Device         string         `json:"device"`
	Screen         string         `json:"screen"`
	Language       string         `json:"language"`
	Country        string         `json:"country"`
	Region         string         `json:"region"`
	City           string         `json:"city"`
	Data           map[string]any `json:"event_data"`
}

// Bounds on the export's fields that have no Clean function of their own.
const (
	maxUmamiHost    = 253 // a DNS name
	maxUmamiVisitID = 64  // Umami's are UUIDs
	maxUmamiPlace   = 100 // region and city
)

// ConvertUmami turns an export line into a record and the local date (in
// loc, nil is UTC) whose day file it belongs in. own are the site's
// addresses: a referrer from one of them, or from the host the event was
// recorded on, is internal and dropped. extraQuery are the site's own query
// names, kept from url_query beside the utm_* columns. Click ids are kept by
// name only, and the visitor id is per Umami session and local day.
//
// rec.K is "" when the line is neither a page view nor an event with a
// usable name: there is nothing to import.
func ConvertUmami(ev UmamiEvent, loc *time.Location, own, extraQuery []string) (date string, rec Record) {
	date = LocalDate(ev.CreatedAt, loc)
	rec = Record{
		T:  ev.CreatedAt.UnixMilli(),
		S:  "um",
		A:  cutRunes(strings.ToLower(strings.TrimSpace(ev.Hostname)), maxUmamiHost),
		P:  CleanPath(ev.URLPath),
		R:  umamiReferrer(ev.ReferrerDomain, ev.ReferrerPath, ev.Hostname, own),
		Ti: CleanTitle(ev.PageTitle),
		Tg: CleanTag(ev.Tag),
		V:  umamiVisitor(ev.SessionID, date),
		Vi: cutRunes(ev.VisitID, maxUmamiVisitID),
		B:  umamiBrowser(ev.Browser, ev.OS),
		O:  umamiOS(ev.OS),
		Dv: umamiDevice(ev.Device),
		Sc: CleanScreen(ev.Screen),
		L:  CleanLanguage(ev.Language),
		C:  umamiCountry(ev.Country),
		Rg: cutRunes(strings.TrimSpace(ev.Region), maxUmamiPlace),
		Ct: cutRunes(strings.TrimSpace(ev.City), maxUmamiPlace),
	}
	rec.Q, rec.Ck = umamiQuery(ev, extraQuery)
	switch ev.EventType {
	case 1:
		rec.K = "p"
	case 2:
		if name, data, ok := CleanEvent(ev.EventName, ev.Data); ok {
			rec.K, rec.N, rec.D = "e", name, data
		}
	}
	return date, rec
}

// umamiVisitor is the first 16 hex characters of
// sha256("umami" || 0 || sessionID || 0 || date): an imported visitor lives
// one local day, as a recorded one does.
func umamiVisitor(sessionID, date string) string {
	h := sha256.New()
	h.Write([]byte("umami"))
	h.Write([]byte{0})
	h.Write([]byte(sessionID))
	h.Write([]byte{0})
	h.Write([]byte(date))
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// umamiReferrer is the referrer as host+path (no query), cut at 200, or ""
// when it is internal: from one of the site's addresses or from the host the
// event was recorded on. Umami stores a referrer's host without its "www.",
// so hosts compare without it.
func umamiReferrer(domain, path, hostname string, own []string) string {
	host := strings.ToLower(strings.TrimSpace(domain))
	if host == "" {
		return ""
	}
	bare := strings.TrimPrefix(host, "www.")
	internal := func(o string) bool { return strings.TrimPrefix(ownHost(o), "www.") == bare }
	if internal(hostname) || slices.ContainsFunc(own, internal) {
		return ""
	}
	if i := strings.IndexAny(path, "?#"); i >= 0 {
		path = path[:i]
	}
	return cutRunes(host+path, maxReferrer)
}

// umamiQuery keeps the utm_* columns, the site's own names from url_query,
// and the names of the click ids present, in a column or in url_query.
func umamiQuery(ev UmamiEvent, extra []string) (map[string]string, []string) {
	q, clicks := KeepQuery(ev.URLQuery, extra)
	for _, c := range [...]struct{ name, v string }{
		{"utm_source", ev.UTMSource}, {"utm_medium", ev.UTMMedium}, {"utm_campaign", ev.UTMCampaign},
		{"utm_term", ev.UTMTerm}, {"utm_content", ev.UTMContent},
	} {
		if c.v != "" {
			if q == nil {
				q = map[string]string{}
			}
			q[c.name] = cutRunes(c.v, maxQueryVal)
		}
	}
	columns := map[string]string{"gclid": ev.Gclid, "fbclid": ev.Fbclid, "msclkid": ev.Msclkid, "ttclid": ev.Ttclid, "li_fat_id": ev.LiFatID, "twclid": ev.Twclid}
	var ck []string
	for _, id := range ClickIDs {
		if columns[id] != "" || slices.Contains(clicks, id) {
			ck = append(ck, id)
		}
	}
	return q, ck
}

// umamiBrowser names an Umami browser key (detect-browser's) as the
// user-agent parser would have named the browser. In-app browsers are an
// Android WebView on Android and Other elsewhere, as the parser sees them.
func umamiBrowser(b, os string) string {
	switch b {
	case "chrome":
		return "Chrome"
	case "crios":
		return "Chrome (iOS)"
	case "chromium-webview":
		return "Android WebView"
	case "edge", "edge-chromium", "edge-ios":
		return "Edge"
	case "firefox":
		return "Firefox"
	case "fxios":
		return "Firefox (iOS)"
	case "safari", "ios":
		return "Safari"
	case "opera", "opera-mini":
		return "Opera"
	case "samsung":
		return "Samsung Internet"
	case "yandexbrowser":
		return "Yandex"
	case "instagram", "facebook":
		if os == "Android OS" {
			return "Android WebView"
		}
	}
	return "Other"
}

// umamiOS names an Umami operating system as the user-agent parser does.
func umamiOS(o string) string {
	switch {
	case o == "Mac OS":
		return "macOS"
	case o == "iOS":
		return "iOS"
	case o == "Android OS":
		return "Android"
	case o == "Linux":
		return "Linux"
	case o == "Chrome OS":
		return "ChromeOS"
	case strings.HasPrefix(o, "Windows"):
		return "Windows"
	}
	return "Other"
}

// umamiDevice keeps a device class Sitebin knows; Umami's are the same four.
func umamiDevice(d string) string {
	switch d {
	case "desktop", "laptop", "tablet", "mobile":
		return d
	}
	return ""
}

// umamiCountry keeps a two-letter country code, upper-cased.
func umamiCountry(c string) string {
	c = strings.ToUpper(strings.TrimSpace(c))
	if len(c) != 2 || c[0] < 'A' || c[0] > 'Z' || c[1] < 'A' || c[1] > 'Z' {
		return ""
	}
	return c
}

// ImportReport is what an import did: the days it wrote and their range
// (local dates), the records it wrote, and what it left out — events at or
// after the cut, days past the retention, and the reports not added.
type ImportReport struct {
	Days             int      `json:"days"`
	Records          int      `json:"records"`
	SkippedAfterCut  int      `json:"skipped_after_cut"`
	SkippedRetention int      `json:"skipped_retention"`
	From             string   `json:"from"`
	To               string   `json:"to"`
	Funnels          []string `json:"funnels"`         // names added to the settings
	Goals            []string `json:"goals"`           // names added to the settings
	SkippedReports   []string `json:"skipped_reports"` // "name (type): why"
}

// ErrBadExport is an export line Import cannot use: not JSON, without a
// time, or neither a page view nor a named event. The runbook's SQL never
// writes one, so the file is not what it produced.
var ErrBadExport = errors.New("analytics: not a line of an Umami export")

// maxExportLine bounds one export line.
const maxExportLine = 4 << 20

// Import writes the events of an Umami export into the site's day files.
// For each local date (in loc) it touches, it rewrites the day file with
// every earlier imported ("um") line removed and the new ones merged in by
// time, so the same export imported twice gives the same files byte for
// byte. The site's own lines ("srv", "js") are kept as they are; a line a
// crash cut short is dropped.
//
// Events at or after cut (zero: none) are skipped and counted: from then on
// Sitebin measured itself. So are days before the retention of
// retentionDays at now (0 keeps every day). The daily cap does not apply.
//
// withSite is store.WithStatsLock for the site whose folder is siteDir: each
// day is read, written to a temp file and renamed under it, and a site
// deleted meanwhile is never created again (the import stops with
// store.ErrNotFound). The export is streamed: sorted by time, as the runbook
// writes it, one day is held at a time; an unsorted one is held until its
// end. On an error the report counts what was written up to it.
func Import(siteDir string, loc *time.Location, own, extraQuery []string, events io.Reader, cut time.Time, retentionDays int, now time.Time, withSite func(func(string) error) (bool, error)) (ImportReport, error) {
	im := &importer{withSite: withSite, sorted: true, pending: map[string][]Record{}, written: map[string]bool{}}
	start := RetentionStart(now, loc, retentionDays)
	sc := bufio.NewScanner(events)
	sc.Buffer(make([]byte, 0, 64<<10), maxExportLine)
	n := 0
	for sc.Scan() {
		n++
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var ev UmamiEvent
		if err := json.Unmarshal(line, &ev); err != nil {
			return im.rep, fmt.Errorf("%w: line %d: %v", ErrBadExport, n, err)
		}
		if ev.CreatedAt.IsZero() {
			return im.rep, fmt.Errorf("%w: line %d has no created_at", ErrBadExport, n)
		}
		date, rec := ConvertUmami(ev, loc, own, extraQuery)
		if rec.K == "" {
			return im.rep, fmt.Errorf("%w: line %d is neither a page view (event_type 1) nor an event (2) with a name of 1 to %d characters", ErrBadExport, n, maxEventName)
		}
		if !cut.IsZero() && !ev.CreatedAt.Before(cut) {
			im.rep.SkippedAfterCut++
			continue
		}
		if date < start {
			im.rep.SkippedRetention++
			continue
		}
		if err := im.add(date, rec); err != nil {
			return im.rep, err
		}
	}
	if err := sc.Err(); err != nil {
		if errors.Is(err, bufio.ErrTooLong) {
			return im.rep, fmt.Errorf("%w: line %d is over %d bytes", ErrBadExport, n+1, maxExportLine)
		}
		return im.rep, err
	}
	dates := make([]string, 0, len(im.pending))
	for d := range im.pending {
		dates = append(dates, d)
	}
	slices.Sort(dates)
	for _, d := range dates {
		if err := im.flush(d); err != nil {
			return im.rep, err
		}
	}
	return im.rep, nil
}

// importer holds an import's pending records by local date. While the
// export comes sorted, only the current date is pending, and it is written
// as the next one starts; once a date goes backwards, everything is held to
// the end.
type importer struct {
	withSite func(func(string) error) (bool, error)
	rep      ImportReport
	sorted   bool
	cur      string
	pending  map[string][]Record
	written  map[string]bool // dates written by this import
}

func (im *importer) add(date string, rec Record) error {
	if im.sorted && im.cur != "" && date != im.cur {
		if date < im.cur {
			im.sorted = false
		} else if err := im.flush(im.cur); err != nil {
			return err
		}
	}
	im.cur = date
	im.pending[date] = append(im.pending[date], rec)
	return nil
}

// flush writes a date's pending records. The first write of a date in this
// import replaces the earlier imports' lines; a second one (an unsorted
// export's date coming back) adds to what the first wrote.
func (im *importer) flush(date string) error {
	recs := im.pending[date]
	delete(im.pending, date)
	add := make([]dayLine, len(recs))
	for i, r := range recs {
		b, err := encodeLine(r)
		if err != nil {
			return err
		}
		add[i] = dayLine{t: r.T, raw: b}
	}
	again := im.written[date]
	found, err := im.withSite(func(dir string) error { return rewriteDay(dir, date, add, !again) })
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("analytics: the site was deleted during the import: %w", store.ErrNotFound)
	}
	im.rep.Records += len(recs)
	if !again {
		im.written[date] = true
		im.rep.Days++
		if im.rep.From == "" || date < im.rep.From {
			im.rep.From = date
		}
		if date > im.rep.To {
			im.rep.To = date
		}
	}
	return nil
}

// dayLine is a day file line as it stands, and its time.
type dayLine struct {
	t   int64
	raw []byte
}

// rewriteDay merges add into a day file, by time and stable (the file's
// lines before the added ones at an equal time), dropping the imported
// lines first when dropImported is set. It runs under the site's stats lock
// and writes through a temp file renamed into place before it returns, as
// PurgeDays requires of every writer of events/.
func rewriteDay(siteDir, date string, add []dayLine, dropImported bool) error {
	dir := eventsDir(siteDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	path := DayFile(siteDir, date)
	var lines []dayLine
	if f, err := os.Open(path); err == nil {
		lines, err = readDayLines(f, dropImported)
		f.Close()
		if err != nil {
			return err
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	lines = append(lines, add...)
	slices.SortStableFunc(lines, func(a, b dayLine) int { return cmp.Compare(a.t, b.t) })

	tmp, err := os.CreateTemp(dir, date+".jsonl.tmp*")
	if err != nil {
		return err
	}
	bw := bufio.NewWriterSize(tmp, 64<<10)
	for _, l := range lines {
		bw.Write(l.raw)
		bw.WriteByte('\n')
	}
	werr := bw.Flush()
	cerr := tmp.Close()
	if err := errors.Join(werr, cerr, os.Chmod(tmp.Name(), 0o644)); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return nil
}

// readDayLines reads a day file's lines as they are, with their times,
// leaving out a line that is not a record and, with dropImported, the
// imported ones.
func readDayLines(r io.Reader, dropImported bool) ([]dayLine, error) {
	var out []dayLine
	err := eachDayLine(r, func(l dayLine, source string) {
		if !dropImported || source != "um" {
			out = append(out, l)
		}
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// eachDayLine calls fn with every line of a day file that is a record, as it
// stands, with its time and its source ("s").
func eachDayLine(r io.Reader, fn func(l dayLine, source string)) error {
	br := bufio.NewReaderSize(r, 64<<10)
	for {
		line, err := br.ReadBytes('\n')
		if l := bytes.TrimSpace(line); len(l) > 0 {
			var h struct {
				T int64  `json:"t"`
				S string `json:"s"`
			}
			if json.Unmarshal(l, &h) == nil {
				fn(dayLine{t: h.T, raw: l}, h.S)
			}
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

// FirstOwnRecord is the time of the site's earliest record of its own (a
// "srv" or "js" line) in any day file, and false when there is none. An
// import stops there, so what cannot be read is an error, never "none": an
// import that missed the site's first own record would count twice. A site
// with no events folder has none, and so does a day file purged between the
// listing and its reading.
func FirstOwnRecord(siteDir string) (time.Time, bool, error) {
	entries, err := os.ReadDir(eventsDir(siteDir)) // sorted by name, so by date
	if errors.Is(err, fs.ErrNotExist) {
		// Only when nothing is there: Windows reports a file in the folder's
		// place as a path not found.
		if _, lerr := os.Lstat(eventsDir(siteDir)); errors.Is(lerr, fs.ErrNotExist) {
			return time.Time{}, false, nil
		}
	}
	if err != nil {
		return time.Time{}, false, err
	}
	var first int64
	found := false
	for _, e := range entries {
		date, ok := strings.CutSuffix(e.Name(), ".jsonl")
		if !ok || !saltDateRe.MatchString(date) {
			continue
		}
		if found {
			// A record of this date was written in a zone at most 14 hours
			// ahead of UTC, so it is no earlier than this; nor is any later
			// date's.
			d, err := time.Parse(time.DateOnly, date)
			if err == nil && d.Add(-14*time.Hour).UnixMilli() >= first {
				break
			}
		}
		if !e.Type().IsRegular() {
			return time.Time{}, false, fmt.Errorf("analytics: the day file %s is not a regular file", e.Name())
		}
		t, ok, err := firstOwnIn(DayFile(siteDir, date))
		if err != nil {
			return time.Time{}, false, err
		}
		if ok && (!found || t < first) {
			first, found = t, true
		}
	}
	if !found {
		return time.Time{}, false, nil
	}
	return time.UnixMilli(first), true, nil
}

// firstOwnIn is the earliest time of a "srv" or "js" line in a day file. A
// file gone meanwhile (the retention purged it) has none.
func firstOwnIn(path string) (int64, bool, error) {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	defer f.Close()
	var first int64
	found := false
	err = eachDayLine(f, func(l dayLine, source string) {
		if (source == "srv" || source == "js") && (!found || l.t < first) {
			first, found = l.t, true
		}
	})
	return first, found, err
}

// UmamiReport is a row of Umami's report table, as the runbook exports it.
type UmamiReport struct {
	Type       string          `json:"type"` // "funnel", "goal", or one not imported
	Name       string          `json:"name"`
	Parameters json.RawMessage `json:"parameters"`
}

// umamiTarget is a funnel step or a goal in Umami's parameters. Older
// Umami versions call a path step "url".
type umamiTarget struct {
	Type  string `json:"type"`
	Value string `json:"value"`
}

func (t umamiTarget) kind() string {
	if t.Type == "url" {
		return "path"
	}
	return t.Type
}

// MergeUmamiReports adds Umami's funnel and goal reports to the settings s,
// by name: a report whose name a funnel (or goal) already has is skipped,
// and so is one that would break the limits or that Validate refuses. Steps,
// windows and values are copied as they are, since funnels and goals match
// with Umami's semantics. A new id is "um-" and 8 hex characters of the
// name's hash. Other report types are skipped. s itself is not changed; out
// is the result, with the names added and the reports skipped
// ("name (type): why").
func MergeUmamiReports(s store.Analytics, reports []UmamiReport) (out store.Analytics, funnels, goals, skipped []string) {
	out = s
	out.Funnels = slices.Clone(s.Funnels)
	out.Goals = slices.Clone(s.Goals)
	skip := func(r UmamiReport, why string) {
		skipped = append(skipped, fmt.Sprintf("%s (%s): %s", r.Name, r.Type, why))
	}
	for _, r := range reports {
		id := umamiReportID(r.Name)
		switch r.Type {
		case "funnel":
			if slices.ContainsFunc(out.Funnels, func(f store.AnalyticsFunnel) bool { return f.Name == r.Name }) {
				skip(r, "a funnel of that name exists")
				continue
			}
			var p struct {
				Steps  []umamiTarget `json:"steps"`
				Window int           `json:"window"`
			}
			if err := json.Unmarshal(r.Parameters, &p); err != nil {
				skip(r, "unreadable parameters")
				continue
			}
			f := store.AnalyticsFunnel{ID: id, Name: r.Name, WindowMinutes: p.Window}
			if f.WindowMinutes == 0 {
				f.WindowMinutes = 60
			}
			for _, st := range p.Steps {
				f.Steps = append(f.Steps, store.AnalyticsStep{Type: st.kind(), Value: st.Value})
			}
			next := append(slices.Clone(out.Funnels), f)
			if _, err := (store.Analytics{Funnels: next}).Validate(nil); err != nil {
				skip(r, err.Error())
				continue
			}
			out.Funnels = next
			funnels = append(funnels, r.Name)
		case "goal":
			if slices.ContainsFunc(out.Goals, func(g store.AnalyticsGoal) bool { return g.Name == r.Name }) {
				skip(r, "a goal of that name exists")
				continue
			}
			var p umamiTarget
			if err := json.Unmarshal(r.Parameters, &p); err != nil {
				skip(r, "unreadable parameters")
				continue
			}
			next := append(slices.Clone(out.Goals), store.AnalyticsGoal{ID: id, Name: r.Name, Type: p.kind(), Value: p.Value})
			if _, err := (store.Analytics{Goals: next}).Validate(nil); err != nil {
				skip(r, err.Error())
				continue
			}
			out.Goals = next
			goals = append(goals, r.Name)
		default:
			skip(r, "only funnels and goals are imported")
		}
	}
	return out, funnels, goals, skipped
}

// umamiReportID is "um-" and the first 8 hex characters of sha256(name).
func umamiReportID(name string) string {
	sum := sha256.Sum256([]byte(name))
	return "um-" + hex.EncodeToString(sum[:4])
}
