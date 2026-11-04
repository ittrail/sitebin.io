package httpapi

import (
	"github.com/ittrail/sitebin.io/internal/gitrepo"
	"github.com/ittrail/sitebin.io/internal/store"
)

// gitState is whether a site has Git, and why not: the one statement of it,
// which the endpoint, the API's Git routes and the edit page's tab all read.
// See docs/superpowers/specs/2026-11-03-site-git-design.md, "Plan gate".
//
//   - Writable: clone, fetch and push; the repository is created on demand.
//   - Available: at least readable — Writable, or a plan without Git while a
//     repository exists (a downgrade leaves it read-only, never deletes it).
//   - Reason: why not Writable, "" when it is.
type gitState struct {
	Available, Writable bool
	Reason              string
}

// Reasons a site has no Git (or read-only Git), in the order they are asked.
const (
	gitReasonDisabled    = "disabled"    // SITEBIN_GIT_ENABLED=false
	gitReasonUnavailable = "unavailable" // no git binary at startup
	gitReasonAnonymous   = "anonymous"   // an account-less site on an accounts instance
	gitReasonLocked      = "locked"      // the operator's evidence hold
	gitReasonPlan        = "plan"        // the site's plan has no Git
)

// gitStateOf answers for site as it was read from meta.json (the lock in
// particular is never taken from memory: the CLI locks from another process).
func (a *API) gitStateOf(site *store.Site) gitState {
	reason := a.gitReason(site)
	return gitState{
		Reason:   reason,
		Writable: reason == "",
		Available: reason == "" ||
			(reason == gitReasonPlan && gitrepo.Exists(site.GitDir())),
	}
}

func (a *API) gitReason(site *store.Site) string {
	switch {
	case !a.cfg.GitEnabled:
		return gitReasonDisabled
	case a.git == nil:
		return gitReasonUnavailable
	case a.gatedAnonymous(site):
		return gitReasonAnonymous
	case site.Meta.IsLocked():
		return gitReasonLocked
	case !a.gitPlanAllows(site):
		return gitReasonPlan
	}
	return ""
}

// gitPlanAllows reads the site's stamped git flag. With accounts enabled it
// must be true — nil is a site not restamped yet, and counts as off. Without
// accounts (community, an enterprise binary in open mode) there are no plans
// to sell it by: nil or true is Git, and only an explicit false is not.
func (a *API) gitPlanAllows(site *store.Site) bool {
	q := site.Meta.QuotaGit
	if a.accountsEnabled() {
		return q != nil && *q
	}
	return q == nil || *q
}
