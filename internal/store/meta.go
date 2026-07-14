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
	OwnerAccountID        string   `json:"owner_account_id,omitempty"` // enterprise: owning account (empty = anonymous)

	// Per-site quota caps stamped from the owner's tier (enterprise). Zero /
	// nil means "inherit the instance global".
	QuotaBytes      int64      `json:"quota_bytes,omitempty"`
	QuotaFiles      int        `json:"quota_files,omitempty"`
	QuotaExpiryDays int        `json:"quota_expiry_days,omitempty"`
	QuotaDomains    *int       `json:"quota_domains,omitempty"` // nil = inherit global; value (incl. 0) = explicit cap
	QuotaWebDAV     *bool      `json:"quota_webdav,omitempty"`  // nil = inherit global
	ExpiresAt       *time.Time `json:"expires_at"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

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
