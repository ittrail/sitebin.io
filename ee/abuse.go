//go:build ee

package ee

import (
	"log/slog"
	"net/http"

	"github.com/ittrail/sitebin.io/internal/ext"
	"github.com/ittrail/sitebin.io/internal/store"
)

// The register's half of abuse detection: the Reports tab, dismissing the
// abuse guard's findings, and naming accounts in the core's alert mails.
// The detection itself is core; see
// docs/superpowers/specs/2026-09-29-abuse-detection.md.

var _ ext.AccountDirectory = (*provider)(nil)

// AccountEmail names an account for the operator's abuse alerts.
func (p *provider) AccountEmail(accountID string) (string, bool) {
	if accountID == "" || p.accounts == nil {
		return "", false
	}
	acc, err := p.accounts.ByID(accountID)
	if err != nil {
		return "", false
	}
	return acc.Email, true
}

// maxShownReports bounds the Reports tab. Reports are kept 14 days and
// capped at 10,000 on disk; a page of all of them helps nobody.
const maxShownReports = 300

type reportRow struct {
	ext.AbuseReport
	TimeText string
	// Site is the resolved site, when the report named one that still
	// exists; OwnerLabel its owner's address.
	Site       *ext.SiteInfo
	OwnerLabel string
	LockText   string
	// LockReason prefills the one-click lock.
	LockReason string
}

type reportsView struct {
	Email   string
	CSRF    string
	Rows    []reportRow
	Total   int
	Flash   string
	Figures instanceFigures
}

// handleAdminReports lists the abuse reports, newest first, each with the
// site it resolved to and a one-click lock for a site still served.
func (p *provider) handleAdminReports(w http.ResponseWriter, r *http.Request) {
	acc, ok := p.adminAccount(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	reps, err := p.host.Sites().Reports()
	if err != nil {
		slog.Error("admin console could not read the reports", "admin", acc.ID, "err", err)
		http.Error(w, "could not read the abuse reports", http.StatusInternalServerError)
		return
	}
	v := reportsView{Email: acc.Email, CSRF: p.csrf(acc), Total: len(reps), Flash: r.URL.Query().Get("flash")}
	v.Figures.Reports = len(reps)
	if len(reps) > maxShownReports {
		reps = reps[:maxShownReports]
	}
	sites := map[string]*ext.SiteInfo{}
	for _, rep := range reps {
		row := reportRow{AbuseReport: rep, TimeText: rep.Time.Local().Format("2006-01-02 15:04")}
		if rep.ViewID != "" {
			info, seen := sites[rep.ViewID]
			if !seen {
				if i, ok := p.host.Sites().Info(rep.ViewID); ok {
					info = &i
				}
				sites[rep.ViewID] = info
			}
			if info != nil {
				row.Site = info
				row.OwnerLabel = "anonymous"
				if info.Owner != "" {
					row.OwnerLabel = info.Owner
					if email, ok := p.AccountEmail(info.Owner); ok {
						row.OwnerLabel = email
					}
				}
				if info.Locked != nil {
					row.LockText = lockText(info.Locked)
				}
				// Prefilled, and shown in an editable field before it is
				// used: an API report's reason is anyone's free text, and a
				// lock reason is published to the owner.
				row.LockReason = store.CleanLockReason("abuse report: " + rep.Reason)
			}
		}
		v.Rows = append(v.Rows, row)
	}
	p.securityHeaders(w)
	reportsTmpl.Execute(w, v)
}

// handleAdminReview dismisses a site's abuse-guard findings: the operator has
// looked, and the same content is not flagged or held again. It changes no
// lock.
func (p *provider) handleAdminReview(w http.ResponseWriter, r *http.Request) {
	acc, ok := p.adminAccount(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if !p.checkCSRF(r, acc) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	viewID := r.PathValue("id")
	if err := p.host.Sites().ClearFindings(viewID); err != nil {
		slog.Error("admin could not dismiss findings", "admin", acc.ID, "site", viewID, "err", err)
		http.Error(w, "could not dismiss the findings", http.StatusInternalServerError)
		return
	}
	slog.Info("admin dismissed a site's abuse findings", "admin", acc.ID, "site", viewID)
	p.redirect(w, r, "/account/admin?flash=reviewed"+listParams(r))
}

// findingText is one finding as a row shows it.
func findingText(f ext.ScanFinding) string {
	out := f.Rule + " (" + f.Severity + ") · " + f.Path
	if f.Excerpt != "" {
		ex := f.Excerpt
		if len([]rune(ex)) > 90 {
			ex = string([]rune(ex)[:90]) + "…"
		}
		out += " — " + ex
	}
	return out
}
