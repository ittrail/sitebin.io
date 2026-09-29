package httpapi

import (
	"encoding/json"
	"net/http"
	"net/mail"
	"net/url"
	"strings"
	"time"

	"github.com/ittrail/sitebin.io/internal/store"
)

// report accepts a public abuse/takedown report as JSON. The report page
// (reportpage.go) is the same thing for people; both go through fileReport.
func (a *API) report(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Target  string `json:"target"`
		Reason  string `json:"reason"`
		Details string `json:"details"`
		Contact string `json:"contact"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, 400, `body must be {"target": "...", "reason": "...", "details": "...", "contact": "you@example.com (optional)"}`)
		return
	}
	if err := a.fileReport(r, body.Target, body.Reason, body.Details, body.Contact, store.ReportViaAPI); err != nil {
		respondErr(w, err)
		return
	}
	writeJSON(w, 202, map[string]string{"status": "received"})
}

// fileReport is every report's one path. It is rate-limited per source and
// instance-wide, writes the report to /data/reports (`sitebin reports`, the
// register's Reports tab) and mails the operator. The same source reporting
// the same target again within a day is acknowledged and not stored: one
// report is one report, however many times the button is pressed.
func (a *API) fileReport(r *http.Request, target, reason, details, contact, via string) error {
	if !a.reportLimiter.Allow(clientIP(r)) || !a.reportGlobal.Allow("all") {
		return &apiError{429, "too many reports, please try again later"}
	}
	target = strings.TrimSpace(target)
	reason = strings.TrimSpace(reason)
	contact = strings.TrimSpace(contact)
	if target == "" || reason == "" {
		return &apiError{400, "target and reason are required"}
	}
	if len(reason) > 500 || len(details) > 4000 || len(target) > 400 || len(contact) > 254 {
		return &apiError{400, "report fields too long"}
	}
	if contact != "" {
		if addr, err := mail.ParseAddress(contact); err != nil || addr.Address != contact {
			return &apiError{400, "contact must be an email address, or left empty"}
		}
	}
	rep := store.Report{
		Time:    time.Now().UTC(),
		Target:  target,
		Reason:  reason,
		Details: details,
		Source:  store.AnonymizeIP(clientIP(r)),
		Contact: contact,
		Via:     via,
	}
	if site := a.resolveTarget(target); site != nil {
		rep.ViewID = site.ViewID
	}
	if !a.reportDedupe.Allow(strings.ToLower(rep.Target) + "|" + rep.Source) {
		return nil
	}
	if err := a.st.AddReport(rep); err != nil {
		a.log.Error("save report", "err", err)
		return &apiError{500, "could not record the report"}
	}
	a.log.Warn("abuse report", "target", target, "site", rep.ViewID, "reason", reason, "via", via)
	a.alerts.notify(a.reportAlert(rep))
	return nil
}

// resolveTarget best-effort maps a reported URL / domain / id to a site.
func (a *API) resolveTarget(target string) *store.Site {
	host := target
	if u, err := url.Parse(target); err == nil && u.Host != "" {
		host = u.Host
		if site := a.siteByURL(target); site != nil {
			return site
		}
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
