package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/ittrail/sitebin.io/internal/auth"
	"github.com/ittrail/sitebin.io/internal/ext"
	"github.com/ittrail/sitebin.io/internal/store"
	"github.com/ittrail/sitebin.io/internal/viewer"
)

// updateSet carries settings changes. Pointers distinguish "absent" from
// zero values; ExpiresAt additionally distinguishes JSON null (= clear).
type updateSet struct {
	Mode          *string         `json:"mode"`
	EntryFile     *string         `json:"entry_file"`
	ViewPassword  *string         `json:"view_password"`
	ViewProtected *bool           `json:"view_password_protected"`
	WebDAV        *bool           `json:"webdav_enabled"`
	FTP           *bool           `json:"ftp_enabled"`
	SPA           *bool           `json:"spa_fallback"`
	ExpiresAt     json.RawMessage `json:"expires_at"` // absent=keep, null=clear, string=set
	Domains       []string        `json:"custom_domains"`
}

func strPtr(s string) *string { return &s }
func boolPtr(b bool) *bool    { return &b }

// expiryCap returns the effective maximum lifetime in days for a site: the
// per-site tier stamp when present, else the instance global. 0 = unlimited.
func (a *API) expiryCap(site *store.Site) int {
	if site.Meta.QuotaExpiryDays > 0 {
		return site.Meta.QuotaExpiryDays
	}
	return a.cfg.MaxExpiryDays
}

// settingsFromForm maps multipart form fields onto an updateSet.
func settingsFromForm(fields url.Values) (updateSet, error) {
	var set updateSet
	if v := fields.Get("mode"); v != "" {
		set.Mode = strPtr(v)
	}
	if v := fields.Get("entry_file"); v != "" {
		set.EntryFile = strPtr(v)
	}
	if _, ok := fields["view_password"]; ok {
		set.ViewPassword = strPtr(fields.Get("view_password"))
	}
	if v := fields.Get("webdav"); v != "" {
		set.WebDAV = boolPtr(v == "true" || v == "on" || v == "1")
	}
	if v := fields.Get("ftp"); v != "" {
		set.FTP = boolPtr(v == "true" || v == "on" || v == "1")
	}
	if v := fields.Get("spa"); v != "" {
		set.SPA = boolPtr(v == "true" || v == "on" || v == "1")
	}
	if v := fields.Get("expires_at"); v != "" {
		set.ExpiresAt = json.RawMessage(fmt.Sprintf("%q", v))
	}
	set.Domains = fields["domain"]
	return set, nil
}

// applySettings validates and persists an updateSet onto the site's meta.
func (a *API) applySettings(site *store.Site, set updateSet) error {
	var expires **time.Time // set → replace, nil → leave alone
	if len(set.ExpiresAt) > 0 {
		raw := strings.TrimSpace(string(set.ExpiresAt))
		if raw == "null" || raw == `""` {
			// A capped site has a lifetime, not an optional one: letting the
			// holder clear it would turn a 24-hour drop into a permanent site.
			if site.Meta.ExpiresAt != nil {
				if maxDays := a.expiryCap(site); maxDays > 0 {
					return &apiError{400, fmt.Sprintf("this site's plan limits it to %d day(s); its expiry cannot be removed", maxDays)}
				}
			}
			var cleared *time.Time
			expires = &cleared
		} else {
			var s string
			if err := json.Unmarshal(set.ExpiresAt, &s); err != nil {
				return &apiError{400, "expires_at must be an RFC3339 timestamp or null"}
			}
			t, err := time.Parse(time.RFC3339, s)
			if err != nil {
				return &apiError{400, "expires_at must be an RFC3339 timestamp, e.g. 2026-08-01T12:00:00Z"}
			}
			// Per-site tier cap (if stamped) overrides the instance global.
			expiryCap := a.expiryCap(site)
			if expiryCap > 0 {
				max := time.Now().Add(time.Duration(expiryCap) * 24 * time.Hour)
				if t.After(max) {
					return &apiError{400, fmt.Sprintf("expiry exceeds the maximum of %d days for this site", expiryCap)}
				}
			}
			// Anonymous capped sites may shorten their lifetime, never extend
			// it. Gated the same way gatedAnonymous gates WebDAV/FTP: only
			// where a provider is registered and reports AccountsEnabled().
			// A community build has no owner on *any* site (OwnerAccountID is
			// only ever stamped inside the `if gated` branch of createSite)
			// and no concept of "sign in", so without this gate the same
			// condition would misfire there and freeze a site's expiry at
			// whatever date was first picked, forever refusing to move it —
			// even though the community build must stay fully open.
			if a.gatedAnonymous(site) && site.Meta.ExpiresAt != nil &&
				a.expiryCap(site) > 0 && t.After(*site.Meta.ExpiresAt) {
				return &apiError{400, "this site's expiry is fixed at creation; sign in to create sites that renew"}
			}
			tt := t.UTC()
			p := &tt
			expires = &p
		}
	}
	if set.Mode != nil && *set.Mode != store.ModeWebserver && *set.Mode != store.ModeViewer {
		return &apiError{400, `mode must be "webserver" or "viewer"`}
	}

	return a.st.Update(site, func(m *store.Meta) error {
		if set.Mode != nil {
			m.Mode = *set.Mode
		}
		if set.EntryFile != nil {
			clean, err := store.CleanRelPath(*set.EntryFile)
			if err != nil {
				return &apiError{400, "invalid entry_file path"}
			}
			m.EntryFile = clean
		}
		if set.ViewPassword != nil {
			if *set.ViewPassword == "" {
				m.ViewPasswordProtected = false
				m.ViewPasswordHash = ""
			} else {
				m.ViewPasswordHash = auth.HashPassword(*set.ViewPassword)
				m.ViewPasswordProtected = true
			}
		}
		if set.ViewProtected != nil {
			if *set.ViewProtected && m.ViewPasswordHash == "" {
				return &apiError{400, "set view_password before enabling protection"}
			}
			m.ViewPasswordProtected = *set.ViewProtected
			if !*set.ViewProtected {
				m.ViewPasswordHash = ""
			}
		}
		if set.WebDAV != nil {
			if *set.WebDAV && m.QuotaWebDAV != nil && !*m.QuotaWebDAV {
				return &apiError{403, "WebDAV is not available on this site's tier"}
			}
			m.WebDAVEnabled = *set.WebDAV && a.cfg.WebDAVAllowed
		}
		if set.FTP != nil {
			// Silently clamp to false when FTP is off instance-wide (like WebDAV).
			m.FTPEnabled = *set.FTP && a.cfg.FTPEnabled
		}
		if set.SPA != nil {
			m.SPAFallback = *set.SPA
		}
		if expires != nil {
			m.ExpiresAt = *expires
			m.ExpiryFromTier = false
		}
		return nil
	})
}

// apiError carries a user-facing message with a status code through the
// store.Update mutate callback.
type apiError struct {
	code int
	msg  string
}

func (e *apiError) Error() string { return e.msg }

func respondErr(w http.ResponseWriter, err error) {
	var ae *apiError
	if ok := asAPIError(err, &ae); ok {
		writeError(w, ae.code, ae.msg)
		return
	}
	storeError(w, err)
}

func asAPIError(err error, target **apiError) bool {
	for err != nil {
		if ae, ok := err.(*apiError); ok {
			*target = ae
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

// syncViewerLayout reconciles the on-disk layout with the site's mode:
// viewer sites get their wrapper (re)generated, webserver sites get raw
// files restored. Persists the effective entry file when it changed.
func (a *API) syncViewerLayout(site *store.Site) error {
	spaOn := site.Meta.Mode == store.ModeWebserver && site.Meta.SPAFallback
	// Clear the SPA marker before any viewer move so it isn't swept into _raw.
	if !spaOn {
		a.st.RemoveSPAMarker(site)
	}
	if site.Meta.Mode != store.ModeViewer {
		if err := viewer.Remove(site); err != nil {
			return err
		}
	} else {
		entry, err := viewer.Apply(site)
		if err != nil {
			return err
		}
		if entry != site.Meta.EntryFile {
			if err := a.st.Update(site, func(m *store.Meta) error { m.EntryFile = entry; return nil }); err != nil {
				return err
			}
		}
	}
	if spaOn {
		return a.st.WriteSPAMarker(site)
	}
	return nil
}

// partFilename extracts the filename from a multipart part, preserving
// relative paths (Part.FileName strips directories since Go 1.17, but folder
// uploads send paths like "assets/app.js").
func partFilename(p *multipart.Part) string {
	if _, params, err := mime.ParseMediaType(p.Header.Get("Content-Disposition")); err == nil {
		if fn := params["filename"]; fn != "" {
			return fn
		}
	}
	return p.FileName()
}

// consumeUploads streams a multipart body into the site: "files" parts are
// stored under their filename, "zip" parts are extracted, other parts are
// collected as form fields and returned.
func (a *API) consumeUploads(r *http.Request, site *store.Site) (url.Values, error) {
	mr, err := r.MultipartReader()
	if err != nil {
		return nil, &apiError{400, "expected multipart/form-data"}
	}
	fields := url.Values{}
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			return fields, nil
		}
		if err != nil {
			return fields, &apiError{400, "malformed multipart body"}
		}
		switch part.FormName() {
		case "files", "file":
			name := partFilename(part)
			if name == "" {
				part.Close()
				continue
			}
			if err := a.st.SaveFile(site, name, part); err != nil {
				part.Close()
				return fields, err
			}
		case "zip":
			err := a.extractZipPart(site, part)
			part.Close()
			if err != nil {
				return fields, err
			}
		default:
			v, err := io.ReadAll(io.LimitReader(part, 64<<10))
			part.Close()
			if err != nil {
				return fields, err
			}
			fields.Add(part.FormName(), string(v))
		}
	}
}

// extractZipPart spools a zip part to a temp file (zip needs random access)
// and extracts it into the site.
func (a *API) extractZipPart(site *store.Site, part io.Reader) error {
	tmp, err := os.CreateTemp("", "sitebin-zip-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	// allow generous compressed size; extraction enforces the real quota
	n, err := io.Copy(tmp, io.LimitReader(part, a.cfg.MaxSiteBytes+1))
	if err != nil {
		return err
	}
	if n > a.cfg.MaxSiteBytes {
		return store.ErrTooLarge
	}
	return a.st.ExtractZip(site, tmp, n)
}

// sitePayload is the full settings/state document returned by GET/PUT.
func (a *API) sitePayload(site *store.Site) map[string]any {
	files, err := a.st.ListFiles(site)
	if err != nil {
		files = []store.FileInfo{}
	}
	bytes, count, _ := a.st.Usage(site)
	stats := a.st.Stats(site)
	m := site.Meta
	accountsEnabled := false
	if p, ok := ext.Get(); ok {
		accountsEnabled = p.AccountsEnabled()
	}
	// An anonymous site on an accounts-enabled instance has no WebDAV or FTP
	// — gatedAnonymous is the same rule webdav.go and ftpauth.go enforce on
	// the connection itself. Reporting availability from instance config
	// alone would let the edit page keep offering a toggle and mount URL
	// that the server then refuses with 403.
	protocolsGated := a.gatedAnonymous(site)
	webdavAvailable := a.cfg.WebDAVAllowed && !protocolsGated
	ftpAvailable := a.cfg.FTPEnabled && !protocolsGated
	payload := map[string]any{
		"views":                   stats.Views,
		"last_seen":               stats.LastSeen,
		"id":                      m.ID,
		"view_url":                a.cfg.ViewURL(m.ID),
		"edit_url":                a.cfg.EditURL(m.EditID),
		"mode":                    m.Mode,
		"entry_file":              m.EntryFile,
		"spa_fallback":            m.SPAFallback,
		"view_password_protected": m.ViewPasswordProtected,
		"webdav_enabled":          m.WebDAVEnabled,
		"webdav_available":        webdavAvailable,
		"ftp_enabled":             m.FTPEnabled,
		"ftp_available":           ftpAvailable,
		"custom_domains":          m.CustomDomains,
		"origin":                  m.Origin,
		"expires_at":              m.ExpiresAt,
		"expiry_cap_days":         a.expiryCap(site),
		// ExpiryFromTier is part of the condition because sliding renewal is:
		// once the owner sets a date of their own, uploads stop moving it.
		"expiry_renews":    m.OwnerAccountID != "" && m.QuotaExpiryDays > 0 && m.ExpiresAt != nil && m.ExpiryFromTier,
		"accounts_enabled": accountsEnabled,
		"created_at":       m.CreatedAt,
		"updated_at":       m.UpdatedAt,
		"files":            files,
		"base_domain":      a.cfg.BaseDomain,
		"dns_target":       a.cfg.BaseDomain,
		"usage": map[string]any{
			"bytes":     bytes,
			"files":     count,
			"max_bytes": a.st.EffMaxBytes(site),
			"max_files": a.st.EffMaxFiles(site),
		},
	}
	if m.WebDAVEnabled && webdavAvailable {
		payload["webdav_url"] = a.cfg.DAVURL(m.EditID)
	}
	if m.FTPEnabled && ftpAvailable {
		payload["ftp_url"] = a.cfg.FTPURL(m.EditID)
	}
	return payload
}

// ---- handlers ----

// createCORS emits CORS headers for POST /api/sites when the request Origin
// is allowlisted via SITEBIN_EMBED_ORIGINS *and* the enterprise extension is
// active — cross-origin embedding of the create flow is a premium capability.
// Reports whether the origin was allowed. Credentials are never allowed.
func (a *API) createCORS(w http.ResponseWriter, r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" || len(a.cfg.EmbedOrigins) == 0 {
		return false
	}
	if p, ok := ext.Get(); !ok || !p.EmbedOriginsAllowed() {
		return false
	}
	w.Header().Add("Vary", "Origin")
	if !a.embedOriginAllowed(strings.ToLower(origin)) {
		return false
	}
	w.Header().Set("Access-Control-Allow-Origin", origin)
	return true
}

// createPreflight answers CORS preflights for the create endpoint. Multipart
// posts from <sitebin-drop> are "simple requests" that skip preflight, but
// answering OPTIONS keeps stricter clients and future headers working.
func (a *API) createPreflight(w http.ResponseWriter, r *http.Request) {
	if !a.createCORS(w, r) {
		writeError(w, 404, "not found")
		return
	}
	w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
	w.Header().Set("Access-Control-Max-Age", "86400")
	w.WriteHeader(204)
}

// createOpts is what differs between the surfaces that create a site. The
// rules that must NOT differ — the account gate, tier quota stamping, the
// trust marker, the tier's default expiry, viewer layout, ownership recording
// — live in createSiteWith, once.
type createOpts struct {
	// origin is stamped on the new site's meta as provenance ("mcp", or empty
	// for the JSON API and the UI).
	origin string
	// browserOK allows the fromOwnBrowser escape hatch that lets Sitebin's own
	// pages create anonymous sites on a gated instance. True for the JSON API,
	// which serves those pages; false for MCP, which is never one of them.
	browserOK bool
	// fill writes the caller's content into the freshly created site and
	// returns any settings that travelled with it. For a multipart POST the
	// files and the settings arrive in the same pass, which is why this is one
	// callback and not two.
	fill func(*store.Site) (updateSet, error)
}

// createSiteWith creates a site and applies everything that must happen for
// every surface. It returns the site, its one-time edit password, and any
// non-fatal warnings (a custom domain that could not be attached does not
// throw away a site that was otherwise created).
//
// On any error the half-built site is removed, so a failed creation leaves
// nothing behind.
func (a *API) createSiteWith(r *http.Request, opts createOpts) (*store.Site, string, []string, error) {
	if a.cfg.ReadOnly {
		return nil, "", nil, &apiError{503, "this instance is read-only: new sites are disabled"}
	}

	// Enterprise: gate creation behind accounts/tiers when a provider is active.
	// The community build has no provider and creation stays fully open.
	var grant ext.CreateGrant
	gated := false
	if p, ok := ext.Get(); ok && p.AccountsEnabled() {
		gated = true
		var err error
		if grant, err = p.AuthorizeCreate(r); err != nil {
			var ce *ext.CreateError
			if errors.As(err, &ce) {
				return nil, "", nil, &apiError{ce.Status, ce.Msg}
			}
			return nil, "", nil, &apiError{403, "site creation not permitted"}
		}
	}
	owner := grant.OwnerAccountID

	// The API is an account feature: an anonymous drop is something you make
	// on Sitebin's own pages (or an allowlisted embed), not from a script.
	if gated && owner == "" && !(opts.browserOK && a.fromOwnBrowser(r)) {
		return nil, "", nil, &apiError{401, "sign in to create sites from the API: " + a.apiAccountHint()}
	}

	site, editPassword, err := a.st.Create()
	if err != nil {
		a.log.Error("create site", "err", err)
		return nil, "", nil, &apiError{500, "could not create site"}
	}
	fail := func(err error) (*store.Site, string, []string, error) {
		a.st.Delete(site)
		return nil, "", nil, err
	}

	// Stamp ownership + per-site quota caps BEFORE uploads are processed, so the
	// tier caps are enforced on this request's files.
	if gated || opts.origin != "" {
		if err := a.st.Update(site, func(m *store.Meta) error {
			m.Origin = opts.origin
			if gated {
				m.OwnerAccountID = owner
				m.QuotaBytes = grant.MaxSiteBytes
				m.QuotaFiles = grant.MaxFiles
				m.QuotaExpiryDays = grant.MaxExpiryDays
				m.QuotaDomains = grant.MaxCustomDomain
				m.QuotaWebDAV = grant.WebDAV
			}
			return nil
		}); err != nil {
			return fail(err)
		}
	}

	// The trust marker decides whether Caddy adds the strict content-security
	// headers. `gated` is false exactly when no provider is registered — the
	// community build — and there every site is trusted, because that build has
	// no accounts, no tiers and no abuse gating at all. Where a provider does
	// run, only a tier it marks trusted earns the exemption.
	if err := a.st.SetTrusted(site, !gated || grant.Trusted); err != nil {
		return fail(err)
	}

	set, err := opts.fill(site)
	if err != nil {
		return fail(err)
	}

	domains := set.Domains
	set.Domains = nil
	if err := a.applySettings(site, set); err != nil {
		return fail(err)
	}
	// A tier expiry cap is also the default lifetime: capped sites created
	// without an explicit expiry (e.g. anonymous drops on a hosted instance)
	// expire at the cap instead of living forever.
	if site.Meta.QuotaExpiryDays > 0 && site.Meta.ExpiresAt == nil {
		exp := time.Now().Add(time.Duration(site.Meta.QuotaExpiryDays) * 24 * time.Hour).UTC()
		if err := a.st.Update(site, func(m *store.Meta) error { m.ExpiresAt = &exp; m.ExpiryFromTier = true; return nil }); err != nil {
			return fail(err)
		}
	}
	// entry file default: single-file viewer uploads should just work
	if err := a.syncViewerLayout(site); err != nil {
		return fail(err)
	}
	var warnings []string
	for _, d := range domains {
		if err := a.st.AddDomain(site, d); err != nil {
			warnings = append(warnings, fmt.Sprintf("domain %q not added: %v", d, err))
		}
	}

	// Record ownership on the account once creation fully succeeded.
	if owner != "" {
		if p, ok := ext.Get(); ok {
			if err := p.OnSiteCreated(owner, site.ViewID); err != nil {
				a.log.Error("link owned site", "account", owner, "site", site.ViewID, "err", err)
			}
		}
	}
	a.log.Info("site created", "id", site.ViewID, "ip", clientIP(r), "owner", owner, "origin", opts.origin)
	return site, editPassword, warnings, nil
}

func (a *API) createSite(w http.ResponseWriter, r *http.Request) {
	a.createCORS(w, r)
	if !a.createLimiter.Allow(clientIP(r)) {
		writeError(w, 429, "site creation rate limit reached, try again later")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, a.cfg.MaxSiteBytes+(10<<20))

	site, editPassword, warnings, err := a.createSiteWith(r, createOpts{
		browserOK: true,
		fill: func(site *store.Site) (updateSet, error) {
			var set updateSet
			ct := r.Header.Get("Content-Type")
			switch {
			case strings.HasPrefix(ct, "multipart/"):
				fields, err := a.consumeUploads(r, site)
				if err != nil {
					return set, err
				}
				return settingsFromForm(fields)
			case strings.Contains(ct, "json"):
				if err := json.NewDecoder(r.Body).Decode(&set); err != nil && err != io.EOF {
					return set, &apiError{400, "invalid JSON body"}
				}
			}
			return set, nil
		},
	})
	if err != nil {
		respondErr(w, err)
		return
	}

	resp := a.sitePayload(site)
	resp["edit_password"] = editPassword
	if len(warnings) > 0 {
		resp["warnings"] = warnings
	}
	writeJSON(w, 201, resp)
}

func (a *API) getSite(w http.ResponseWriter, r *http.Request, site *store.Site) {
	writeJSON(w, 200, a.sitePayload(site))
}

// getFileContent returns a single content file's bytes (for the editor).
func (a *API) getFileContent(w http.ResponseWriter, r *http.Request, site *store.Site) {
	b, err := a.st.ReadContentFile(site, r.PathValue("path"))
	if err != nil {
		respondErr(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(b)
}

// downloadSite streams the site's content files as a zip.
func (a *API) downloadSite(w http.ResponseWriter, r *http.Request, site *store.Site) {
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="`+site.ViewID+`.zip"`)
	if err := a.st.ZipContent(site, w); err != nil {
		a.log.Error("zip download", "id", site.ViewID, "err", err)
	}
}

func (a *API) updateSite(w http.ResponseWriter, r *http.Request, site *store.Site) {
	var set updateSet
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := json.NewDecoder(r.Body).Decode(&set); err != nil {
		writeError(w, 400, "invalid JSON body")
		return
	}
	oldHash := site.Meta.ViewPasswordHash
	if err := a.applySettings(site, set); err != nil {
		respondErr(w, err)
		return
	}
	if err := a.syncViewerLayout(site); err != nil {
		respondErr(w, err)
		return
	}
	if site.Meta.ViewPasswordHash != oldHash {
		a.log.Info("view password changed", "id", site.ViewID)
	}
	writeJSON(w, 200, a.sitePayload(site))
}

func (a *API) deleteSite(w http.ResponseWriter, r *http.Request, site *store.Site) {
	if err := a.st.Delete(site); err != nil {
		respondErr(w, err)
		return
	}
	a.verifyCache.Drop(site.EditID + ":")
	a.log.Info("site deleted", "id", site.ViewID, "ip", clientIP(r))
	writeJSON(w, 200, map[string]string{"status": "deleted"})
}

func (a *API) uploadFiles(w http.ResponseWriter, r *http.Request, site *store.Site) {
	r.Body = http.MaxBytesReader(w, r.Body, a.cfg.MaxSiteBytes+(10<<20))
	if r.URL.Query().Get("replace") == "true" {
		if err := a.st.ClearFiles(site); err != nil {
			respondErr(w, err)
			return
		}
	}
	if _, err := a.consumeUploads(r, site); err != nil {
		respondErr(w, err)
		return
	}
	if err := a.syncViewerLayout(site); err != nil {
		respondErr(w, err)
		return
	}
	writeJSON(w, 200, a.sitePayload(site))
}

func (a *API) deleteFile(w http.ResponseWriter, r *http.Request, site *store.Site) {
	rel := r.PathValue("path")
	if err := a.st.DeleteFile(site, rel); err != nil {
		respondErr(w, err)
		return
	}
	if err := a.syncViewerLayout(site); err != nil {
		respondErr(w, err)
		return
	}
	writeJSON(w, 200, a.sitePayload(site))
}

func (a *API) addDomain(w http.ResponseWriter, r *http.Request, site *store.Site) {
	// Custom domains are an enterprise feature; the community build has no
	// provider and rejects them.
	if p, ok := ext.Get(); !ok {
		writeError(w, 403, "custom domains are an enterprise feature; see the Enterprise edition")
		return
	} else if err := p.CustomDomainsAllowed(); err != nil {
		writeError(w, 403, err.Error())
		return
	}
	var body struct {
		Domain string `json:"domain"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Domain == "" {
		writeError(w, 400, `body must be {"domain": "example.com"}`)
		return
	}
	if err := a.st.AddDomain(site, body.Domain); err != nil {
		respondErr(w, err)
		return
	}
	writeJSON(w, 200, a.sitePayload(site))
}

func (a *API) removeDomain(w http.ResponseWriter, r *http.Request, site *store.Site) {
	if err := a.st.RemoveDomain(site, r.PathValue("domain")); err != nil {
		respondErr(w, err)
		return
	}
	writeJSON(w, 200, a.sitePayload(site))
}
