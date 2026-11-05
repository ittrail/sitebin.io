package cleanup

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/ittrail/sitebin.io/internal/analytics"
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

// dayFiles writes one file per name under the site's analytics events, each
// two hours old: a temp file among them is a crashed write's.
func dayFiles(t *testing.T, site *store.Site, now time.Time, names ...string) {
	t.Helper()
	dir := filepath.Join(site.Dir(), "analytics", "events")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, n := range names {
		p := filepath.Join(dir, n)
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		mt := now.Add(-2 * time.Hour)
		if err := os.Chtimes(p, mt, mt); err != nil {
			t.Fatal(err)
		}
	}
}

func eventNames(t *testing.T, site *store.Site) []string {
	t.Helper()
	es, err := os.ReadDir(filepath.Join(site.Dir(), "analytics", "events"))
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range es {
		out = append(out, e.Name())
	}
	sort.Strings(out)
	return out
}

// Days older than the retention go, counted in the site's own zone: its
// setting, else the instance's default. A newer day stays whole.
func TestPurgeAnalyticsRetention(t *testing.T) {
	ext.Reset()
	st, err := store.New(t.TempDir(), "sitebin.example", 1<<20, 100)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC) // 6 November in Kiritimati (UTC+14)
	files := []string{
		"2026-10-25.jsonl", "2026-10-25.sum.json", "2026-10-25.over", "2026-10-25.over.tmp1", "2026-10-25.sum.json.tmp2",
		"2026-10-26.jsonl", "2026-10-26.sum.json", "2026-10-26.over",
		"2026-10-27.jsonl", "2026-11-05.jsonl", "2026-11-05.over.tmp3",
	}
	// 10 days in UTC keep 26 October on; in Kiritimati, 27 October on.
	utc, _, _ := st.Create()
	dayFiles(t, utc, now, files...)
	own, _, _ := st.Create()
	if err := st.SetAnalytics(own, &store.Analytics{Enabled: true, Timezone: "Pacific/Kiritimati"}); err != nil {
		t.Fatal(err)
	}
	dayFiles(t, own, now, files...)
	byDefault, _, _ := st.Create()
	dayFiles(t, byDefault, now, files...)

	if n, err := PurgeAnalytics(st, utc, now, 10, "UTC"); err != nil || n != 6 {
		t.Errorf("UTC site: purged %d, %v; want 6", n, err)
	}
	if got, want := eventNames(t, utc), []string{"2026-10-26.jsonl", "2026-10-26.over", "2026-10-26.sum.json", "2026-10-27.jsonl", "2026-11-05.jsonl"}; !reflect.DeepEqual(got, want) {
		t.Errorf("UTC site kept %v, want %v", got, want)
	}
	if n, err := PurgeAnalytics(st, own, now, 10, "UTC"); err != nil || n != 9 {
		t.Errorf("own zone: purged %d, %v; want 9", n, err)
	}
	if got, want := eventNames(t, own), []string{"2026-10-27.jsonl", "2026-11-05.jsonl"}; !reflect.DeepEqual(got, want) {
		t.Errorf("own zone kept %v, want %v", got, want)
	}
	if _, err := PurgeAnalytics(st, byDefault, now, 10, "Pacific/Kiritimati"); err != nil {
		t.Fatal(err)
	}
	if got, want := eventNames(t, byDefault), []string{"2026-10-27.jsonl", "2026-11-05.jsonl"}; !reflect.DeepEqual(got, want) {
		t.Errorf("default zone kept %v, want %v", got, want)
	}
}

// A retention of 0 keeps every day, and everything beside them.
func TestPurgeAnalyticsRetentionZeroKeepsEverything(t *testing.T) {
	ext.Reset()
	st, _ := store.New(t.TempDir(), "sitebin.example", 1<<20, 100)
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	site, _, _ := st.Create()
	files := []string{"2020-01-01.jsonl", "2020-01-01.sum.json", "2020-01-01.over", "2026-11-05.jsonl"}
	dayFiles(t, site, now, files...)
	if n, err := PurgeAnalytics(st, site, now, 0, "UTC"); err != nil || n != 0 {
		t.Fatalf("retention 0: purged %d, %v", n, err)
	}
	sort.Strings(files)
	if got := eventNames(t, site); !reflect.DeepEqual(got, files) {
		t.Errorf("kept %v, want %v", got, files)
	}
}

// The sweep applies the retention to every site but a locked one, whose
// records are part of the evidence the lock holds.
func TestSweepAppliesAnalyticsRetention(t *testing.T) {
	ext.Reset()
	st, _ := store.New(t.TempDir(), "sitebin.example", 1<<20, 100)
	now := time.Now().UTC()
	old := now.AddDate(0, 0, -40).Format("2006-01-02") + ".jsonl"
	recent := now.AddDate(0, 0, -5).Format("2006-01-02") + ".jsonl"
	open, _, _ := st.Create()
	dayFiles(t, open, now, old, recent)
	held, _, _ := st.Create()
	dayFiles(t, held, now, old, recent)
	if _, err := st.SetLock(held, &store.SiteLock{At: now, By: store.LockByAdmin, Reason: "phishing"}); err != nil {
		t.Fatal(err)
	}

	if _, err := Sweep(st, now, Options{AnalyticsRetentionDays: 30, AnalyticsTimezone: "UTC"}); err != nil {
		t.Fatal(err)
	}
	if got := eventNames(t, open); !reflect.DeepEqual(got, []string{recent}) {
		t.Errorf("open site kept %v, want only %s", got, recent)
	}
	if got := eventNames(t, held); len(got) != 2 {
		t.Errorf("locked site kept %v, want both days", got)
	}
}

// The sweep deletes the salts of days that are over everywhere, once per
// sweep, the same rule the recorder applies.
func TestSweepPrunesSalts(t *testing.T) {
	ext.Reset()
	st, _ := store.New(t.TempDir(), "sitebin.example", 1<<20, 100)
	saltDir := t.TempDir()
	salts := analytics.NewSalts(saltDir)
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	for _, d := range []string{"2026-11-02", "2026-11-03", "2026-11-04", "2026-11-05"} {
		if _, err := salts.For(d); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := Sweep(st, now, Options{AnalyticsSalts: salts}); err != nil {
		t.Fatal(err)
	}
	es, err := os.ReadDir(filepath.Join(saltDir, "analytics", "salts"))
	if err != nil {
		t.Fatal(err)
	}
	var left []string
	for _, e := range es {
		left = append(left, e.Name())
	}
	if want := []string{"2026-11-04", "2026-11-05"}; !reflect.DeepEqual(left, want) {
		t.Errorf("salts left = %v, want %v", left, want)
	}
	// No salt store, no pruning, and no failure.
	if _, err := Sweep(st, now, Options{}); err != nil {
		t.Fatal(err)
	}
}
