package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ittrail/sitebin.io/internal/analytics"
	"github.com/ittrail/sitebin.io/internal/store"
)

// The analytics routes of a site, for the edit page's Analytics tab:
//
//	GET /api/sites/{editID}/analytics             state and settings
//	PUT /api/sites/{editID}/analytics             the settings, whole
//	GET /api/sites/{editID}/analytics/overview    totals and a series
//	GET /api/sites/{editID}/analytics/table       one dimension's rows
//	GET /api/sites/{editID}/analytics/event-data  one event's data
//	GET /api/sites/{editID}/analytics/realtime    the last 30 minutes
//	GET /api/sites/{editID}/analytics/funnels     {funnels: the saved funnels' results}
//	GET /api/sites/{editID}/analytics/goals       {goals: the saved goals' results}
//
// All of them go through withEditAuth: the owner, an admin, the edit password
// or an account token, never an MCP OAuth token. Only the state answers a
// locked site, and it carries no record. The data routes read what is stored
// whatever the instance switch, the plan or the site's own switch says: a
// downgrade stops collection, never access to what was collected. They write
// nothing but the engine's day summaries. See "API" in
// docs/superpowers/specs/2026-11-04-site-analytics-design.md.

const (
	maxAnalyticsSpanDays  = 760     // the longest range, whatever the retention
	defaultAnalyticsLimit = 10      // table rows when the caller names no limit
	maxAnalyticsLimit     = 500     // table rows at most
	maxAnalyticsFilter    = 500     // characters of one filter parameter
	maxAnalyticsFilters   = 20      // filter parameters of one query
	maxAnalyticsSettings  = 1 << 20 // bytes of a settings body
)

type analyticsGeoState struct {
	Present     bool   `json:"present"`
	Attribution string `json:"attribution,omitempty"`
}

// analyticsStateBody is the tab's state: whether analytics is available and
// why not, the settings as stored (the mode spelled out; a timezone of ""
// follows the instance's default) with the zone in effect beside them,
// every address the site serves, the instance's limits, and what is stored.
type analyticsStateBody struct {
	Available         bool                      `json:"available"`
	Reason            string                    `json:"reason,omitempty"`
	UpgradeURL        string                    `json:"upgrade_url,omitempty"`
	Settings          store.Analytics           `json:"settings"`
	EffectiveTimezone string                    `json:"effective_timezone"`
	Addresses         []string                  `json:"addresses"`
	Geo               analyticsGeoState         `json:"geo"`
	RetentionDays     int                       `json:"retention_days"`
	DailyCap          int                       `json:"daily_cap"`
	Bytes             int64                     `json:"bytes"`
	FirstDay          string                    `json:"first_day,omitempty"`
	Imported          []analytics.ImportedRange `json:"imported"`
}

// analyticsEventRow is a row of the event table, which counts events, not
// views.
type analyticsEventRow struct {
	Value    string `json:"value"`
	Count    int    `json:"count"`
	Visitors int    `json:"visitors"`
}

// analyticsStateOf answers the state of site. What cannot be read of the
// stored files is logged and left at zero: the settings still show.
func (a *API) analyticsStateOf(site *store.Site) analyticsStateBody {
	reason := a.analyticsReason(site)
	var s store.Analytics
	if site.Meta.Analytics != nil {
		s = *site.Meta.Analytics
	}
	if s.Mode == "" {
		s.Mode = "server"
	}
	out := analyticsStateBody{
		Available:         reason == "",
		Reason:            reason,
		Settings:          s,
		EffectiveTimezone: a.analyticsTZ(site).String(),
		Addresses:         a.siteAddresses(site),
		Geo:               analyticsGeoState{Present: a.analyticsGeo != nil, Attribution: a.analyticsGeo.Attribution()},
		RetentionDays:     a.cfg.AnalyticsRetentionDays,
		DailyCap:          a.cfg.AnalyticsDailyCap,
		Imported:          []analytics.ImportedRange{},
	}
	if reason == analyticsReasonPlan {
		out.UpgradeURL = a.apiAccountHint()
	}
	var err error
	if out.Bytes, out.FirstDay, err = analytics.Storage(site.Dir()); err != nil {
		a.log.Warn("analytics: reading what a site stores", "id", site.ViewID, "err", err)
	}
	if imported, err := analytics.Imported(site.Dir()); err != nil {
		a.log.Warn("analytics: reading a site's imported ranges", "id", site.ViewID, "err", err)
	} else {
		out.Imported = imported
	}
	return out
}

func (a *API) getAnalytics(w http.ResponseWriter, r *http.Request, site *store.Site) {
	writeJSON(w, 200, a.analyticsStateOf(site))
}

// putAnalytics replaces the site's settings with the body, whole, once they
// validate; a bad value, or a field the settings do not have, is a 422
// naming its field, and a body over 1 MiB a 413. It is allowed when only the
// plan lacks analytics, so a site downgraded can still be turned off or have
// its funnels edited.
func (a *API) putAnalytics(w http.ResponseWriter, r *http.Request, site *store.Site) {
	var s store.Analytics
	r.Body = http.MaxBytesReader(w, r.Body, maxAnalyticsSettings)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&s); err != nil {
		var tooBig *http.MaxBytesError
		switch name, unknown := unknownJSONField(err); {
		case errors.As(err, &tooBig):
			writeError(w, 413, fmt.Sprintf("the settings are at most %d bytes", maxAnalyticsSettings))
		case unknown:
			writeJSON(w, 422, map[string]string{"error": fmt.Sprintf("unknown field %q", name), "field": name})
		default:
			writeError(w, 400, "invalid JSON body")
		}
		return
	}
	if field, err := s.Validate(a.siteAddresses(site)); err != nil {
		writeJSON(w, 422, map[string]string{"error": err.Error(), "field": field})
		return
	}
	noteOf(r).detail = analyticsDetail(s)
	if err := a.st.SetAnalytics(site, &s); err != nil {
		respondErr(w, err)
		return
	}
	writeJSON(w, 200, a.analyticsStateOf(site))
}

// unknownJSONField names the field a decoder with DisallowUnknownFields
// refused. encoding/json has no error type for it, only this message; a
// field inside a funnel or a goal is named without its path.
func unknownJSONField(err error) (string, bool) {
	rest, ok := strings.CutPrefix(err.Error(), "json: unknown field ")
	if !ok {
		return "", false
	}
	name, err := strconv.Unquote(rest)
	return name, err == nil
}

// analyticsDetail is a settings change as the site's log names it: "off", or
// "on" with the mode.
func analyticsDetail(s store.Analytics) string {
	if !s.Enabled {
		return "off"
	}
	mode := s.Mode
	if mode == "" {
		mode = "server"
	}
	return "on (" + mode + ")"
}

func (a *API) analyticsOverview(w http.ResponseWriter, r *http.Request, site *store.Site) {
	q, ok := a.analyticsQueryOr400(w, r, site)
	if !ok {
		return
	}
	ov, err := a.engine.Overview(site.Dir(), q, r.URL.Query().Get("compare") == "1")
	if err != nil {
		a.analyticsError(w, site, err)
		return
	}
	writeJSON(w, 200, ov)
}

// analyticsTable answers one dimension's rows, limit (1 to 500, default 10)
// from offset: {rows: [{value, visitors, views}], total}, and for the event
// table rows of {value, count, visitors}.
func (a *API) analyticsTable(w http.ResponseWriter, r *http.Request, site *store.Site) {
	v := r.URL.Query()
	dim := v.Get("dim")
	if dim == "" {
		writeError(w, 400, "dim is required")
		return
	}
	limit, err := intParam(v, "limit", defaultAnalyticsLimit)
	if err != nil || limit < 1 || limit > maxAnalyticsLimit {
		writeError(w, 400, fmt.Sprintf("limit must be 1 to %d", maxAnalyticsLimit))
		return
	}
	offset, err := intParam(v, "offset", 0)
	if err != nil || offset < 0 {
		writeError(w, 400, "offset must be 0 or more")
		return
	}
	q, ok := a.analyticsQueryOr400(w, r, site)
	if !ok {
		return
	}
	rows, total, err := a.engine.Table(site.Dir(), q, dim, limit, offset)
	if err != nil {
		a.analyticsError(w, site, err)
		return
	}
	if dim != "event" {
		writeJSON(w, 200, map[string]any{"rows": rows, "total": total})
		return
	}
	events := make([]analyticsEventRow, len(rows))
	for i, row := range rows {
		events[i] = analyticsEventRow{Value: row.Value, Count: row.Count, Visitors: row.Visitors}
	}
	writeJSON(w, 200, map[string]any{"rows": events, "total": total})
}

func (a *API) analyticsEventData(w http.ResponseWriter, r *http.Request, site *store.Site) {
	name := r.URL.Query().Get("name")
	if name == "" {
		writeError(w, 400, "name is required")
		return
	}
	q, ok := a.analyticsQueryOr400(w, r, site)
	if !ok {
		return
	}
	keys, err := a.engine.EventData(site.Dir(), q, name)
	if err != nil {
		a.analyticsError(w, site, err)
		return
	}
	writeJSON(w, 200, map[string]any{"keys": keys})
}

// analyticsRealtime answers the last 30 minutes up to now; it takes no range
// and no filter.
func (a *API) analyticsRealtime(w http.ResponseWriter, r *http.Request, site *store.Site) {
	rt, err := a.engine.Realtime(site.Dir(), time.Now(), a.analyticsTZ(site))
	if err != nil {
		a.analyticsError(w, site, err)
		return
	}
	writeJSON(w, 200, rt)
}

func (a *API) analyticsFunnels(w http.ResponseWriter, r *http.Request, site *store.Site) {
	q, ok := a.analyticsQueryOr400(w, r, site)
	if !ok {
		return
	}
	var saved []store.AnalyticsFunnel
	if s := site.Meta.Analytics; s != nil {
		saved = s.Funnels
	}
	res, err := a.engine.Funnels(site.Dir(), q, saved)
	if err != nil {
		a.analyticsError(w, site, err)
		return
	}
	writeJSON(w, 200, map[string]any{"funnels": res})
}

func (a *API) analyticsGoals(w http.ResponseWriter, r *http.Request, site *store.Site) {
	q, ok := a.analyticsQueryOr400(w, r, site)
	if !ok {
		return
	}
	var saved []store.AnalyticsGoal
	if s := site.Meta.Analytics; s != nil {
		saved = s.Goals
	}
	res, err := a.engine.Goals(site.Dir(), q, saved)
	if err != nil {
		a.analyticsError(w, site, err)
		return
	}
	writeJSON(w, 200, map[string]any{"goals": res})
}

// analyticsError answers a failed query: a bad one is the caller's (400);
// anything else is logged, and answered without its message, which may name
// server paths.
func (a *API) analyticsError(w http.ResponseWriter, site *store.Site, err error) {
	if errors.Is(err, analytics.ErrBadQuery) {
		writeError(w, 400, strings.TrimPrefix(err.Error(), "analytics: "))
		return
	}
	a.log.Error("analytics: query failed", "id", site.ViewID, "err", err)
	if errors.Is(err, analytics.ErrDayTooLarge) {
		writeError(w, 500, "this day's analytics file is too large to read")
		return
	}
	writeError(w, 500, "could not read the site's analytics")
}

// analyticsQueryOr400 reads the request's query, answering 400 and false
// when it is bad.
func (a *API) analyticsQueryOr400(w http.ResponseWriter, r *http.Request, site *store.Site) (analytics.Query, bool) {
	q, err := a.analyticsQuery(r.URL.Query(), a.analyticsTZ(site), time.Now())
	if err != nil {
		writeError(w, 400, err.Error())
		return q, false
	}
	return q, true
}

// analyticsQuery reads a data route's range and filters, read in loc (the
// site's zone). The range is from and to, local dates (YYYY-MM-DD, both
// inclusive): from's local midnight up to the one after to, built with
// time.Date so a day of 23 or 25 hours is whole. Or it is range=last24h or
// last30m, ending now. A range spans at most analyticsMaxSpan days. There
// are at most 20 filter parameters, each dim:value; an unknown dimension is
// the engine's to refuse.
func (a *API) analyticsQuery(v url.Values, loc *time.Location, now time.Time) (analytics.Query, error) {
	q := analytics.Query{Loc: loc}
	switch rng, from, to := v.Get("range"), v.Get("from"), v.Get("to"); {
	case rng != "" && (from != "" || to != ""):
		return q, errors.New("give either range or from and to")
	case rng == "last24h":
		q.From, q.To = now.Add(-24*time.Hour), now
	case rng == "last30m":
		q.From, q.To = now.Add(-30*time.Minute), now
	case rng != "":
		return q, errors.New(`range must be "last24h" or "last30m"`)
	default:
		f, err := time.Parse(time.DateOnly, from)
		if err != nil {
			return q, errors.New("from must be a date, YYYY-MM-DD")
		}
		t, err := time.Parse(time.DateOnly, to)
		if err != nil {
			return q, errors.New("to must be a date, YYYY-MM-DD")
		}
		if t.Before(f) {
			return q, errors.New("to is before from")
		}
		// Both parsed in UTC, so the difference is whole days.
		if days, limit := int(t.Sub(f)/(24*time.Hour))+1, a.analyticsMaxSpan(); days > limit {
			return q, fmt.Errorf("a range spans at most %d days", limit)
		}
		q.From = time.Date(f.Year(), f.Month(), f.Day(), 0, 0, 0, 0, loc)
		q.To = time.Date(t.Year(), t.Month(), t.Day()+1, 0, 0, 0, 0, loc)
	}
	if len(v["filter"]) > maxAnalyticsFilters {
		return q, fmt.Errorf("at most %d filters", maxAnalyticsFilters)
	}
	for _, raw := range v["filter"] {
		f, err := parseAnalyticsFilter(raw)
		if err != nil {
			return q, err
		}
		q.Filters = append(q.Filters, f)
	}
	return q, nil
}

// analyticsMaxSpan is the longest range in days: the retention, at most 760
// (and 760 for a retention of 0, which keeps every day).
func (a *API) analyticsMaxSpan() int {
	if d := a.cfg.AnalyticsRetentionDays; d > 0 && d < maxAnalyticsSpanDays {
		return d
	}
	return maxAnalyticsSpanDays
}

// parseAnalyticsFilter splits a filter parameter, dim:value, the value taken
// as it is. A query:<name> or data:<key> dimension holds a colon of its own,
// so its name ends at the second colon: a data key with a colon in it cannot
// be filtered on.
func parseAnalyticsFilter(s string) (analytics.Filter, error) {
	if utf8.RuneCountInString(s) > maxAnalyticsFilter {
		return analytics.Filter{}, fmt.Errorf("a filter is at most %d characters", maxAnalyticsFilter)
	}
	dim, value, ok := strings.Cut(s, ":")
	if ok && (dim == "query" || dim == "data") {
		var name string
		name, value, ok = strings.Cut(value, ":")
		dim += ":" + name
	}
	if !ok {
		return analytics.Filter{}, errors.New("a filter is dim:value")
	}
	return analytics.Filter{Dim: dim, Value: value}, nil
}

// intParam reads an integer query parameter, def when it is absent.
func intParam(v url.Values, name string, def int) (int, error) {
	s := v.Get(name)
	if s == "" {
		return def, nil
	}
	return strconv.Atoi(s)
}
