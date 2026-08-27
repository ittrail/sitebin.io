//go:build ee

package ee

import (
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/ittrail/sitebin.io/ee/account"
	"github.com/ittrail/sitebin.io/internal/ext"
)

// expiringSoon is the window the console highlights: a site falling due inside
// it is the operator's cue to act, because after it lapses the sweep deletes.
const expiringSoon = 7 * 24 * time.Hour

// isAdmin reports whether acc may reach the admin console. BOTH conditions
// must hold: the account's resolved tier carries the admin flag, and its email
// is in SITEBIN_ADMIN_ACCOUNTS. The tier lets a plan source nominate an
// account; the allowlist lets the operator of the container confirm it.
//
// The tier is read with the ordinary effectiveTier, which falls open to the
// stored tier when PayGate cannot be reached. That is safe precisely because
// the allowlist is the hard gate: degrading to a stored tier cannot promote
// anyone the operator has not already named.
func (p *provider) isAdmin(acc *account.Account) bool {
	if acc == nil || len(p.cfg.AdminAccounts) == 0 {
		return false
	}
	if !p.effectiveTier(acc).Admin {
		return false
	}
	email := strings.ToLower(strings.TrimSpace(acc.Email))
	for _, allowed := range p.cfg.AdminAccounts {
		if allowed == email {
			return true
		}
	}
	return false
}

// adminAccount resolves the request to an admin account. Callers that get
// ok=false must answer 404, not 403 or a login redirect: the console does not
// announce its existence to people who cannot use it, and "sign in and you
// could" is an announcement.
func (p *provider) adminAccount(r *http.Request) (*account.Account, bool) {
	acc, ok := p.currentAccount(r)
	if !ok || !p.isAdmin(acc) {
		return nil, false
	}
	return acc, true
}

// instanceFigures are the numbers above the list. They describe the whole
// instance, never the filtered subset — a filter narrows what you are looking
// at, not what is there.
type instanceFigures struct {
	Sites        int
	Owned        int
	Anonymous    int
	Bytes        int64
	Files        int
	Expiring     int // carrying any expiry
	ExpiringSoon int // falling due within expiringSoon
	Flagged      int // sites with at least one CSP violation
}

// HumanBytes renders the stored total for the figure stub.
func (f instanceFigures) HumanBytes() string { return humanBytes(f.Bytes) }

func (p *provider) instanceStats(sites []ext.SiteInfo) instanceFigures {
	var f instanceFigures
	cutoff := time.Now().Add(expiringSoon)
	for _, s := range sites {
		f.Sites++
		if s.Owner != "" {
			f.Owned++
		} else {
			f.Anonymous++
		}
		f.Bytes += s.Bytes
		f.Files += s.Files
		if s.ExpiresAt != nil {
			f.Expiring++
			if s.ExpiresAt.Before(cutoff) {
				f.ExpiringSoon++
			}
		}
		if s.Violations > 0 {
			f.Flagged++
		}
	}
	return f
}

// ownerEmails resolves the distinct owner ids in sites to email addresses. An
// id that no longer resolves keeps its raw form rather than vanishing: an
// orphaned site is exactly the kind of thing an operator opened this page to
// find.
func (p *provider) ownerEmails(sites []ext.SiteInfo) map[string]string {
	out := map[string]string{}
	for _, s := range sites {
		if s.Owner == "" {
			continue
		}
		if _, done := out[s.Owner]; done {
			continue
		}
		if acc, err := p.accounts.ByID(s.Owner); err == nil {
			out[s.Owner] = acc.Email
		} else {
			out[s.Owner] = s.Owner
		}
	}
	return out
}

type adminRow struct {
	ext.SiteInfo
	OwnerLabel  string
	SizeText    string
	CreatedText string
	// ExpiryText is the compact column form: the bare date, or an em dash. The
	// dashboard's "expires 2026-08-28" phrasing reads well in a sentence and
	// wraps badly in a table that already has an Expiry heading.
	ExpiryText  string
	DomainsText string
	ExpiringNow bool
	ExpiryValue string // yyyy-mm-dd for the date input, empty when none
	BlockedText string // the destinations the site's pages were stopped from reaching
	// Confirming turns this row into the delete confirmation. The page's CSP
	// is script-src 'none', so a confirm() dialog would silently never appear;
	// the confirmation has to be a step the server renders.
	Confirming bool
}

type adminView struct {
	Email   string
	CSRF    string
	Figures instanceFigures
	Rows    []adminRow
	Query   string
	Filter  string
	Shown   int
	Flash   string
	// Params carries q and filter through the confirm step, so cancelling
	// returns to the list the operator was actually looking at. Params is
	// prefixed with "&" for appending to an existing query; ParamsQ is the
	// bare form for starting one.
	Params  string
	ParamsQ string
}

// matches decides whether a site survives the text query. It searches the three
// handles an operator is likely to have: the view id, the owner's email, and a
// custom domain.
func matches(row adminRow, q string) bool {
	if q == "" {
		return true
	}
	if strings.Contains(strings.ToLower(row.ViewID), q) ||
		strings.Contains(strings.ToLower(row.OwnerLabel), q) {
		return true
	}
	for _, d := range row.Domains {
		if strings.Contains(strings.ToLower(d), q) {
			return true
		}
	}
	return false
}

func (p *provider) handleAdmin(w http.ResponseWriter, r *http.Request) {
	acc, ok := p.adminAccount(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	sites, err := p.host.Sites().All()
	if err != nil {
		// Never fall through to an empty list: telling an operator their
		// instance is empty is the one wrong answer to "what is on it".
		slog.Error("admin console could not enumerate sites", "admin", acc.ID, "err", err)
		http.Error(w, "could not read the instance's sites", http.StatusInternalServerError)
		return
	}

	figures := p.instanceStats(sites)
	emails := p.ownerEmails(sites)
	q := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	filter := r.URL.Query().Get("filter")
	confirm := r.URL.Query().Get("confirm")
	cutoff := time.Now().Add(expiringSoon)

	rows := make([]adminRow, 0, len(sites))
	for _, s := range sites {
		row := adminRow{
			SiteInfo:    s,
			OwnerLabel:  emails[s.Owner],
			SizeText:    humanBytes(s.Bytes),
			CreatedText: s.CreatedAt.Local().Format("2006-01-02"),
			ExpiryText:  "—",
			DomainsText: strings.Join(s.Domains, ", "),
			BlockedText: strings.Join(s.Blocked, ", "),
			Confirming:  s.ViewID == confirm,
		}
		if row.OwnerLabel == "" {
			row.OwnerLabel = "anonymous"
		}
		if s.ExpiresAt != nil {
			row.ExpiringNow = s.ExpiresAt.Before(cutoff)
			row.ExpiryValue = s.ExpiresAt.Local().Format("2006-01-02")
			row.ExpiryText = row.ExpiryValue
		}
		switch filter {
		case "owned":
			if s.Owner == "" {
				continue
			}
		case "anon":
			if s.Owner != "" {
				continue
			}
		case "expiring":
			if !row.ExpiringNow {
				continue
			}
		case "flagged":
			if s.Violations == 0 {
				continue
			}
		}
		if !matches(row, q) {
			continue
		}
		rows = append(rows, row)
	}
	sort.Slice(rows, func(a, b int) bool { return rows[a].CreatedAt.After(rows[b].CreatedAt) })

	p.securityHeaders(w)
	adminTmpl.Execute(w, adminView{
		Email:   acc.Email,
		CSRF:    p.csrf(acc),
		Figures: figures,
		Rows:    rows,
		Query:   r.URL.Query().Get("q"),
		Filter:  filter,
		Shown:   len(rows),
		Flash:   r.URL.Query().Get("flash"),
		Params:  listParams(r),
		ParamsQ: strings.TrimPrefix(listParams(r), "&"),
	})
}

// listParams re-encodes just the list's own query so a redirect or a cancel
// link lands back on the same view, without carrying "confirm" along.
func listParams(r *http.Request) string {
	v := url.Values{}
	if q := r.URL.Query().Get("q"); q != "" {
		v.Set("q", q)
	}
	if f := r.URL.Query().Get("filter"); f != "" {
		v.Set("filter", f)
	}
	if len(v) == 0 {
		return ""
	}
	return "&" + v.Encode()
}

func (p *provider) handleAdminDelete(w http.ResponseWriter, r *http.Request) {
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
	if err := p.host.Sites().Delete(viewID); err != nil {
		http.Error(w, "could not delete the site", http.StatusInternalServerError)
		return
	}
	// An operator acting on sites that are not theirs leaves a trail.
	slog.Info("admin deleted a site", "admin", acc.ID, "site", viewID)
	p.redirect(w, r, "/account/admin?flash=deleted"+listParams(r))
}

func (p *provider) handleAdminExpiry(w http.ResponseWriter, r *http.Request) {
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
	var at *time.Time
	if raw := strings.TrimSpace(r.FormValue("expires")); raw != "" {
		// End of the chosen day, in the server's zone: an operator picking a
		// date means "through that day", not "at midnight as it begins".
		d, err := time.ParseInLocation("2006-01-02", raw, time.Local)
		if err != nil {
			http.Error(w, "expiry must be a date like 2027-01-31", http.StatusBadRequest)
			return
		}
		d = d.Add(24*time.Hour - time.Second)
		at = &d
	}
	if err := p.host.Sites().SetExpiry(viewID, at); err != nil {
		http.Error(w, "could not set the expiry", http.StatusInternalServerError)
		return
	}
	slog.Info("admin set a site's expiry", "admin", acc.ID, "site", viewID, "expires", at)
	p.redirect(w, r, "/account/admin?flash=expiry"+listParams(r))
}
