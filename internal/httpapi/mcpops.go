package httpapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/ittrail/sitebin.io/internal/ext"
	"github.com/ittrail/sitebin.io/internal/mcp"
	"github.com/ittrail/sitebin.io/internal/store"
)

// mcpOps implements mcp.Ops on top of the JSON API's own helpers. Every rule
// an agent meets here — the account gate, tier quotas, expiry caps, rate
// limits, the Argon2 verify cache — is the rule curl meets, because it is
// literally the same code. Nothing about authorization is restated in this
// file; what is here is translation.
type mcpOps struct{ a *API }

// Authenticate resolves the request's credentials. It is the ONLY place MCP
// decides who a caller is, which is what makes Phase 2 (OAuth access tokens) a
// second branch here rather than a change to twelve tool handlers.
//
// It accepts a bearer token and nothing else. The extension's AuthorizeCreate
// additionally honours a dashboard session cookie, so an exotic caller — a
// browser-based MCP client carrying a live Sitebin session — could create an
// owned site here while the per-site tools still ask it for edit passwords.
// That asymmetry is deliberate and errs strict: a session cookie is a person
// at a keyboard, and an agent should be holding a token it was given on
// purpose, not riding someone's browser login.
func (o mcpOps) Authenticate(r *http.Request) mcp.Auth {
	auth := mcp.Auth{ClientIP: clientIP(r), Request: r}
	p, ok := ext.Get()
	if !ok {
		return auth // community build: no accounts, no tokens, fully open
	}
	auth.AccountsEnabled = p.AccountsEnabled()
	if id, ok := p.BearerAccount(r); ok {
		auth.AccountID = id
	}
	return auth
}

// ---- errors ----

// mcpError turns an internal error into the sentence the agent is shown. The
// agent reads nothing else at the moment it is stuck, so every message says
// what to do next, and anything unexpected is logged here and replaced rather
// than leaked.
func (o mcpOps) mcpError(err error) error {
	var ae *apiError
	if asAPIError(err, &ae) {
		return errors.New(ae.msg)
	}
	switch {
	case errors.Is(err, store.ErrNotFound):
		return errors.New("no site with that edit_id — check the id, or list_sites if you have an account token")
	case errors.Is(err, store.ErrDomainTaken):
		return errors.New("that domain is already in use by another site")
	case errors.Is(err, store.ErrTooLarge):
		return errors.New("the site's size limit would be exceeded — delete files, or use a plan with a larger quota")
	case errors.Is(err, store.ErrTooManyFiles):
		return errors.New("the site's file-count limit would be exceeded")
	case errors.Is(err, store.ErrTooManyDomain), errors.Is(err, store.ErrBadDomain):
		return errors.New(err.Error())
	case errors.Is(err, store.ErrBadPath):
		return errors.New("invalid file path: use a relative path with forward slashes, e.g. assets/app.js")
	default:
		o.a.log.Error("mcp internal error", "err", err)
		return errors.New("internal error")
	}
}

// ---- site addressing ----

// openSite authenticates one tool call against one site. It is withEditAuth's
// rule set, reached without an http.ResponseWriter:
//
//   - an account token that owns the site stands in for the edit password;
//   - otherwise the edit password is verified through the same rate-limited,
//     cached path the API and FTP use;
//   - and a site created without an account has no API on a gated instance,
//     so it has no MCP either.
//
// The last rule is where MCP is deliberately stricter than the JSON API: the
// API lets Sitebin's own pages through on browser fetch metadata, and an MCP
// client is never one of Sitebin's own pages.
func (o mcpOps) openSite(auth mcp.Auth, ref mcp.SiteRef) (*store.Site, error) {
	if strings.TrimSpace(ref.EditID) == "" {
		return nil, errors.New("edit_id is required")
	}
	site, err := o.a.st.ByEditID(ref.EditID)
	if err != nil {
		return nil, o.mcpError(err)
	}
	if auth.AccountID != "" && site.Meta.OwnerAccountID != "" && auth.AccountID == site.Meta.OwnerAccountID {
		return site, nil
	}
	if ref.EditPassword == "" {
		if auth.AccountID != "" {
			return nil, errors.New("this site is not owned by the connected account: pass its edit_password, or use list_sites to see the sites this token can manage")
		}
		return nil, errors.New("edit_password is required for this site — it was returned once by create_site")
	}
	switch o.a.verifyEditIP(auth.ClientIP, site, ref.EditPassword) {
	case verifyOK:
		if o.a.gatedAnonymous(site) {
			return nil, errors.New("this site was created without an account, so it cannot be managed by a script or an agent — create it with an account API token at " + o.a.apiAccountHint() + " to automate it")
		}
		return site, nil
	case verifyThrottled:
		return nil, errors.New("too many password attempts for this site, slow down")
	default:
		return nil, errors.New("wrong edit_password")
	}
}

// ---- results ----

// siteResult maps a site onto the struct every site-shaped tool returns.
func (o mcpOps) siteResult(site *store.Site) *mcp.SiteResult {
	files, err := o.a.st.ListFiles(site)
	if err != nil {
		files = []store.FileInfo{}
	}
	bytes, count, _ := o.a.st.Usage(site)
	m := site.Meta
	out := &mcp.SiteResult{
		ID:            m.ID,
		EditID:        m.EditID,
		ViewURL:       o.a.cfg.ViewURL(m.ID),
		EditURL:       o.a.cfg.EditURL(m.EditID),
		Mode:          m.Mode,
		EntryFile:     m.EntryFile,
		SPAFallback:   m.SPAFallback,
		ViewProtected: m.ViewPasswordProtected,
		WebDAVEnabled: m.WebDAVEnabled,
		FTPEnabled:    m.FTPEnabled,
		CustomDomains: m.CustomDomains,
		ExpiresAt:     m.ExpiresAt,
		ExpiryCapDays: o.a.expiryCap(site),
		Files:         make([]mcp.FileInfo, 0, len(files)),
		Bytes:         bytes,
		FileCount:     count,
		MaxBytes:      o.a.st.EffMaxBytes(site),
		MaxFiles:      o.a.st.EffMaxFiles(site),
	}
	for _, f := range files {
		out.Files = append(out.Files, mcp.FileInfo{Path: f.Path, Bytes: f.Size})
	}
	return out
}

// settings maps the tool argument onto the JSON API's updateSet, so
// applySettings — and every expiry and quota rule inside it — is reached
// unchanged. ExpiresAt is the interesting one: the API distinguishes absent,
// null and a timestamp through json.RawMessage, and MCP's *string carries the
// same three states as nil, "" and a value.
func settingsToUpdateSet(s mcp.Settings) updateSet {
	set := updateSet{
		Mode:         s.Mode,
		EntryFile:    s.EntryFile,
		ViewPassword: s.ViewPassword,
		WebDAV:       s.WebDAV,
		FTP:          s.FTP,
		SPA:          s.SPA,
	}
	if s.ExpiresAt != nil {
		if *s.ExpiresAt == "" {
			set.ExpiresAt = []byte("null")
		} else {
			set.ExpiresAt = []byte(fmt.Sprintf("%q", *s.ExpiresAt))
		}
	}
	return set
}

// ---- tools ----

func (o mcpOps) CreateSite(_ context.Context, auth mcp.Auth, in mcp.CreateInput) (*mcp.SiteResult, error) {
	if !o.a.createLimiter.Allow(auth.ClientIP) {
		return nil, errors.New("site creation rate limit reached, try again later")
	}
	// The MCP request is handed to createSiteWith so the extension resolves the
	// caller's tier from the same Authorization header the tools authenticated
	// with. browserOK is false: an MCP client is never one of Sitebin's pages.
	site, editPassword, warnings, err := o.a.createSiteWith(auth.Request, createOpts{
		origin:    store.OriginMCP,
		browserOK: false,
		fill: func(site *store.Site) (updateSet, error) {
			for _, f := range in.Files {
				if err := o.a.st.SaveFile(site, f.Path, bytes.NewReader(f.Data)); err != nil {
					return updateSet{}, err
				}
			}
			set := settingsToUpdateSet(in.Settings)
			set.Domains = in.Domains
			return set, nil
		},
	})
	if err != nil {
		return nil, o.mcpError(err)
	}
	res := o.siteResult(site)
	res.EditPassword = editPassword
	res.Warnings = warnings
	return res, nil
}

func (o mcpOps) ListSites(_ context.Context, auth mcp.Auth) ([]mcp.SiteSummary, error) {
	if auth.AccountID == "" {
		if !auth.AccountsEnabled {
			return nil, errors.New("this Sitebin instance has no accounts, so there is no list of sites to show — keep the edit_id and edit_password create_site returned")
		}
		return nil, errors.New("list_sites needs an account API token: create one at " + o.a.apiAccountHint() + " and send it as an Authorization: Bearer header")
	}
	p, ok := ext.Get()
	if !ok {
		return nil, errors.New("accounts are not available on this instance")
	}
	ids, ok := p.AccountSiteIDs(auth.AccountID)
	if !ok {
		return nil, errors.New("could not read this account's sites")
	}
	out := make([]mcp.SiteSummary, 0, len(ids))
	for _, id := range ids {
		site, err := o.a.st.ByViewID(id)
		if err != nil {
			// The account's ownership record can outlive the site — the edit
			// page's delete and the cleanup sweep both go straight to the
			// store. A stale id is a row to skip, not a failed listing.
			continue
		}
		b, count, _ := o.a.st.Usage(site)
		out = append(out, mcp.SiteSummary{
			ID:        site.Meta.ID,
			EditID:    site.Meta.EditID,
			ViewURL:   o.a.cfg.ViewURL(site.Meta.ID),
			Mode:      site.Meta.Mode,
			Bytes:     b,
			FileCount: count,
			Domains:   site.Meta.CustomDomains,
			CreatedAt: site.Meta.CreatedAt,
			ExpiresAt: site.Meta.ExpiresAt,
			Origin:    site.Meta.Origin,
		})
	}
	return out, nil
}

func (o mcpOps) GetSite(_ context.Context, auth mcp.Auth, ref mcp.SiteRef) (*mcp.SiteResult, error) {
	site, err := o.openSite(auth, ref)
	if err != nil {
		return nil, err
	}
	return o.siteResult(site), nil
}

func (o mcpOps) UpdateSite(_ context.Context, auth mcp.Auth, ref mcp.SiteRef, s mcp.Settings) (*mcp.SiteResult, error) {
	site, err := o.openSite(auth, ref)
	if err != nil {
		return nil, err
	}
	if err := o.a.applySettings(site, settingsToUpdateSet(s)); err != nil {
		return nil, o.mcpError(err)
	}
	if err := o.a.syncViewerLayout(site); err != nil {
		return nil, o.mcpError(err)
	}
	return o.siteResult(site), nil
}

func (o mcpOps) ListFiles(_ context.Context, auth mcp.Auth, ref mcp.SiteRef) ([]mcp.FileInfo, error) {
	site, err := o.openSite(auth, ref)
	if err != nil {
		return nil, err
	}
	return o.siteResult(site).Files, nil
}

// ReadFile returns text as text and anything else as base64. The choice is
// made from the bytes, not the extension: a .txt holding a JPEG is still a
// JPEG, and handing invalid UTF-8 to a model as "text" corrupts it silently.
func (o mcpOps) ReadFile(_ context.Context, auth mcp.Auth, ref mcp.SiteRef, path string) (mcp.File, error) {
	site, err := o.openSite(auth, ref)
	if err != nil {
		return mcp.File{}, err
	}
	b, err := o.a.st.ReadContentFile(site, path)
	if err != nil {
		return mcp.File{}, o.mcpError(err)
	}
	if len(b) > mcp.MaxContentBytes {
		return mcp.File{}, fmt.Errorf("%s is larger than %d MiB — use download_site, or fetch it over HTTP from the site's URL", path, mcp.MaxContentBytes>>20)
	}
	f := mcp.File{Path: path}
	if utf8.Valid(b) && !bytes.ContainsRune(b, 0) {
		f.Text = string(b)
	} else {
		f.Base64 = base64.StdEncoding.EncodeToString(b)
	}
	return f, nil
}

func (o mcpOps) WriteFiles(_ context.Context, auth mcp.Auth, ref mcp.SiteRef, files []mcp.DecodedFile, replace bool) (*mcp.SiteResult, error) {
	site, err := o.openSite(auth, ref)
	if err != nil {
		return nil, err
	}
	if replace {
		if err := o.a.st.ClearFiles(site); err != nil {
			return nil, o.mcpError(err)
		}
	}
	for _, f := range files {
		if err := o.a.st.SaveFile(site, f.Path, bytes.NewReader(f.Data)); err != nil {
			return nil, o.mcpError(err)
		}
	}
	if err := o.a.syncViewerLayout(site); err != nil {
		return nil, o.mcpError(err)
	}
	return o.siteResult(site), nil
}

func (o mcpOps) DeleteFile(_ context.Context, auth mcp.Auth, ref mcp.SiteRef, path string) (*mcp.SiteResult, error) {
	site, err := o.openSite(auth, ref)
	if err != nil {
		return nil, err
	}
	if err := o.a.st.DeleteFile(site, path); err != nil {
		return nil, o.mcpError(err)
	}
	if err := o.a.syncViewerLayout(site); err != nil {
		return nil, o.mcpError(err)
	}
	return o.siteResult(site), nil
}

func (o mcpOps) DeleteSite(_ context.Context, auth mcp.Auth, ref mcp.SiteRef) error {
	site, err := o.openSite(auth, ref)
	if err != nil {
		return err
	}
	if err := o.a.st.Delete(site); err != nil {
		return o.mcpError(err)
	}
	o.a.verifyCache.Drop(site.EditID + ":")
	o.a.log.Info("site deleted", "id", site.ViewID, "via", "mcp")
	return nil
}

func (o mcpOps) AddDomain(_ context.Context, auth mcp.Auth, ref mcp.SiteRef, domain string) (*mcp.SiteResult, error) {
	site, err := o.openSite(auth, ref)
	if err != nil {
		return nil, err
	}
	if p, ok := ext.Get(); !ok || !p.CustomDomainsAllowed() {
		return nil, errors.New("custom domains are an enterprise feature and are not available on this instance")
	}
	if err := o.a.st.AddDomain(site, domain); err != nil {
		return nil, o.mcpError(err)
	}
	return o.siteResult(site), nil
}

func (o mcpOps) RemoveDomain(_ context.Context, auth mcp.Auth, ref mcp.SiteRef, domain string) (*mcp.SiteResult, error) {
	site, err := o.openSite(auth, ref)
	if err != nil {
		return nil, err
	}
	if err := o.a.st.RemoveDomain(site, domain); err != nil {
		return nil, o.mcpError(err)
	}
	return o.siteResult(site), nil
}

func (o mcpOps) DownloadSite(_ context.Context, auth mcp.Auth, ref mcp.SiteRef) ([]byte, error) {
	site, err := o.openSite(auth, ref)
	if err != nil {
		return nil, err
	}
	// Buffered rather than streamed: the result is one JSON-RPC message, so it
	// has to be complete before it can be sent. The cap is what keeps that
	// buffer bounded.
	if b, _, _ := o.a.st.Usage(site); b > mcp.MaxContentBytes {
		return nil, fmt.Errorf("this site holds more than %d MiB — download it over HTTP from its edit URL instead", mcp.MaxContentBytes>>20)
	}
	var buf bytes.Buffer
	if err := o.a.st.ZipContent(site, &buf); err != nil {
		return nil, o.mcpError(err)
	}
	return buf.Bytes(), nil
}
