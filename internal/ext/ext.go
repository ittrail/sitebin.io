// Package ext is Sitebin's open-core extension seam. The MIT-licensed core
// calls into a registered Provider when one is present; with no provider
// (the community build) Sitebin runs fully open, with no accounts.
//
// The enterprise extension under ee/ (separately licensed) registers a
// Provider in an init() guarded by the `ee` build tag, so the community
// binary never contains that code at all — it is excluded at compile time,
// not merely disabled by a flag.
package ext

import "net/http"

// Provider is implemented by the enterprise extension. Its methods are the
// only surface the core depends on; the core has no compile-time reference to
// any ee/ package.
type Provider interface {
	// Name and Version identify the extension for logs and the health payload.
	Name() string
	Version() string

	// Init wires the provider to the running instance. It is called once at
	// startup with the host services the extension needs. Returning an error
	// aborts startup.
	Init(Host) error

	// PublicRoutes returns handlers to mount on the public mux, keyed by
	// net/http pattern (e.g. "GET /account/{$}"). May be empty.
	PublicRoutes() map[string]http.Handler

	// AccountsEnabled reports whether account gating is active (mode != open).
	AccountsEnabled() bool

	// AuthorizeCreate is consulted before a site is created. It returns the
	// owner account id to stamp on the new site (empty for anonymous) or an
	// error to reject creation (quota, login required, tier disabled). In the
	// community build this is never called and creation stays open.
	AuthorizeCreate(r *http.Request) (ownerAccountID string, err error)
}

// Host is the set of core services handed to the extension at Init. It is
// intentionally an interface so the core package does not import ee/ and ee/
// depends only on this contract. It is fleshed out in Phase 1 as concrete
// integration points are wired; kept minimal here so both editions compile.
type Host interface {
	// DataDir is the instance data root (SITEBIN_DATA_DIR).
	DataDir() string
	// BaseDomain is the configured main domain.
	BaseDomain() string
	// HTTPOnly reports whether the instance serves plain HTTP (affects cookies).
	HTTPOnly() bool
	// Secret is the per-instance signing secret (for session cookies).
	Secret() []byte
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
