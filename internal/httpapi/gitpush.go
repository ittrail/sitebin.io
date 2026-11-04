package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

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
//
// The queries for that wait up to postPushSlotWait for a git process slot,
// not a request's five seconds, and all of them together get one deadline
// (postPushBookkeeping). Should they still fail after git said it finished,
// the record says what was asked and that it is not verified — never in the
// form of what happened — and the repository is still marked, since the
// push's bytes may have landed. A force check that found no slot is not
// tried again for the next ref: the rest say "(forced: unknown)".

// postPushSlotWait bounds how long one query of the bookkeeping after a push
// waits for a git process slot.
const postPushSlotWait = 30 * time.Second

// postPushBookkeeping bounds the whole bookkeeping after a push: one slot
// wait and one read. It holds the push's registration, which a delete or a
// lock waits for, so it must not add up query by query.
const postPushBookkeeping = postPushSlotWait + gitrepo.ReadTimeout

// pushInspector is what the bookkeeping asks of the repository: the runner,
// or a test's stand-in for one that cannot answer.
type pushInspector interface {
	RefTargets(ctx context.Context, dir string) (map[string]string, error)
	IsAncestor(ctx context.Context, dir, old, new string) (bool, error)
}

func (a *API) pushInspect() pushInspector {
	if a.gitInspect != nil {
		return a.gitInspect
	}
	return a.git
}

// pushedRef is one ref a push changed. old is "" for a created ref, new ""
// for a deleted one. forcedUnknown: whether it was a force push could not
// be told.
type pushedRef struct {
	ref, old, new string
	forced        bool
	forcedUnknown bool
}

func (p pushedRef) String() string {
	switch {
	case p.old == "":
		return p.ref + " " + shortSHA(p.new) + " created"
	case p.new == "":
		return p.ref + " " + shortSHA(p.old) + " deleted"
	case p.forced:
		return p.ref + " " + shortSHA(p.old) + ".." + shortSHA(p.new) + " forced"
	case p.forcedUnknown:
		return p.ref + " " + shortSHA(p.old) + ".." + shortSHA(p.new) + " (forced: unknown)"
	}
	return p.ref + " " + shortSHA(p.old) + ".." + shortSHA(p.new)
}

// requested is how an update the push asked for, but which could not be
// checked, is named: never in pushedRef's form of what happened.
func requested(u gitrepo.RefUpdate) string {
	switch {
	case isZeroSHA(u.Old):
		return u.Ref + " create " + shortSHA(u.New)
	case isZeroSHA(u.New):
		return u.Ref + " delete " + shortSHA(u.Old)
	}
	return u.Ref + " " + shortSHA(u.Old) + " to " + shortSHA(u.New)
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
// It runs inside the push's registration, so the site folder is still there.
// ctx is not the request's (a client may hang up as soon as it has git's
// report) but is cancelled with the registration's, by a delete or a lock.
func (a *API) recordPush(ctx context.Context, r *http.Request, site *store.Site, act actor, before map[string]string, res gitrepo.RPCResult, gitOK bool) {
	if len(res.Updates) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(gitrepo.WithSlotWait(ctx, postPushSlotWait), postPushBookkeeping)
	defer cancel()
	ins := a.pushInspect()
	dir := site.GitDir()
	after, err := ins.RefTargets(ctx, dir)
	if err != nil {
		if !gitOK {
			a.log.Warn("git: could not read the refs after a failed push", "id", site.ViewID, "err", err)
			return
		}
		// Git said it finished: its bytes may have landed, and the record
		// names what was asked, as asked.
		a.log.Warn("git: could not read the refs after a push; recording it unverified", "id", site.ViewID, "err", err)
		a.markPushed(site)
		e := entryFor(r, act, provenance.ActionGitPush)
		e.Detail = requestedDetail(res.Updates, res.BytesIn)
		a.record(site, e)
		return
	}
	changed := refsChanged(before, after, res.Updates)
	if len(changed) == 0 {
		return
	}
	a.markPushed(site)
	// Only the refs the record names are checked for a force push, and none
	// once a check found no slot or the deadline passed: the next would wait
	// as long again for the same answer.
	gaveUp := false
	for i := range changed[:min(len(changed), maxNotedPaths)] {
		p := &changed[i]
		if p.old == "" || p.new == "" {
			continue
		}
		if gaveUp {
			p.forcedUnknown = true
			continue
		}
		switch ff, err := ins.IsAncestor(ctx, dir, p.old, p.new); {
		case err != nil:
			a.log.Warn("git: could not tell a force push", "id", site.ViewID, "ref", p.ref, "err", err)
			p.forcedUnknown = true
			gaveUp = errors.Is(err, gitrepo.ErrBusy) || ctx.Err() != nil
		case !ff:
			p.forced = true
		}
	}
	e := entryFor(r, act, provenance.ActionGitPush)
	e.Detail = pushDetail(changed, res.BytesIn)
	a.record(site, e)
}

func (a *API) markPushed(site *store.Site) {
	if err := gitrepo.MarkPushed(site.GitDir()); err != nil {
		a.log.Warn("git: could not mark the repository pushed", "id", site.ViewID, "err", err)
	}
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
func pushDetail(refs []pushedRef, bytesIn int64) string {
	names := make([]string, len(refs))
	for i, p := range refs {
		names[i] = p.String()
	}
	return noted(names, bytesIn)
}

// requestedDetail is the description of a push whose effect could not be
// read: every update is what was asked, and says so.
func requestedDetail(updates []gitrepo.RefUpdate, bytesIn int64) string {
	names := make([]string, len(updates))
	for i, u := range updates {
		names[i] = requested(u)
	}
	return "requested, not verified: " + noted(names, bytesIn)
}

// noted lists the first few names, how many more, and the bytes received.
func noted(names []string, bytesIn int64) string {
	var parts []string
	for i, n := range names {
		if i == maxNotedPaths {
			parts = append(parts, fmt.Sprintf("%d more", len(names)-i))
			break
		}
		parts = append(parts, n)
	}
	parts = append(parts, store.HumanBytes(bytesIn)+" received")
	return strings.Join(parts, ", ")
}
