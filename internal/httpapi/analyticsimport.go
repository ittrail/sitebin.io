package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"github.com/ittrail/sitebin.io/internal/analytics"
	"github.com/ittrail/sitebin.io/internal/ids"
	"github.com/ittrail/sitebin.io/internal/store"
)

// POST /internal/analytics/import: a site's Umami history, imported by the
// running server, because only it holds the stats lock the recorder appends
// under. `sitebin analytics import-umami` streams the export here over the
// internal listener, which is bound to loopback and never proxied. See
// "Umami import, per site" in
// docs/superpowers/specs/2026-11-04-site-analytics-design.md and the runbook
// in docs/analytics-umami-import.md.
//
// The body is multipart, its parts in this order: site (the view id), until
// (RFC3339, optional), reports (Umami's report rows, a JSON array,
// optional), then events (the export, JSON Lines), which is streamed and
// must come last. The answer is the analytics.ImportReport.

const (
	analyticsImportTimeout = time.Hour // an export streams for as long as this
	maxImportField         = 1 << 10   // bytes of the site or until part
	maxImportReports       = 1 << 20   // bytes of the reports part
)

func (a *API) analyticsImport(w http.ResponseWriter, r *http.Request) {
	// Loopback only, whatever address the internal listener is bound to: the
	// CLI runs in the container, and nothing else may write a site's
	// analytics.
	if !fromLoopback(r) {
		writeError(w, 403, "the import is accepted from a loopback address only: run sitebin analytics import-umami inside the container")
		return
	}
	// The internal listener's read timeout is for Caddy's subrequests; a
	// whole site's history takes longer to send.
	_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(analyticsImportTimeout))
	mr, err := r.MultipartReader()
	if err != nil {
		writeError(w, 400, "a multipart body with site and events is expected")
		return
	}
	var (
		site    *store.Site
		until   time.Time
		reports []analytics.UmamiReport
	)
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			writeError(w, 400, "events is required")
			return
		}
		if err != nil {
			writeError(w, 400, "unreadable multipart body")
			return
		}
		switch name := part.FormName(); name {
		case "site":
			id, err := readImportField(part, maxImportField)
			if err != nil || !ids.ValidID(id) {
				writeError(w, 400, "site must be a view id")
				return
			}
			if site, err = a.st.ByViewID(id); err != nil {
				if errors.Is(err, store.ErrNotFound) {
					writeError(w, 404, "no site with that view id")
					return
				}
				a.log.Error("analytics import: loading the site", "id", id, "err", err)
				writeError(w, 500, "could not read the site")
				return
			}
			// A site whose plan lacks analytics may be imported into: the
			// operator moves sites, and what is stored stays readable.
			if reason := a.analyticsReason(site); reason == analyticsReasonInstance || reason == analyticsReasonLocked {
				writeImportRefusal(w, reason, nil)
				return
			}
		case "until":
			v, err := readImportField(part, maxImportField)
			if err == nil {
				until, err = time.Parse(time.RFC3339, v)
			}
			if err != nil {
				writeError(w, 400, "until must be an RFC 3339 time")
				return
			}
		case "reports":
			if reports, err = readImportReports(part); err != nil {
				writeError(w, 400, err.Error())
				return
			}
		case "events":
			if site == nil {
				writeError(w, 400, "site must come before events")
				return
			}
			a.importUmami(w, site, until, reports, part)
			return
		default:
			writeError(w, 400, fmt.Sprintf("unknown part %q", name))
			return
		}
	}
}

// fromLoopback reports whether the request's peer is a loopback address
// (127.0.0.0/8 or ::1, an IPv4-mapped one included). This is the TCP peer,
// never a header a client could set.
func fromLoopback(r *http.Request) bool {
	ap, err := netip.ParseAddrPort(r.RemoteAddr)
	return err == nil && ap.Addr().Unmap().IsLoopback()
}

// importUmami imports the events into site, then adds the reports to its
// settings, and answers the report. The cut is until or the site's first own
// record, whichever is earlier.
func (a *API) importUmami(w http.ResponseWriter, site *store.Site, until time.Time, reports []analytics.UmamiReport, events io.Reader) {
	// One import per site at a time: a second would interleave its days with
	// the first's.
	if _, busy := a.imports.LoadOrStore(site.ViewID, struct{}{}); busy {
		writeError(w, 409, "an import into this site is running")
		return
	}
	defer a.imports.Delete(site.ViewID)

	// Where Sitebin's own records begin must be known: an import past it
	// counts twice, so a day file that cannot be read stops it here.
	cut := until
	first, ok, err := analytics.FirstOwnRecord(site.Dir())
	if err != nil {
		a.log.Error("analytics import: finding the site's first own record", "id", site.ViewID, "err", err)
		writeError(w, 500, "nothing imported: the site's day files could not be read to find its first own record: "+strings.TrimPrefix(err.Error(), "analytics: "))
		return
	}
	if ok && (cut.IsZero() || first.Before(cut)) {
		cut = first
	}
	var extra []string
	if s := site.Meta.Analytics; s != nil {
		extra = s.Query
	}
	withSite := func(fn func(string) error) (bool, error) { return a.st.WithStatsLock(site.ViewID, fn) }
	rep, err := analytics.Import(site.Dir(), a.analyticsTZ(site), a.siteAddresses(site), extra, events,
		cut, a.cfg.AnalyticsRetentionDays, time.Now(), withSite)
	if rep.Days > 0 {
		// What was written stands, even when the import stopped part way.
		a.invalidateDays(site, rep.From, rep.To)
		rng := analytics.ImportedRange{From: rep.From, To: rep.To}
		if _, rerr := a.st.WithStatsLock(site.ViewID, func(dir string) error { return analytics.AddImported(dir, rng) }); rerr != nil && err == nil {
			err = fmt.Errorf("the records are imported, but their range was not recorded: %w", rerr)
		}
	}
	if err != nil {
		code := 500
		switch {
		case errors.Is(err, analytics.ErrBadExport):
			code = 400
		case errors.Is(err, store.ErrNotFound):
			code = 404
		}
		a.log.Warn("analytics import failed", "id", site.ViewID, "days", rep.Days, "records", rep.Records, "err", err)
		writeJSON(w, code, map[string]any{"error": strings.TrimPrefix(err.Error(), "analytics: "), "report": withLists(rep)})
		return
	}
	if len(reports) > 0 {
		err := a.st.Update(site, func(m *store.Meta) error {
			// The lock is read again where meta is written: one that came
			// during the import freezes the settings.
			if m.IsLocked() {
				return errImportLocked
			}
			var s store.Analytics
			if m.Analytics != nil {
				s = *m.Analytics
			}
			var merged store.Analytics
			merged, rep.Funnels, rep.Goals, rep.SkippedReports = analytics.MergeUmamiReports(s, reports)
			if len(rep.Funnels)+len(rep.Goals) == 0 {
				return errNothingToMerge
			}
			m.Analytics = &merged
			return nil
		})
		switch {
		case errors.Is(err, errImportLocked):
			writeImportRefusal(w, analyticsReasonLocked, &rep)
			return
		case err != nil && !errors.Is(err, errNothingToMerge):
			a.log.Error("analytics import: adding the reports", "id", site.ViewID, "err", err)
			writeJSON(w, 500, map[string]any{"error": "the records are imported, but the reports could not be added", "report": withLists(rep)})
			return
		}
	}
	a.log.Info("analytics import", "id", site.ViewID, "days", rep.Days, "records", rep.Records, "from", rep.From, "to", rep.To,
		"cut", cut, "skipped_after_cut", rep.SkippedAfterCut, "skipped_retention", rep.SkippedRetention,
		"funnels", len(rep.Funnels), "goals", len(rep.Goals))
	writeJSON(w, 200, withLists(rep))
}

// errNothingToMerge leaves meta.json unwritten when no report was added;
// errImportLocked when the site was locked meanwhile.
var (
	errNothingToMerge = errors.New("no report added")
	errImportLocked   = errors.New("the site is locked")
)

// writeImportRefusal answers an import refused for reason (an
// analyticsReason): 409 with the reason. rep is the report of a site locked
// during the import, whose records were written before the lock was seen.
func writeImportRefusal(w http.ResponseWriter, reason string, rep *analytics.ImportReport) {
	body := map[string]any{"reason": reason}
	switch {
	case rep != nil:
		body["error"] = "the records are imported, but the site was locked during the import, so its funnels and goals were not added"
		body["report"] = withLists(*rep)
	case reason == analyticsReasonInstance:
		body["error"] = "analytics is turned off on this instance (SITEBIN_ANALYTICS=false)"
	default:
		body["error"] = "the site is locked: it is an evidence hold, and nothing is imported into it"
	}
	writeJSON(w, 409, body)
}

// invalidateDays drops every local date from..to (inclusive) from the
// engine: the days an import rewrote are among them, and a rewrite could
// keep a file's size and mtime.
func (a *API) invalidateDays(site *store.Site, from, to string) {
	d, err := time.Parse(time.DateOnly, from)
	if err != nil {
		return
	}
	for day := d.Format(time.DateOnly); day <= to; day = d.Format(time.DateOnly) {
		a.engine.Invalidate(site.Dir(), day)
		d = d.AddDate(0, 0, 1)
	}
}

// withLists answers the report's lists as [] rather than null.
func withLists(rep analytics.ImportReport) analytics.ImportReport {
	for _, l := range []*[]string{&rep.Funnels, &rep.Goals, &rep.SkippedReports} {
		if *l == nil {
			*l = []string{}
		}
	}
	return rep
}

// readImportField reads a short part, at most max bytes, trimmed.
func readImportField(r io.Reader, max int64) (string, error) {
	b, err := io.ReadAll(io.LimitReader(r, max+1))
	if err != nil {
		return "", err
	}
	if int64(len(b)) > max {
		return "", errors.New("too long")
	}
	return strings.TrimSpace(string(b)), nil
}

// readImportReports reads the reports part: a JSON array of Umami's report
// rows, or nothing (Umami's json_agg over no rows is null).
func readImportReports(r io.Reader) ([]analytics.UmamiReport, error) {
	b, err := io.ReadAll(io.LimitReader(r, maxImportReports+1))
	if err != nil {
		return nil, errors.New("unreadable reports")
	}
	if len(b) > maxImportReports {
		return nil, fmt.Errorf("reports are at most %d bytes", maxImportReports)
	}
	var out []analytics.UmamiReport
	if s := strings.TrimSpace(string(b)); s != "" {
		if err := json.Unmarshal([]byte(s), &out); err != nil {
			return nil, errors.New("reports must be a JSON array of Umami report rows (type, name, parameters)")
		}
	}
	return out, nil
}
