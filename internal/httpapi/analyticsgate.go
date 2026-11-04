package httpapi

import "github.com/ittrail/sitebin.io/internal/store"

// Why site analytics is not available for a site. The empty string means it
// is available.
const (
	analyticsReasonInstance = "instance"
	analyticsReasonPlan     = "plan"
	analyticsReasonLocked   = "locked"
)

// analyticsPlanAllows reads the site's stamped analytics flag. With accounts
// enabled it must be true: nil is a site not restamped yet, and counts as
// off. Without accounts there are no plans to sell it by: nil or true is on,
// and only an explicit false is not.
func (a *API) analyticsPlanAllows(site *store.Site) bool {
	q := site.Meta.QuotaAnalytics
	if a.accountsEnabled() {
		return q != nil && *q
	}
	return q == nil || *q
}

// analyticsReason says why analytics is unavailable for the site, or "" when
// it is available. It ignores the site's own enabled setting.
func (a *API) analyticsReason(site *store.Site) string {
	switch {
	case !a.cfg.AnalyticsEnabled:
		return analyticsReasonInstance
	case site.Meta.IsLocked():
		return analyticsReasonLocked
	case !a.analyticsPlanAllows(site):
		return analyticsReasonPlan
	}
	return ""
}

// analyticsInEffect reports whether the site collects: analytics is available
// and the owner (or an admin) has turned it on. A site's analytics is off
// until its settings say so.
func (a *API) analyticsInEffect(site *store.Site) bool {
	return a.analyticsReason(site) == "" && site.Meta.Analytics != nil && site.Meta.Analytics.Enabled
}
