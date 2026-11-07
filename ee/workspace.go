//go:build ee

package ee

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"html/template"
	"io"
	"io/fs"
	"log/slog"
	"mime"
	"net/http"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/ittrail/sitebin.io/ee/account"
	"github.com/ittrail/sitebin.io/internal/ext"
)

// The workspace interface: a second account UI, chosen per account and
// stored on it (account.Account.UI). It is a different PRESENTATION of the
// same routes — every action posts to the handler the classic page posts to,
// and the site page runs the core's own edit.js — so no rule is stated twice.
// See docs/superpowers/specs/2026-11-06-workspace-ui-design.md.

const (
	// account.Account.UI: "" is no choice of the account's own (the
	// instance's default applies, see uiOf), else the interface it chose.
	uiClassic   = "classic"
	uiWorkspace = "workspace"
	// uiHeader asks a POST route to answer JSON instead of a page. A custom
	// header: a cross-site form cannot send it, and nothing about what the
	// handler decides changes — only the shape of its answer.
	uiHeader = "X-Sitebin-UI"
)

//go:embed ui
var uiFS embed.FS

type uiAsset struct {
	body    []byte
	version string
	ctype   string
}

// uiAssets are the workspace's files, by name, each with the content hash
// its URL carries (?v=), so a release never meets a stale cached copy.
var uiAssets = loadUIAssets()

func loadUIAssets() map[string]uiAsset {
	out := map[string]uiAsset{}
	entries, err := fs.ReadDir(uiFS, "ui")
	if err != nil {
		panic(err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		b, err := fs.ReadFile(uiFS, "ui/"+e.Name())
		if err != nil {
			panic(err)
		}
		sum := sha256.Sum256(b)
		ct := mime.TypeByExtension(path.Ext(e.Name()))
		switch path.Ext(e.Name()) {
		case ".js":
			ct = "text/javascript; charset=utf-8"
		case ".css":
			ct = "text/css; charset=utf-8"
		}
		out[e.Name()] = uiAsset{body: b, version: hex.EncodeToString(sum[:6]), ctype: ct}
	}
	return out
}

// uiURL is the versioned URL of a workspace asset.
func uiURL(name string) string { return "/account/ui/" + name + "?v=" + uiAssets[name].version }

// frameHead and frameFoot wrap a server-rendered page in the workspace: its
// sidebar (filled by shell.js from /account/api/state) and its styles, which
// restyle the classic markup. The page's own body is unchanged.
var frameHead = pageHeadStart +
	`<link rel="stylesheet" href="` + uiURL("app.css") + `">` + acctCSS +
	`</head><body class="ws ws-frame"><div class="app" id="app"><aside class="side" id="side" aria-label="Navigation"></aside>` +
	`<div class="main"><div class="view" id="view"><div class="frame">`

var frameFoot = `</div></div></div></div><div class="toast" id="toast" role="status"></div>` +
	`<script src="` + uiURL("shell.js") + `"></script></body></html>`

// pageTmpl is an account page parsed twice: with the classic head and inside
// the workspace frame. Execute picks by the writer withUI marked.
type pageTmpl struct{ classic, frame *template.Template }

func twin(name string, funcs template.FuncMap, body string) *pageTmpl {
	mk := func(head, foot string) *template.Template {
		t := template.New(name)
		if funcs != nil {
			t = t.Funcs(funcs)
		}
		return template.Must(t.Parse(head + body + foot))
	}
	return &pageTmpl{classic: mk(pageHead, pageFoot), frame: mk(frameHead, frameFoot)}
}

func (t *pageTmpl) Execute(w io.Writer, data any) error {
	if c, ok := w.(*uiCapture); ok {
		c.data, c.got = data, true // the console's JSON: the view, not the page
		return nil
	}
	if u, ok := w.(*uiWriter); ok {
		if u.json {
			// A message — a refusal, or a secret shown once — is the one page
			// the workspace reads instead of showing.
			if m, ok := data.(msgView); ok {
				u.answer(uiAnswer{OK: u.status < 400, Message: &uiMessage{Title: m.Title, Body: m.Body, Detail: m.Detail, Back: m.Back}})
				return nil
			}
		}
		if u.frame {
			return t.frame.Execute(w, data)
		}
	}
	return t.classic.Execute(w, data)
}

// uiWriter marks a response for the workspace: frame (render pages inside
// it) and json (answer redirects and messages as JSON). In json mode the
// status a handler sets is held until the body, so a JSON answer carries it.
type uiWriter struct {
	http.ResponseWriter
	json, frame bool
	status      int
	sent        bool
}

func (u *uiWriter) WriteHeader(code int) {
	if u.json && !u.sent {
		u.status = code
		return
	}
	u.sent = true
	u.ResponseWriter.WriteHeader(code)
}

func (u *uiWriter) Write(b []byte) (int, error) {
	if u.json && !u.sent {
		u.sent = true
		if u.status == 0 {
			u.status = http.StatusOK
		}
		u.ResponseWriter.WriteHeader(u.status)
	}
	return u.ResponseWriter.Write(b)
}

func (u *uiWriter) Unwrap() http.ResponseWriter { return u.ResponseWriter }

type uiAnswer struct {
	OK       bool       `json:"ok"`
	Redirect string     `json:"redirect,omitempty"`
	Leave    string     `json:"leave,omitempty"`
	Message  *uiMessage `json:"message,omitempty"`
}

type uiMessage struct {
	Title  string `json:"title"`
	Body   string `json:"body"`
	Detail string `json:"detail,omitempty"`
	Back   string `json:"back,omitempty"`
}

func (u *uiWriter) answer(v uiAnswer) {
	h := u.Header()
	h.Set("Content-Type", "application/json")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	code := u.status
	if code == 0 {
		code = http.StatusOK
	}
	u.sent = true
	u.ResponseWriter.WriteHeader(code)
	json.NewEncoder(u.ResponseWriter).Encode(v)
}

// uiJSON is the writer when the request asked for JSON answers.
func uiJSON(w http.ResponseWriter) (*uiWriter, bool) {
	u, ok := w.(*uiWriter)
	return u, ok && u.json
}

// uiFrame reports whether pages go out inside the workspace frame.
func uiFrame(w io.Writer) bool {
	u, ok := w.(*uiWriter)
	return ok && u.frame
}

// uiOf is the interface the account sees: its own choice, else the
// instance's default (SITEBIN_DEFAULT_UI, the workspace unless an operator
// keeps the classic pages). A provider without that setting — the tests' —
// shows the classic pages.
func (p *provider) uiOf(acc *account.Account) string {
	switch acc.UI {
	case uiWorkspace, uiClassic:
		return acc.UI
	}
	if p.cfg.DefaultUI == uiWorkspace {
		return uiWorkspace
	}
	return uiClassic
}

// withUI marks every account route's writer: frame for an account that sees
// the workspace, json for a request that sent uiHeader. A request with
// neither passes untouched — the classic pages are exactly what they were.
func (p *provider) withUI(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		js := r.Header.Get(uiHeader) == "json"
		frame := false
		if acc, ok := p.currentAccount(r); ok && p.uiOf(acc) == uiWorkspace {
			frame = true
		}
		if !js && !frame {
			h.ServeHTTP(w, r)
			return
		}
		h.ServeHTTP(&uiWriter{ResponseWriter: w, json: js, frame: frame}, r)
	})
}

func (p *provider) workspaceRoutes(routes map[string]http.Handler) {
	routes["POST /account/ui"] = http.HandlerFunc(p.handleSetUI)
	routes["GET /account/ui/{file}"] = http.HandlerFunc(p.handleUIAsset)
	routes["GET /account/api/state"] = http.HandlerFunc(p.handleUIState)
	routes["GET /account/api/sites/{id}/activity"] = http.HandlerFunc(p.handleSiteActivity)
	p.adminAPIRoutes(routes)
}

// handleSetUI stores the account's choice of interface and opens it.
func (p *provider) handleSetUI(w http.ResponseWriter, r *http.Request) {
	acc, ok := p.currentAccount(r)
	if !ok {
		p.redirect(w, r, "/account/login")
		return
	}
	if !p.checkCSRF(r, acc) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	// Stored as chosen, never as "": an account that went back to the classic
	// pages keeps them whatever the instance's default becomes.
	ui := r.PostFormValue("ui")
	switch ui {
	case uiClassic, uiWorkspace:
	default:
		http.Error(w, "unknown interface", http.StatusBadRequest)
		return
	}
	if err := p.accounts.Update(acc, func(a *account.Account) error { a.UI = ui; return nil }); err != nil {
		slog.Error("could not store the interface choice", "account", acc.ID, "err", err)
		http.Error(w, "could not save the choice", http.StatusInternalServerError)
		return
	}
	p.redirect(w, r, "/account")
}

// handleUIAsset serves the workspace's embedded files. They are code, not
// data, and carry nothing about any account, so they need no session.
func (p *provider) handleUIAsset(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("file")
	a, ok := uiAssets[name]
	if !ok || strings.HasSuffix(name, ".html") {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", a.ctype)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.URL.Query().Get("v") != "" {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		w.Header().Set("Cache-Control", "no-cache")
	}
	w.Write(a.body)
}

// workspaceCSP is the account pages' policy with the workspace's own scripts
// admitted ('self'); the copy button's hash stays for the classic markup a
// framed page may carry.
func workspaceCSP() string {
	return "default-src 'self'; img-src 'self' data: blob:; style-src 'self' 'unsafe-inline'; font-src 'self' data:; " +
		"connect-src 'self'; script-src 'self' " + copyScriptCSP + "; frame-ancestors 'none'; base-uri 'none'; form-action 'self'"
}

var appTmpl = template.Must(template.New("app").Parse(string(uiAssets["app.html"].body)))
var siteTmpl = template.Must(template.New("site").Parse(string(uiAssets["site.html"].body)))

// uiPage is what the workspace's two HTML shells are executed with: the
// versioned asset URLs and the one-time notice.
type uiPage struct {
	Notice string
	Asset  map[string]string
}

func uiPageData(notice string) uiPage {
	m := map[string]string{}
	for name := range uiAssets {
		m[strings.NewReplacer(".", "_", "-", "_").Replace(name)] = uiURL(name)
	}
	return uiPage{Notice: notice, Asset: m}
}

func (p *provider) writeUIPage(w http.ResponseWriter, t *template.Template, notice string) {
	w.Header().Set("Content-Security-Policy", workspaceCSP())
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := t.Execute(w, uiPageData(notice)); err != nil {
		slog.Error("workspace page", "err", err)
	}
}

// renderWorkspace answers /account for an account that chose the workspace.
func (p *provider) renderWorkspace(w http.ResponseWriter, r *http.Request, acc *account.Account) {
	p.writeUIPage(w, appTmpl, p.takeNotice(w, r, acc))
}

// ServeEditPage implements ext.EditPages: the site page of the workspace, for
// a signed-in account that chose it. Everyone else — an edit password, the
// classic choice — gets the core's edit.html. The page carries no data: its
// scripts read the site through the same API edit.html uses.
func (p *provider) ServeEditPage(w http.ResponseWriter, r *http.Request) bool {
	acc, ok := p.currentAccount(r)
	if !ok || p.uiOf(acc) != uiWorkspace {
		return false
	}
	p.writeUIPage(w, siteTmpl, "")
	return true
}

var _ ext.EditPages = (*provider)(nil)

// ---- the state the workspace boots from ----

type wsState struct {
	Account   wsAccount      `json:"account"`
	License   *licenseNotice `json:"license,omitempty"`
	Sites     []wsSite       `json:"sites"`
	Tags      []wsTag        `json:"tags"`
	Groups    []wsGroup      `json:"groups"`
	Tokens    []wsToken      `json:"tokens"`
	Zones     []ext.ZoneInfo `json:"zones"`
	ZonesMax  int            `json:"zones_max"`
	Takeovers []takeoverCard `json:"takeovers"`
	Invites   []inviteCard   `json:"invites"`
	Plan      wsPlan         `json:"plan"`
}

type wsAccount struct {
	Email   string `json:"email"`
	Tier    string `json:"tier"`
	CSRF    string `json:"csrf"`
	IsAdmin bool   `json:"is_admin"`
	// Containers shows the console's Containers view: the instance runs
	// container sites (as the classic console's tab).
	Containers    bool   `json:"containers"`
	UI            string `json:"ui"`
	PublishURL    string `json:"publish_url"`
	MCPEndpoint   string `json:"mcp_endpoint"`
	Base          string `json:"base"`
	AccountURL    string `json:"account_url,omitempty"`
	DeletionURL   string `json:"deletion_url,omitempty"`
	CancelURL     string `json:"cancel_url,omitempty"`
	StackDeletion bool   `json:"stack_deletion"`
	Verified      bool   `json:"verified"`
}

// wsSite is what the OWNER's dashboard row shows, field by field — never
// ext.SiteInfo wholesale: the owner must not read who placed a lock, the
// abuse findings or the CSP report data, which only the register shows.
type wsSite struct {
	ID             string           `json:"id"`
	EditURL        string           `json:"edit_url,omitempty"`
	Name           string           `json:"name"`
	Mode           string           `json:"mode"`
	SPA            bool             `json:"spa,omitempty"`
	ViewURL        string           `json:"view_url"`
	ViewOff        bool             `json:"view_off"`
	ViewRedirectTo string           `json:"view_redirect_to,omitempty"`
	Domains        []ext.DomainLink `json:"domains"`
	Bytes          int64            `json:"bytes"`
	GitBytes       int64            `json:"git_bytes"`
	Files          int              `json:"files"`
	Forms          int              `json:"forms"`
	CreatedAt      time.Time        `json:"created_at"`
	UpdatedAt      time.Time        `json:"updated_at"`
	ExpiresAt      *time.Time       `json:"expires_at,omitempty"`
	ExpiryByOwner  bool             `json:"expiry_by_owner"`
	ViewPassword   bool             `json:"view_password"`
	Container      *wsContainer     `json:"container,omitempty"`
	Locked         *wsLock          `json:"locked,omitempty"`
	Tags           []wsChip         `json:"tags"`
	Role           string           `json:"role"` // owner | admin
	CanShare       bool             `json:"can_share"`
	Admins         []shareAdminRow  `json:"admins,omitempty"`
	OpenInvites    []shareInviteRow `json:"open_invites,omitempty"`
	SharedBy       string           `json:"shared_by,omitempty"`
	SharedUntil    string           `json:"shared_until,omitempty"`
	SharedPaused   bool             `json:"shared_paused,omitempty"`
	SharedNote     string           `json:"shared_note,omitempty"`
}

// wsChip is a tag on a site row: its spelling, its key and its palette id.
type wsChip struct {
	Name  string `json:"name"`
	Key   string `json:"key"`
	Color string `json:"color"`
}

func wsChips(cs []tagChip) []wsChip {
	out := make([]wsChip, 0, len(cs))
	for _, c := range cs {
		out = append(out, wsChip{Name: c.Name, Key: c.Key, Color: c.Color})
	}
	return out
}

type wsContainer struct {
	Enabled bool   `json:"enabled"`
	Status  string `json:"status,omitempty"`
}

type wsLock struct {
	At     time.Time `json:"at"`
	Reason string    `json:"reason,omitempty"`
}

type wsTag struct {
	Name  string `json:"name"`
	Key   string `json:"key"`
	Color string `json:"color"`
	Group string `json:"group,omitempty"`
	Count int    `json:"count"`
}

type wsGroup struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Color string `json:"color"`
}

type wsToken struct {
	ID      string    `json:"id"`
	Name    string    `json:"name"`
	Prefix  string    `json:"prefix"`
	Created time.Time `json:"created"`
}

type wsPlan struct {
	Current    string       `json:"current"`
	Tiers      []tierOption `json:"tiers"`
	SelfSelect bool         `json:"self_select"`
	Checkout   bool         `json:"checkout"`
	Portal     bool         `json:"portal"`
	Caps       wsCaps       `json:"caps"`
}

// wsCaps are the current plan's limits, for Plan & usage. 0 is the plan's
// "unlimited" (or "none" for containers, forms and zones — the page says
// which, as the pricing page does).
type wsCaps struct {
	SiteBytes     int64 `json:"site_bytes"`
	Files         int   `json:"files"`
	Sites         int   `json:"sites"`
	CustomDomains int   `json:"custom_domains"`
	ExpiryDays    int   `json:"expiry_days"`
	Containers    int   `json:"containers"`
	Zones         int   `json:"zones"`
	Forms         int   `json:"forms"`
	WebDAV        bool  `json:"webdav"`
	Git           bool  `json:"git"`
	Analytics     bool  `json:"analytics"`
	Sharing       bool  `json:"sharing"`
}

// dashboardRows are the account's site rows — its own, then the ones shared
// with it — exactly as the classic dashboard builds them.
func (p *provider) dashboardRows(acc *account.Account, token string) []siteRow {
	infos, _ := p.ownedSiteInfos(acc)
	rows := make([]siteRow, 0, len(infos))
	canShare, openInvites, plans := p.canShareFrom(acc), p.liveInvitesBySite(acc), sharingMemo{}
	owned := map[string]bool{}
	for _, info := range infos {
		owned[info.ViewID] = true
		row := siteRow{
			SiteInfo:   info,
			SizeText:   humanBytes(info.Bytes + info.GitBytes),
			ExpiryText: expiryText(info.ExpiresAt),
			CSRF:       token,
		}
		if info.GitBytes > 0 {
			row.GitText = humanBytes(info.GitBytes)
		}
		if info.Locked != nil {
			row.LockedText = info.Locked.At.Local().Format("2006-01-02")
			// A lock outlives the expiry; saying the date would promise
			// a deletion that is not coming.
			row.ExpiryText = "kept while locked"
		}
		p.shareRowsFor(&row, canShare, openInvites, plans)
		rows = append(rows, row)
	}
	// Sites shared with the account sit among its own.
	return append(rows, p.sharedRows(acc, token, owned)...)
}

func (p *provider) handleUIState(w http.ResponseWriter, r *http.Request) {
	acc, ok := p.currentAccount(r)
	if !ok {
		writeJSONStatus(w, http.StatusUnauthorized, map[string]string{"error": "signed out", "login": "/account/login"})
		return
	}
	// As the dashboard render: the first moment a PayGate change is seen,
	// and the expiries the rows show depend on it.
	p.syncTier(acc)
	token := p.csrf(acc)
	rows := p.dashboardRows(acc, token)
	reg := p.tagRegistry(acc)

	st := wsState{Sites: make([]wsSite, 0, len(rows)), Tags: []wsTag{}, Groups: []wsGroup{}, Tokens: []wsToken{}, Zones: []ext.ZoneInfo{}}
	counts := map[string]int{}
	for _, row := range rows {
		s := wsSite{
			ID: row.ViewID, EditURL: row.EditURL, Name: row.Name, Mode: row.Mode, SPA: row.SPA,
			ViewURL: row.ViewURL, ViewOff: row.ViewOff, ViewRedirectTo: row.ViewRedirectTo,
			Domains: row.DomainLinks, Bytes: row.Bytes, GitBytes: row.GitBytes, Files: row.Files, Forms: row.Forms,
			CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt, ExpiresAt: row.ExpiresAt, ExpiryByOwner: row.ExpiryByOwner,
			ViewPassword: row.ViewPassword, Tags: wsChips(siteChips(row.SiteInfo.Tags, reg)),
			Role: "owner", CanShare: row.CanShare, Admins: row.AdminRows, OpenInvites: row.InviteRows,
		}
		if s.Domains == nil {
			s.Domains = []ext.DomainLink{}
		}
		if row.ContainerEnabled || row.Mode == "container" {
			s.Container = &wsContainer{Enabled: row.ContainerEnabled, Status: row.ContainerStatus}
		}
		if row.Locked != nil {
			s.Locked = &wsLock{At: row.Locked.At, Reason: row.Locked.Reason}
		}
		if row.Shared {
			s.Role, s.SharedBy, s.SharedUntil = "admin", row.SharedBy, row.SharedUntil
			s.SharedPaused, s.SharedNote = row.SharedPaused, row.SharedPausedText
			if row.SharedPaused {
				s.EditURL = "" // no standing now: the page would refuse it
			}
		}
		for _, t := range s.Tags {
			counts[t.Key]++
		}
		st.Sites = append(st.Sites, s)
	}
	for _, g := range sortedGroups(reg) {
		st.Groups = append(st.Groups, wsGroup{ID: g.ID, Name: g.Name, Color: paletteID(g.Color)})
	}
	for key, m := range reg.Tags {
		st.Tags = append(st.Tags, wsTag{Name: m.Name, Key: key, Color: paletteID(reg.ColorOf(key)), Group: m.Group, Count: counts[key]})
	}
	// Tags in use that the registry does not decorate yet still filter.
	for _, s := range st.Sites {
		for _, c := range s.Tags {
			if _, ok := reg.Tags[c.Key]; !ok && !containsTag(st.Tags, c.Key) {
				st.Tags = append(st.Tags, wsTag{Name: c.Name, Key: c.Key, Color: c.Color, Count: counts[c.Key]})
			}
		}
	}
	sort.Slice(st.Tags, func(i, j int) bool { return strings.ToLower(st.Tags[i].Name) < strings.ToLower(st.Tags[j].Name) })

	toks, err := p.accounts.ListTokens(acc)
	if err != nil {
		slog.Error("workspace state: list api tokens", "account", acc.ID, "err", err)
	}
	for _, t := range toks {
		st.Tokens = append(st.Tokens, wsToken{ID: t.ID, Name: t.Name, Prefix: t.Prefix, Created: t.CreatedAt})
	}
	if zs, err := p.host.Sites().Zones(acc.ID); err == nil {
		st.Zones = append(st.Zones, zs...)
	} else {
		slog.Error("workspace state: list zones", "account", acc.ID, "err", err)
	}

	current := p.effectiveTierFresh(acc)
	tier := current.Label
	if tier == "" {
		tier = current.ID
	}
	if tier == "" {
		tier = acc.Tier
	}
	st.ZonesMax = current.MaxZones
	st.Takeovers = p.takeoverCards(acc)
	st.Invites = p.inviteCards(acc)
	if st.Takeovers == nil {
		st.Takeovers = []takeoverCard{}
	}
	if st.Invites == nil {
		st.Invites = []inviteCard{}
	}
	st.License = p.licenseNotice()

	checkout := p.canCheckout()
	plan := wsPlan{Current: current.ID, Tiers: []tierOption{}, SelfSelect: p.cfg.SelfSelect, Checkout: checkout, Portal: p.billing != nil,
		Caps: wsCaps{
			SiteBytes: current.MaxSiteBytes, Files: current.MaxFiles, Sites: current.MaxSites, CustomDomains: current.CustomDomains,
			ExpiryDays: current.MaxExpiryDays, Containers: current.MaxContainers, Zones: current.MaxZones, Forms: current.MaxForms,
			WebDAV: current.WebDAV, Git: current.Git, Analytics: current.Analytics, Sharing: current.Sharing,
		}}
	if p.cfg.SelfSelect || checkout {
		for _, t := range p.cfg.Tiers {
			label := t.Label
			if label == "" {
				label = t.ID
			}
			plan.Tiers = append(plan.Tiers, tierOption{ID: t.ID, Label: label, Current: t.ID == current.ID, Paid: t.Paid(), Price: t.Price.Label()})
		}
	}
	st.Plan = plan

	var accountURL, deletionURL, cancelURL string
	if acc.Provider == account.OIDCProv {
		accountURL = p.cfg.AccountConsoleURL(p.baseURL() + "/account")
		if deletionURL = p.cfg.AccountDeletionURL(); deletionURL == "" {
			deletionURL = accountURL
		}
	}
	if p.cfg.PayGate != nil {
		cancelURL = p.cfg.PayGate.CancelURL
	}
	st.Account = wsAccount{
		Email: acc.Email, Tier: tier, CSRF: token, IsAdmin: p.isAdmin(acc), UI: p.uiOf(acc),
		Containers: p.containerLimits != nil && p.isAdmin(acc),
		PublishURL: p.host.PublishURL(), MCPEndpoint: p.host.BaseURL() + "/mcp", Base: p.baseURL(),
		AccountURL: accountURL, DeletionURL: deletionURL, CancelURL: cancelURL,
		StackDeletion: p.stackDeletion(acc), Verified: acc.EmailVerified,
	}
	writeJSONStatus(w, http.StatusOK, st)
}

func containsTag(list []wsTag, key string) bool {
	for _, t := range list {
		if t.Key == key {
			return true
		}
	}
	return false
}

// writeJSONStatus answers a workspace read. Never cached: it is one
// account's private state.
func writeJSONStatus(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}
