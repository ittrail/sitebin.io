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
	"context"
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

// ErrSiteLocked reports that the site is locked by the operator, so the
// change asked for — a rename, a password rotation, a delete — is refused.
// Like ErrSiteGone it is the seam's own sentinel. The lock itself is visible
// in SiteInfo.Locked; see SiteLock.
var ErrSiteLocked = errors.New("ext: site is locked by the operator")

// ErrSiteNotLocked refuses an evidence hold on a site that is not locked:
// there would be nothing for it to hold.
var ErrSiteNotLocked = errors.New("ext: site is not locked")

// ErrNotOwner: the account named as the site's current owner is not it (a
// transfer), or the recipient is empty.
var ErrNotOwner = errors.New("ext: account does not own the site")

// ErrNotAdoptable: only an anonymous, unlocked, unexpired site can be adopted.
var ErrNotAdoptable = errors.New("ext: site cannot be adopted")

// ErrTooManyAdmins: a site has at most store.MaxAdmins admins.
var ErrTooManyAdmins = errors.New("ext: too many admins")

// Who placed a lock (SiteLock.By). An operator lock replaces any lock; an
// account lock, and the abuse guard's, is applied only to an unlocked site.
const (
	LockByAdmin   = "admin"
	LockByAccount = "account"
	// LockByScanner is the abuse guard holding a site for review: an upload
	// matched a blocking rule, or a CSP report proved an exfiltration
	// attempt. See docs/superpowers/specs/2026-09-29-abuse-detection.md.
	LockByScanner = "scanner"
)

// SiteLock is the operator's hold on a site: it is served to nobody,
// changed by nobody but the operator, and outlives its expiry — every tier
// restamp leaves it alone. ForceDelete removes it, and so does the cleanup
// sweep once the lock is older than the instance's lock retention and
// carries no evidence hold. See
// docs/superpowers/specs/2026-09-28-site-lock-and-account-suspension.md.
type SiteLock struct {
	// At is when the site was locked: the start of its continuous lock,
	// which a lock replacing a lock (the register's Keep) keeps — it is the
	// retention's clock. SetLock uses it only for a site not yet locked.
	At time.Time
	// Reason is shown to the owner and in the register. Optional.
	Reason string
	// By is LockByAdmin (the operator, from the register or the CLI) or
	// LockByAccount (a stack-level suspension of the owner, lifted again by
	// the unsuspension).
	By string
	// Hold is the operator's evidence hold ("case open"), or nil. Read-only
	// here: SetLock ignores it, SetHold places and releases it.
	Hold *LockHold
	// PurgeAt is when the lock retention runs out, computed by the core:
	// the cleanup sweep purges the site then — unless Hold stands, which
	// does not move the date. Nil when the instance keeps locked sites
	// forever. Read-only.
	PurgeAt *time.Time
}

// LockHold is an evidence hold on a lock: a case, investigation or
// proceeding is still open, so the site is kept past the lock retention.
type LockHold struct {
	At time.Time
	// By is who placed it: the admin's account id, or "cli".
	By string
}

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
	// CustomDomainsAllowed reports whether ANOTHER custom domain may be added.
	// A nil error means yes; a non-nil error is the reason, and it is shown to
	// the caller — because "you have reached your licence's limit" and "this
	// edition has no custom domains" are different answers and a paying
	// customer must not be told the second when the first is true.
	//
	// The community build has no provider, so the core answers for itself and
	// custom domains stay off there.
	CustomDomainsAllowed() error

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

	// AccountSiteIDs returns the view ids of the sites an account owns, for
	// the MCP server's list_sites tool. ok=false means the account is unknown
	// or accounts are not enabled.
	//
	// It returns ids, not sites: the extension is the side that knows which
	// sites an account owns, and the core is the side that knows what a site
	// is. Returning SiteInfo here would make the extension resolve state it
	// does not own.
	AccountSiteIDs(accountID string) ([]string, bool)

	// BearerCredential resolves an Authorization: Bearer credential to what it
	// grants. ok=false means no usable credential was presented — a wrong one
	// and an absent one are the same answer, deliberately.
	//
	// The extension decides WHICH kind of credential it was looking at: an
	// account API token, or an OAuth access token from the configured issuer.
	// The core never learns the difference, which is the same division of
	// labour the rest of this seam draws — it is handed an account id and, for
	// OAuth, the scopes the user consented to.
	//
	// The core uses it to let a credential stand in for a site's edit password
	// on sites that account owns. The ownership comparison belongs to the core,
	// which is the side that holds the site's metadata. An OAuth access token
	// is for /mcp and the Git endpoint alone: the extension resolves one only
	// on a request carrying one of their markers (OAuthSurface), and the core
	// ignores one anywhere else regardless.
	BearerCredential(r *http.Request) (Credential, bool)
}

// Credential is what an Authorization: Bearer header resolved to.
type Credential struct {
	// AccountID is the account the credential acts for.
	AccountID string
	// Scopes are the OAuth scopes granted. EMPTY MEANS UNRESTRICTED: an
	// account API token grants everything its account can do and carries no
	// scopes, which is what that credential has always meant. Only an OAuth
	// access token narrows it.
	Scopes []string
	// OAuth marks an access token from the configured authorization server,
	// as opposed to an account API token. Its audience is the MCP resource, so
	// it is honoured only on requests carrying the MCP marker (WithMCPCaller)
	// or the Git gate's (WithGitCaller), both of which hold it to its scopes:
	// the JSON API's owner check never accepts one, and neither does the
	// extension's account resolution for site creation, which is /mcp's alone.
	// Before this flag existed a read-only token could create, overwrite and
	// delete sites through /api/*.
	OAuth bool
}

// ctxKey is unexported, so no other package can build a key that collides
// with these: a value under them can only have been put there through the
// functions below, which is what makes them unforgeable by anything a client
// sends.
type ctxKey int

const (
	mcpCallerKey ctxKey = iota
	credentialKey
	gitCallerKey
	refusalKey
)

// WithMCPCaller marks a request context as having arrived through the MCP
// endpoint. The core's /mcp handler sets it on every request, OAuth or not;
// nothing else may.
func WithMCPCaller(ctx context.Context) context.Context {
	return context.WithValue(ctx, mcpCallerKey, true)
}

// IsMCPCaller reports whether the request came through the MCP endpoint.
func IsMCPCaller(ctx context.Context) bool {
	v, _ := ctx.Value(mcpCallerKey).(bool)
	return v
}

// WithGitCaller marks a request context as the Git endpoint's question about
// an OAuth access token. Only the core's Git gate sets it, and only on the
// copy of the request it hands BearerCredential for a JWS-shaped password —
// never on the request its handler goes on to serve.
func WithGitCaller(ctx context.Context) context.Context {
	return context.WithValue(ctx, gitCallerKey, true)
}

// IsGitCaller reports whether the Git gate marked the request.
func IsGitCaller(ctx context.Context) bool {
	v, _ := ctx.Value(gitCallerKey).(bool)
	return v
}

// OAuthSurface reports whether an OAuth access token may act on this request:
// it came through /mcp or the Git gate, the only two places one does. The
// JSON API is neither, and refuses one regardless.
func OAuthSurface(ctx context.Context) bool {
	return IsMCPCaller(ctx) || IsGitCaller(ctx)
}

// Refusal is why the extension refused a credential, for the one caller that
// must explain itself. The verifier never says why to a client that did not
// ask (an oracle), so the answer travels only through a slot the caller
// installed with WithRefusalNote.
type Refusal int

const (
	// RefusalNone: nothing was noted — the credential passed, or it failed
	// for a reason that is not explained.
	RefusalNone Refusal = iota
	// RefusalConsent: the token is valid, and the stack ANSWERED that its
	// subject has a required document outstanding. It is noted on that
	// answer and on nothing else — never when the stack could not be asked
	// or gave no verdict (that is RefusalUnavailable, or nothing). Signing in
	// once in a browser passes the consent gate; a Git credential helper
	// never opens one on a 401.
	RefusalConsent
	// RefusalUnavailable: the token is valid, but the stack could not say
	// whether the person may use it right now — it did not answer in time,
	// answered with an error status, or answered without a verdict. The
	// token was refused (the check fails closed), and the same token may
	// work a minute later.
	RefusalUnavailable
)

// WithRefusalNote installs an empty refusal slot in ctx. NoteRefusal on ctx,
// or on any context derived from it, fills it; RefusalFrom reads it.
func WithRefusalNote(ctx context.Context) context.Context {
	return context.WithValue(ctx, refusalKey, new(Refusal))
}

// NoteRefusal records why a credential was refused. Without a slot it does
// nothing, so a verifier may note on every path, whoever its caller is.
func NoteRefusal(ctx context.Context, why Refusal) {
	if slot, ok := ctx.Value(refusalKey).(*Refusal); ok && slot != nil {
		*slot = why
	}
}

// RefusalFrom returns what was noted in ctx's slot, or RefusalNone.
func RefusalFrom(ctx context.Context) Refusal {
	if slot, ok := ctx.Value(refusalKey).(*Refusal); ok && slot != nil {
		return *slot
	}
	return RefusalNone
}

// WithCredential records the credential the MCP endpoint already verified
// for this request, so the tools do not verify the same bearer a second time.
func WithCredential(ctx context.Context, c Credential) context.Context {
	return context.WithValue(ctx, credentialKey, c)
}

// CredentialFrom returns the credential WithCredential recorded. ok=false
// means none was — the request carried no bearer, or the endpoint did not
// resolve it (OAuth off), and the caller should resolve it as it always has.
func CredentialFrom(ctx context.Context) (Credential, bool) {
	c, ok := ctx.Value(credentialKey).(Credential)
	return c, ok
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
	Git             *bool // nil = no git repository for the site
	Analytics       *bool // nil = no site analytics
	// MaxForms caps the site's email forms (tier max_forms). nil = inherit the
	// instance default; a value, 0 included, is an explicit cap.
	MaxForms *int
	// Trusted exempts the site from the strict content-security headers that
	// untrusted uploads are served with. Only a tier marked trusted grants it;
	// anonymous sites never are. The core's default is the opposite of this
	// zero value only where no provider exists at all — see store.SetTrusted
	// and the community-build note in createSite.
	Trusted bool
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
	// BaseURL is the instance's own absolute base URL, scheme and port
	// included. The extension must not rebuild this from BaseDomain(): the
	// port lives in the core's config and a hand-built URL silently drops it,
	// which breaks every absolute link the extension emits — the OAuth
	// redirect_uri above all — on any instance not served on 80/443.
	BaseURL() string
	// MCPOAuthIssuer is the authorization server whose access tokens the MCP
	// endpoint accepts, or empty when OAuth is not configured. Sitebin is only
	// ever a resource server: it validates what this issuer signed and issues
	// nothing itself.
	MCPOAuthIssuer() string
	// MCPResource is this server's OAuth resource identifier — the value that
	// must appear in a token's audience.
	MCPResource() string
	// GitOAuthClientID is the OAuth client id the Git CLI client signs in
	// with (SITEBIN_GIT_OAUTH_CLIENT_ID), or empty when none is offered.
	GitOAuthClientID() string
	// Sites exposes the core site operations the dashboard needs.
	Sites() SiteService
	// LockRetention is how long a lock keeps a site before the cleanup sweep
	// purges it (SITEBIN_LOCK_RETENTION_DAYS); zero means forever. For
	// wording only — SiteLock.PurgeAt is the date.
	LockRetention() time.Duration
	// PublishURL is where a person goes to publish a new site: the front
	// door's URL when one is configured, else "/".
	PublishURL() string
}

// FormsMailHost is optional on Host: whether the forms mailer
// (SITEBIN_FORMS_SMTP_*) is configured. The operator's health view states
// it next to the account mailer; nothing else asks.
type FormsMailHost interface {
	FormsMailConfigured() bool
}

// SnapshotsHost is optional on Host: the unix socket of the snapshots agent
// on the instance's host (SITEBIN_SNAPSHOTS_AGENT), "" when there is none.
// The operator's Backups view asks it. See
// docs/superpowers/specs/2026-11-07-snapshots-and-restore-design.md.
type SnapshotsHost interface {
	SnapshotsAgent() string
}

// SiteService lets the extension read and manage sites without importing the
// core store. Implemented by the httpapi layer (which also invalidates its
// auth caches on mutation).
type SiteService interface {
	// Info returns a lightweight view of a site, or ok=false if absent.
	Info(viewID string) (SiteInfo, bool)
	// All returns every site on the instance, anonymous ones included, for the
	// admin console's instance-wide view. Unreadable entries are skipped rather
	// than failing the whole listing, exactly as the cleanup sweep's own pass
	// does — one corrupt meta.json must not blind an operator to the other 198
	// sites.
	All() ([]SiteInfo, error)
	// SetExpiry sets a site's expiry, or clears it when at is nil. It clears
	// ExpiryFromTier: a date an operator chose is not one the plan imposed, and
	// leaving the flag set would let the next sliding renewal overwrite it. Like
	// ApplyQuota it reports a site that no longer exists as ErrSiteGone.
	SetExpiry(viewID string, at *time.Time) error
	// RotateEditPassword issues a new edit password, returning it once. A
	// locked site is refused with ErrSiteLocked.
	RotateEditPassword(viewID string) (newPassword string, err error)
	// SetName sets the site's name, or clears it when name is empty. The name
	// is validated by the core's one rule, and a refused name is an error
	// whose message states that rule and is safe to show as-is. A site that
	// no longer exists is ErrSiteGone; a locked one is ErrSiteLocked.
	SetName(viewID, name string) error
	// UpdateTags edits a site's tags under the site lock: edit receives the
	// tags stored in meta.json right now and returns the list to store, so a
	// change computed from the current list (an account-wide rename) cannot
	// lose a concurrent write. The result is validated by the core's one rule
	// (store.CleanTags); a refused list is an error whose message states that
	// rule and is safe to show as-is, and an error returned by edit is
	// returned unchanged, with nothing written. A site that no longer exists
	// is ErrSiteGone; a locked one is ErrSiteLocked.
	UpdateTags(viewID string, edit func(current []string) ([]string, error)) error
	// Delete removes a site and its indexes. Like ApplyQuota it reports a site
	// that no longer exists as ErrSiteGone, so a caller erasing everything an
	// account owns can tell "already gone, carry on" from "could not delete,
	// stop" — the second must never be reported as the first. A locked site
	// is refused with ErrSiteLocked: the lock is an evidence hold, and an
	// owner's delete — of the site or of their whole account — must not end
	// it.
	Delete(viewID string) error
	// ForceDelete is Delete for a locked site too: the operator's explicit
	// takedown. Only the admin console calls it, after its own confirmation.
	ForceDelete(viewID string) error
	// SetLock locks a site, or lifts any lock when lock is nil. An account
	// lock (By LockByAccount) is applied only to a site that is not locked
	// already, so a suspension never replaces the operator's own lock and a
	// repeated one keeps the first date; the operator's lock replaces any,
	// keeping the lock's date (the retention clock) and its evidence hold.
	// Locking also ends the site's upload tokens and stops its containers.
	// A site that no longer exists is ErrSiteGone.
	SetLock(viewID string, lock *SiteLock) error
	// ReleaseLock lifts the site's lock only when it was placed by by, and
	// reports whether it did — how an unsuspension lifts its own locks and
	// leaves the operator's. A site that no longer exists is ErrSiteGone.
	ReleaseLock(viewID, by string) (released bool, err error)
	// SetHold places the operator's evidence hold on a locked site ("case
	// open": the lock retention does not apply while it stands), or
	// releases it when hold is nil. A second hold keeps the first. A site
	// that is not locked is ErrSiteNotLocked; one that no longer exists is
	// ErrSiteGone.
	SetHold(viewID string, hold *LockHold) error
	// ClearFindings dismisses the abuse guard's findings on a site without
	// unlocking it: the operator has looked, and the content they were about
	// becomes reviewed, so the same bytes are not flagged or held again. (An
	// operator unlock through SetLock(nil) does the same.) A site that no
	// longer exists is ErrSiteGone.
	ClearFindings(viewID string) error
	// Reports lists the stored abuse reports, newest first, for the
	// register. They are purged after 14 days.
	Reports() ([]AbuseReport, error)
	// ApplyQuota restamps a site's per-site caps from a grant and reconciles its
	// expiry with the new lifetime cap. It returns an error wrapping ErrSiteGone
	// when viewID names a site that no longer exists, which callers holding
	// their own ownership records must treat as "drop the record", not as a
	// failure. A locked site is left exactly as it is and reported as done:
	// the hold freezes its caps and its expiry along with everything else.
	ApplyQuota(viewID string, g CreateGrant) error
	// CustomDomainCount is the number of custom domains attached across the
	// instance, anonymous sites included. It exists because the licence
	// ceiling asks on EVERY domain add, and answering that through All() —
	// which walks every site's files for its size — was a multi-second,
	// disk-thrashing operation any customer could trigger in a loop.
	CustomDomainCount() (int, error)

	// ---- container sites (see ContainerRuntime) ----

	// ContainerSites returns every site in container mode, with its desired
	// state and the bytes of its compose file. The runtime's full scan.
	ContainerSites() ([]ContainerSite, error)
	// ContainerSite is ContainerSites for one site. A site that no longer
	// exists is ErrSiteGone; one no longer in container mode is returned with
	// Container=false, so the runtime knows to tear it down.
	ContainerSite(viewID string) (ContainerSite, error)
	// PrepareVolume makes sure a root folder of the site exists as a real
	// directory — never a link — and returns its path relative to the data
	// directory, slash-separated. The core owns the layout; the runtime only
	// turns the answer into a mount.
	PrepareVolume(viewID, folder string) (string, error)
	// SetContainerState records what the runtime observed. It never touches
	// the desired half (enabled, restart_seq), which is the core's.
	SetContainerState(viewID string, st ContainerState) error
	// SyncContainerEndpoints tells the core which HTTP ports ("service:port",
	// in the compose file's order) a compose file that parsed declares. The
	// core gives each one a generated address and drops the addresses of
	// ports no longer declared; which custom domain serves which port is the
	// site's domain settings', never the file's.
	SyncContainerEndpoints(viewID string, endpoints []string) error
	// SetContainerLimits replaces the operator's resource limits for the
	// site's services (by service name); nil or empty clears them, and an
	// entry that sets nothing is dropped. Desired state, like enabled and
	// restart_seq: the core writes it, on the admin console's order, and the
	// runtime converges to it. A site that no longer exists is ErrSiteGone.
	SetContainerLimits(viewID string, limits map[string]ContainerLimits) error

	// ---- account zones (see ZoneAccounts) ----

	// ClaimZone records the account's claim on a zone and checks it at once.
	// ok=false with a nil error is pending (the record or a conflict is still
	// in the way); an error is a refusal whose text is shown to the owner.
	ClaimZone(accountID, zone string) (z ZoneInfo, ok bool, err error)
	// Zones lists the account's zones, pending ones included, with the names
	// each one currently holds.
	Zones(accountID string) ([]ZoneInfo, error)
	// ReleaseZone removes one of the account's zones; ReleaseZones all of them
	// (account deletion). Names that relied on a zone fall back to per-name
	// proof; nothing is detached on the spot.
	ReleaseZone(accountID, zone string) error
	ReleaseZones(accountID string) error
}

// AbuseReport is one report filed through the public report page or
// POST /api/report.
type AbuseReport struct {
	Time time.Time
	// Target is what the reporter typed; ViewID the site it resolved to, or
	// empty.
	Target string
	ViewID string
	Reason string
	// Details and Contact (the reporter's optional address) are the
	// reporter's own words: show them, never follow them.
	Details string
	Contact string
	// Source is the reporter's network, truncated (/24, /48).
	Source string
	// Via is "page" or "api".
	Via string
}

// ScanFinding is one abuse-guard hit on a site: a rule that matched a file,
// or a CSP report its files proved (Source "csp").
type ScanFinding struct {
	Rule     string
	Severity string // "block" or "flag"
	Path     string
	Excerpt  string
	Source   string // "upload", "csp" or "scan"
	At       time.Time
}

// AccountDirectory is implemented by a Provider that can name an account.
// OPTIONAL, like OperatorAccounts: the core asks it to put an owner's
// address into an abuse alert, and for the "shared by" line an admin of a
// shared site is shown (the owner's address, never the other way round). It
// is never asked on the serving path.
type AccountDirectory interface {
	AccountEmail(accountID string) (email string, ok bool)
}

// ZoneInfo is one account zone as the dashboard shows it.
type ZoneInfo struct {
	Zone         string
	Verified     bool
	TXTName      string
	TXTValue     string
	RequestedAt  time.Time
	FailingSince *time.Time
	Conflicts    []string
	Names        []ZoneName
}

// ZoneName is one attached name inside a zone and the site holding it.
type ZoneName struct {
	Domain string
	ViewID string
}

// OperatorAccounts is implemented by a Provider that knows which accounts
// are the instance's operator. OPTIONAL, like ContainerProvider. Operator
// zones (SITEBIN_OPERATOR_DOMAINS) are attached only to sites such an
// account owns; with no provider, nobody is the operator.
type OperatorAccounts interface {
	IsOperator(accountID string) bool
}

// SessionAccounts is implemented by a Provider whose dashboard signs people
// in with a browser session. OPTIONAL, like OperatorAccounts: without it the
// per-site API knows no session, only edit passwords and tokens.
//
// The core asks it on the per-site API routes so a signed-in owner (or an
// admin of a shared site) manages the site on the edit page without the
// site's edit password. The extension answers only WHOSE session this is —
// signed cookie, live account, current token version, so signing out revokes
// it. The standing and the cross-site rule are the core's
// (httpapi.sessionStanding, httpapi.accountStanding), exactly as for
// BearerCredential.
type SessionAccounts interface {
	SessionAccount(r *http.Request) (accountID string, ok bool)
}

// SharingAccounts is implemented by a Provider whose plans let an owner share
// a site with admins (the tier flag `sharing`). OPTIONAL, like SessionAccounts:
// without it no account has admin standing on any site, whatever a site's
// Meta.Admins lists, so the community build and a provider without sharing
// stay exactly as they were.
//
// The core keeps the per-site truth (who owns a site, who its admins are and
// until when, in meta.json) and states the standing rule once
// (httpapi.accountStanding): an account listed in Meta.Admins whose Until is
// unset or still ahead, while SharingAllowed is true for BOTH the site's
// owner and that account. The extension answers only about accounts and
// keeps what is an admin's own: their tags on a shared site and the index of
// sites shared with them.
//
// The owner's address on a shared site ("shared by") comes from
// AccountDirectory, which already names an account; this interface does not
// repeat it.
type SharingAccounts interface {
	// SharingAllowed reports whether the account's CURRENT plan includes
	// sharing. An error means unknown: the core refuses the admin standing
	// for that one request and changes nothing stored — a share is never
	// ended because a plan lookup failed.
	SharingAllowed(accountID string) (bool, error)
	// AdminTags is the admin's own tags on the shared site viewID (nil for
	// none). An admin never sees or writes the owner's Meta.Tags.
	AdminTags(accountID, viewID string) []string
	// SetAdminTags stores the admin's own tags on viewID, already validated
	// by store.CleanTags (an empty list clears them).
	SetAdminTags(accountID, viewID string, tags []string) error
	// SharedSiteIDs lists the view ids shared with the account. The core
	// re-checks each against meta.json (the standing rule above) before it
	// shows one, so a stale entry grants nothing.
	SharedSiteIDs(accountID string) []string
}

// ZoneAccounts is implemented by a Provider whose plans include account
// zones (a customer's own wildcard zone, proven once; see store/zones.go).
// OPTIONAL, like ContainerProvider: without it no account can claim a zone.
type ZoneAccounts interface {
	// ZonesAllowed returns how many zones the account's CURRENT plan permits.
	// An error means unknown: refuse the new zone or name, never release an
	// existing one. It is asked only where a zone or a name is ADDED.
	ZonesAllowed(accountID string) (int, error)
}

// ContainerProvider is implemented by a Provider that can run container
// sites. It is OPTIONAL — asserted, not part of Provider — so a provider
// without it, and the community build with no provider at all, simply have
// no container mode. Containers() returning nil means the instance has the
// mode switched off.
type ContainerProvider interface {
	Containers() ContainerRuntime
}

// ContainerRuntime runs container sites. It is a reconciler: the core
// records the desired state in the site's metadata and calls Kick; the
// runtime converges and reports what it observed through
// SiteService.SetContainerState. Nothing on the serving path calls it —
// routing reads the observed state from meta.json.
type ContainerRuntime interface {
	// Allowed reports whether the owner may put a site into container mode.
	// A nil error means yes; otherwise the error is the reason to show.
	Allowed(ownerAccountID string) error
	// Kick asks the runtime to converge this site now rather than at its
	// next tick. It does not wait.
	Kick(viewID string)
	// Stop removes the site's containers and networks and returns when they
	// are gone. Leaving container mode and deleting a site call it before
	// they touch the files the containers were writing.
	Stop(viewID string) error
	// Logs returns the recent output of one service.
	Logs(ctx context.Context, viewID, service string, tail int) (string, error)
	// Usage is what the site's running services use, as last measured;
	// ok=false when there is no recent measurement.
	Usage(viewID string) (u ContainerUsage, ok bool)
}

// ContainerUsage is what a container site's running services use right now.
// See docs/superpowers/specs/2026-10-25-container-usage-design.md.
type ContainerUsage struct {
	At       time.Time
	Services []ServiceUsage
	// Web is the project's web traffic when several of its services serve
	// HTTP and it cannot be given to one of them; nil otherwise.
	Web *Traffic
}

// ServiceUsage is one running service's usage against its limits. CPU, In
// and Out are rates over the last interval, nil until there are two samples.
type ServiceUsage struct {
	Name             string
	MemoryBytes      int64
	MemoryLimitBytes int64
	CPU              *float64 // cores
	CPULimit         float64
	Pids, PidsLimit  int
	// In and Out are its public traffic in bytes per second: the project's
	// web traffic when it is the one service serving HTTP, and its own
	// internet traffic — never what it exchanges with the project's other
	// services.
	In, Out *float64
}

// Traffic is bytes per second in and out.
type Traffic struct {
	In, Out float64
}

// ContainerSite is a container site as the runtime needs to see it.
type ContainerSite struct {
	ViewID string
	Owner  string
	// Container is false for a site that has left container mode.
	Container  bool
	Enabled    bool
	RestartSeq int
	Expired    bool
	// Locked is the operator's hold (SiteLock): the project is stopped like
	// an expired one and counts against nothing.
	Locked bool
	// Compose is the compose file, or nil with ComposeErr saying why not.
	Compose    []byte
	ComposeErr string
	// MaxBytes is the site's storage cap. The runtime measures the site
	// against it now and then through Info, which walks the tree and is far
	// too costly for every tick.
	MaxBytes int64
	// Name is the owner's label for the site, for the admin console.
	Name string
	// Limits are the operator's own resource limits for the site's
	// services, by service name: desired state the core writes on the admin
	// console's order. A field not set in them comes from the instance
	// default.
	Limits map[string]ContainerLimits
	// Observed is what the runtime last recorded.
	Observed ContainerState
}

// ContainerLimits are one container's resource limits. A zero field is not
// set: an operator's value for a service inherits it from the instance
// default, and a container recorded before limits were recorded has none.
type ContainerLimits struct {
	MemoryMB int
	CPUs     float64
	Pids     int
}

// Or is l with every field it does not set taken from d.
func (l ContainerLimits) Or(d ContainerLimits) ContainerLimits {
	if l.MemoryMB <= 0 {
		l.MemoryMB = d.MemoryMB
	}
	if l.CPUs <= 0 {
		l.CPUs = d.CPUs
	}
	if l.Pids <= 0 {
		l.Pids = d.Pids
	}
	return l
}

// ContainerState is what the runtime observed of a site.
type ContainerState struct {
	Status      string // starting, running, stopped, error
	Message     string
	AppliedHash string
	AppliedSeq  int
	// AppliedAt is when the project was last started from its compose file.
	// A downgrade stops the most recently started projects first.
	AppliedAt time.Time
	Services  []ContainerService
}

// ContainerService is one service as last applied.
type ContainerService struct {
	Name    string
	Image   string
	Egress  bool
	Volumes []string
	Ports   []int  // the ports it speaks HTTP on (compose http:)
	State   string // Docker's: running, restarting, exited…
	Host    string // its name on the project network
	// Limits are what was last applied to its container.
	Limits ContainerLimits
}

// SiteInfo is the dashboard's view of a site.
type SiteInfo struct {
	ViewID string
	// Owner is the owning account's id, empty for an anonymous site. The
	// account dashboard never reads it — there the owner is the viewer — but
	// the admin console's instance-wide list needs it as a column.
	Owner string
	Mode  string
	// SPA is the web server's single-page-app fallback, so a list can name
	// the site's type as its settings do.
	SPA bool
	// Name is the owner's optional label for the site; empty when unnamed.
	// Private: the dashboard and the admin console show it, nothing served
	// ever does.
	Name string
	// Tags are the owner's labels for the site, in stored order; nil when
	// untagged. Private like Name: nothing served ever shows them.
	Tags []string
	// Admins are the accounts the site is shared with, in stored order; nil
	// when it is shared with none.
	Admins []SiteAdmin
	// Domains are the site's VERIFIED custom domains, if any. The admin
	// console lists and searches by them; with the name, they are the only
	// human-memorable handles a site has.
	Domains []string
	// DomainLinks are Domains again, each with the URL it serves at, followed
	// by the claims still waiting for their DNS proof. The account dashboard
	// lists them; the URL is built by the core because the scheme and port
	// are the core's business.
	DomainLinks []DomainLink
	// Origin is the surface that created the site — "mcp", or empty for the UI
	// and the JSON API. Provenance only; nothing gates on it.
	Origin string
	// Bytes is the site's files alone: the container runtime's stop rule
	// reads it and must never count a push. GitBytes is its Git repository,
	// which shares the byte cap; a size shown to a person is Bytes+GitBytes.
	Bytes    int64
	GitBytes int64
	Files    int
	// Forms is how many email forms the site holds (pending ones count, as
	// they do against the plan's cap).
	Forms   int
	ViewURL string
	// ViewOff is set when the generated address (ViewURL) does not serve the
	// site — its owner switched it off, or it redirects to ViewRedirectTo.
	// The dashboard then shows it without a link.
	ViewOff        bool
	ViewRedirectTo string
	EditURL        string
	CreatedAt      time.Time
	// Violations counts content-security-policy reports the site's pages have
	// produced, and Blocked names the distinct destinations they tried to
	// reach. A site whose first visitor trips form-action against a foreign
	// host is almost always phishing, so the admin console shows both.
	Violations int
	Blocked    []string
	// Reporters is the most distinct reporting networks seen in one
	// aggregation window — the number that separates a phishing page with
	// real visitors from one person's script flagging a competitor.
	Reporters int
	// ExpiresAt is when the site stops serving, or nil if it has no expiry. A
	// downgrade stamps a 30-day grace here, and that date is the only warning
	// the owner gets before the sweep deletes the site — the dashboard must
	// show it.
	ExpiresAt *time.Time
	// ExpiryByOwner is set when ExpiresAt is a date the owner (or the
	// operator) chose rather than the plan's lifetime. Such a date survives
	// a transfer, shortened to the recipient's cap, so the recipient is told.
	ExpiryByOwner bool
	// ViewPassword is set when the site is behind a view password. It is not
	// reset by a transfer, so the recipient is told to set a new one.
	ViewPassword bool
	// Locked is the operator's hold on the site, or nil. The register shows
	// and filters it; the owner's dashboard shows it and hides the actions
	// the lock refuses.
	Locked *SiteLock
	// Findings are the abuse guard's hits not yet reviewed, newest last. The
	// register shows them; the owner never sees them.
	Findings []ScanFinding
	// UpdatedAt is the site's last content or settings change, as meta.json
	// records it. The workspace list sorts and shows by it.
	UpdatedAt time.Time
	// ContainerEnabled and ContainerStatus are a container site's desired
	// state and the runtime's last observed word for it ("running",
	// "stopped", "error"…), for the list's status column.
	ContainerEnabled bool
	ContainerStatus  string
}

// EditPages is implemented by a Provider that serves its own page at
// /e/{editID} to some visitors — the enterprise workspace interface, for a
// signed-in account that chose it. OPTIONAL: the core asks it after the
// site is known to exist; false (and no provider) serves edit.html as
// always. The page it serves must use the same site API, so no rule moves.
type EditPages interface {
	ServeEditPage(w http.ResponseWriter, r *http.Request) bool
}

// SiteAdmin is one account a site is shared with; Until, when set, is when
// the share ends. The seam twin of the core's store.SiteAdmin.
type SiteAdmin struct {
	Account string
	Until   *time.Time
}

// ChangeBy is who asked for an ownership change, for the site's log. IP must
// be auth.ClientIP(r) of the acting request (the last X-Forwarded-For entry,
// Caddy's), never an address the client chose.
type ChangeBy struct {
	Account   string
	IP        string
	UserAgent string
	// Surface is where the change came in, a provenance surface; empty is
	// the dashboard. The extension's background loop, finishing a transfer
	// its recipient accepted earlier, passes provenance.SurfaceBackground
	// with no address and no user agent.
	Surface string
}

// SiteOwnership is what one meta.json read says about who stands on a site:
// its owner ("" for an anonymous site) and the accounts listed as admins
// (whether or not an end date has passed).
type SiteOwnership struct {
	Owner  string
	Admins []string
}

// OwnershipSites is the part of the core's site service that moves a site
// between accounts, shares it and measures it. It is OPTIONAL: the core's
// SiteService implements it and the extension asserts it, so a SiteService
// without it (a fake, an older core) stays whole. Every method is inert for
// an extension that never calls it, and the community build has no caller.
//
// Errors: a site that no longer exists is ErrSiteGone, a locked one
// ErrSiteLocked, and the ownership refusals are ErrNotOwner, ErrNotAdoptable
// and ErrTooManyAdmins.
type OwnershipSites interface {
	// TransferSite hands the site from account from to account to under one
	// site lock. The core stamps the recipient's caps from g exactly as a
	// creation does; detaches every address whose proof is from's account
	// (the rules WillNotMove shows, decided on the meta under the lock: a
	// zone that cannot be read refuses the transfer with nothing written);
	// clears the tags and the admins; sets a fresh edit password (returned
	// once); cancels a push that is running; drops the verify cache and
	// revokes the site's upload tokens. It does not relink the extension's
	// own account indexes. An empty to is ErrNotOwner.
	TransferSite(viewID, from, to string, g CreateGrant, by ChangeBy) (newPassword string, err error)
	// AdoptSite gives an anonymous, unlocked, unexpired site to account to
	// and stamps g onto it as a creation does.
	AdoptSite(viewID, to string, g CreateGrant, by ChangeBy) error
	// SetAdmins edits the site's admins under the site lock: edit receives
	// the stored list and returns the one to store (de-duplicated by
	// account, at most store.MaxAdmins). Under the same lock the site's
	// owner must be expectedOwner (never empty), else ErrNotOwner with
	// nothing written (the DeleteSiteIfOwner pattern). An error from edit is
	// returned unchanged, with nothing written. edit runs under the site lock
	// and must not call back into the site service.
	SetAdmins(viewID, expectedOwner string, by ChangeBy, edit func([]SiteAdmin) ([]SiteAdmin, error)) error
	// CheckEditPassword verifies a site's edit password through the same
	// per-address and per-site limits as every other surface. ip must be
	// auth.ClientIP(r) of the request that carries the password: a caller
	// that passes anything else lets one client spend another's allowance
	// (or none at all). retryAfter is
	// set when the call was throttled (ok is then false).
	CheckEditPassword(viewID, password, ip string) (ok bool, retryAfter time.Duration)
	// ResolveSite maps a view URL, an edit URL, a view id, an edit id or a
	// custom domain (host or host/path) to the site's view id.
	ResolveSite(urlOrID string) (viewID string, ok bool)
	// WillNotMove lists the site's addresses whose proof depends on the
	// sender's account (a name in the sender's zone, an operator zone, an
	// address held through its host) and would not survive a transfer. For
	// display: an unreadable site or zone lists nothing; TransferSite
	// decides again under the lock and refuses on such an error.
	WillNotMove(viewID, sender string) []string
	// HostConflicts maps each host used by the bundle to the sender's OTHER
	// sites (outside viewIDs) that have an address on that host. A meta
	// read error (other than a site that is gone) is an error: the answer
	// is then unknown, never "no conflict".
	HostConflicts(viewIDs []string, sender string) (map[string][]string, error)
	// FormRecipients lists the recipients of the site's active or pending
	// forms, each once.
	FormRecipients(viewID string) []string
	// SiteBytes is the site's central byte count (files plus its Git
	// repository) and its file count.
	SiteBytes(viewID string) (bytes int64, files int, err error)
	// SiteOwner is the owner account id in the site's meta.json ("" for an
	// anonymous site). It reads meta only, never the content or the git
	// repository, so it is cheap enough for a walk over an account's
	// sites. A site that no longer exists is ErrSiteGone, distinct from
	// any other read error.
	SiteOwner(viewID string) (owner string, err error)
	// SiteOwners maps every site's view id to the owner in its meta.json (""
	// for an anonymous site), reading meta only -- never the content or the
	// git repository -- so a background walk over all sites stays cheap. A
	// folder that vanished while the walk ran is simply absent. A site whose
	// meta.json cannot be read is NOT listed and makes the returned error
	// non-nil, together with the sites that did read: absence from the map is
	// "gone" only when the error is nil. ContainerSites is not this list.
	SiteOwners() (map[string]SiteOwnership, error)
	// SiteLocked reports whether the operator holds the site locked, from its
	// meta.json only (never the content or the git repository). A site that
	// no longer exists is ErrSiteGone.
	SiteLocked(viewID string) (bool, error)
	// SiteAdmins is the admins listed in the site's meta.json, with their
	// end dates (nil for none). Like SiteOwner it reads meta only, never
	// the content or the git repository. A site that no longer exists is
	// ErrSiteGone.
	SiteAdmins(viewID string) ([]SiteAdmin, error)
	// DeleteSiteIfOwner deletes the site only if, under the site lock, its
	// meta.json owner is owner (never empty); otherwise it deletes nothing
	// and returns ErrNotOwner. Every rule of SiteService.Delete holds: a
	// locked site is ErrSiteLocked, a running push is stopped, a missing
	// site is ErrSiteGone. An unreadable meta.json is ErrNotOwner.
	DeleteSiteIfOwner(viewID, owner string) error
	// RotateEditPasswordIfOwner is SiteService.RotateEditPassword with the
	// owner checked where the write happens: under the site lock, meta.json
	// must name owner (never empty), else ErrNotOwner and nothing is
	// written. A rotate that passed the caller's own check and then lost a
	// race with a transfer must never hand the old owner a password to the
	// new owner's site. A locked site is ErrSiteLocked, a missing one
	// ErrSiteGone.
	RotateEditPasswordIfOwner(viewID, owner string) (newPassword string, err error)
	// SetNameIfOwner is SiteService.SetName with the same owner check under
	// the site lock: expectedOwner is the owner the caller's standing was
	// decided against (for an admin, the owner who shared the site).
	SetNameIfOwner(viewID, expectedOwner, name string) error
	// UpdateTagsIfOwner is SiteService.UpdateTags with the same owner check
	// under the site lock, so a racing transfer never receives the old
	// owner's labels.
	UpdateTagsIfOwner(viewID, expectedOwner string, edit func(current []string) ([]string, error)) error
}

// DomainLink is one custom domain of a site as the dashboard shows it. A
// pending claim serves nothing yet, so it has no URL. RedirectTo is the
// custom domain it redirects to, when it does.
type DomainLink struct {
	Domain     string
	URL        string
	Pending    bool
	RedirectTo string
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
