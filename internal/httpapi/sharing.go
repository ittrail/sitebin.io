package httpapi

import (
	"net/http"

	"github.com/ittrail/sitebin.io/internal/ext"
	"github.com/ittrail/sitebin.io/internal/store"
)

// Shared sites: what an admin may not do, and what an admin is shown. The
// standing itself is decided in one place, accountStanding (server.go); this
// file only reads the role the gate put on the actor. See
// docs/superpowers/specs/2026-11-03-site-ownership-design.md.

// The roles a credential earns on a site (actor.role), as the payloads name
// them. rolePassword is the edit password's holder, who keeps every right
// the owner has: the password is the owner's credential, on owned and
// anonymous sites alike.
const (
	roleOwner    = "owner"
	roleAdmin    = "admin"
	rolePassword = "password"
)

// msgOwnerOnly answers an admin asking for what only the owner may do, on
// the API (403) and over MCP alike.
const msgOwnerOnly = "only the site's owner can do this"

// ownerOnly is the one statement of who is refused an owner-only action:
// an admin, and nobody else. The owner-only actions are deleting the site,
// setting, changing or clearing its expiry, and deleting its repository
// (rotating the edit password, transferring and managing shares are the
// extension's dashboard actions, which it refuses to an admin itself).
func ownerOnly(role string) error {
	if role == roleAdmin {
		return &apiError{http.StatusForbidden, msgOwnerOnly}
	}
	return nil
}

// refuseUnlessOwner answers 403 msgOwnerOnly and reports true when the
// request's actor (the gate's) is an admin.
func (a *API) refuseUnlessOwner(w http.ResponseWriter, r *http.Request) bool {
	if err := ownerOnly(a.actorOf(r).role); err != nil {
		respondErr(w, err)
		return true
	}
	return false
}

// ownerOnlySettings reports whether set writes a setting only the owner may
// change. Per field: a write that carries one is refused whole, with nothing
// written; the same fields without it are anyone's who may manage the site.
func ownerOnlySettings(set updateSet) bool {
	return len(set.ExpiresAt) > 0 // set, changed or cleared (null)
}

// applySettingsAs is applySettings for the caller the gate let in as act —
// the API's PUT and MCP's update_site alike:
//   - an admin is refused an owner-only setting (ownerOnlySettings) before
//     anything is written;
//   - an admin's tags are the admin's own (SetAdminTags), validated by
//     store.CleanTags, and never touch Meta.Tags, which are the owner's.
//     They are stored after the site's own settings, outside the site lock:
//     nothing nests a seam call inside it.
func (a *API) applySettingsAs(site *store.Site, set updateSet, act actor) error {
	if ownerOnlySettings(set) {
		if err := ownerOnly(act.role); err != nil {
			return err
		}
	}
	var adminTags []string
	if act.role == roleAdmin && set.Tags != nil {
		tags, err := store.CleanTags(*set.Tags)
		if err != nil {
			return &apiError{http.StatusBadRequest, err.Error()}
		}
		adminTags, set.Tags = tags, nil
	}
	if err := a.applySettings(site, set); err != nil {
		return err
	}
	if adminTags == nil {
		return nil
	}
	sa, ok := sharingProvider()
	if !ok {
		// An admin standing comes only from this provider; it went away
		// mid-request.
		return &apiError{http.StatusServiceUnavailable, msgAdminTagsUnavailable}
	}
	if err := sa.SetAdminTags(act.account, site.ViewID, adminTags); err != nil {
		a.log.Error("sharing: could not store an admin's tags", "id", site.ViewID, "account", act.account, "err", err)
		return &apiError{http.StatusServiceUnavailable, msgAdminTagsUnavailable}
	}
	return nil
}

// msgAdminTagsUnavailable: an admin's tags could not be stored right now.
const msgAdminTagsUnavailable = "your tags for this site could not be saved right now; try again in a minute"

// tagsFor is the site's tags as the account in role sees them: an admin's
// own (AdminTags), never the owner's Meta.Tags; the site's for anyone else.
// Never nil, so a payload always carries a list.
func (a *API) tagsFor(site *store.Site, role, account string) []string {
	tags := site.Meta.Tags
	if role == roleAdmin {
		tags = nil
		if sa, ok := sharingProvider(); ok {
			tags = sa.AdminTags(account, site.ViewID)
		}
	}
	if tags == nil {
		return []string{}
	}
	return append([]string(nil), tags...)
}

// ownerEmail is the address of the site's owner, for the "shared by" line an
// admin is shown, through the extension's account directory.
func ownerEmail(site *store.Site) (string, bool) {
	p, ok := ext.Get()
	if !ok || site.Meta.OwnerAccountID == "" {
		return "", false
	}
	dir, ok := p.(ext.AccountDirectory)
	if !ok {
		return "", false
	}
	email, ok := dir.AccountEmail(site.Meta.OwnerAccountID)
	return email, ok && email != ""
}

// payloadFor is sitePayload for the caller the gate let in: plus "role"
// (owner, admin or password; absent where no gate ran) and, for an admin,
// "shared_by" (the owner's address) and the admin's own tags in place of the
// owner's. Every per-site handler answers with it; sitePayload stays the
// site's own document. Older clients ignore the new keys.
func (a *API) payloadFor(r *http.Request, site *store.Site) map[string]any {
	p := a.sitePayload(site)
	act := a.actorOf(r)
	if act.role == "" {
		return p
	}
	p["role"] = act.role
	if act.role == roleAdmin {
		p["tags"] = a.tagsFor(site, act.role, act.account)
		if email, ok := ownerEmail(site); ok {
			p["shared_by"] = email
		}
	}
	return p
}
