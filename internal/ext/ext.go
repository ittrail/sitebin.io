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
	"errors"
	"net/http"
	"time"
)

// ErrSiteGone reports that the site no longer exists; the caller's reference is
// stale. It is the seam's own sentinel, so the core's store error types do not
// cross into the extension.
//
// It exists because the extension keeps its own ownership markers and a site
// can be deleted without it hearing about it (the edit page's delete and the
// cleanup sweep both go straight to the store). A caller holding such a marker
// has not failed at anything — it has learned the marker is stale and should
// drop it and carry on, rather than treating the site as unreachable.
var ErrSiteGone = errors.New("ext: site no longer exists")

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

	// CustomDomainsAllowed reports whether custom domains may be added — a
	// premium feature. The community build has no provider, so custom domains
	// are off there.
	CustomDomainsAllowed() bool

	// EmbedOriginsAllowed reports whether SITEBIN_EMBED_ORIGINS is honored —
	// letting foreign origins embed the create flow (<sitebin-drop>) is a
	// premium feature. The community build has no provider, so cross-origin
	// creation stays off there.
	EmbedOriginsAllowed() bool

	// AuthorizeCreate is consulted before a site is created. It returns a
	// CreateGrant (owner + per-site quota caps to stamp), or a *CreateError to
	// reject creation. In the community build it is never called and creation
	// stays open.
	AuthorizeCreate(r *http.Request) (CreateGrant, error)

	// QuotaFor returns the caps the owner's CURRENT tier grants, so the core can
	// restamp a site whose stamped quotas predate a tier change. ok=false means
	// the account is unknown (deleted, or accounts are not enabled). A non-nil
	// error means the tier could not be determined — callers MUST NOT act
	// destructively on an error; a site kept too long is recoverable, a deleted
	// one is not.
	QuotaFor(ownerAccountID string) (CreateGrant, bool, error)

	// OnSiteCreated records ownership after a site is created (no-op for
	// anonymous owners). Errors are logged, not surfaced to the client.
	OnSiteCreated(ownerAccountID, viewID string) error
}

// CreateGrant is the result of a successful AuthorizeCreate: who owns the new
// site and the per-site quota caps to stamp on it. Zero-valued cap fields mean
// "inherit the instance global"; they are set only in tiers mode.
type CreateGrant struct {
	OwnerAccountID  string
	MaxSiteBytes    int64
	MaxFiles        int
	MaxExpiryDays   int
	MaxCustomDomain *int  // nil = inherit global; value (incl. 0) = explicit cap
	WebDAV          *bool // nil = inherit global
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
	// PathViews reports whether sites are served on /v/<id> main-domain paths
	// (SITEBIN_VIEW_ACCESS=path|both), which shares an origin with the API.
	PathViews() bool
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
	// ApplyQuota restamps a site's per-site caps from a grant and reconciles its
	// expiry with the new lifetime cap. It returns an error wrapping ErrSiteGone
	// when viewID names a site that no longer exists, which callers holding
	// their own ownership records must treat as "drop the record", not as a
	// failure.
	ApplyQuota(viewID string, g CreateGrant) error
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
	// ExpiresAt is when the site stops serving, or nil if it has no expiry. A
	// downgrade stamps a 30-day grace here, and that date is the only warning
	// the owner gets before the sweep deletes the site — the dashboard must
	// show it.
	ExpiresAt *time.Time
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
