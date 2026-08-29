package main

import (
	"github.com/ittrail/sitebin.io/internal/config"
	"github.com/ittrail/sitebin.io/internal/ext"
)

// extHost adapts the running instance to the ext.Host interface handed to the
// enterprise extension at startup.
type extHost struct {
	cfg    config.Config
	secret []byte
	sites  ext.SiteService
}

func (h extHost) DataDir() string        { return h.cfg.DataDir }
func (h extHost) BaseDomain() string     { return h.cfg.BaseDomain }
func (h extHost) HTTPOnly() bool         { return h.cfg.HTTPOnly }
func (h extHost) Secret() []byte         { return h.secret }
func (h extHost) PathViews() bool        { return h.cfg.PathViews() }
func (h extHost) Sites() ext.SiteService { return h.sites }

func (h extHost) MCPOAuthIssuer() string { return h.cfg.MCPOAuthIssuer }

// MCPResource falls back to the endpoint's own URL, which is what it is. An
// operator only needs to set it when the instance is reached under a different
// name than it generates for itself.
func (h extHost) MCPResource() string {
	if h.cfg.MCPResource != "" {
		return h.cfg.MCPResource
	}
	return h.cfg.SiteURL(h.cfg.BaseDomain) + "/mcp"
}

var _ ext.Host = extHost{}
