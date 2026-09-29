//go:build ee

package ee

import (
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/ittrail/sitebin.io/ee/account"
	"github.com/ittrail/sitebin.io/internal/ext"
	"github.com/ittrail/sitebin.io/internal/provenance"
)

// Provenance in the instance register: where each site came from and who last
// changed it, a site's whole trail, an account's log, and the address search
// that lists every site and account seen from one address or range. All
// server-rendered: the console's CSP is script-src 'none'. See
// docs/superpowers/specs/2026-09-29-provenance-csp-apex.md.

// quickMint is how soon after sign-up a token mint is flagged: a person
// exploring the dashboard takes longer than a script does.
const quickMint = 10 * time.Minute

// siteProvenance returns the core's optional provenance seam, if any.
func (p *provider) siteProvenance() (ext.SiteProvenance, bool) {
	sp, ok := p.host.Sites().(ext.SiteProvenance)
	return sp, ok
}

// rowProv is what a register row shows about provenance, precomputed so the
// register's template needs no functions.
type rowProv struct {
	CreatedIP   string
	CreatedText string // "ui · session · 2026-09-28 08:49", after the address
	CreatedUA   string
	LastIP      string
	LastText    string // "upload · 2026-09-28 18:31"
	LastUA      string
	Trail       string
}

// rowProvenance reads the log of every row shown. It runs after filtering,
// so a narrowed register reads only what it displays.
func (p *provider) rowProvenance(rows []adminRow) map[string]*rowProv {
	out := map[string]*rowProv{}
	sp, ok := p.siteProvenance()
	if !ok {
		return out
	}
	for _, row := range rows {
		es, err := sp.SiteProvenance(row.ViewID)
		if err != nil || len(es) == 0 {
			continue
		}
		rp := rowProv{Trail: "/account/admin/sites/" + row.ViewID + "/trail"}
		first := es[0]
		if first.Action == provenance.ActionCreate {
			rp.CreatedIP = first.IP
			rp.CreatedText = joinDot(first.Surface, first.Auth)
			rp.CreatedUA = first.UA
		}
		if last := es[len(es)-1]; len(es) > 1 || last.Action != provenance.ActionCreate {
			rp.LastIP = last.IP
			rp.LastText = joinDot(last.Action, last.Latest().Local().Format("2006-01-02 15:04"))
			rp.LastUA = last.UA
		}
		out[row.ViewID] = &rp
	}
	return out
}

func joinDot(parts ...string) string {
	var keep []string
	for _, s := range parts {
		if s != "" {
			keep = append(keep, s)
		}
	}
	return strings.Join(keep, " · ")
}

// provRow is one log entry as the trail and account pages show it.
type provRow struct {
	When      string
	Repeat    string
	Action    string
	What      string
	Via       string
	AccountID string
	Account   string
	IP        string
	UA        string
	Site      string
	Flag      string // a warning worth the operator's eye
}

// provRows renders entries newest first, naming acting accounts by email.
func (p *provider) provRows(es []provenance.Entry) []provRow {
	emails := map[string]string{}
	out := make([]provRow, 0, len(es))
	for i := len(es) - 1; i >= 0; i-- {
		e := es[i]
		row := provRow{
			When:      e.Time.Local().Format("2006-01-02 15:04:05"),
			Action:    e.Action,
			Via:       joinDot(e.Surface, e.Auth),
			AccountID: e.Account,
			IP:        e.IP,
			UA:        e.UA,
			Site:      e.Site,
		}
		if e.N > 1 {
			row.Repeat = fmt.Sprintf("×%d until %s", e.N, e.Latest().Local().Format("15:04:05"))
		}
		what := e.Detail
		if e.Files > 0 {
			what = joinDot(fmt.Sprintf("%d file(s)", e.Files), e.Detail)
		}
		row.What = what
		if e.Account != "" {
			if _, ok := emails[e.Account]; !ok {
				emails[e.Account] = e.Account
				if acc, err := p.accounts.ByID(e.Account); err == nil {
					emails[e.Account] = acc.Email
				}
			}
			row.Account = emails[e.Account]
		}
		out = append(out, row)
	}
	return out
}

// accountProv is an account's log as the console shows it.
type accountProv struct {
	ID        string
	Email     string
	Created   string
	Suspended string
	Signup    *provRow
	Tokens    []provRow
	Signins   []provRow
	Rows      []provRow
}

// maxSignins is how many sign-ins the account panel lists.
const maxSignins = 10

func (p *provider) accountProvenance(acc *account.Account) accountProv {
	ap := accountProv{ID: acc.ID, Email: acc.Email, Created: acc.CreatedAt.Local().Format("2006-01-02 15:04:05")}
	if acc.Suspended() {
		ap.Suspended = "suspended " + acc.SuspendedAt.Local().Format("2006-01-02 15:04")
		if acc.SuspendedReason != "" {
			ap.Suspended += ": " + acc.SuspendedReason
		}
	}
	es, err := p.accounts.Provenance(acc.ID)
	if err != nil {
		slog.Error("admin: could not read an account's provenance", "account", acc.ID, "err", err)
	}
	signupAt := acc.CreatedAt
	for _, e := range es {
		if e.Action == provenance.ActionSignup {
			signupAt = e.Time
			break
		}
	}
	rows := p.provRows(es)
	for i, e := range es {
		row := rows[len(rows)-1-i]
		switch e.Action {
		case provenance.ActionSignup:
			r := row
			ap.Signup = &r
		case provenance.ActionTokenMint:
			d := e.Time.Sub(signupAt)
			row.Flag = "minted " + humanDelay(d) + " after sign-up"
			if d >= quickMint {
				row.Flag = ""
				row.What = joinDot(row.What, humanDelay(d)+" after sign-up")
			}
			rows[len(rows)-1-i] = row
			ap.Tokens = append(ap.Tokens, row)
		}
	}
	for _, row := range rows {
		if row.Action == provenance.ActionSignin && len(ap.Signins) < maxSignins {
			ap.Signins = append(ap.Signins, row)
		}
	}
	ap.Rows = rows
	return ap
}

// humanDelay says how long something took, in the unit a person would use.
func humanDelay(d time.Duration) string {
	switch {
	case d < 0:
		return "0 s"
	case d < 90*time.Second:
		return fmt.Sprintf("%d s", int(d.Seconds()))
	case d < 90*time.Minute:
		return fmt.Sprintf("%d min", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%d h", int(d.Hours()))
	}
	return fmt.Sprintf("%d days", int(d.Hours()/24))
}

// ipAccount is one account the address search found, with what it did there.
type ipAccount struct {
	ID        string
	Email     string
	Suspended bool
	Rows      []provRow
}

// ipPanel is the address search's account list.
type ipPanel struct {
	Query    string
	Sites    int
	Accounts []ipAccount
}

// ipSearch lists every account seen from m — in its own log (sign-up,
// sign-in, token, its sites' creation and deletion) or as the acting account
// on a matching site entry.
func (p *provider) ipSearch(m provenance.Match, seen map[string][]provenance.Entry) *ipPanel {
	panel := &ipPanel{Query: m.String(), Sites: len(seen)}
	found := map[string][]provenance.Entry{}
	// A site's creation is in its own log and mirrored into its owner's:
	// the pair is one event, shown once.
	mirrored := map[string]bool{}
	if ids, err := p.accounts.ListIDs(); err == nil {
		for _, id := range ids {
			es, err := p.accounts.Provenance(id)
			if err != nil {
				continue
			}
			if hit := m.Filter(es); len(hit) > 0 {
				found[id] = append(found[id], hit...)
				for _, e := range hit {
					if e.Site != "" {
						mirrored[e.Site+"|"+e.Time.String()] = true
					}
				}
			}
		}
	}
	for site, es := range seen {
		for _, e := range es {
			if e.Account == "" || mirrored[site+"|"+e.Time.String()] {
				continue
			}
			e.Site = site
			found[e.Account] = append(found[e.Account], e)
		}
	}
	for id, es := range found {
		sort.SliceStable(es, func(i, j int) bool { return es[i].Time.Before(es[j].Time) })
		ia := ipAccount{ID: id, Email: id, Rows: p.provRows(es)}
		if acc, err := p.accounts.ByID(id); err == nil {
			ia.Email = acc.Email
			ia.Suspended = acc.Suspended()
		}
		panel.Accounts = append(panel.Accounts, ia)
	}
	sort.Slice(panel.Accounts, func(i, j int) bool { return panel.Accounts[i].Email < panel.Accounts[j].Email })
	return panel
}

// ---- pages ----

type trailView struct {
	Site    ext.SiteInfo
	Owner   string
	Lock    string
	Rows    []provRow
	Account *accountProv
}

func (p *provider) handleAdminTrail(w http.ResponseWriter, r *http.Request) {
	acc, ok := p.adminAccount(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	viewID := r.PathValue("id")
	info, found := p.host.Sites().Info(viewID)
	if !found {
		http.NotFound(w, r)
		return
	}
	v := trailView{Site: info}
	if sp, ok := p.siteProvenance(); ok {
		es, err := sp.SiteProvenance(viewID)
		if err != nil {
			slog.Error("admin: could not read a site's provenance", "admin", acc.ID, "site", viewID, "err", err)
		}
		v.Rows = p.provRows(es)
	}
	if info.Locked != nil {
		v.Lock = lockText(info.Locked)
	}
	if info.Owner != "" {
		v.Owner = info.Owner
		if owner, err := p.accounts.ByID(info.Owner); err == nil {
			v.Owner = owner.Email
			ap := p.accountProvenance(owner)
			v.Account = &ap
		}
	}
	p.securityHeaders(w)
	trailTmpl.Execute(w, v)
}

func (p *provider) handleAdminAccount(w http.ResponseWriter, r *http.Request) {
	if _, ok := p.adminAccount(r); !ok {
		http.NotFound(w, r)
		return
	}
	acc, err := p.accounts.ByID(r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	ap := p.accountProvenance(acc)
	p.securityHeaders(w)
	accountProvTmpl.Execute(w, ap)
}

// ipLink is the register's address search for ip.
func ipLink(ip string) string { return "/account/admin?q=" + url.QueryEscape(ip) }

var provFuncs = template.FuncMap{"iplink": ipLink}

// provConsoleCSS is the trail and account pages' own layout, on the console's.
const provConsoleCSS = `
<style>
  .adm .back { font: 12px var(--mono); color: var(--ink-dim); }
  .adm .meta { display: flex; flex-wrap: wrap; gap: 6px 18px; font: 12px var(--mono); color: var(--ink-dim); margin: 6px 0 20px; }
  .adm .meta b { color: var(--ink); font-weight: 600; }
  .adm .meta .lockt { color: var(--danger); }
  .adm h2 { font: 650 17px var(--display); margin: 26px 0 10px; letter-spacing: -.01em; }
  .adm .stub { border: 1px dashed var(--line); border-radius: 10px; padding: 12px 16px; margin-bottom: 12px; background: linear-gradient(160deg, #151e33, #101727); font: 12px var(--mono); }
  .adm .stub .k { display: block; font: 600 10px var(--mono); letter-spacing: .14em; text-transform: uppercase; color: var(--ink-faint); margin-bottom: 4px; }
  .adm .stub .ua { display: block; color: var(--ink-faint); margin-top: 3px; overflow-wrap: anywhere; }
  .adm table.log { width: 100%; border-collapse: collapse; font-size: 12px; background: var(--bg-card); border: 1px solid var(--line-soft); border-radius: var(--radius); overflow: hidden; }
  .adm table.log th { text-align: left; font: 600 10px var(--mono); letter-spacing: .12em; text-transform: uppercase; color: var(--ink-faint); background: var(--bg-raise); padding: 8px 10px; border-bottom: 1px solid var(--line); }
  .adm table.log td { padding: 7px 10px; border-top: 1px solid var(--line-soft); vertical-align: top; font-family: var(--mono); color: var(--ink-dim); overflow-wrap: anywhere; }
  .adm table.log td.act { color: var(--ink); white-space: nowrap; }
  .adm table.log td .rep { display: block; color: var(--ink-faint); font-size: 11px; }
  .adm table.log td .ua { display: block; color: var(--ink-faint); font-size: 11px; max-width: 360px; }
  .adm .flagged { color: var(--danger); font-weight: 600; }
  .adm .empty { padding: 20px 16px; text-align: center; color: var(--ink-faint); font: 13px var(--mono); }
  @media (max-width: 900px) { .adm table.log th:nth-child(6), .adm table.log td:nth-child(6) { display: none; } }
</style>`

const provTableTmpl = `{{define "log"}}{{if .}}
<table class="log">
  <tr><th>When</th><th>What</th><th>Details</th><th>Via</th><th>Account</th><th>Address · client</th></tr>
  {{range .}}<tr>
    <td>{{.When}}{{if .Repeat}}<span class="rep">{{.Repeat}}</span>{{end}}</td>
    <td class="act">{{.Action}}{{if .Site}} <a href="/account/admin/sites/{{.Site}}/trail">{{.Site}}</a>{{end}}</td>
    <td>{{.What}}{{if .Flag}} <span class="flagged">&#9888; {{.Flag}}</span>{{end}}</td>
    <td>{{.Via}}</td>
    <td>{{if .AccountID}}<a href="/account/admin/accounts/{{.AccountID}}">{{.Account}}</a>{{else}}&mdash;{{end}}</td>
    <td>{{if .IP}}<a href="{{iplink .IP}}">{{.IP}}</a>{{else}}&mdash;{{end}}{{if .UA}}<span class="ua">{{.UA}}</span>{{end}}</td>
  </tr>{{end}}
</table>{{else}}<p class="empty">Nothing recorded — or older than 90 days.</p>{{end}}{{end}}
{{define "account"}}
<div class="meta"><span>account <b>{{.Email}}</b></span><span>{{.ID}}</span><span>created {{.Created}}</span>{{if .Suspended}}<span class="lockt">{{.Suspended}}</span>{{end}}</div>
{{with .Signup}}<div class="stub"><span class="k">Sign-up</span>{{.When}} · {{.Via}} · {{if .IP}}<a href="{{iplink .IP}}">{{.IP}}</a>{{else}}no address{{end}}{{if .UA}}<span class="ua">{{.UA}}</span>{{end}}</div>{{end}}
{{range .Tokens}}<div class="stub"><span class="k">API token minted</span>{{.When}} · {{.What}} · {{if .IP}}<a href="{{iplink .IP}}">{{.IP}}</a>{{end}}{{if .Flag}} <span class="flagged">&#9888; {{.Flag}}</span>{{end}}{{if .UA}}<span class="ua">{{.UA}}</span>{{end}}</div>{{end}}
{{if .Signins}}<div class="stub"><span class="k">Last sign-ins</span>{{range .Signins}}{{.When}} · <a href="{{iplink .IP}}">{{.IP}}</a>{{if .Repeat}} ({{.Repeat}}){{end}}<br>{{end}}</div>{{end}}
<h2>Account log</h2>
{{template "log" .Rows}}
{{end}}`

var trailTmpl = template.Must(template.New("trail").Funcs(provFuncs).Parse(pageHead + adminConsoleCSS + provConsoleCSS + provTableTmpl + `
<main class="adm">
  <a class="back" href="/account/admin">&larr; instance register</a>
  <h1>Trail of {{if .Site.Name}}{{.Site.Name}} · {{end}}{{.Site.ViewID}}</h1>
  <div class="meta">
    <span>owner <b>{{if .Owner}}{{.Owner}}{{else}}anonymous{{end}}</b></span>
    {{if .Site.Origin}}<span>origin {{.Site.Origin}}</span>{{end}}
    <span>created {{.Site.CreatedAt.Local.Format "2006-01-02 15:04:05"}}</span>
    {{range .Site.Domains}}<span>{{.}}</span>{{end}}
    {{if .Lock}}<span class="lockt">{{.Lock}}</span>{{end}}
  </div>
  <p class="lede">Every creation and write of this site, newest first: the address it came from (the one Caddy saw), the client, the surface and the credential. Kept 90 days; a locked site's trail is kept with the lock.</p>
  {{template "log" .Rows}}
  {{with .Account}}<h2>Owner</h2>{{template "account" .}}{{end}}
</main>
` + pageFoot))

var accountProvTmpl = template.Must(template.New("accountprov").Funcs(provFuncs).Parse(pageHead + adminConsoleCSS + provConsoleCSS + provTableTmpl + `
<main class="adm">
  <a class="back" href="/account/admin">&larr; instance register</a>
  <h1>Account {{.Email}}</h1>
  <p class="lede">Where this account signed up and signs in from, the tokens it minted, and its sites' creation and deletion. Kept 90 days; a suspended account's log is kept. <a href="/account/admin?q={{.Email}}">Its sites in the register</a>.</p>
  {{template "account" .}}
</main>
` + pageFoot))
