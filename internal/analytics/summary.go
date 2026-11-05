package analytics

import (
	"cmp"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"time"
)

const (
	// summaryVersion is the format of a day summary: a stored summary of
	// another version is built again.
	summaryVersion = 1
	// summaryTop is the number of values a day summary keeps per dimension.
	summaryTop = 1000
)

// Other is the row a day summary sums a dimension's values beyond its top
// 1000 into; event data keys and values are kept the same way. Its visitors
// are the visitors of those values combined, each counted once per value as
// in their own rows. A table and the event data list it last, whatever its
// count.
const Other = "(other)"

// Summary is a closed day's contribution to an unfiltered query, built from
// its day file on first need and stored beside it as <date>.sum.json, so a
// long range adds up one summary per day instead of parsing every day file.
//
// Size and Mtime are the day file's when it was read (mtime in Unix
// nanoseconds): a summary whose file no longer matches them, or of another
// version V, is built again. FirstT and LastT are the instants of the day's
// first and last record (Unix ms), so a query can tell without parsing the
// day whether all, some or none of its records are in range.
//
// Every count is the day's own and counted as the raw scan counts a day:
// visitors are distinct within the day, a visit's views and attribution are
// the whole visit's. So the days of a range add up to the raw scan's answer.
// Dims holds a table per dimension of Dims, keyed by its name, except that
// every query parameter, utm_* included, is "query:<name>"; Events is the
// event table and EventData the data of its events by name. Each keeps its
// top 1000 values and sums the rest into Other; the data of an event summed
// into Other is not kept.
type Summary struct {
	V         int                    `json:"v"`
	Size      int64                  `json:"size"`
	Mtime     int64                  `json:"mtime"`
	Records   int                    `json:"records"`
	FirstT    int64                  `json:"first_t"`
	LastT     int64                  `json:"last_t"`
	Totals    SummaryTotals          `json:"totals"`
	Hours     []HourCount            `json:"hours"`
	Dims      map[string][]Row       `json:"dims"`
	Events    []Row                  `json:"events"`
	EventData map[string][]KeyValues `json:"event_data"`
}

// SummaryTotals are a day's totals as sums, so that days add up: the bounce
// rate and the average duration are computed once the days are merged.
type SummaryTotals struct {
	Views      int   `json:"views"`
	Visitors   int   `json:"visitors"`
	Visits     int   `json:"visits"`
	Bounces    int   `json:"bounces"`
	DurationMs int64 `json:"duration_ms"`
}

// HourCount is one local hour of a day summary, keyed by its start instant
// (Unix ms): a day of 23 or 25 hours has 23 or 25 of them, each once.
type HourCount struct {
	T        int64 `json:"t"`
	Views    int   `json:"views"`
	Visitors int   `json:"visitors"`
}

// summaryFile holds a day file's summary:
// <siteDir>/analytics/events/<date>.sum.json.
func summaryFile(siteDir, date string) string {
	return filepath.Join(eventsDir(siteDir), date+".sum.json")
}

// BuildSummary summarises one day file's records, its hours read in loc
// (nil is UTC). Size and Mtime are left to the caller, who knows the file.
func BuildSummary(records []Record, loc *time.Location) Summary {
	if loc == nil {
		loc = time.UTC
	}
	return summarize(indexDay(slices.Clone(records)), loc)
}

// summarize counts every record of d as an unfiltered query whose range
// holds the whole day does, with the same accumulators.
func summarize(d *parsedDay, loc *time.Location) Summary {
	s := Summary{V: summaryVersion, Records: len(d.recs), Dims: map[string][]Row{}, EventData: map[string][]KeyValues{}}
	if len(d.recs) == 0 {
		return s
	}
	s.FirstT, s.LastT = d.recs[0].T, d.recs[len(d.recs)-1].T
	var ds dayScan
	ds.prepare(d, 0, len(d.recs), &compiled{})

	// The hours of the local days the records fall on, bucketed as a query
	// in loc buckets them: normally one day of 23, 24 or 25 hours.
	f, l := time.UnixMilli(s.FirstT).In(loc), time.UnixMilli(s.LastT).In(loc)
	hours, _ := bucketStarts(time.Date(f.Year(), f.Month(), f.Day(), 0, 0, 0, 0, loc), time.Date(l.Year(), l.Month(), l.Day()+1, 0, 0, 0, 0, loc), loc, "hour")
	ov := newOverviewAcc(hours)
	ov.add(&ds)
	s.Totals = SummaryTotals{Views: ov.views, Visitors: ov.visitors, Visits: ov.visits, Bounces: ov.bounces, DurationMs: ov.durMs}
	s.Hours = make([]HourCount, len(hours))
	for i := range hours {
		s.Hours[i] = HourCount{T: ov.starts[i], Views: ov.bViews[i], Visitors: ov.bVisitors[i]}
	}

	for _, dm := range summaryDims(d.recs) {
		acc := newTableAcc(dm)
		acc.add(&ds)
		if len(acc.rows) > 0 {
			s.Dims[dm.key()] = keepTop(acc.sorted(), sumRows)
		}
	}

	events := newTableAcc(dim{kind: dimEvent})
	events.add(&ds)
	byCount := events.sorted()
	data := map[string]*dataAcc{}
	for _, r := range byCount[:min(len(byCount), summaryTop)] {
		data[r.Value] = newDataAcc()
	}
	s.Events = keepTop(byCount, sumRows)
	for i := range d.recs {
		if r := &d.recs[i]; r.K == "e" {
			if a := data[r.N]; a != nil {
				a.add(r)
			}
		}
	}
	for name, a := range data {
		if len(a.keys) > 0 {
			s.EventData[name] = keepTopData(a.result())
		}
	}
	return s
}

// summaryDims are the dimensions a summary tables: those of Dims but the
// event table, and every query parameter the day's records carry.
func summaryDims(recs []Record) []dim {
	var out []dim
	seen := map[string]bool{}
	add := func(d dim) {
		if k := d.key(); !seen[k] {
			seen[k] = true
			out = append(out, d)
		}
	}
	for _, name := range Dims {
		if d, err := parseDim(name); err == nil && d.kind != dimEvent {
			add(d)
		}
	}
	for i := range recs {
		for p := range recs[i].Q {
			add(dim{kind: dimQuery, param: p})
		}
	}
	return out
}

// key names a dimension's table in a summary: its name, and query:<name>
// for a query parameter, whichever name the query used.
func (d dim) key() string {
	if d.kind == dimQuery {
		return "query:" + d.param
	}
	return kindNames[d.kind]
}

var kindNames = func() map[dimKind]string {
	m := make(map[dimKind]string, len(dimKinds))
	for name, k := range dimKinds {
		m[k] = name
	}
	return m
}()

// keepTop keeps the first summaryTop items, which come most first, and
// folds the rest into one.
func keepTop[T any](items []T, fold func(rest []T) T) []T {
	if len(items) <= summaryTop {
		return items
	}
	return append(items[:summaryTop:summaryTop], fold(items[summaryTop:]))
}

func sumRows(rest []Row) Row {
	o := Row{Value: Other}
	for _, r := range rest {
		o.Visitors += r.Visitors
		o.Views += r.Views
		o.Count += r.Count
	}
	return o
}

// keepTopData keeps an event's top values of each key, and its top keys by
// the number of values counted under them; what is left out goes into
// Other, the keys left out into one key Other with one value Other.
func keepTopData(kvs []KeyValues) []KeyValues {
	total := func(vs []ValueCount) int {
		n := 0
		for _, v := range vs {
			n += v.Count
		}
		return n
	}
	for i := range kvs {
		kvs[i].Values = keepTop(kvs[i].Values, func(rest []ValueCount) ValueCount {
			return ValueCount{Value: Other, Count: total(rest)}
		})
	}
	if len(kvs) <= summaryTop {
		return kvs
	}
	slices.SortStableFunc(kvs, func(a, b KeyValues) int { return cmp.Compare(total(b.Values), total(a.Values)) })
	return keepTop(kvs, func(rest []KeyValues) KeyValues {
		n := 0
		for _, kv := range rest {
			n += total(kv.Values)
		}
		return KeyValues{Key: Other, Values: []ValueCount{{Value: Other, Count: n}}}
	})
}

// otherLast orders Other after every other value: it sums values left out,
// so it is not a value of its own, whatever its count.
func otherLast(a, b string) int {
	switch {
	case a == b:
		return 0
	case a == Other:
		return 1
	case b == Other:
		return -1
	}
	return 0
}

// wholeDay takes a closed day lying whole in a query's range from its
// summary; start and end are the day's local midnights (Unix ms). It
// returns false, having changed nothing, when the summary cannot answer the
// query, which then scans the day raw.
type wholeDay func(s *Summary, start, end int64) bool

// fromSummary answers a closed day of c from its summary where it can, and
// reports whether it did: a day without a record in range is done, and so
// is one whole took.
//
// whole gets a day only when c has no filter, the day's local midnight to
// midnight lies inside the range, and so do all its records: a day file is
// named in the zone the site had when it was written, so after a zone
// change it holds records of a neighbouring local day. A neighbour read
// only for such records, or a day the range cuts, is never answered whole;
// its summary only tells whether it can be skipped. The days inside the
// range of a filtered query are scanned raw without one.
func (e *Engine) fromSummary(siteDir, date string, c *compiled, whole wholeDay) (bool, error) {
	day, err := time.Parse("2006-01-02", date)
	if err != nil {
		return false, nil
	}
	start := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, c.loc).UnixMilli()
	end := time.Date(day.Year(), day.Month(), day.Day()+1, 0, 0, 0, 0, c.loc).UnixMilli()
	inside := start >= c.from && end <= c.to
	answer := inside && whole != nil && len(c.visitFilters)+len(c.recordFilters) == 0
	if inside && !answer {
		return false, nil
	}
	s, err := e.summary(siteDir, date, c.loc)
	if err != nil {
		return false, err
	}
	if s == nil || s.Records == 0 || s.LastT < c.from || s.FirstT >= c.to {
		return true, nil
	}
	return answer && s.FirstT >= start && s.LastT < end && whole(s, start, end), nil
}

// summary returns the summary of date's day file, nil when there is no day
// file. A stored summary counts only while its size, mtime and version
// match the file and this code; otherwise it is built from the parsed day,
// its hours read in loc, and stored. Storing is best effort: a summary is a
// cache of its file, and the query is answered all the same.
func (e *Engine) summary(siteDir, date string, loc *time.Location) (*Summary, error) {
	fi, err := os.Stat(DayFile(siteDir, date))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if s := readSummary(siteDir, date); s != nil && s.V == summaryVersion && s.Size == fi.Size() && s.Mtime == fi.ModTime().UnixNano() {
		return s, nil
	}
	d, key, err := e.day(siteDir, date)
	if err != nil || d == nil {
		return nil, err
	}
	s := summarize(d, loc)
	s.Size, s.Mtime = key.size, key.mtime
	_ = writeSummary(siteDir, date, &s)
	return &s, nil
}

// readSummary reads a stored summary; one that is missing, unreadable or
// does not parse is nil, and built again.
func readSummary(siteDir, date string) *Summary {
	b, err := os.ReadFile(summaryFile(siteDir, date))
	if err != nil {
		return nil
	}
	s := new(Summary)
	if json.Unmarshal(b, s) != nil {
		return nil
	}
	return s
}

// writeSummary stores s through a temp file and a rename, so a reader
// never sees half a summary. It takes no lock: two queries building the
// same summary write the same content. It never creates the events
// directory, so a site deleted meanwhile is not recreated.
func writeSummary(siteDir, date string, s *Summary) error {
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	path := summaryFile(siteDir, date)
	tmp, err := os.CreateTemp(filepath.Dir(path), date+".sum.json.tmp*")
	if err != nil {
		return err
	}
	_, werr := tmp.Write(b)
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

// addSummary adds a closed day whose records lie in one bucket, or whose
// hours are the query's buckets of [start, end). It changes nothing and
// returns false otherwise.
func (a *overviewAcc) addSummary(s *Summary, start, end int64) bool {
	if len(a.starts) > 0 {
		if b := bucketOf(a.starts, s.FirstT); b >= 0 && b == bucketOf(a.starts, s.LastT) {
			a.bViews[b] += s.Totals.Views
			a.bVisitors[b] += s.Totals.Visitors
		} else if lo, ok := a.hoursAt(s, start, end); ok {
			for i, h := range s.Hours {
				a.bViews[lo+i] += h.Views
				a.bVisitors[lo+i] += h.Visitors
			}
		} else {
			return false
		}
	}
	a.views += s.Totals.Views
	a.visitors += s.Totals.Visitors
	a.visits += s.Totals.Visits
	a.bounces += s.Totals.Bounces
	a.durMs += s.Totals.DurationMs
	return true
}

// hoursAt is the index of the query's bucket for the summary's first hour,
// when the summary's hours start exactly where the query's buckets in
// [start, end) start. Each hour's records then fall in the bucket of the
// same start: the summary's records lie before end, and the bucket after
// those starts at end or later.
func (a *overviewAcc) hoursAt(s *Summary, start, end int64) (int, bool) {
	lo, _ := slices.BinarySearch(a.starts, start)
	hi, _ := slices.BinarySearch(a.starts, end)
	if hi-lo != len(s.Hours) || lo == hi {
		return 0, false
	}
	for i, h := range s.Hours {
		if a.starts[lo+i] != h.T {
			return 0, false
		}
	}
	return lo, true
}

// addSummary adds a closed day's rows of the dimension.
func (a *tableAcc) addSummary(s *Summary, _, _ int64) bool {
	rows := s.Dims[a.dim.key()]
	if a.dim.kind == dimEvent {
		rows = s.Events
	}
	for _, r := range rows {
		j := a.row(r.Value)
		a.rows[j].Visitors += r.Visitors
		a.rows[j].Views += r.Views
		a.rows[j].Count += r.Count
	}
	return true
}
