package cleanup

import (
	"log/slog"

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
