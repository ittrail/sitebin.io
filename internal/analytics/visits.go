package analytics

import (
	"slices"
	"time"
)

// visitGap ends a visit: a visitor's record more than this after their
// previous one starts a new visit. Imported records carry Umami's visit id
// instead and are grouped by it.
const visitGap = 30 * time.Minute

// visitInfo is the part of one visit a query's range holds. The record
// indexes point into the day's records; -1 is none.
type visitInfo struct {
	first, last int32 // first and last record
	entry, exit int32 // first and last page view
	attr        int32 // first record carrying a referrer, a query or a click id
	views       int32 // page views
	selViews    int32 // page views passing every filter
	selFirst    int32 // first page view passing every filter
	selLast     int32 // last page view passing every filter
	match       bool  // passes every visit-level filter
	hit         bool  // holds a record passing every filter
}

// dayScan is one day of a query: records [lo, hi) of d are in range, vis
// has an entry for each of the day's visits, and sel says for each record
// in range whether it passes every filter.
//
// Restricting a visit to a range of time keeps it one visit with the same
// gaps, so visits numbered over the whole day (parsedDay) are the visits of
// any range within it.
type dayScan struct {
	d        *parsedDay
	lo, hi   int
	vis      []visitInfo
	sel      []bool
	byRecord bool // a record-level filter is on
	buf      []byte
}

// prepare fills s for records [lo, hi) of d under c's filters: first the
// visits, then which visits pass the visit-level filters, then which of
// their records pass the record-level ones.
func (s *dayScan) prepare(d *parsedDay, lo, hi int, c *compiled) {
	s.d, s.lo, s.hi = d, lo, hi
	s.byRecord = len(c.recordFilters) > 0
	s.vis = resize(s.vis, d.nVisits)
	for i := range s.vis {
		s.vis[i] = visitInfo{first: -1, last: -1, entry: -1, exit: -1, attr: -1, selFirst: -1, selLast: -1}
	}
	for i := lo; i < hi; i++ {
		r := &d.recs[i]
		v := &s.vis[d.visit[i]]
		at := int32(i)
		if v.first < 0 {
			v.first = at
		}
		v.last = at
		if r.K == "p" {
			if v.entry < 0 {
				v.entry = at
			}
			v.exit = at
			v.views++
		}
		if v.attr < 0 && (r.R != "" || len(r.Q) > 0 || len(r.Ck) > 0) {
			v.attr = at
		}
	}
	for i := range s.vis {
		v := &s.vis[i]
		v.match = v.first >= 0 && s.visitMatches(v, c.visitFilters)
	}
	s.sel = resize(s.sel, hi-lo)
	for i := lo; i < hi; i++ {
		v := &s.vis[d.visit[i]]
		ok := v.match && s.recordMatches(&d.recs[i], c.recordFilters)
		s.sel[i-lo] = ok
		if ok {
			v.hit = true
			if d.recs[i].K == "p" {
				if v.selFirst < 0 {
					v.selFirst = int32(i)
				}
				v.selLast = int32(i)
				v.selViews++
			}
		}
	}
}

// bounceAndDuration measures a visit as Umami does, on page views only: a
// bounce has exactly one, and the duration runs from the first to the last
// (0 with fewer than two; an event never extends it). Under a record-level
// filter only the kept page views count, as Umami applies the filter inside
// its per-visit query.
func (s *dayScan) bounceAndDuration(v *visitInfo) (bounce bool, ms int64) {
	n, first, last := v.views, v.entry, v.exit
	if s.byRecord {
		n, first, last = v.selViews, v.selFirst, v.selLast
	}
	if n >= 2 {
		ms = s.d.recs[last].T - s.d.recs[first].T
	}
	return n == 1, ms
}

func (s *dayScan) selected(i int) bool { return s.sel[i-s.lo] }

// visitor is the day's number of the visit's visitor.
func (s *dayScan) visitor(v *visitInfo) int32 { return s.d.visitor[v.first] }

// attribution is the record a visit's referrer, query parameters, click ids
// and channel come from: its first record carrying any of them, normally
// its entry page view. All of them come from that one record, so the
// channel sees what the visit arrived with.
func (s *dayScan) attribution(v *visitInfo) *Record {
	if v.attr < 0 {
		return &noAttribution
	}
	return &s.d.recs[v.attr]
}

// noAttribution is the attribution of a visit that carried none. Read only.
var noAttribution Record

// visitValue is a visit's value of a single-valued visit-level dimension:
// its entry or exit path, its attribution, or, for where and with what it
// came, the value on its first record.
func (s *dayScan) visitValue(v *visitInfo, d dim) string {
	recs := s.d.recs
	switch d.kind {
	case dimEntry:
		if v.entry >= 0 {
			return recs[v.entry].P
		}
		return ""
	case dimExit:
		if v.exit >= 0 {
			return recs[v.exit].P
		}
		return ""
	case dimReferrer:
		return s.attribution(v).R
	case dimChannel:
		return Channel(*s.attribution(v))
	case dimQuery:
		return s.attribution(v).Q[d.param]
	}
	return field(&recs[v.first], d.kind)
}

func (s *dayScan) visitMatches(v *visitInfo, fs []filter) bool {
	for i := range fs {
		f := &fs[i]
		var ok bool
		switch f.dim.kind {
		case dimClickID:
			ok = slices.Contains(s.attribution(v).Ck, f.value)
		case dimReferrer:
			r := s.visitValue(v, f.dim)
			ok = r == f.value || (r == "" && f.value == DirectReferrer)
		default:
			ok = s.visitValue(v, f.dim) == f.value
		}
		if !ok {
			return false
		}
	}
	return true
}

func (s *dayScan) recordMatches(r *Record, fs []filter) bool {
	for i := range fs {
		if !fs[i].matchRecord(r, &s.buf) {
			return false
		}
	}
	return true
}

// resize returns s with length n, reusing its array when it is big enough.
// The contents are not cleared.
func resize[T any](s []T, n int) []T {
	if cap(s) < n {
		return make([]T, n)
	}
	return s[:n]
}

// cleared returns s with length n, every element zero.
func cleared[T any](s []T, n int) []T {
	s = resize(s, n)
	clear(s)
	return s
}
