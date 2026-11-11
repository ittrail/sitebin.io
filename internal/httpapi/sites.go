package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ittrail/sitebin.io/internal/auth"
	"github.com/ittrail/sitebin.io/internal/ext"
	"github.com/ittrail/sitebin.io/internal/provenance"
	"github.com/ittrail/sitebin.io/internal/store"
	"github.com/ittrail/sitebin.io/internal/viewer"
)

// updateSet carries settings changes. Pointers distinguish "absent" from
// zero values; ExpiresAt additionally distinguishes JSON null (= clear).
type updateSet struct {
	Name          *string         `json:"name"` // "" clears it
	Tags          *[]string       `json:"tags"` // nil = leave alone, [] = clear
	Mode          *string         `json:"mode"`
	EntryFile     *string         `json:"entry_file"`
	ViewPassword  *string         `json:"view_password"`
	ViewProtected *bool           `json:"view_password_protected"`
	WebDAV        *bool           `json:"webdav_enabled"`
	FTP           *bool           `json:"ftp_enabled"`
	SPA           *bool           `json:"spa_fallback"`
	Cache         *string         `json:"cache"`          // auto, revalidate, hour, day, week, year or off
	NotFoundPage  *string         `json:"not_found_page"` // "" = the convention (404.html)
	ExpiresAt     json.RawMessage `json:"expires_at"`     // absent=keep, null=clear, string=set
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
	// Present-but-empty is meaningful, as for view_password: it clears.
	if _, ok := fields["name"]; ok {
		set.Name = strPtr(fields.Get("name"))
	}
	// Present means set; each value is split on commas, empty pieces dropped.
	if vals, ok := fields["tags"]; ok {
		tags := []string{}
		for _, v := range vals {
			for _, p := range strings.Split(v, ",") {
				if strings.TrimSpace(p) != "" {
					tags = append(tags, p)
				}
			}
		}
		set.Tags = &tags
	}
	if v := fields.Get("mode"); v != "" {
		set.Mode = strPtr(v)
	}
	// Present but empty asks for the File viewer's list, as "" does in a PUT.
	if _, ok := fields["entry_file"]; ok {
		set.EntryFile = strPtr(fields.Get("entry_file"))
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
	if v := fields.Get("cache"); v != "" {
		set.Cache = strPtr(v)
	}
	// Present-but-empty returns to the convention, as for name.
	if _, ok := fields["not_found_page"]; ok {
		set.NotFoundPage = strPtr(fields.Get("not_found_page"))
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
	if set.Mode != nil && *set.Mode != store.ModeWebserver && *set.Mode != store.ModeViewer && *set.Mode != store.ModeContainer {
		return &apiError{400, `mode must be "webserver", "viewer" or "container"`}
	}
	entering := set.Mode != nil && *set.Mode == store.ModeContainer && site.Meta.Mode != store.ModeContainer
	leaving := set.Mode != nil && *set.Mode != store.ModeContainer && site.Meta.Mode == store.ModeContainer
	if entering {
		if err := containerModeAllowed(site); err != nil {
			return err
		}
	}
	if leaving {
		// Before the mode is written: the containers must be gone and their
		// links purged before Caddy starts serving the tree as files.
		if err := a.leaveContainerMode(site); err != nil {
			return err
		}
		// And what the containers wrote, which no upload scan ever saw, is
		// scanned as the file site it is about to become: a hold stops the
		// switch (the site is locked either way).
		if err := a.st.ScanBeforeServing(site); err != nil {
			return err
		}
	}
	if err := a.writeSettings(site, set, expires); err != nil {
		return err
	}
	if entering {
		return a.enterContainerMode(site)
	}
	return nil
}

// writeSettings persists a validated updateSet.
func (a *API) writeSettings(site *store.Site, set updateSet, expires **time.Time) error {
	return a.st.Update(site, func(m *store.Meta) error {
		if set.Name != nil {
			name, err := store.CleanSiteName(*set.Name)
			if err != nil {
				return &apiError{400, err.Error()}
			}
			m.Name = name
		}
		if set.Tags != nil {
			tags, err := store.CleanTags(*set.Tags)
			if err != nil {
				return &apiError{400, err.Error()}
			}
			if len(tags) == 0 {
				tags = nil
			}
			m.Tags = tags
		}
		if set.Mode != nil {
			m.Mode = *set.Mode
		}
		if set.EntryFile != nil {
			// "" opens the File viewer on its file list.
			clean := ""
			if *set.EntryFile != "" {
				var err error
				if clean, err = store.CleanRelPath(*set.EntryFile); err != nil {
					return &apiError{400, "invalid entry_file path"}
				}
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
		if set.Cache != nil {
			policy, err := store.CleanCachePolicy(*set.Cache)
			if err != nil {
				return &apiError{400, err.Error()}
			}
			m.Cache = policy
		}
		if set.NotFoundPage != nil {
			page, err := store.CleanNotFoundPage(*set.NotFoundPage)
			if err != nil {
				return &apiError{400, err.Error()}
			}
			m.NotFoundPage = page
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

// uploadFormRoom is what a multipart body may carry beyond the site's size
// cap: boundaries, part headers and form fields.
const uploadFormRoom = 10 << 20

// UploadReadTimeout is how long the public server reads one request's body
// (cmd/sitebin's ReadTimeout): an upload must arrive within it. Here so the
// answer to one that did not can name it.
const UploadReadTimeout = 10 * time.Minute

// uploadBodyLimit is the most a multipart upload to site may carry: the
// site's own size cap — the plan's, stamped at creation, else the instance's
// default — plus room for the form. The store enforces the cap itself on
// every file; this only stops a body that could never fit from streaming on.
func (a *API) uploadBodyLimit(site *store.Site) int64 {
	return a.st.EffMaxBytes(site) + uploadFormRoom
}

// errUploadCutOff is a request body that ended in the middle of a file: the
// connection dropped on the way, which is the client's to retry, not a fault
// of the server. Only bodyReader produces it, so a short read anywhere else
// can never be mistaken for one.
var errUploadCutOff = errors.New("the upload was cut off before it was complete — send it again")

// errUploadTimedOut is a request body still arriving when the public
// server's read timeout ran out.
var errUploadTimedOut = fmt.Errorf("the upload took longer than the %d minutes one request may take — send the files in several requests", int(UploadReadTimeout/time.Minute))

// bodyError names what went wrong reading an upload's body, or returns nil
// when it was something else (a malformed body): too large (the
// *http.MaxBytesError itself, which storeError answers with 413 and the
// limit), timed out, or cut off.
func bodyError(err error) error {
	var tooLarge *http.MaxBytesError
	switch {
	case errors.As(err, &tooLarge):
		return tooLarge
	case errors.Is(err, os.ErrDeadlineExceeded):
		return errUploadTimedOut
	case errors.Is(err, io.ErrUnexpectedEOF), errors.Is(err, errUploadCutOff):
		return errUploadCutOff
	}
	return nil
}

// bodyReader reads an upload part and reports the body ending early as
// errUploadCutOff, and the read timeout running out as errUploadTimedOut.
type bodyReader struct{ r io.Reader }

func (b bodyReader) Read(p []byte) (int, error) {
	n, err := b.r.Read(p)
	if err != nil && err != io.EOF {
		if be := bodyError(err); be != nil {
			err = be
		}
	}
	return n, err
}

// uploadSink is where an upload's files go: straight into the live site, or
// into a staged replacement that reaches the site only once it is complete.
type uploadSink interface {
	SaveFile(relPath string, r io.Reader) error
	ExtractZip(r io.ReaderAt, size int64) error
}

// liveSink writes into the site as it serves.
type liveSink struct {
	st   *store.Store
	site *store.Site
}

func (l liveSink) SaveFile(p string, r io.Reader) error    { return l.st.SaveFile(l.site, p, r) }
func (l liveSink) ExtractZip(r io.ReaderAt, n int64) error { return l.st.ExtractZip(l.site, r, n) }

// consumeUploads streams a multipart body into sink: "files" parts are
// stored under their filename, "zip" parts are extracted, other parts are
// collected as form fields and returned. maxBytes is the site's size cap,
// which also bounds a zip archive's spool.
func (a *API) consumeUploads(r *http.Request, sink uploadSink, maxBytes int64) (url.Values, error) {
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
			// Between parts the body can still run past its limit or out of
			// time; only what is neither is a malformed body.
			if be := bodyError(err); be != nil {
				return fields, be
			}
			return fields, &apiError{400, "malformed multipart body"}
		}
		switch part.FormName() {
		case "files", "file":
			name := partFilename(part)
			if name == "" {
				part.Close()
				continue
			}
			if err := sink.SaveFile(name, bodyReader{part}); err != nil {
				part.Close()
				return fields, err
			}
		case "zip":
			err := a.extractZipPart(sink, bodyReader{part}, maxBytes)
			part.Close()
			if err != nil {
				return fields, err
			}
		default:
			v, err := io.ReadAll(io.LimitReader(bodyReader{part}, 64<<10))
			part.Close()
			if err != nil {
				return fields, err
			}
			fields.Add(part.FormName(), string(v))
		}
	}
}

// zipSpoolSlots bounds how many archives are spooled at once: each is up to a
// site's byte cap on disk, and the spool lives on the data volume, so a burst
// of uploads must queue rather than fill it.
var zipSpoolSlots = make(chan struct{}, 4)

// extractZipPart spools a zip part to a temp file (zip needs random access)
// under the data volume — never the container's /tmp, which is unbounded and
// not what the operator sized — and extracts it into sink. maxBytes is the
// site's size cap: no archive larger than that can hold what fits.
func (a *API) extractZipPart(sink uploadSink, part io.Reader, maxBytes int64) error {
	zipSpoolSlots <- struct{}{}
	defer func() { <-zipSpoolSlots }()
	spool := filepath.Join(a.cfg.DataDir, "tmp")
	if err := os.MkdirAll(spool, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(spool, "sitebin-zip-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	// allow generous compressed size; extraction enforces the real quota
	n, err := io.Copy(tmp, io.LimitReader(part, maxBytes+1))
	if err != nil {
		return err
	}
	if n > maxBytes {
		return store.ErrTooLarge
	}
	return sink.ExtractZip(tmp, n)
}

// pendingDomain is one claimed-but-unproven custom domain and the DNS
// record that proves it. Either record suffices.
type pendingDomain struct {
	Domain      string `json:"domain"`
	TXTName     string `json:"txt_name"`
	TXTValue    string `json:"txt_value"`
	CNAMETarget string `json:"cname_target,omitempty"`
	RequestedAt string `json:"requested_at"`
	// CheckedAt and CheckResult are the last check, the owner's or the
	// sweep's, and what it found (store.DomainClaim.CheckResult).
	CheckedAt   string `json:"checked_at,omitempty"`
	CheckResult string `json:"check_result,omitempty"`
}

// pendingDomains lists the site's unverified claims with their records. The
// CNAME route exists only where the site has a view host.
func (a *API) pendingDomains(site *store.Site) []pendingDomain {
	out := []pendingDomain{}
	for _, c := range site.PendingDomains() {
		p := pendingDomain{Domain: c.Domain, TXTName: c.TXTName(), TXTValue: c.TXTValue(), RequestedAt: c.RequestedAt.UTC().Format(time.RFC3339), CheckResult: c.CheckResult}
		if c.CheckedAt != nil {
			p.CheckedAt = c.CheckedAt.UTC().Format(time.RFC3339)
		}
		if a.cfg.SubdomainViews() {
			p.CNAMETarget = site.ViewID + "." + a.cfg.ViewDomain
		}
		out = append(out, p)
	}
	return out
}

// domainProof is the record that makes a custom domain held through its
// host independent: the same TXT a pending claim shows.
type domainProof struct {
	ViaHost  bool   `json:"via_host"`
	TXTName  string `json:"txt_name"`
	TXTValue string `json:"txt_value"`
}

// addressNotes are what the owner needs to know about the site's custom
// domains with a path and about the host they share: a site not built for
// its path (domain_warnings), an address held through the host and the
// record that would make it independent (domain_proofs), and the addresses
// that would lose their hold were this one removed (domain_dependents).
// Each is left out of the payload when empty.
func (a *API) addressNotes(site *store.Site, payload map[string]any) {
	warnings := map[string]string{}
	proofs := map[string]domainProof{}
	dependents := map[string][]string{}
	for _, c := range site.Meta.DomainClaims {
		if w := a.st.BasePathWarning(site, c.Domain); w != "" {
			warnings[c.Domain] = w
		}
		if c.Verified() && c.ViaHost {
			proofs[c.Domain] = domainProof{ViaHost: true, TXTName: c.TXTName(), TXTValue: c.TXTValue()}
		}
	}
	for _, d := range site.Meta.CustomDomains {
		if deps := a.st.Dependents(site, d); len(deps) > 0 {
			dependents[d] = deps
		}
	}
	if len(warnings) > 0 {
		payload["domain_warnings"] = warnings
	}
	if len(proofs) > 0 {
		payload["domain_proofs"] = proofs
	}
	if len(dependents) > 0 {
		payload["domain_dependents"] = dependents
	}
}

// sitePayload is the full settings/state document returned by GET/PUT.
func (a *API) sitePayload(site *store.Site) map[string]any {
	files, err := a.st.ListFiles(site)
	if err != nil || site.Meta.IsLocked() {
		// A locked site is shown to its owner so they can see the lock, and
		// its content stays out of reach: not even the names of its files.
		files = []store.FileInfo{}
	}
	// A container site's tree holds what its containers wrote — a
	// node_modules alone is tens of thousands of files — and the edit page
	// renders every entry it is sent.
	filesTruncated := false
	if m := site.Meta; m.Mode == store.ModeContainer && len(files) > maxListedContainerFiles {
		files, filesTruncated = files[:maxListedContainerFiles], true
	}
	usage, _ := a.st.SiteUsage(site)
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
	tags := m.Tags
	if tags == nil {
		tags = []string{}
	}
	gs := a.gitStateOf(site)
	ar := a.analyticsReason(site)
	payload := map[string]any{
		"views":                   stats.Views,
		"last_seen":               stats.LastSeen,
		"id":                      m.ID,
		"name":                    m.Name,
		"tags":                    tags,
		"view_url":                a.cfg.ViewURL(m.ID),
		"edit_url":                a.cfg.EditURL(m.EditID),
		"mode":                    m.Mode,
		"entry_file":              m.EntryFile,
		"spa_fallback":            m.SPAFallback,
		"cache":                   m.CachePolicy(),
		"not_found_page":          m.NotFoundPage,
		"not_found_serves":        store.NotFoundServes(site),
		"start_page_missing":      store.MissingStartPage(site),
		"view_password_protected": m.ViewPasswordProtected,
		"webdav_enabled":          m.WebDAVEnabled,
		"webdav_available":        webdavAvailable,
		"ftp_enabled":             m.FTPEnabled,
		"ftp_available":           ftpAvailable,
		"analytics_available":     ar == "" || ar == analyticsReasonPlan, // the Analytics tab, shown like the Git tab's
		"custom_domains":          m.CustomDomains,
		"domain_redirects":        redirectsPayload(m),
		"view_address":            viewAddressPayload(m),
		"addresses":               a.addressesPayload(m),
		"domain_targets":          domainTargetsPayload(m),
		"primary_url":             a.primaryURL(site),
		"container":               a.containerPayload(site),
		"pending_domains":         a.pendingDomains(site),
		"domain_recheck_minutes":  a.recheckMinutes(),
		"zone_domains":            a.zoneDomains(site),
		"origin":                  m.Origin,
		"locked":                  lockPayload(m.Locked),
		"expires_at":              m.ExpiresAt,
		"expiry_cap_days":         a.expiryCap(site),
		// ExpiryFromTier is part of the condition because sliding renewal is:
		// once the owner sets a date of their own, uploads stop moving it.
		"expiry_renews":    m.OwnerAccountID != "" && m.QuotaExpiryDays > 0 && m.ExpiresAt != nil && m.ExpiryFromTier,
		"accounts_enabled": accountsEnabled,
		// The edit page's Git tab: shown when Git works, or when only the
		// plan lacks it (the tab then carries the upgrade notice).
		"git_available": gs.Available || gs.Reason == gitReasonPlan,
		// The Backups tab: the instance has a snapshots agent.
		"backups_available": a.snap != nil,
		"created_at":        m.CreatedAt,
		"updated_at":        m.UpdatedAt,
		"files":             files,
		"files_truncated":   filesTruncated,
		"base_domain":       a.cfg.BaseDomain,
		"dns_target":        a.cfg.BaseDomain,
		"usage": map[string]any{
			"bytes":     usage.Bytes(), // files + repository, the figure the cap is checked against
			"git_bytes": usage.GitBytes,
			"files":     usage.Files,
			"max_bytes": a.st.EffMaxBytes(site),
			"max_files": maxFilesPayload(a.st.EffMaxFiles(site)),
		},
	}
	if m.WebDAVEnabled && webdavAvailable {
		payload["webdav_url"] = a.cfg.DAVURL(m.EditID)
	}
	if m.FTPEnabled && ftpAvailable {
		payload["ftp_url"] = a.cfg.FTPURL(m.EditID)
	}
	a.addressNotes(site, payload)
	return payload
}

// ---- handlers ----

// createCORS emits CORS headers for POST /api/sites when the request Origin
// is allowlisted via SITEBIN_EMBED_ORIGINS *and* the enterprise extension is
// active — cross-origin embedding of the create flow is a premium capability.
// Reports whether the origin was allowed. Credentials are allowed only for a
// session origin (SITEBIN_SESSION_ORIGINS), whose drop may carry the visitor's
// session; see createSiteWith.
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
	if a.isSessionOrigin(origin) {
		w.Header().Set("Access-Control-Allow-Credentials", "true")
	}
	return true
}

// createPreflight answers CORS preflights for the create endpoint. Multipart
// posts from <sitebin-drop> are "simple requests" that skip preflight, but
// answering OPTIONS keeps stricter clients and future headers working. A
// session origin's drop sends the session header, which needs this preflight:
// only a session origin is allowed it, so no other page can ask for the
// visitor's session from a browser.
func (a *API) createPreflight(w http.ResponseWriter, r *http.Request) {
	if !a.createCORS(w, r) {
		writeError(w, 404, "not found")
		return
	}
	w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
	if a.isSessionOrigin(r.Header.Get("Origin")) {
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, "+sessionHeader)
	} else {
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
	}
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
	// actor is who is creating the site, for its provenance record. Nil for
	// the JSON API and the UI, where createSiteWith works it out from the
	// request and the grant.
	actor *actor
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
		// A browser's session cookie counts for a new site only on the
		// instance's own pages, or from a session origin that asked for it
		// with the session header. Any other cross-origin request that
		// carries the cookie — a same-site page, an embed origin — is handed
		// to the extension without it, so it creates an anonymous site (or is
		// refused like any anonymous script), never one in the visitor's
		// account. Ignored, not refused. Everything else below keeps r.
		// A surface that is never one of Sitebin's pages (MCP, browserOK
		// false) never has its session read, whatever headers it carries.
		authReq := r
		if !opts.browserOK || (a.crossOriginBrowser(r) && !a.sessionOriginRequest(r)) {
			authReq = withoutCookies(r)
		}
		var err error
		if grant, err = p.AuthorizeCreate(authReq); err != nil {
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
				stampCaps(m, grant)
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
	if errors.Is(err, store.ErrHeld) {
		// The abuse guard locked the site on its first files. It stays, as
		// evidence — its creation recorded like any other, since who made
		// it is the first thing the operator will ask — and its caller gets
		// neither its address nor its password.
		a.keepHeld(site, owner)
		a.recordCreate(r, site, a.creator(r, owner, opts))
		return nil, "", nil, &apiError{403, msgHeldCreate}
	}
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
		if err := a.st.Update(site, func(m *store.Meta) error { setTierExpiry(m, time.Now()); return nil }); err != nil {
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
	a.recordCreate(r, site, a.creator(r, owner, opts))
	a.log.Info("site created", "id", site.ViewID, "owner", owner, "origin", opts.origin)
	return site, editPassword, warnings, nil
}

// creator is who is creating a site, for its provenance record.
func (a *API) creator(r *http.Request, owner string, opts createOpts) actor {
	if opts.actor != nil {
		return *opts.actor
	}
	return a.createActor(r, owner)
}

// keepHeld finishes the bookkeeping of a creation the abuse guard held: the
// site is stamped with its plan's lifetime, which applies again should the
// operator release it, and linked to its owner, so the owner's dashboard
// shows it locked and a suspension of the owner finds it.
func (a *API) keepHeld(site *store.Site, owner string) {
	if err := a.st.Update(site, func(m *store.Meta) error {
		if m.QuotaExpiryDays > 0 && m.ExpiresAt == nil {
			exp := time.Now().Add(time.Duration(m.QuotaExpiryDays) * 24 * time.Hour).UTC()
			m.ExpiresAt, m.ExpiryFromTier = &exp, true
		}
		return nil
	}); err != nil {
		a.log.Error("held site: stamp expiry", "id", site.ViewID, "err", err)
	}
	if owner != "" {
		if p, ok := ext.Get(); ok {
			if err := p.OnSiteCreated(owner, site.ViewID); err != nil {
				a.log.Error("link owned site", "account", owner, "site", site.ViewID, "err", err)
			}
		}
	}
	a.log.Info("site created and held for review", "id", site.ViewID, "owner", owner)
}

func (a *API) createSite(w http.ResponseWriter, r *http.Request) {
	a.createCORS(w, r)
	// An upload token belongs to one existing site; it creates nothing.
	if uploadCredential(r) != "" {
		writeError(w, 403, msgUploadTokenOnlyUploads)
		return
	}
	if !a.createLimiter.Allow(clientIP(r)) {
		writeError(w, 429, "site creation rate limit reached, try again later")
		return
	}
	site, editPassword, warnings, err := a.createSiteWith(r, createOpts{
		browserOK: true,
		fill: func(site *store.Site) (updateSet, error) {
			// Nothing reads the body before the site exists: its cap — the
			// plan's, stamped by now — is what bounds it, not the instance's
			// default.
			r.Body = http.MaxBytesReader(w, r.Body, a.uploadBodyLimit(site))
			var set updateSet
			ct := r.Header.Get("Content-Type")
			switch {
			case strings.HasPrefix(ct, "multipart/"):
				fields, err := a.consumeUploads(r, liveSink{st: a.st, site: site}, a.st.EffMaxBytes(site))
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

	resp := a.payloadFor(r, site) // a creation has no gate actor: no role
	resp["edit_password"] = editPassword
	// Whether the site went into an account, so an embed can tell an owned
	// ticket from an anonymous one without asking again.
	resp["owned"] = site.Meta.OwnerAccountID != ""
	warnings = append(warnings, a.startPageWarnings(site)...)
	if len(warnings) > 0 {
		resp["warnings"] = warnings
	}
	writeJSON(w, 201, resp)
}

func (a *API) getSite(w http.ResponseWriter, r *http.Request, site *store.Site) {
	writeJSON(w, 200, a.payloadFor(r, site))
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

// listDir answers one folder of the site — its sub-folders and files — for
// the edit page's folder browser (?path=, "" for the top).
func (a *API) listDir(w http.ResponseWriter, r *http.Request, site *store.Site) {
	rel, err := store.CleanDirPath(r.URL.Query().Get("path"))
	if err != nil {
		storeError(w, err)
		return
	}
	entries, truncated, err := a.st.ListDir(site, rel)
	if err != nil {
		storeError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"path": rel, "entries": entries, "truncated": truncated})
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
	noteOf(r).detail = settingsDetail(set)
	// An admin is refused an owner-only setting here, before anything is
	// written, and an admin's tags are their own (applySettingsAs).
	changed, err := a.applySettingsAs(site, set, a.actorOf(r))
	if err != nil {
		respondErr(w, err)
		return
	}
	if !changed {
		// Only an admin's own labels changed: nothing of the site to lay
		// out, or to record in its log.
		noteOf(r).skip = true
		writeJSON(w, 200, a.payloadFor(r, site))
		return
	}
	if err := a.syncViewerLayout(site); err != nil {
		respondErr(w, err)
		return
	}
	if site.Meta.ViewPasswordHash != oldHash {
		a.log.Info("view password changed", "id", site.ViewID)
	}
	writeJSON(w, 200, a.payloadFor(r, site))
}

func (a *API) deleteSite(w http.ResponseWriter, r *http.Request, site *store.Site) {
	if a.refuseUnlessOwner(w, r) {
		return
	}
	a.stopContainersBeforeDelete(site)
	if err := a.st.Delete(site); err != nil {
		respondErr(w, err)
		return
	}
	a.verifyCache.Drop(site.EditID + ":")
	a.uploads.revokeSite(site.ViewID)
	a.log.Info("site deleted", "id", site.ViewID)
	writeJSON(w, 200, map[string]string{"status": "deleted"})
}

func (a *API) uploadFiles(w http.ResponseWriter, r *http.Request, site *store.Site) {
	r.Body = http.MaxBytesReader(w, r.Body, a.uploadBodyLimit(site))
	note := noteOf(r)
	if r.URL.Query().Get("replace") == "true" {
		note.action = provenance.ActionReplace
		// The old files go only once the new ones are all in and within the
		// site's caps: a failed, cut-off or over-quota replace leaves the
		// site exactly as it was.
		rep, err := a.st.BeginReplace(site)
		if err != nil {
			respondErr(w, err)
			return
		}
		defer rep.Abort()
		if _, err := a.consumeUploads(r, countingSink{rep, note}, a.st.EffMaxBytes(site)); err != nil {
			respondErr(w, err)
			return
		}
		if err := rep.Commit(); err != nil {
			// A held replace is committed, as evidence: its files are the
			// ones the trail names.
			note.held = errors.Is(err, store.ErrHeld)
			respondErr(w, err)
			return
		}
	} else if _, err := a.consumeUploads(r, countingSink{liveSink{st: a.st, site: site}, note}, a.st.EffMaxBytes(site)); err != nil {
		respondErr(w, err)
		return
	}
	if err := a.syncViewerLayout(site); err != nil {
		respondErr(w, err)
		return
	}
	resp := a.payloadFor(r, site)
	if ws := a.startPageWarnings(site); len(ws) > 0 {
		resp["warnings"] = ws
	}
	writeJSON(w, 200, resp)
}

func (a *API) deleteFile(w http.ResponseWriter, r *http.Request, site *store.Site) {
	rel := r.PathValue("path")
	noteOf(r).detail = rel
	if err := a.st.DeleteFile(site, rel); err != nil {
		respondErr(w, err)
		return
	}
	if err := a.syncViewerLayout(site); err != nil {
		respondErr(w, err)
		return
	}
	writeJSON(w, 200, a.payloadFor(r, site))
}

func (a *API) addDomain(w http.ResponseWriter, r *http.Request, site *store.Site) {
	var body struct {
		Domain string `json:"domain"`
		Target string `json:"target"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Domain == "" {
		writeError(w, 400, `body must be {"domain": "example.com"} — on a container site optionally with "target": "service:port"`)
		return
	}
	pending, err := a.claimDomain(site, body.Domain, body.Target)
	if err != nil {
		respondErr(w, err)
		return
	}
	noteOf(r).detail = strings.ToLower(strings.TrimSpace(body.Domain))
	if t := site.Meta.DomainTargets[strings.ToLower(strings.TrimSuffix(strings.TrimSpace(body.Domain), "."))]; t != "" {
		noteOf(r).detail += " → " + t
	}
	if pending != "" {
		noteOf(r).detail += " (pending)"
		// Recorded, not attached: the payload carries the record to create
		// and what this check found.
		a.log.Info("custom domain pending verification", "id", site.ViewID, "owner", site.Meta.OwnerAccountID, "domain", body.Domain)
		writeJSON(w, 202, a.payloadFor(r, site))
		return
	}
	a.log.Info("custom domain attached", "id", site.ViewID, "owner", site.Meta.OwnerAccountID, "domain", body.Domain)
	writeJSON(w, 200, a.payloadFor(r, site))
}

// Checks an owner asks for — adding a domain, "Check now", MCP's add_domain —
// each send a handful of queries to the nameservers of a domain the owner
// names. Per claim: three at once, then one a minute; per site: ten, then two
// a minute. The sweep's own checks are not counted: one pass per interval.
const (
	domainChecksPerHour     = 60
	domainChecksBurst       = 3
	domainSiteChecksPerHour = 120
	domainSiteChecksBurst   = 10
)

// claimDomain is the one path an owner's request takes to claim a custom
// domain or to check a claim again, from the edit page, the API and MCP
// alike: the edition and licence gate, the target rule, the rate limit, then
// the store. A claim left pending is not an error: pending is what the check
// found, which the claim also records. Both "" and nil: attached.
//
// On a container site the domain serves target, one of the HTTP ports the
// compose file declares; without one it keeps the target it has, or gets the
// port of the site's own address.
func (a *API) claimDomain(site *store.Site, domain, target string) (pending string, err error) {
	// Custom domains are an enterprise feature; the community build has no
	// provider and rejects them.
	if p, ok := ext.Get(); !ok {
		return "", &apiError{403, "custom domains are an enterprise feature and are not available on this instance; see the Enterprise edition"}
	} else if err := p.CustomDomainsAllowed(); err != nil {
		return "", &apiError{403, err.Error()}
	}
	target = strings.TrimSpace(target)
	if target != "" {
		if site.Meta.Mode != store.ModeContainer {
			return "", &apiError{400, "target names a port of a container site; this site's domains serve its files"}
		}
		if !site.Meta.HasEndpoint(target) {
			return "", addressError(site.Meta.UnknownEndpoint(target))
		}
	}
	key := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(domain), "."))
	if !a.domainChecks.Allow(site.ViewID+"|"+key) || !a.domainSites.Allow(site.ViewID) {
		return "", &apiError{429, fmt.Sprintf("%s was checked a moment ago; try again in a minute. "+
			"The instance also checks pending domains by itself every %d minutes.", key, a.recheckMinutes())}
	}
	err = a.st.AddDomain(site, domain)
	if err == nil || errors.Is(err, store.ErrDomainPending) {
		// The domain is claimed now, attached or not: it serves its port as
		// soon as it is attached.
		var terr error
		if target != "" {
			terr = a.st.SetDomainTarget(site, domain, target)
		} else {
			terr = a.st.DefaultDomainTarget(site, domain)
		}
		if terr != nil {
			return "", addressError(terr)
		}
	}
	if !errors.Is(err, store.ErrDomainPending) {
		return "", err
	}
	for _, c := range site.PendingDomains() {
		if c.Domain == key && c.CheckResult != "" {
			return c.CheckResult, nil
		}
	}
	return strings.TrimPrefix(err.Error(), store.ErrDomainPending.Error()+": "), nil
}

// recheckMinutes is how often the sweep checks pending domains on its own.
func (a *API) recheckMinutes() int {
	return max(1, int(a.cfg.CleanupInterval.Round(time.Minute).Minutes()))
}

func (a *API) removeDomain(w http.ResponseWriter, r *http.Request, site *store.Site) {
	noteOf(r).detail = r.PathValue("domain")
	if err := a.st.RemoveDomain(site, r.PathValue("domain")); err != nil {
		respondErr(w, err)
		return
	}
	writeJSON(w, 200, a.payloadFor(r, site))
}

// maxListedContainerFiles caps the file listing of a container site.
const maxListedContainerFiles = 2000

// maxFilesPayload reports "no cap" as 0, which is how the edit page and API
// clients read an absent limit.
func maxFilesPayload(n int) int {
	if n == math.MaxInt {
		return 0
	}
	return n
}
