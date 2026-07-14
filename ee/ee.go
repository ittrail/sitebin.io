//go:build ee

// Package ee is the Sitebin Enterprise Edition: the premium accounts, tiers,
// SMTP, OAuth, and billing features.
//
// This package and everything under ee/ is NOT covered by the repository's MIT
// license. It is licensed under ee/LICENSE (see that file). It is compiled into
// the binary only with the `ee` build tag; the community build excludes it
// entirely.
//
// Registration is intentionally minimal in this scaffold commit — the concrete
// account/tier/billing implementation lands in the phased plan
// (docs/superpowers/plans). This file establishes and verifies the open-core
// seam: `go build -tags ee` includes it, `go build` does not.
package ee

import (
	"net/http"

	"github.com/ittrail/sitebin/internal/ext"
)

// Version of the enterprise extension.
const Version = "0.0.0-scaffold"

func init() {
	ext.Register(&provider{})
}

// provider is the enterprise ext.Provider. Methods are stubs at this stage and
// are implemented across the phased plan.
type provider struct {
	host ext.Host
}

func (p *provider) Name() string    { return "sitebin-ee" }
func (p *provider) Version() string { return Version }

func (p *provider) Init(h ext.Host) error {
	p.host = h
	return nil
}

func (p *provider) PublicRoutes() map[string]http.Handler { return nil }

func (p *provider) AccountsEnabled() bool { return false }

func (p *provider) AuthorizeCreate(r *http.Request) (string, error) { return "", nil }

func (p *provider) OnSiteCreated(ownerAccountID, viewID string) error { return nil }
