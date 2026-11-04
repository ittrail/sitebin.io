package analytics

import (
	"cmp"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Filter selects by one dimension's value. Dim is one of Dims,
// "query:<name>" or "data:<key>". Several filters AND.
//
// A visit-level filter (VisitLevel) keeps whole visits; a record-level one
// keeps records (path, title and tag the page views and events carrying the
// value, event and data:<key> events). Views and visitors count the records
// kept; visits are the visits holding at least one of them. Bounces and
// durations are measured on a visit's page views, and with a record-level
// filter on its kept page views only, as Umami filters inside its
// per-visit query. A referrer filter of DirectReferrer keeps the visits
// that came with no referrer.
type Filter struct{ Dim, Value string }

// Query is the range of instants [From, To), read in Loc (nil is UTC): its
// local dates name the day files, and a series counts in its local hours,
// days or months. Unit is "hour", "day" or "month"; "" picks by the span a
// calendar in Loc shows: up to 48 hours by the hour, up to 92 days by the
// day, beyond by the month.
type Query struct {
	From, To time.Time
	Loc      *time.Location
	Filters  []Filter
	Unit     string
}

// Totals are a range's metrics as Umami defines them. Visitors are the sum
// of each day's distinct visitors (a visitor id lives one day). Bounce
// rate is the share of visits with exactly one page view; duration is the
// average time from a visit's first to its last page view, in seconds (a
// bounce counts 0, and events never extend it).
type Totals struct {
	Views      int     `json:"views"`
	Visitors   int     `json:"visitors"`
	Visits     int     `json:"visits"`
	BounceRate float64 `json:"bounce_rate"`
	DurationS  float64 `json:"duration_s"`
}

// Point is one bucket of a series, starting at T in the query's zone.
// Visitors are distinct within the bucket for an hour or a day, and the sum
// of each day's distinct visitors for a month.
type Point struct {
	T        time.Time `json:"t"`
	Views    int       `json:"views"`
	Visitors int       `json:"visitors"`
}

// Overview is the overview tab's answer. Previous holds the totals of the
// period of the same length before (the same number of local days, when the
// range is whole days); OverCap the records over the daily cap that were
// not stored in the range's days.
type Overview struct {
	Totals   Totals  `json:"totals"`
	Previous *Totals `json:"previous,omitempty"`
	Series   []Point `json:"series"`
	Unit     string  `json:"unit"`
	OverCap  int     `json:"over_cap"`
}

// Row is one value of a table. Count is the number of events, for the
// event table only.
type Row struct {
	Value    string `json:"value"`
	Visitors int    `json:"visitors"`
	Views    int    `json:"views"`
	Count    int    `json:"count,omitempty"`
}

// ValueCount is one value of an event data property and how many events
// carried it.
type ValueCount struct {
	Value string `json:"value"`
	Count int    `json:"count"`
}

// KeyValues is one event data property with its values, most frequent
// first.
type KeyValues struct {
	Key    string       `json:"key"`
	Values []ValueCount `json:"values"`
}

// Recent is one of the latest records: the path of a page view or the name
// of an event, and where and with what it came.
type Recent struct {
	T  time.Time `json:"t"`
	K  string    `json:"k"`
	P  string    `json:"p,omitempty"`
	N  string    `json:"n,omitempty"`
	C  string    `json:"c"`
	B  string    `json:"b"`
	Dv string    `json:"dv"`
}

// Realtime is the last 30 minutes: the visitors active in the last 5, a
// point per minute, the top pages, referrers and countries, and the latest
// records, newest first.
type Realtime struct {
	Active    int      `json:"active"`
	PerMinute []Point  `json:"per_minute"`
	Pages     []Row    `json:"pages"`
	Referrers []Row    `json:"referrers"`
	Countries []Row    `json:"countries"`
	Latest    []Recent `json:"latest"`
}

const (
	realtimeWindow  = 30 * time.Minute
	realtimeActive  = 5 * time.Minute
	realtimeTop     = 10
	realtimeLatest  = 50
	maxAutoHourSpan = 48 * time.Hour
	maxAutoDaySpan  = 92 * 24 * time.Hour
)

// Overview answers the totals and the series of q, and with compare the
// totals of the period before.
func (e *Engine) Overview(siteDir string, q Query, compare bool) (Overview, error) {
	c, err := compile(q.From, q.To, q.Loc, q.Filters)
	if err != nil {
		return Overview{}, err
	}
	unit := q.Unit
	if unit == "" {
		unit = autoUnit(q.From, q.To, c.loc)
	}
	starts, err := bucketStarts(q.From, q.To, c.loc, unit)
	if err != nil {
		return Overview{}, err
	}
	acc := newOverviewAcc(starts)
	if err := e.scan(siteDir, c, acc.add); err != nil {
		return Overview{}, err
	}
	ov := Overview{Totals: acc.totals(), Series: acc.points(), Unit: unit}
	if ov.OverCap, err = overCap(siteDir, c.dates); err != nil {
		return Overview{}, err
	}
	if compare {
		from, to := previousRange(q.From, q.To, c.loc)
		pc, err := compile(from, to, c.loc, q.Filters)
		if err != nil {
			return Overview{}, err
		}
		prev := newOverviewAcc(nil)
		if err := e.scan(siteDir, pc, prev.add); err != nil {
			return Overview{}, err
		}
		t := prev.totals()
		ov.Previous = &t
	}
	return ov, nil
}

// Table answers one dimension's rows over q, most first: the event table
// by count, then visitors, every other by visitors, then views; ties by
// value. total is the number of values. A limit of 0 or less is no limit.
//
// A visit-level dimension counts each visit holding a kept record under
// its value: its visitor, and its kept page views ("click_id" a visit under
// each click id it carried). path, title and tag count kept page views;
// event counts kept events by name. Empty values are left out, except a
// visit without a referrer, which is counted as DirectReferrer.
func (e *Engine) Table(siteDir string, q Query, dimName string, limit, offset int) (rows []Row, total int, err error) {
	d, err := parseDim(dimName)
	if err != nil {
		return nil, 0, err
	}
	if d.kind == dimData {
		return nil, 0, fmt.Errorf("%w: %q is a filter, not a table", ErrBadQuery, dimName)
	}
	c, err := compile(q.From, q.To, q.Loc, q.Filters)
	if err != nil {
		return nil, 0, err
	}
	acc := newTableAcc(d)
	if err := e.scan(siteDir, c, acc.add); err != nil {
		return nil, 0, err
	}
	rows = acc.sorted()
	total = len(rows)
	offset = min(max(offset, 0), total)
	rows = rows[offset:]
	if limit > 0 && limit < len(rows) {
		rows = rows[:limit]
	}
	return rows, total, nil
}

// EventData answers the data properties of the kept events named name: for
// each key (sorted), its values by the number of events carrying them.
func (e *Engine) EventData(siteDir string, q Query, name string) ([]KeyValues, error) {
	c, err := compile(q.From, q.To, q.Loc, q.Filters)
	if err != nil {
		return nil, err
	}
	keys := map[string]map[string]*int{}
	var buf []byte
	err = e.scan(siteDir, c, func(s *dayScan) {
		for i := s.lo; i < s.hi; i++ {
			r := &s.d.recs[i]
			if r.K != "e" || r.N != name || !s.selected(i) {
				continue
			}
			for k, v := range r.D {
				vals := keys[k]
				if vals == nil {
					vals = map[string]*int{}
					keys[k] = vals
				}
				var n *int
				if str, ok := v.(string); ok {
					if n = vals[str]; n == nil {
						n = new(int)
						vals[str] = n
					}
				} else {
					buf = appendData(buf[:0], v)
					if n = vals[string(buf)]; n == nil {
						n = new(int)
						vals[string(buf)] = n
					}
				}
				*n++
			}
		}
	})
	if err != nil {
		return nil, err
	}
	out := make([]KeyValues, 0, len(keys))
	for k, vals := range keys {
		kv := KeyValues{Key: k, Values: make([]ValueCount, 0, len(vals))}
		for v, n := range vals {
			kv.Values = append(kv.Values, ValueCount{Value: v, Count: *n})
		}
		slices.SortFunc(kv.Values, func(a, b ValueCount) int {
			if c := cmp.Compare(b.Count, a.Count); c != 0 {
				return c
			}
			return strings.Compare(a.Value, b.Value)
		})
		out = append(out, kv)
	}
	slices.SortFunc(out, func(a, b KeyValues) int { return strings.Compare(a.Key, b.Key) })
	return out, nil
}

// Realtime answers the records of [now-30m, now], read in loc: the
// visitors of the last 5 minutes, one point for each of the 30 clock
// minutes up to now's, the top 10 pages, referrers and countries (as
// Table counts them) and the 50 latest records.
func (e *Engine) Realtime(siteDir string, now time.Time, loc *time.Location) (Realtime, error) {
	if loc == nil {
		loc = time.UTC
	}
	end := now.Truncate(time.Millisecond).Add(time.Millisecond) // a record at now counts
	c, err := compile(now.Add(-realtimeWindow), end, loc, nil)
	if err != nil {
		return Realtime{}, err
	}
	minutes := make([]time.Time, realtimeWindow/time.Minute)
	first := now.Truncate(time.Minute).Add(-time.Duration(len(minutes)-1) * time.Minute)
	for i := range minutes {
		minutes[i] = first.Add(time.Duration(i) * time.Minute).In(loc)
	}
	series := newOverviewAcc(minutes)
	pages, referrers, countries := newTableAcc(dim{kind: dimPath}), newTableAcc(dim{kind: dimReferrer}), newTableAcc(dim{kind: dimCountry})
	activeFrom := ceilMilli(now.Add(-realtimeActive))
	active := 0
	var seen []bool
	var latest []*Record
	err = e.scan(siteDir, c, func(s *dayScan) {
		series.add(s)
		pages.add(s)
		referrers.add(s)
		countries.add(s)
		seen = cleared(seen, s.d.nVisitors)
		for i := s.lo; i < s.hi; i++ {
			if w := s.d.visitor[i]; s.d.recs[i].T >= activeFrom && !seen[w] {
				seen[w] = true
				active++
			}
		}
		for i := max(s.lo, s.hi-realtimeLatest); i < s.hi; i++ {
			latest = append(latest, &s.d.recs[i])
		}
	})
	if err != nil {
		return Realtime{}, err
	}
	slices.SortStableFunc(latest, func(a, b *Record) int { return cmp.Compare(b.T, a.T) })
	latest = latest[:min(len(latest), realtimeLatest)]
	rt := Realtime{
		Active:    active,
		PerMinute: series.points(),
		Pages:     top(pages.sorted(), realtimeTop),
		Referrers: top(referrers.sorted(), realtimeTop),
		Countries: top(countries.sorted(), realtimeTop),
		Latest:    make([]Recent, len(latest)),
	}
	for i, r := range latest {
		l := Recent{T: time.UnixMilli(r.T).In(loc), K: r.K, C: r.C, B: r.B, Dv: r.Dv}
		if r.K == "e" {
			l.N = r.N
		} else {
			l.P = r.P
		}
		rt.Latest[i] = l
	}
	return rt, nil
}

func top(rows []Row, n int) []Row { return rows[:min(len(rows), n)] }

// compiled is a query ready to scan: its range in Unix milliseconds, the
// local dates it touches, the day files it reads, and its filters by level.
//
// The files are the range's dates and one more on each side. A day file is
// named in the zone the site had when it was written, so after a zone
// change the records near midnight sit in a neighbouring date's file; scan
// keeps only the records in range, so reading more is safe.
type compiled struct {
	loc           *time.Location
	from, to      int64    // to exclusive
	dates         []string // the range's own local dates
	files         []string // the dates whose day files are read
	visitFilters  []filter
	recordFilters []filter
}

func compile(from, to time.Time, loc *time.Location, fs []Filter) (*compiled, error) {
	if loc == nil {
		loc = time.UTC
	}
	if !to.After(from) {
		return nil, fmt.Errorf("%w: empty range %v .. %v", ErrBadQuery, from, to)
	}
	c := &compiled{loc: loc, from: ceilMilli(from), to: ceilMilli(to), dates: localDates(from, to, loc, 0), files: localDates(from, to, loc, 1)}
	for _, f := range fs {
		d, err := parseDim(f.Dim)
		if err != nil {
			return nil, err
		}
		if d.visitLevel() {
			c.visitFilters = append(c.visitFilters, filter{dim: d, value: f.Value})
		} else {
			c.recordFilters = append(c.recordFilters, filter{dim: d, value: f.Value})
		}
	}
	return c, nil
}

// ceilMilli is the first Unix millisecond at or after t: a record at
// millisecond m is at or after t exactly when m >= ceilMilli(t).
// UnixMilli rounds down (whole seconds floor, nanoseconds are >= 0).
func ceilMilli(t time.Time) int64 {
	ms := t.UnixMilli()
	if t.Nanosecond()%int(time.Millisecond) != 0 {
		ms++
	}
	return ms
}

// localDates are the local dates in loc that [from, to) touches, and pad
// more on each side.
func localDates(from, to time.Time, loc *time.Location, pad int) []string {
	f, l := from.In(loc), to.Add(-time.Nanosecond).In(loc)
	last := time.Date(l.Year(), l.Month(), l.Day()+pad, 0, 0, 0, 0, time.UTC)
	var out []string
	for d := time.Date(f.Year(), f.Month(), f.Day()-pad, 0, 0, 0, 0, time.UTC); !d.After(last); d = d.AddDate(0, 0, 1) {
		out = append(out, d.Format("2006-01-02"))
	}
	return out
}

// scan calls fn for each day of c that has records in range, prepared
// under c's filters. Days come in date order and records in time order.
func (e *Engine) scan(siteDir string, c *compiled, fn func(*dayScan)) error {
	var s dayScan
	for _, date := range c.files {
		d, err := e.day(siteDir, date)
		if err != nil {
			return err
		}
		if d == nil {
			continue
		}
		lo := sort.Search(len(d.recs), func(i int) bool { return d.recs[i].T >= c.from })
		hi := sort.Search(len(d.recs), func(i int) bool { return d.recs[i].T >= c.to })
		if lo >= hi {
			continue
		}
		s.prepare(d, lo, hi, c)
		fn(&s)
	}
	return nil
}

// wallClock is t's reading on a clock in loc, as if that were UTC: the
// difference of two is the span a calendar shows, whatever DST did between.
func wallClock(t time.Time, loc *time.Location) time.Time {
	l := t.In(loc)
	return time.Date(l.Year(), l.Month(), l.Day(), l.Hour(), l.Minute(), l.Second(), l.Nanosecond(), time.UTC)
}

func autoUnit(from, to time.Time, loc *time.Location) string {
	switch span := wallClock(to, loc).Sub(wallClock(from, loc)); {
	case span <= maxAutoHourSpan:
		return "hour"
	case span <= maxAutoDaySpan:
		return "day"
	}
	return "month"
}

// bucketStarts are the starts of the local hours, days or months in loc
// that [from, to) touches.
//
// Days and months come from time.Date, never from adding 24 hours, so a
// day of 23 or 25 hours is one bucket. Hours step by an hour and are cut
// to the local hour: time.Date cannot name the first 02:00 of a fall-back
// night (it returns the second), so building hours with it would skip one
// and double the next.
func bucketStarts(from, to time.Time, loc *time.Location, unit string) ([]time.Time, error) {
	var out []time.Time
	l := from.In(loc)
	switch unit {
	case "hour":
		for b := startOfHour(from, loc); b.Before(to); b = startOfHour(b.Add(time.Hour), loc) {
			out = append(out, b)
		}
	case "day":
		for i := 0; ; i++ {
			b := time.Date(l.Year(), l.Month(), l.Day()+i, 0, 0, 0, 0, loc)
			if !b.Before(to) {
				break
			}
			out = append(out, b)
		}
	case "month":
		for i := 0; ; i++ {
			b := time.Date(l.Year(), l.Month()+time.Month(i), 1, 0, 0, 0, 0, loc)
			if !b.Before(to) {
				break
			}
			out = append(out, b)
		}
	default:
		return nil, fmt.Errorf("%w: unknown unit %q", ErrBadQuery, unit)
	}
	return out, nil
}

func startOfHour(t time.Time, loc *time.Location) time.Time {
	l := t.In(loc)
	return l.Add(-(time.Duration(l.Minute())*time.Minute + time.Duration(l.Second())*time.Second + time.Duration(l.Nanosecond())))
}

// previousRange is the period of the same length before [from, to): the
// same number of local days when both ends are local midnights (a 25-hour
// day compares with the whole day before), else the same duration.
func previousRange(from, to time.Time, loc *time.Location) (time.Time, time.Time) {
	f, t := from.In(loc), to.In(loc)
	if isMidnight(f) && isMidnight(t) {
		n := int(wallClock(t, loc).Sub(wallClock(f, loc)) / (24 * time.Hour))
		return time.Date(f.Year(), f.Month(), f.Day()-n, 0, 0, 0, 0, loc), from
	}
	return from.Add(-to.Sub(from)), from
}

func isMidnight(t time.Time) bool {
	return t.Hour() == 0 && t.Minute() == 0 && t.Second() == 0 && t.Nanosecond() == 0
}

// overCap sums the days' counts of records over the daily cap. A count
// that does not parse is none, as the recorder reads it.
func overCap(siteDir string, dates []string) (int, error) {
	n := 0
	for _, d := range dates {
		b, err := os.ReadFile(overFile(siteDir, d))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return 0, err
		}
		if v, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil && v > 0 {
			n += v
		}
	}
	return n, nil
}

// overviewAcc sums the totals of kept records and visits day by day, and
// with bucket starts a series. Every sum is additive over days.
type overviewAcc struct {
	views, visitors, visits, bounces int
	durMs                            int64

	times             []time.Time // bucket starts
	starts            []int64     // the same in Unix milliseconds
	bViews, bVisitors []int

	seen       []bool  // per visitor of the day: counted in the totals
	lastBucket []int32 // per visitor of the day: the last bucket that counted them, +1
}

func newOverviewAcc(starts []time.Time) *overviewAcc {
	a := &overviewAcc{times: starts, starts: make([]int64, len(starts)), bViews: make([]int, len(starts)), bVisitors: make([]int, len(starts))}
	for i, t := range starts {
		a.starts[i] = ceilMilli(t)
	}
	return a
}

func (a *overviewAcc) add(s *dayScan) {
	d := s.d
	a.seen = cleared(a.seen, d.nVisitors)
	if len(a.starts) > 0 {
		a.lastBucket = cleared(a.lastBucket, d.nVisitors)
	}
	for i := s.lo; i < s.hi; i++ {
		if !s.selected(i) {
			continue
		}
		r := &d.recs[i]
		w := d.visitor[i]
		page := r.K == "p"
		if page {
			a.views++
		}
		if !a.seen[w] {
			a.seen[w] = true
			a.visitors++
		}
		if len(a.starts) == 0 {
			continue
		}
		// A visitor's records come in time order, so their buckets do too:
		// remembering the last one counts each visitor once per bucket.
		b := bucketOf(a.starts, r.T)
		if b < 0 {
			continue
		}
		if page {
			a.bViews[b]++
		}
		if a.lastBucket[w] != int32(b+1) {
			a.lastBucket[w] = int32(b + 1)
			a.bVisitors[b]++
		}
	}
	for i := range s.vis {
		v := &s.vis[i]
		if !v.hit {
			continue
		}
		a.visits++
		bounce, ms := s.bounceAndDuration(v)
		if bounce {
			a.bounces++
		}
		a.durMs += ms
	}
}

// bucketOf is the bucket holding t, -1 before the first.
func bucketOf(starts []int64, t int64) int {
	i, found := slices.BinarySearch(starts, t)
	if found {
		return i
	}
	return i - 1
}

func (a *overviewAcc) totals() Totals {
	t := Totals{Views: a.views, Visitors: a.visitors, Visits: a.visits}
	if a.visits > 0 {
		t.BounceRate = float64(a.bounces) / float64(a.visits)
		t.DurationS = float64(a.durMs) / 1000 / float64(a.visits)
	}
	return t
}

func (a *overviewAcc) points() []Point {
	out := make([]Point, len(a.times))
	for i, t := range a.times {
		out[i] = Point{T: t, Views: a.bViews[i], Visitors: a.bVisitors[i]}
	}
	return out
}

// tableAcc counts one dimension's rows day by day. A row's visitors are
// distinct within each day and summed over days.
type tableAcc struct {
	dim   dim
	index map[string]int32
	rows  []Row
	pairs []uint64 // the day's (row, visitor) pairs, row in the high half
}

func newTableAcc(d dim) *tableAcc { return &tableAcc{dim: d, index: map[string]int32{}} }

func (a *tableAcc) row(value string) int32 {
	i, ok := a.index[value]
	if !ok {
		i = int32(len(a.rows))
		a.index[value] = i
		a.rows = append(a.rows, Row{Value: value})
	}
	return i
}

func (a *tableAcc) count(row, visitor int32) {
	a.pairs = append(a.pairs, uint64(row)<<32|uint64(uint32(visitor)))
}

func (a *tableAcc) add(s *dayScan) {
	d := s.d
	a.pairs = a.pairs[:0]
	switch {
	case a.dim.kind == dimEvent:
		for i := s.lo; i < s.hi; i++ {
			r := &d.recs[i]
			if r.K != "e" || r.N == "" || !s.selected(i) {
				continue
			}
			j := a.row(r.N)
			a.rows[j].Count++
			a.count(j, d.visitor[i])
		}
	case !a.dim.visitLevel():
		for i := s.lo; i < s.hi; i++ {
			r := &d.recs[i]
			if r.K != "p" || !s.selected(i) {
				continue
			}
			if v := field(r, a.dim.kind); v != "" {
				j := a.row(v)
				a.rows[j].Views++
				a.count(j, d.visitor[i])
			}
		}
	default:
		for i := range s.vis {
			v := &s.vis[i]
			if !v.hit {
				continue
			}
			w := s.visitor(v)
			if a.dim.kind == dimClickID {
				for _, id := range s.attribution(v).Ck {
					j := a.row(id)
					a.rows[j].Views += int(v.selViews)
					a.count(j, w)
				}
				continue
			}
			value := s.visitValue(v, a.dim)
			if value == "" {
				if a.dim.kind != dimReferrer {
					continue
				}
				value = DirectReferrer
			}
			j := a.row(value)
			a.rows[j].Views += int(v.selViews)
			a.count(j, w)
		}
	}
	slices.Sort(a.pairs)
	for i, p := range a.pairs {
		if i == 0 || p != a.pairs[i-1] {
			a.rows[p>>32].Visitors++
		}
	}
}

// sorted returns the rows most first, then by value: events by count and
// visitors, as Umami's events table sorts them, the rest by visitors and
// views.
func (a *tableAcc) sorted() []Row {
	rows := append(make([]Row, 0, len(a.rows)), a.rows...)
	first, second := func(r Row) int { return r.Visitors }, func(r Row) int { return r.Views }
	if a.dim.kind == dimEvent {
		first, second = func(r Row) int { return r.Count }, func(r Row) int { return r.Visitors }
	}
	slices.SortFunc(rows, func(x, y Row) int {
		if c := cmp.Compare(first(y), first(x)); c != 0 {
			return c
		}
		if c := cmp.Compare(second(y), second(x)); c != 0 {
			return c
		}
		return strings.Compare(x.Value, y.Value)
	})
	return rows
}
