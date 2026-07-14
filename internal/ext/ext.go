// Package ext is Sitebin's open-core extension seam. The MIT-licensed core
// calls into a registered Provider when one is present; with no provider
// (the community build) Sitebin runs fully open, with no accounts.
//
// The enterprise extension under ee/ (separately licensed) registers a
// Provider in an init() guarded by the `ee` build tag, so the community
// binary never contains that code at all — it is excluded at compile time,
// not merely disabled by a flag.
package ext

import (
	"net/http"
	"time"
)

// Provider is implemented by the enterprise extension. Its methods are the
// only surface the core depends on; the core has no compile-time reference to
// any ee/ package.
type Provider interface {
	// Name and Version identify the extension for logs and the health payload.
	Name() string
	Version() string

	// Init wires the provider to the running instance. Returning an error
	// aborts startup.
	Init(Host) error

	// PublicRoutes returns handlers to mount on the public mux, keyed by
	// net/http pattern (e.g. "GET /account/{$}"). May be empty.
	PublicRoutes() map[string]http.Handler

	// AccountsEnabled reports whether account gating is active (mode != open).
	AccountsEnabled() bool

	// AuthorizeCreate is consulted before a site is created. It returns the
	// owner account id to stamp on the new site (empty for anonymous), or a
	// *CreateError to reject creation. In the community build it is never
	// called and creation stays open.
	AuthorizeCreate(r *http.Request) (ownerAccountID string, err error)

	// OnSiteCreated records ownership after a site is created (no-op for
	// anonymous owners). Errors are logged, not surfaced to the client.
	OnSiteCreated(ownerAccountID, viewID string) error
}

// CreateError rejects a site creation with an HTTP status and message.
type CreateError struct {
	Status int
	Msg    string
}

func (e *CreateError) Error() string { return e.Msg }

// Host is the set of core services handed to the extension at Init. It is an
// interface so the core does not import ee/ and ee/ depends only on this
// contract.
type Host interface {
	DataDir() string
	BaseDomain() string
	HTTPOnly() bool
	Secret() []byte
	// Sites exposes the core site operations the dashboard needs.
	Sites() SiteService
}

// SiteService lets the extension read and manage sites without importing the
// core store. Implemented by the httpapi layer (which also invalidates its
// auth caches on mutation).
type SiteService interface {
	// Info returns a lightweight view of a site, or ok=false if absent.
	Info(viewID string) (SiteInfo, bool)
	// RotateEditPassword issues a new edit password, returning it once.
	RotateEditPassword(viewID string) (newPassword string, err error)
	// Delete removes a site and its indexes.
	Delete(viewID string) error
}

// SiteInfo is the dashboard's view of an owned site.
type SiteInfo struct {
	ViewID    string
	Mode      string
	Bytes     int64
	Files     int
	ViewURL   string
	EditURL   string
	CreatedAt time.Time
}

var registered Provider

// Register installs the extension Provider. Called from ee/ init() under the
// `ee` build tag. Panics if called twice.
func Register(p Provider) {
	if registered != nil {
		panic("ext: a provider is already registered")
	}
	registered = p
}

// Get returns the registered provider, or (nil, false) in the community build.
func Get() (Provider, bool) { return registered, registered != nil }

// Reset clears the registered provider. Test-only helper.
func Reset() { registered = nil }
