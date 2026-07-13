package httpapi

import (
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/ittrail/sitebin/internal/auth"
	"github.com/ittrail/sitebin/internal/store"
	"github.com/ittrail/sitebin/internal/viewer"
)

// updateSet carries settings changes. Pointers distinguish "absent" from
// zero values; ExpiresAt additionally distinguishes JSON null (= clear).
type updateSet struct {
	Mode          *string         `json:"mode"`
	EntryFile     *string         `json:"entry_file"`
	ViewPassword  *string         `json:"view_password"`
	ViewProtected *bool           `json:"view_password_protected"`
	WebDAV        *bool           `json:"webdav_enabled"`
	ExpiresAt     json.RawMessage `json:"expires_at"` // absent=keep, null=clear, string=set
	Domains       []string        `json:"custom_domains"`
}

func strPtr(s string) *string { return &s }
func boolPtr(b bool) *bool    { return &b }

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
			if a.cfg.MaxExpiryDays > 0 {
				max := time.Now().Add(time.Duration(a.cfg.MaxExpiryDays) * 24 * time.Hour)
				if t.After(max) {
					return &apiError{400, fmt.Sprintf("expiry exceeds this instance's maximum of %d days", a.cfg.MaxExpiryDays)}
				}
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
			m.WebDAVEnabled = *set.WebDAV && a.cfg.WebDAVAllowed
		}
		if expires != nil {
			m.ExpiresAt = *expires
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
	if site.Meta.Mode != store.ModeViewer {
		return viewer.Remove(site)
	}
	entry, err := viewer.Apply(site)
	if err != nil {
		return err
	}
	if entry != site.Meta.EntryFile {
		return a.st.Update(site, func(m *store.Meta) error {
			m.EntryFile = entry
			return nil
		})
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
	m := site.Meta
	payload := map[string]any{
		"id":                      m.ID,
		"view_url":                a.cfg.ViewURL(m.ID),
		"edit_url":                a.cfg.EditURL(m.EditID),
		"mode":                    m.Mode,
		"entry_file":              m.EntryFile,
		"view_password_protected": m.ViewPasswordProtected,
		"webdav_enabled":          m.WebDAVEnabled,
		"webdav_available":        a.cfg.WebDAVAllowed,
		"custom_domains":          m.CustomDomains,
		"expires_at":              m.ExpiresAt,
		"created_at":              m.CreatedAt,
		"updated_at":              m.UpdatedAt,
		"files":                   files,
		"base_domain":             a.cfg.BaseDomain,
		"dns_target":              a.cfg.BaseDomain,
		"usage": map[string]any{
			"bytes":     bytes,
			"files":     count,
			"max_bytes": a.cfg.MaxSiteBytes,
			"max_files": a.cfg.MaxFiles,
		},
	}
	if m.WebDAVEnabled && a.cfg.WebDAVAllowed {
		payload["webdav_url"] = a.cfg.DAVURL(m.EditID)
	}
	return payload
}

// ---- handlers ----

func (a *API) createSite(w http.ResponseWriter, r *http.Request) {
	if a.cfg.ReadOnly {
		writeError(w, 503, "this instance is read-only: new sites are disabled")
		return
	}
	if !a.createLimiter.Allow(clientIP(r)) {
		writeError(w, 429, "site creation rate limit reached, try again later")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, a.cfg.MaxSiteBytes+(10<<20))

	site, editPassword, err := a.st.Create()
	if err != nil {
		a.log.Error("create site", "err", err)
		writeError(w, 500, "could not create site")
		return
	}
	fail := func(err error) {
		a.st.Delete(site)
		respondErr(w, err)
	}

	var set updateSet
	ct := r.Header.Get("Content-Type")
	switch {
	case strings.HasPrefix(ct, "multipart/"):
		fields, err := a.consumeUploads(r, site)
		if err != nil {
			fail(err)
			return
		}
		if set, err = settingsFromForm(fields); err != nil {
			fail(err)
			return
		}
	case strings.Contains(ct, "json"):
		if err := json.NewDecoder(r.Body).Decode(&set); err != nil && err != io.EOF {
			fail(&apiError{400, "invalid JSON body"})
			return
		}
	}

	domains := set.Domains
	set.Domains = nil
	if err := a.applySettings(site, set); err != nil {
		fail(err)
		return
	}
	// entry file default: single-file viewer uploads should just work
	if err := a.syncViewerLayout(site); err != nil {
		fail(err)
		return
	}
	var warnings []string
	for _, d := range domains {
		if err := a.st.AddDomain(site, d); err != nil {
			warnings = append(warnings, fmt.Sprintf("domain %q not added: %v", d, err))
		}
	}

	resp := a.sitePayload(site)
	resp["edit_password"] = editPassword
	if len(warnings) > 0 {
		resp["warnings"] = warnings
	}
	a.log.Info("site created", "id", site.ViewID, "ip", clientIP(r))
	writeJSON(w, 201, resp)
}

func (a *API) getSite(w http.ResponseWriter, r *http.Request, site *store.Site) {
	writeJSON(w, 200, a.sitePayload(site))
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
