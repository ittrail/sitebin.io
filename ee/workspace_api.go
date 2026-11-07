//go:build ee

package ee

import (
	"bytes"
	"net/http"
	"strings"
	"time"

	"github.com/ittrail/sitebin.io/ee/licensing"
	"github.com/ittrail/sitebin.io/internal/analytics"
	"github.com/ittrail/sitebin.io/internal/ext"
	"github.com/ittrail/sitebin.io/internal/store"
)

// ---- a site's activity, for its owner and its admins ----

// wsActivity is one line of a site's log as the workspace's Activity tab
// shows it. Never the address or the user agent: those are the operator's
// evidence, and the GDPR export redacts them on foreign lines the same way.
type wsActivity struct {
	Time    time.Time  `json:"time"`
	Last    *time.Time `json:"last,omitempty"`
	Times   int        `json:"times"`
	Action  string     `json:"action"`
	Surface string     `json:"surface,omitempty"`
	Auth    string     `json:"auth,omitempty"`
	// Actor is "you", "owner", "admin", "edit password" or "" (anonymous,
	// the background): who, as far as the reader may know it.
	Actor  string `json:"actor,omitempty"`
	Files  int    `json:"files,omitempty"`
	Detail string `json:"detail,omitempty"`
}

func (p *provider) handleSiteActivity(w http.ResponseWriter, r *http.Request) {
	acc, ok := p.currentAccount(r)
	if !ok {
		writeJSONStatus(w, http.StatusUnauthorized, map[string]string{"error": "signed out"})
		return
	}
	viewID := r.PathValue("id")
	info, found := p.host.Sites().Info(viewID)
	owner, admin := p.manages(acc, viewID)
	if !found || (!owner && !admin) {
		// Not "forbidden": a stranger learns nothing about whether it exists.
		writeJSONStatus(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	out := []wsActivity{}
	if sp, ok := p.siteProvenance(); ok {
		es, err := sp.SiteProvenance(viewID)
		if err != nil {
			writeJSONStatus(w, http.StatusInternalServerError, map[string]string{"error": "could not read the site's activity"})
			return
		}
		for i := len(es) - 1; i >= 0; i-- { // newest first
			e := es[i]
			a := wsActivity{Time: e.Time, Last: e.Last, Times: e.Times(), Action: e.Action, Surface: e.Surface, Auth: e.Auth, Files: e.Files, Detail: e.Detail}
			switch {
			case e.Account != "" && e.Account == acc.ID:
				a.Actor = "you"
			case e.Account != "" && e.Account == info.Owner:
				a.Actor = "owner"
			case e.Account != "":
				a.Actor = "admin"
			case e.Auth == "password":
				a.Actor = "edit password"
			}
			out = append(out, a)
		}
	}
	writeJSONStatus(w, http.StatusOK, out)
}

// ---- the operator's console as JSON ----

// uiCapture runs a console handler and keeps the view it would have
// rendered, instead of the page. The JSON is then exactly what the HTML
// console shows — same filters, same rows, same refusals — with no second
// copy of how a row is built.
type uiCapture struct {
	header http.Header
	status int
	body   bytes.Buffer
	data   any
	got    bool
}

func (c *uiCapture) Header() http.Header {
	if c.header == nil {
		c.header = http.Header{}
	}
	return c.header
}
func (c *uiCapture) WriteHeader(code int) {
	if c.status == 0 {
		c.status = code
	}
}
func (c *uiCapture) Write(b []byte) (int, error) {
	if c.status == 0 {
		c.status = http.StatusOK
	}
	return c.body.Write(b)
}

func (p *provider) captured(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c := &uiCapture{}
		h(c, r)
		if !c.got {
			code := c.status
			if code == 0 || code < 400 {
				code = http.StatusInternalServerError
			}
			writeJSONStatus(w, code, map[string]string{"error": strings.TrimSpace(c.body.String())})
			return
		}
		writeJSONStatus(w, http.StatusOK, c.data)
	}
}

func (p *provider) adminAPIRoutes(routes map[string]http.Handler) {
	routes["GET /account/api/admin/register"] = p.captured(p.handleAdmin)
	routes["GET /account/api/admin/reports"] = p.captured(p.handleAdminReports)
	routes["GET /account/api/admin/containers"] = p.captured(p.handleAdminContainers)
	routes["GET /account/api/admin/health"] = http.HandlerFunc(p.handleAdminHealth)
	routes["GET /account/api/admin/backups"] = http.HandlerFunc(p.handleAdminBackups)
}

type wsHealth struct {
	License   wsLicense `json:"license"`
	DataFree  int64     `json:"data_free"`  // bytes free on the data volume, -1 unknown
	DataTotal int64     `json:"data_total"` // the volume's size, -1 unknown
	// Backup is the last complete `sitebin backup`, from the note it leaves
	// in the data folder; nil when none has run on this data.
	Backup        *store.BackupMark `json:"backup"`
	Mailer        bool              `json:"mailer"`
	FormsMail     bool              `json:"forms_mail"`
	Containers    bool              `json:"containers"`
	Running       int               `json:"running"`
	Stopped       int               `json:"stopped"`
	Failing       int               `json:"failing"`
	RetentionDays int               `json:"retention_days"`
}

type wsLicense struct {
	State      string    `json:"state"`
	Restricted bool      `json:"restricted"`
	Plan       string    `json:"plan,omitempty"`
	ExpiresAt  time.Time `json:"expires_at,omitzero"`
	GraceUntil time.Time `json:"grace_until,omitzero"`
	Notice     string    `json:"notice,omitempty"`
}

// handleAdminHealth is the instance's state for the operator's overview:
// what the console already knows, gathered on one card.
func (p *provider) handleAdminHealth(w http.ResponseWriter, r *http.Request) {
	if _, ok := p.adminAccount(r); !ok {
		writeJSONStatus(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	st := p.licenseStatus()
	h := wsHealth{
		License: wsLicense{State: licenseStateWord(st.State), Restricted: st.Restricted, Plan: st.Plan,
			ExpiresAt: st.ExpiresAt, GraceUntil: st.GraceUntil, Notice: st.Notice()},
		DataFree:      -1,
		Mailer:        p.mailer != nil,
		Containers:    p.containerLimits != nil,
		RetentionDays: int(p.host.LockRetention() / (24 * time.Hour)),
	}
	if free, err := analytics.DiskFree(p.host.DataDir()); err == nil {
		h.DataFree = int64(free)
	}
	h.DataTotal = -1
	if total, err := analytics.DiskSize(p.host.DataDir()); err == nil {
		h.DataTotal = int64(total)
	}
	if m, ok, err := store.ReadBackupMark(p.host.DataDir()); err == nil && ok {
		h.Backup = &m
	}
	if fm, ok := p.host.(ext.FormsMailHost); ok {
		h.FormsMail = fm.FormsMailConfigured()
	}
	if h.Containers {
		if cs, err := p.host.Sites().ContainerSites(); err == nil {
			for _, s := range cs {
				if !s.Container || !s.Enabled || s.Locked || s.Expired {
					continue
				}
				for _, svc := range s.Observed.Services {
					switch svc.State {
					case "running":
						h.Running++
					case "restarting", "dead":
						h.Failing++
					default:
						h.Stopped++
					}
				}
			}
		}
	}
	writeJSONStatus(w, http.StatusOK, h)
}

func licenseStateWord(s licensing.State) string { return string(s) }
