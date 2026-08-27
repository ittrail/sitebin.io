// Package cleanup deletes expired sites and repairs the filesystem indexes.
// Between expiry and deletion (a 24h grace window) the authz endpoint serves
// 410 Gone, so visitors see a clear signal before the site disappears.
package cleanup

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/ittrail/sitebin.io/internal/ext"
	"github.com/ittrail/sitebin.io/internal/store"
)

// grace is how long an expired site is kept (serving 410) before deletion.
const grace = 24 * time.Hour

// reconcileTrust re-derives a site's trust marker from its owner's current
// tier. The marker is written when quotas are stamped, but a site created
// before this existed has none, and a tier can change without any restamp
// touching it — so the sweep, which walks every site anyway, is the safety net
// the marker's fail-safe polarity relies on.
//
// Only OWNED sites without a marker cost a lookup: an anonymous site is never
// trusted and needs no question asked, and once a trusted site has its marker
// it is never looked up again. A site owned on an untrusted tier is the one
// case that asks every sweep, and the answer comes from the extension's own
// per-account cache.
func reconcileTrust(st *store.Store, site *store.Site) {
	if site.Meta.OwnerAccountID == "" {
		if st.Trusted(site) {
			// An anonymous site must never carry the marker. One here means a
			// site lost its owner (a deleted account) and kept the exemption.
			st.SetTrusted(site, false)
		}
		return
	}
	p, ok := ext.Get()
	if !ok {
		return // community build: creation already marked everything trusted
	}
	grant, known, err := p.QuotaFor(site.Meta.OwnerAccountID)
	if err != nil || !known {
		// Unknown tier or a provider outage: leave the marker exactly as it is.
		// Guessing "trusted" here would strip a phishing site's headers on the
		// strength of a failed lookup.
		return
	}
	if st.Trusted(site) != grant.Trusted {
		if err := st.SetTrusted(site, grant.Trusted); err != nil {
			slog.Error("cleanup: reconcile trust marker", "id", site.ViewID, "owner", site.Meta.OwnerAccountID, "err", err)
		}
	}
}

// Sweep removes sites expired for longer than the grace period and prunes
// index links whose target no longer exists. It returns the number of sites
// deleted.
func Sweep(st *store.Store, now time.Time) (int, error) {
	sites, err := st.AllSites()
	if err != nil {
		return 0, err
	}
	removed := 0
	for _, site := range sites {
		reconcileTrust(st, site)
		if site.Meta.ExpiresAt == nil || !now.After(site.Meta.ExpiresAt.Add(grace)) {
			continue
		}
		// Every decision line carries the owner: the failure modes this sweep
		// now has are per-account (one PayGate outage, one account on a tier the
		// config does not have) and these lines are the only place an operator
		// can see that N sites are being held for one owner. An empty owner is
		// an anonymous site.
		if keep, err := reconcile(st, site, now); err != nil {
			slog.Error("cleanup: reconcile failed, keeping site", "id", site.ViewID, "owner", site.Meta.OwnerAccountID, "err", err)
			continue
		} else if keep {
			// Two shapes of reprieve reach this line: the owner's current tier
			// no longer expires sites at all (expires is nil), or it still does
			// but its cap grew and carried the date out with it. The date says
			// which, so the message must not claim either.
			slog.Info("cleanup: kept expired site, its owner's current tier gives it more time",
				"id", site.ViewID, "owner", site.Meta.OwnerAccountID, "expires", site.Meta.ExpiresAt)
			continue
		}
		if err := st.Delete(site); err != nil {
			slog.Error("cleanup: delete site", "id", site.ViewID, "owner", site.Meta.OwnerAccountID, "err", err)
			continue
		}
		slog.Info("cleanup: deleted expired site", "id", site.ViewID, "owner", site.Meta.OwnerAccountID)
		removed++
	}
	dangling, err := st.DanglingIndexLinks()
	if err != nil {
		return removed, err
	}
	for _, link := range dangling {
		if err := os.Remove(link); err != nil {
			slog.Error("cleanup: prune link", "link", link, "err", err)
		} else {
			slog.Info("cleanup: pruned dangling link", "link", link)
		}
	}
	return removed, nil
}

// reconcile restamps an owned site from its owner's CURRENT tier before the
// site is deleted, and reports whether the site should now be kept. A tier
// change between creation and expiry is invisible in the stamped meta, so an
// upgrade would otherwise arrive too late to save the site.
//
// An error means the owner's tier could not be determined; the caller must keep
// the site and retry on the next sweep. The error is wrapped so the log can
// tell a provider-side lookup failure apart from a local ApplyQuota failure
// (a disk error, or the site being deleted concurrently) — the two call for
// different responses from whoever reads the log.
//
// now is the sweep's timestamp, not a fresh read: ApplyQuota takes the site
// lock and re-reads meta.json from disk, so by the time it returns,
// site.Meta may reflect a concurrent write (e.g. an upload sliding the
// expiry forward) that happened while this site waited its turn in the
// sweep. The keep decision must re-test that fresh expiry against now,
// not just check it for nil, or such a site would be deleted anyway.
func reconcile(st *store.Store, site *store.Site, now time.Time) (keep bool, err error) {
	if site.Meta.OwnerAccountID == "" {
		return false, nil // anonymous: nothing to look up
	}
	p, ok := ext.Get()
	if !ok {
		return false, nil // community build: no accounts, no tiers
	}
	grant, found, err := p.QuotaFor(site.Meta.OwnerAccountID)
	if err != nil {
		return false, fmt.Errorf("quota lookup: %w", err)
	}
	if !found {
		return false, nil // the account is gone; the site is orphaned
	}
	// grace 0: this site already has an expiry, and a sweep must never invent one
	if err := st.ApplyQuota(site, store.Quota{
		Bytes:      grant.MaxSiteBytes,
		Files:      grant.MaxFiles,
		ExpiryDays: grant.MaxExpiryDays,
		Domains:    grant.MaxCustomDomain,
		WebDAV:     grant.WebDAV,
	}, 0); err != nil {
		return false, fmt.Errorf("apply quota: %w", err)
	}
	return site.Meta.ExpiresAt == nil || !now.After(site.Meta.ExpiresAt.Add(grace)), nil
}

// Run sweeps on the given interval until ctx is cancelled.
func Run(ctx context.Context, st *store.Store, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if _, err := Sweep(st, time.Now()); err != nil {
				slog.Error("cleanup sweep", "err", err)
			}
		}
	}
}
