package cleanup

import (
	"errors"
	"testing"

	"github.com/ittrail/sitebin.io/internal/ext"
	"github.com/ittrail/sitebin.io/internal/store"
)

func TestReconcileAnalyticsWritesOnlyTheStamp(t *testing.T) {
	yes := true
	ext.Register(&stubProvider{ok: true, grant: ext.CreateGrant{OwnerAccountID: "acct-1", Analytics: &yes, MaxSiteBytes: 999, MaxFiles: 77, MaxExpiryDays: 1}})
	defer ext.Reset()
	st, err := store.New(t.TempDir(), "sitebin.example", 1<<20, 100)
	if err != nil {
		t.Fatal(err)
	}
	site := gitOwnedSite(t, st)
	before := *site.Meta.ExpiresAt

	reconcileAnalytics(st, site)

	got, _ := st.ByViewID(site.ViewID)
	if got.Meta.QuotaAnalytics == nil || !*got.Meta.QuotaAnalytics {
		t.Fatalf("QuotaAnalytics = %v, want true", got.Meta.QuotaAnalytics)
	}
	if got.Meta.ExpiresAt == nil || !got.Meta.ExpiresAt.Equal(before) || got.Meta.QuotaBytes != 5 {
		t.Fatalf("reconcileAnalytics touched other fields: expires %v (was %v), bytes %d", got.Meta.ExpiresAt, before, got.Meta.QuotaBytes)
	}

	// a plan change is followed
	no := false
	ext.Reset()
	ext.Register(&stubProvider{ok: true, grant: ext.CreateGrant{Analytics: &no}})
	reconcileAnalytics(st, got)
	got, _ = st.ByViewID(site.ViewID)
	if got.Meta.QuotaAnalytics == nil || *got.Meta.QuotaAnalytics {
		t.Fatalf("QuotaAnalytics = %v after downgrade, want false", got.Meta.QuotaAnalytics)
	}
}

func TestReconcileAnalyticsLookupErrorChangesNothing(t *testing.T) {
	yes := true
	for name, p := range map[string]*stubProvider{
		"error":   {ok: true, err: errors.New("outage"), grant: ext.CreateGrant{Analytics: &yes}},
		"unknown": {ok: false, grant: ext.CreateGrant{Analytics: &yes}},
	} {
		t.Run(name, func(t *testing.T) {
			ext.Register(p)
			defer ext.Reset()
			st, _ := store.New(t.TempDir(), "sitebin.example", 1<<20, 100)
			site := gitOwnedSite(t, st)
			reconcileAnalytics(st, site)
			got, _ := st.ByViewID(site.ViewID)
			if got.Meta.QuotaAnalytics != nil {
				t.Fatalf("QuotaAnalytics = %v, want nil", *got.Meta.QuotaAnalytics)
			}
		})
	}
}

func TestReconcileAnalyticsCommunityBuildDoesNothing(t *testing.T) {
	ext.Reset()
	st, _ := store.New(t.TempDir(), "sitebin.example", 1<<20, 100)
	site := gitOwnedSite(t, st)
	reconcileAnalytics(st, site)
	got, _ := st.ByViewID(site.ViewID)
	if got.Meta.QuotaAnalytics != nil {
		t.Fatal("community build must not stamp analytics")
	}
}
