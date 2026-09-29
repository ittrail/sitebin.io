package httpapi

import (
	"archive/zip"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/ittrail/sitebin.io/internal/ext"
	"github.com/ittrail/sitebin.io/internal/ids"
	"github.com/ittrail/sitebin.io/internal/mcp"
	"github.com/ittrail/sitebin.io/internal/provenance"
	"github.com/ittrail/sitebin.io/internal/store"
)

// Provenance: every creation and every write of a site is recorded in the
// site's log with the client address, its user agent, the surface it came
// through, the credential that authorised it and the acting account. See
// internal/provenance and docs/superpowers/specs/2026-09-29-provenance-csp-apex.md.
//
// The rule for where: at the one place each surface authenticated the
// caller. The JSON API's per-site routes are wrapped in `recorded` at the
// route table, after withEditAuth / withUploadAuth put the actor in the
// request context; creation records in createSiteWith; MCP, WebDAV and FTP
// record in their own handlers. Recording is best effort: a change that
// happened is never failed or undone because its record could not be
// written.

// actor is who made a change.
type actor struct {
	surface string
	auth    string
	account string
}

type actorKey struct{}

func withActor(r *http.Request, act actor) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), actorKey{}, act))
}

// actorOf returns the actor the gate recorded, or an anonymous API caller.
func (a *API) actorOf(r *http.Request) actor {
	if act, ok := r.Context().Value(actorKey{}).(actor); ok {
		return act
	}
	return actor{surface: a.apiSurface(r), auth: provenance.AuthNone}
}

// apiSurface labels a JSON API request: "ui" when it is browser-shaped like
// Sitebin's own pages, "api" otherwise. fromOwnBrowser is a forgeable plan
// heuristic, and that is fine here: this is a label next to the address and
// the client, never a check.
func (a *API) apiSurface(r *http.Request) string {
	if a.fromOwnBrowser(r) {
		return provenance.SurfaceUI
	}
	return provenance.SurfaceAPI
}

// entryFor builds an entry for r. The address is auth.ClientIP's: the last
// X-Forwarded-For entry, the one Caddy appended.
func entryFor(r *http.Request, act actor, action string) provenance.Entry {
	return provenance.Entry{
		Time:    time.Now().UTC(),
		Action:  action,
		Surface: act.surface,
		Auth:    act.auth,
		Account: act.account,
		IP:      clientIP(r),
		UA:      r.UserAgent(),
	}
}

// record adds e to the site's log, logging (never returning) a failure.
func (a *API) record(site *store.Site, e provenance.Entry) {
	if err := a.st.RecordProvenance(site, e); err != nil {
		a.log.Warn("provenance: could not record", "id", site.ViewID, "action", e.Action, "err", err)
	}
}

// mirror adds e to the owning account's log, when the extension keeps one:
// the creation and deletion of an account's sites, so a site its owner
// deleted still leaves where it came from behind.
func (a *API) mirror(owner string, e provenance.Entry) {
	if owner == "" {
		return
	}
	p, ok := ext.Get()
	if !ok {
		return
	}
	if ap, ok := p.(ext.AccountProvenance); ok {
		ap.RecordAccountProvenance(owner, e)
	}
}

// provNote is what a wrapped handler tells `recorded` about the change it
// made: an action other than the route's (a replace), how many files, and a
// short description.
type provNote struct {
	action string
	files  int
	paths  []string
	detail string
	// held says the abuse guard locked the site on this request's write:
	// the request fails, but the write happened and is the evidence, so it
	// is recorded like a success.
	held bool
}

type provNoteKey struct{}

// noteOf returns the note of a recorded request, or a throwaway one.
func noteOf(r *http.Request) *provNote {
	if n, ok := r.Context().Value(provNoteKey{}).(*provNote); ok {
		return n
	}
	return &provNote{}
}

// maxNotedPaths bounds how many file names an entry lists.
const maxNotedPaths = 3

func (n *provNote) addFile(path string) {
	n.files++
	if len(n.paths) < maxNotedPaths {
		n.paths = append(n.paths, path)
	}
}

func (n *provNote) text() string {
	parts := append([]string(nil), n.paths...)
	if n.detail != "" {
		parts = append(parts, n.detail)
	}
	return strings.Join(parts, ", ")
}

// recorded wraps a per-site route: when the handler answers with a success,
// the change is recorded with the actor the gate authenticated. A deletion
// takes the site's log with it, so it goes to the owner's account log.
func (a *API) recorded(action string, next func(http.ResponseWriter, *http.Request, *store.Site)) func(http.ResponseWriter, *http.Request, *store.Site) {
	return func(w http.ResponseWriter, r *http.Request, site *store.Site) {
		note := &provNote{}
		r = r.WithContext(context.WithValue(r.Context(), provNoteKey{}, note))
		sw := &statusWriter{ResponseWriter: w, code: 200}
		next(sw, r, site)
		if sw.code >= 300 && !note.held {
			return
		}
		if note.held {
			note.detail = strings.TrimPrefix(note.detail+", held for review", ", ")
		}
		if note.action != "" {
			action = note.action
		}
		e := entryFor(r, a.actorOf(r), action)
		e.Files = note.files
		e.Detail = note.text()
		if action == provenance.ActionSiteDelete {
			e.Site = site.ViewID
			a.mirror(site.Meta.OwnerAccountID, e)
			return
		}
		a.record(site, e)
	}
}

// createActor is who is creating a site through the JSON API or the UI: the
// account the extension resolved, and whether it came with a bearer token or
// a browser session.
func (a *API) createActor(r *http.Request, owner string) actor {
	act := actor{surface: a.apiSurface(r), auth: provenance.AuthNone, account: owner}
	if owner != "" {
		act.auth = provenance.AuthSession
		if strings.HasPrefix(strings.ToLower(r.Header.Get("Authorization")), "bearer ") {
			act.auth = provenance.AuthToken
		}
	}
	return act
}

// mcpActor is who acts through MCP: the connection's account, and the
// credential that opened the site — the account's own token or OAuth grant
// when it owns the site, the edit password otherwise.
func mcpActor(auth mcp.Auth, site *store.Site) actor {
	act := actor{surface: provenance.SurfaceMCP, auth: provenance.AuthNone, account: auth.AccountID}
	switch {
	case site != nil && auth.AccountID != "" && auth.AccountID == site.Meta.OwnerAccountID:
		act.auth = provenance.AuthToken
		if auth.OAuth {
			act.auth = provenance.AuthOAuth
		}
	case site != nil:
		act.auth = provenance.AuthPassword
	case auth.OAuth:
		act.auth = provenance.AuthOAuth
	case auth.AccountID != "":
		act.auth = provenance.AuthToken
	}
	return act
}

// recordMCP records one MCP write.
func (a *API) recordMCP(auth mcp.Auth, site *store.Site, action string, files int, detail string) {
	r := auth.Request
	if r == nil {
		r = &http.Request{Header: http.Header{}}
	}
	e := entryFor(r, mcpActor(auth, site), action)
	if auth.ClientIP != "" {
		e.IP = auth.ClientIP
	}
	e.Files = files
	e.Detail = detail
	if action == provenance.ActionSiteDelete {
		e.Site = site.ViewID
		a.mirror(site.Meta.OwnerAccountID, e)
		return
	}
	a.record(site, e)
}

// filesDetail names the first few of paths.
func filesDetail(paths []string) string {
	if len(paths) > maxNotedPaths {
		return strings.Join(paths[:maxNotedPaths], ", ") + ", …"
	}
	return strings.Join(paths, ", ")
}

// recordCreate records a new site: in its own log, with the files it was
// created with, and — for an owned site — in the owner's account log.
func (a *API) recordCreate(r *http.Request, site *store.Site, act actor) {
	e := entryFor(r, act, provenance.ActionCreate)
	if files, err := a.st.ListFiles(site); err == nil {
		e.Files = len(files)
		var paths []string
		for i, f := range files {
			if i > maxNotedPaths {
				break
			}
			paths = append(paths, f.Path)
		}
		e.Detail = filesDetail(paths)
	}
	a.record(site, e)
	m := e
	m.Action = provenance.ActionSiteCreate
	m.Site = site.ViewID
	a.mirror(site.Meta.OwnerAccountID, m)
}

// settingsDetail names the settings a change touched — never a value, except
// the mode, which is not a secret and says what the site became.
func settingsDetail(set updateSet) string {
	var parts []string
	if set.Name != nil {
		parts = append(parts, "name")
	}
	if set.Mode != nil {
		parts = append(parts, "mode="+*set.Mode)
	}
	if set.EntryFile != nil {
		parts = append(parts, "entry_file")
	}
	if set.ViewPassword != nil {
		parts = append(parts, "view_password")
	}
	if set.ViewProtected != nil {
		parts = append(parts, "view_password_protected")
	}
	if set.WebDAV != nil {
		parts = append(parts, "webdav")
	}
	if set.FTP != nil {
		parts = append(parts, "ftp")
	}
	if set.SPA != nil {
		parts = append(parts, "spa")
	}
	if len(set.ExpiresAt) > 0 {
		parts = append(parts, "expires_at")
	}
	if len(set.Domains) > 0 {
		parts = append(parts, "custom_domains")
	}
	return strings.Join(parts, ", ")
}

// countingSink notes every file an upload writes, zips included (counted
// from the archive's directory, which is read anyway).
type countingSink struct {
	uploadSink
	note *provNote
}

func (c countingSink) SaveFile(p string, r io.Reader) error {
	err := c.uploadSink.SaveFile(p, r)
	if errors.Is(err, store.ErrHeld) {
		// written, and it held the site: the file the trail must name
		c.note.addFile(p)
		c.note.held = true
	}
	if err != nil {
		return err
	}
	c.note.addFile(p)
	return nil
}

func (c countingSink) ExtractZip(r io.ReaderAt, n int64) error {
	if err := c.uploadSink.ExtractZip(r, n); err != nil {
		if errors.Is(err, store.ErrHeld) {
			c.note.held = true
			c.note.detail = strings.TrimPrefix(c.note.detail+", a zip", ", ")
		}
		return err
	}
	if zr, err := zip.NewReader(r, n); err == nil {
		for _, f := range zr.File {
			if !strings.HasSuffix(f.Name, "/") {
				c.note.addFile(strings.ReplaceAll(f.Name, `\`, "/"))
			}
		}
	}
	return nil
}

// ---- ext.SiteProvenance ----

var _ ext.SiteProvenance = siteService{}

func (s siteService) SiteProvenance(viewID string) ([]provenance.Entry, error) {
	site, err := s.a.st.ByViewID(viewID)
	if err != nil {
		return nil, mapSiteGone(err, viewID)
	}
	return s.a.st.Provenance(site)
}

func (s siteService) RecordSiteProvenance(viewID string, e provenance.Entry) {
	site, err := s.a.st.ByViewID(viewID)
	if err != nil {
		return
	}
	s.a.record(site, e)
}

func (s siteService) SitesSeenFrom(m provenance.Match) (map[string][]provenance.Entry, error) {
	sites, err := s.a.st.AllSites()
	if err != nil {
		return nil, err
	}
	out := map[string][]provenance.Entry{}
	for _, site := range sites {
		es, err := s.a.st.Provenance(site)
		if err != nil {
			continue
		}
		if hit := m.Filter(es); len(hit) > 0 {
			out[site.ViewID] = hit
		}
	}
	return out, nil
}

// uploadIssuer is the account behind an upload token, for the record.
func (a *API) uploadIssuer(secret string) string {
	if !strings.HasPrefix(secret, ids.UploadTokenPrefix) {
		return ""
	}
	return a.uploads.issuer(secret)
}
