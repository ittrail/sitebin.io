package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Meta is the per-site metadata file (meta.json), the single source of truth
// for a site's settings. Plaintext passwords are never stored here.
type Meta struct {
	ID                    string   `json:"id"`
	EditID                string   `json:"edit_id"`
	EditPasswordHash      string   `json:"edit_password_hash"`
	Mode                  string   `json:"mode"`
	ViewPasswordProtected bool     `json:"view_password_protected"`
	ViewPasswordHash      string   `json:"view_password_hash,omitempty"`
	WebDAVEnabled         bool     `json:"webdav_enabled"`
	FTPEnabled            bool     `json:"ftp_enabled"`
	CustomDomains         []string `json:"custom_domains"`
	EntryFile             string   `json:"entry_file"`
	SPAFallback           bool     `json:"spa_fallback"`               // webserver mode: serve index.html for unknown paths
	OwnerAccountID        string   `json:"owner_account_id,omitempty"` // enterprise: owning account (empty = anonymous)

	// Origin records which surface created the site. Empty — the value every
	// meta.json written before this field has — means the UI or the JSON API,
	// so no migration is needed and no existing site is mislabelled. It is
	// provenance only: nothing in Sitebin gates on it. It exists because
	// "was this made by an agent?" is unanswerable after the fact if it was
	// never written down, and the admin console needs to be able to ask.
	Origin string `json:"origin,omitempty"`

	// Per-site quota caps stamped from the owner's tier (enterprise). Zero /
	// nil means "inherit the instance global".
	QuotaBytes      int64      `json:"quota_bytes,omitempty"`
	QuotaFiles      int        `json:"quota_files,omitempty"`
	QuotaExpiryDays int        `json:"quota_expiry_days,omitempty"`
	QuotaDomains    *int       `json:"quota_domains,omitempty"` // nil = inherit global; value (incl. 0) = explicit cap
	QuotaWebDAV     *bool      `json:"quota_webdav,omitempty"`  // nil = inherit global
	ExpiresAt       *time.Time `json:"expires_at"`
	// ExpiryFromTier records whether ExpiresAt was imposed by the owner's tier
	// (the creation default, sliding renewal, or a downgrade grace) rather than
	// chosen by a caller. A tier change may lift an imposed expiry; it must
	// never silently discard one the owner asked for.
	ExpiryFromTier bool      `json:"expiry_from_tier,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// OriginMCP marks a site created through the MCP server. See Meta.Origin.
const OriginMCP = "mcp"

// Expired reports whether the site is past its expiry at time now.
func (m Meta) Expired(now time.Time) bool {
	return m.ExpiresAt != nil && now.After(*m.ExpiresAt)
}

func metaPath(siteDir string) string { return filepath.Join(siteDir, "meta.json") }

func readMeta(siteDir string) (Meta, error) {
	var m Meta
	b, err := os.ReadFile(metaPath(siteDir))
	if err != nil {
		return m, err
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return m, fmt.Errorf("parse %s: %w", metaPath(siteDir), err)
	}
	if m.CustomDomains == nil {
		m.CustomDomains = []string{}
	}
	return m, nil
}

// writeMeta writes meta.json atomically (tmp file + rename).
func writeMeta(siteDir string, m Meta) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	tmp := metaPath(siteDir) + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		return fmt.Errorf("write meta: %w", err)
	}
	if err := os.Rename(tmp, metaPath(siteDir)); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("commit meta: %w", err)
	}
	return nil
}
