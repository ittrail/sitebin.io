package httpapi

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/ittrail/sitebin.io/internal/store"
)

// report accepts a public abuse/takedown report. It is rate-limited and writes
// the report to /data/reports for the operator to review (`sitebin reports`).
func (a *API) report(w http.ResponseWriter, r *http.Request) {
	if !a.reportLimiter.Allow(clientIP(r)) {
		writeError(w, 429, "too many reports, please try again later")
		return
	}
	var body struct {
		Target  string `json:"target"`
		Reason  string `json:"reason"`
		Details string `json:"details"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, 400, `body must be {"target": "...", "reason": "...", "details": "..."}`)
		return
	}
	body.Target = strings.TrimSpace(body.Target)
	body.Reason = strings.TrimSpace(body.Reason)
	if body.Target == "" || body.Reason == "" {
		writeError(w, 400, "target and reason are required")
		return
	}
	if len(body.Reason) > 500 || len(body.Details) > 4000 || len(body.Target) > 400 {
		writeError(w, 400, "report fields too long")
		return
	}
	rep := store.Report{
		Time:    time.Now().UTC(),
		Target:  body.Target,
		Reason:  body.Reason,
		Details: body.Details,
		IP:      clientIP(r),
	}
	if site := a.resolveTarget(body.Target); site != nil {
		rep.ViewID = site.ViewID
	}
	if err := a.st.AddReport(rep); err != nil {
		a.log.Error("save report", "err", err)
		writeError(w, 500, "could not record the report")
		return
	}
	a.log.Warn("abuse report", "target", body.Target, "site", rep.ViewID, "reason", body.Reason)
	writeJSON(w, 202, map[string]string{"status": "received"})
}

// resolveTarget best-effort maps a reported URL / domain / id to a site.
func (a *API) resolveTarget(target string) *store.Site {
	host := target
	if u, err := url.Parse(target); err == nil && u.Host != "" {
		host = u.Host
	}
	if site, err := a.siteByHost(host); err == nil {
		return site
	}
	if site, err := a.st.ByViewID(strings.ToLower(target)); err == nil {
		return site
	}
	if site, err := a.st.ByDomain(host); err == nil {
		return site
	}
	return nil
}
