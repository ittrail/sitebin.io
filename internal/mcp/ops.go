// Package mcp serves Sitebin's Model Context Protocol endpoint: the tool
// catalog an AI agent uses to publish and manage sites.
//
// It is a second transport onto the authorization the JSON API already has,
// not a second set of rules. This package knows the MCP protocol and the shape
// of the tools; everything else — the store, quotas, accounts, rate limits —
// lives behind the Ops interface, implemented by internal/httpapi. That
// boundary is why this package can be tested without a filesystem, and why the
// adapter can be tested without a fake.
package mcp

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// MaxContentBytes caps the decoded file content one tool call may carry.
//
// This is a protocol sanity limit, not a security limit — the store's quota is
// the security limit, and it still applies. Moving more than this through a
// JSON-RPC body and a model's tool-call serializer is a mistake rather than a
// use case, and the error says which protocol to use instead.
const MaxContentBytes = 8 << 20

// Auth is a session's resolved identity, produced once from the HTTP request
// that opens the MCP session.
//
// It has three states, mirroring the JSON API exactly:
//
//	AccountID != ""                      an account API token was presented
//	AccountID == "", AccountsEnabled     an anonymous caller on a gated instance
//	AccountID == "", !AccountsEnabled    the community build: fully open
type Auth struct {
	// AccountID is the account an Authorization: Bearer token resolved to, or
	// empty when no usable token was presented.
	AccountID string
	// AccountsEnabled reports whether the instance gates on accounts at all.
	// False in the community build, where there is no provider.
	AccountsEnabled bool
	// ClientIP identifies the caller for rate limiting. It is opaque to this
	// package, which only carries it: the adapter owns the limiters, because
	// an agent must not be able to guess an edit password faster than curl.
	ClientIP string
	// Request is the HTTP request this identity was resolved from. It is
	// carried because the extension seam resolves an account's tier from an
	// *http.Request, so creating a site needs the original request rather than
	// a summary of it. Tool handlers must not read it for authorization —
	// Authenticate has already answered that question, and answering it twice
	// is how the two answers drift apart.
	Request *http.Request
}

// SiteRef addresses one site. EditPassword may be empty when the session's
// token already owns the site — that is the whole point of a token, and the
// adapter decides, not this package.
type SiteRef struct {
	EditID       string
	EditPassword string
}

// File is one file moving in or out of a site. Exactly one of Text or Base64
// is set; see DecodeFiles for why that is checked here rather than in the
// adapter.
type File struct {
	Path   string `json:"path" jsonschema:"relative path inside the site, e.g. index.html or assets/app.js"`
	Text   string `json:"text,omitempty" jsonschema:"UTF-8 contents; use this for HTML, CSS, JS, JSON, SVG and Markdown"`
	Base64 string `json:"base64,omitempty" jsonschema:"base64-encoded contents; use this only for binary files such as PNG or JPEG"`
}

// DecodedFile is a File with its content resolved to bytes.
type DecodedFile struct {
	Path string
	Data []byte
}

// Settings is the subset of a site's configuration a tool may change. Every
// field is optional; a nil pointer means "leave it alone", which is the same
// distinction the JSON API's updateSet draws with the same technique.
type Settings struct {
	Mode         *string `json:"mode,omitempty" jsonschema:"webserver (serve the files as a site) or viewer (wrap a single document in a viewer)"`
	EntryFile    *string `json:"entry_file,omitempty" jsonschema:"the file to serve at the site root"`
	ViewPassword *string `json:"view_password,omitempty" jsonschema:"password visitors must enter; an empty string removes the protection"`
	WebDAV       *bool   `json:"webdav_enabled,omitempty" jsonschema:"expose the site over WebDAV"`
	FTP          *bool   `json:"ftp_enabled,omitempty" jsonschema:"expose the site over FTP"`
	SPA          *bool   `json:"spa_fallback,omitempty" jsonschema:"webserver mode: serve index.html for unknown paths, for single-page apps"`
	ExpiresAt    *string `json:"expires_at,omitempty" jsonschema:"RFC3339 timestamp when the site stops serving; the empty string clears it, where the plan allows"`
}

// SiteResult is what every site-shaped tool returns. It is deliberately the
// same struct for create, get, update and the file tools: an agent that has
// learned to read one has learned to read all of them.
type SiteResult struct {
	ID            string     `json:"id" jsonschema:"the site's view id"`
	EditID        string     `json:"edit_id" jsonschema:"the id that addresses this site in later tool calls"`
	EditPassword  string     `json:"edit_password,omitempty" jsonschema:"returned once, at creation only, and never again — store it or you cannot manage this site without an account token"`
	ViewURL       string     `json:"view_url" jsonschema:"the public URL of the site"`
	EditURL       string     `json:"edit_url" jsonschema:"the human edit page for this site"`
	Mode          string     `json:"mode"`
	EntryFile     string     `json:"entry_file,omitempty"`
	SPAFallback   bool       `json:"spa_fallback"`
	ViewProtected bool       `json:"view_password_protected"`
	WebDAVEnabled bool       `json:"webdav_enabled"`
	FTPEnabled    bool       `json:"ftp_enabled"`
	CustomDomains []string   `json:"custom_domains,omitempty"`
	ExpiresAt     *time.Time `json:"expires_at,omitempty"`
	ExpiryCapDays int        `json:"expiry_cap_days,omitempty" jsonschema:"the maximum lifetime this site's plan allows, in days; 0 means unlimited"`
	Files         []FileInfo `json:"files"`
	Bytes         int64      `json:"bytes"`
	FileCount     int        `json:"file_count"`
	MaxBytes      int64      `json:"max_bytes,omitempty"`
	MaxFiles      int        `json:"max_files,omitempty"`
	Warnings      []string   `json:"warnings,omitempty"`
}

// FileInfo is one entry in a site's file listing.
type FileInfo struct {
	Path  string `json:"path"`
	Bytes int64  `json:"bytes"`
}

// SiteSummary is one row of list_sites. It carries EditID because that is the
// handle every other tool takes: an agent should be able to go from "my sites"
// to "change this one" without ever handling a password.
type SiteSummary struct {
	ID        string     `json:"id"`
	EditID    string     `json:"edit_id"`
	ViewURL   string     `json:"view_url"`
	Mode      string     `json:"mode"`
	Bytes     int64      `json:"bytes"`
	FileCount int        `json:"file_count"`
	Domains   []string   `json:"custom_domains,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	Origin    string     `json:"origin,omitempty" jsonschema:"which surface created the site: mcp, or empty for the web UI and the JSON API"`
}

// CreateInput is what create_site hands to Ops. Its files are already decoded
// and validated: a malformed base64 argument must be refused before a site is
// created, not after, so there is nothing to roll back.
type CreateInput struct {
	Files    []DecodedFile
	Settings Settings
	Domains  []string
}

// Ops is everything internal/mcp needs from the rest of Sitebin. It is
// implemented by internal/httpapi, which reuses the JSON API's own helpers so
// no rule is stated twice.
//
// Error contract: a returned error is shown to the agent verbatim, as an MCP
// tool error. Implementations must therefore return messages that are safe to
// show and that say what to do next — the agent reads nothing else at the
// moment it is stuck. Anything internal must be logged and replaced with a
// generic message by the implementation, not passed through.
type Ops interface {
	// Authenticate resolves the request's credentials. It is called once per
	// MCP session, on the request that opens it. This is the single point
	// where Phase 2's OAuth access tokens will attach.
	Authenticate(r *http.Request) Auth

	CreateSite(ctx context.Context, a Auth, in CreateInput) (*SiteResult, error)
	ListSites(ctx context.Context, a Auth) ([]SiteSummary, error)
	GetSite(ctx context.Context, a Auth, ref SiteRef) (*SiteResult, error)
	UpdateSite(ctx context.Context, a Auth, ref SiteRef, s Settings) (*SiteResult, error)
	ListFiles(ctx context.Context, a Auth, ref SiteRef) ([]FileInfo, error)
	ReadFile(ctx context.Context, a Auth, ref SiteRef, path string) (File, error)
	WriteFiles(ctx context.Context, a Auth, ref SiteRef, files []DecodedFile, replace bool) (*SiteResult, error)
	DeleteFile(ctx context.Context, a Auth, ref SiteRef, path string) (*SiteResult, error)
	DeleteSite(ctx context.Context, a Auth, ref SiteRef) error
	AddDomain(ctx context.Context, a Auth, ref SiteRef, domain string) (*SiteResult, error)
	RemoveDomain(ctx context.Context, a Auth, ref SiteRef, domain string) (*SiteResult, error)
	DownloadSite(ctx context.Context, a Auth, ref SiteRef) (zip []byte, err error)
}

// ErrTooLarge reports that a call's content exceeds MaxContentBytes.
var ErrTooLarge = errors.New("too much content for one MCP call")

// DecodeFiles validates and decodes a tool's file arguments.
//
// The checks live here, not in the adapter, because they are protocol rules
// rather than storage rules: "exactly one of text or base64" is a statement
// about how files travel over JSON-RPC, and it must produce the same message
// whichever tool carried them. Path validation is deliberately NOT here — the
// store owns what a legal path is, and duplicating that rule is how the two
// definitions drift apart.
func DecodeFiles(files []File) ([]DecodedFile, error) {
	out := make([]DecodedFile, 0, len(files))
	total := 0
	for i, f := range files {
		where := f.Path
		if where == "" {
			where = fmt.Sprintf("file %d", i+1)
		}
		if f.Path == "" {
			return nil, fmt.Errorf("%s: path is required", where)
		}
		hasText, hasB64 := f.Text != "", f.Base64 != ""
		switch {
		case hasText && hasB64:
			return nil, fmt.Errorf("%s: set either text or base64, not both", where)
		case !hasText && !hasB64:
			// An empty file is a real thing to want, but "I forgot the
			// content" is far more likely, and silently publishing an empty
			// index.html is the worse failure. Say so.
			return nil, fmt.Errorf("%s: no content — set text (for UTF-8) or base64 (for binary); use an explicit empty string in text for a genuinely empty file", where)
		}
		var data []byte
		if hasB64 {
			b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(f.Base64))
			if err != nil {
				return nil, fmt.Errorf("%s: base64 is not valid: %v", where, err)
			}
			data = b
		} else {
			data = []byte(f.Text)
		}
		total += len(data)
		if total > MaxContentBytes {
			return nil, fmt.Errorf("%w: this call carries more than %d MiB of file content. Split it across several write_files calls, or use WebDAV, FTP or the JSON API's zip upload for a site this size", ErrTooLarge, MaxContentBytes>>20)
		}
		out = append(out, DecodedFile{Path: f.Path, Data: data})
	}
	return out, nil
}
