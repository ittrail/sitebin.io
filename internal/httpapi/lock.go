package httpapi

import (
	"net/http"
	"time"

	"github.com/ittrail/sitebin.io/internal/store"
)

// Locked sites: the operator's evidence hold (store.SiteLock). authz serves
// them to nobody; every write surface refuses them after it has
// authenticated the caller, so a stranger learns nothing about the site and
// the owner learns why. See
// docs/superpowers/specs/2026-09-28-site-lock-and-account-suspension.md.

const (
	msgSuspendedTitle = "Site suspended"
	msgSuspendedBody  = "This site has been suspended by the operator for violating the terms of use."
)

// refuseLocked answers a request for a locked site with 403 and the lock's
// message, and reports whether it did. Every per-site gate calls it once the
// caller is authenticated.
func refuseLocked(w http.ResponseWriter, site *store.Site) bool {
	if !site.Meta.IsLocked() {
		return false
	}
	writeError(w, 403, store.LockedMessage(site.Meta.Locked))
	return true
}

// lockPayload is the lock as the owner sees it: when, and why. Who placed it
// ("admin" or "account") is the operator's business and is left out.
func lockPayload(l *store.SiteLock) map[string]any {
	if l == nil {
		return nil
	}
	out := map[string]any{"at": l.At.UTC().Format(time.RFC3339)}
	if l.Reason != "" {
		out["reason"] = l.Reason
	}
	return out
}

// lockChanged is what follows a lock or an unlock of site in this process.
// The site's cached password checks and its upload tokens go: whoever held
// access loses it with the lock, and must authenticate afresh after it. A
// container project is handed to the runtime at once — a lock stops it, an
// unlock (which bumped its restart sequence) starts it again — rather than
// at the runtime's next tick. None of this is needed for the lock to hold:
// every gate reads the lock from meta.json, which is how a lock the CLI
// writes from another process takes effect too.
func (a *API) lockChanged(site *store.Site) {
	a.verifyCache.Drop(site.EditID + ":")
	a.uploads.revokeSite(site.ViewID)
	if site.Meta.Mode == store.ModeContainer {
		if rt, ok := containerRuntime(); ok {
			rt.Kick(site.ViewID)
		}
	}
}
