package cleanup

import (
	"errors"
	"log/slog"
	"time"

	"github.com/ittrail/sitebin.io/internal/analytics"
	"github.com/ittrail/sitebin.io/internal/ext"
	"github.com/ittrail/sitebin.io/internal/store"
)

// reconcileAnalytics backfills the analytics stamp onto an owned site from its
// owner's current plan, and follows a plan change. It writes only that field:
// the site's caps and expiry stay exactly as they are. A community build, a
// lookup error and an unknown owner change nothing.
func reconcileAnalytics(st *store.Store, site *store.Site) {
	if site.Meta.OwnerAccountID == "" {
		return
	}
	p, ok := ext.Get()
	if !ok {
		return
	}
	grant, known, err := p.QuotaFor(site.Meta.OwnerAccountID)
	if err != nil || !known {
		return
	}
	if equalBoolPtr(grant.Analytics, site.Meta.QuotaAnalytics) {
		return
	}
	if err := st.SetQuotaAnalytics(site, grant.Analytics); err != nil {
		slog.Error("cleanup: reconcile analytics stamp", "id", site.ViewID, "owner", site.Meta.OwnerAccountID, "err", err)
	}
}

// PurgeAnalytics deletes the site's analytics days older than retentionDays,
// counted in the site's zone (its setting, else defaultZone, the instance's):
// each day's records, summary and over-the-cap count, and what a crashed
// write left. A retention of 0 keeps every day. It holds the site's stats
// lock, as the recorder does, so no writer of this process is half-way
// through a file it deletes, and a site deleted meanwhile is left alone.
//
// Under that lock it reads the site again from meta.json, never trusting
// the caller's copy: a site locked since (the CLI locks from another
// process) is evidence and keeps every day, and a meta.json that cannot be
// read deletes nothing. It returns the number of files deleted.
func PurgeAnalytics(st *store.Store, site *store.Site, now time.Time, retentionDays int, defaultZone string) (int, error) {
	if retentionDays <= 0 {
		return 0, nil
	}
	n := 0
	_, err := st.WithStatsLock(site.ViewID, func(siteDir string) error {
		fresh, err := st.ByViewID(site.ViewID)
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if fresh.Meta.IsLocked() {
			return nil
		}
		start := analytics.RetentionStart(now, fresh.Meta.AnalyticsZone(defaultZone), retentionDays)
		n, err = analytics.PurgeDays(siteDir, start, now)
		return err
	})
	return n, err
}
