package main

import (
	"github.com/ittrail/sitebin/internal/config"
	"github.com/ittrail/sitebin/internal/ext"
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
func (h extHost) Sites() ext.SiteService { return h.sites }

var _ ext.Host = extHost{}
