package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/ittrail/sitebin.io/internal/gitrepo"
	"github.com/ittrail/sitebin.io/internal/provenance"
	"github.com/ittrail/sitebin.io/internal/store"
)

// After a push: what it changed, recorded at the gate like every other write
// (see "Provenance is recorded at the gate" in the repo guide).
//
// Git refuses a ref update in-band — a stale old value, a missing object, a
// pack over the allowance — and still exits 0. So the record is not of what
// the client asked for but of what the repository shows: a ref counts as
// pushed only when its value changed during the push, to what the client
// asked (or away, for a delete). A push that changed nothing records
// nothing and is not marked for the sweep's collection.

// pushedRef is one ref a push changed. old is "" for a created ref, new ""
// for a deleted one.
type pushedRef struct {
	ref, old, new string
	forced        bool
}

func (p pushedRef) String() string {
	switch {
	case p.old == "":
		return p.ref + " " + shortSHA(p.new) + " created"
	case p.new == "":
		return p.ref + " " + shortSHA(p.old) + " deleted"
	case p.forced:
		return p.ref + " " + shortSHA(p.old) + ".." + shortSHA(p.new) + " forced"
	}
	return p.ref + " " + shortSHA(p.old) + ".." + shortSHA(p.new)
}

func shortSHA(s string) string {
	if len(s) > 7 {
		return s[:7]
	}
	return s
}

// isZeroSHA reports git's "no object": a created ref's old value, a deleted
// ref's new one.
func isZeroSHA(s string) bool { return strings.Trim(s, "0") == "" }

// recordPush works out what the push changed — before is the repository's
// refs as they were before git ran — then marks the repository as pushed
// and records it. gitOK says git ran to its end; without it, an unreadable
// repository afterwards means nothing is known and nothing is recorded.
//
// It runs inside the push's registration, so the site folder is still there;
// its git queries are not tied to the request, which may be gone, nor to the
// registry's cancel: they only read, and a lock that cancelled the push
// still wants to know what the push had changed.
func (a *API) recordPush(r *http.Request, site *store.Site, act actor, before map[string]string, res gitrepo.RPCResult, gitOK bool) {
	if len(res.Updates) == 0 {
		return
	}
	ctx := context.Background()
	dir := site.GitDir()
	var changed []pushedRef
	unverified := false
	after, err := a.git.RefTargets(ctx, dir)
	switch {
	case err == nil:
		changed = refsChanged(before, after, res.Updates)
	case gitOK:
		// Git said it finished; what it was asked is the best account left.
		a.log.Warn("git: could not read the refs after a push", "id", site.ViewID, "err", err)
		unverified = true
		for _, u := range res.Updates {
			p := pushedRef{ref: u.Ref, old: u.Old, new: u.New}
			if isZeroSHA(u.Old) {
				p.old = ""
			}
			if isZeroSHA(u.New) {
				p.new = ""
			}
			changed = append(changed, p)
		}
	default:
		a.log.Warn("git: could not read the refs after a failed push", "id", site.ViewID, "err", err)
		return
	}
	if len(changed) == 0 {
		return
	}
	if err := gitrepo.MarkPushed(dir); err != nil {
		a.log.Warn("git: could not mark the repository pushed", "id", site.ViewID, "err", err)
	}
	// Only the refs the record names are checked for a force push.
	for i := range changed[:min(len(changed), maxNotedPaths)] {
		p := &changed[i]
		if p.old == "" || p.new == "" {
			continue
		}
		if ff, err := a.git.IsAncestor(ctx, dir, p.old, p.new); err == nil && !ff {
			p.forced = true
		}
	}
	e := entryFor(r, act, provenance.ActionGitPush)
	e.Detail = pushDetail(changed, res.BytesIn, unverified)
	a.record(site, e)
}

// refsChanged is the updates of a push that took effect: the ref's value
// changed between before and after, to the update's new value — or away,
// for a delete.
func refsChanged(before, after map[string]string, updates []gitrepo.RefUpdate) []pushedRef {
	var out []pushedRef
	for _, u := range updates {
		was, existed := before[u.Ref]
		now, exists := after[u.Ref]
		if existed == exists && was == now {
			continue // untouched: refused, or a no-op
		}
		if isZeroSHA(u.New) {
			if exists {
				continue
			}
		} else if !exists || now != u.New {
			continue
		}
		p := pushedRef{ref: u.Ref, old: was, new: now}
		if !existed {
			p.old = ""
		}
		out = append(out, p)
	}
	return out
}

// pushDetail is the record's description: the first few refs a push
// changed, how many more, and the bytes it brought.
func pushDetail(refs []pushedRef, bytesIn int64, unverified bool) string {
	var parts []string
	for i, p := range refs {
		if i == maxNotedPaths {
			parts = append(parts, fmt.Sprintf("%d more", len(refs)-i))
			break
		}
		parts = append(parts, p.String())
	}
	parts = append(parts, store.HumanBytes(bytesIn)+" received")
	if unverified {
		parts = append(parts, "not verified")
	}
	return strings.Join(parts, ", ")
}
