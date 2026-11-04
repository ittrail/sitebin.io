package httpapi

import (
	"testing"

	"github.com/ittrail/sitebin.io/internal/ext"
	"github.com/ittrail/sitebin.io/internal/store"
)

func analyticsSite(quota *bool, settings *store.Analytics) *store.Site {
	return &store.Site{Meta: store.Meta{QuotaAnalytics: quota, Analytics: settings}}
}

func TestAnalyticsPlanAllowsCommunity(t *testing.T) {
	e := newEnv(t, nil)
	yes, no := true, false
	if !e.api.analyticsPlanAllows(analyticsSite(nil, nil)) {
		t.Error("nil stamp without accounts must allow")
	}
	if !e.api.analyticsPlanAllows(analyticsSite(&yes, nil)) {
		t.Error("true stamp must allow")
	}
	if e.api.analyticsPlanAllows(analyticsSite(&no, nil)) {
		t.Error("false stamp must not allow")
	}
}

func TestAnalyticsPlanAllowsAccounts(t *testing.T) {
	e := newEnv(t, nil)
	ext.Register(&fakeProvider{enabled: true})
	defer ext.Reset()
	yes, no := true, false
	if e.api.analyticsPlanAllows(analyticsSite(nil, nil)) {
		t.Error("nil stamp with accounts counts as off")
	}
	if e.api.analyticsPlanAllows(analyticsSite(&no, nil)) {
		t.Error("false stamp must not allow")
	}
	if !e.api.analyticsPlanAllows(analyticsSite(&yes, nil)) {
		t.Error("true stamp must allow")
	}
}

func TestAnalyticsInEffectNeedsEnabled(t *testing.T) {
	e := newEnv(t, nil)
	for name, tc := range map[string]struct {
		s    *store.Analytics
		want bool
	}{
		"no settings": {nil, false},
		"disabled":    {&store.Analytics{}, false},
		"enabled":     {&store.Analytics{Enabled: true}, true},
	} {
		if got := e.api.analyticsInEffect(analyticsSite(nil, tc.s)); got != tc.want {
			t.Errorf("%s: in effect = %v, want %v", name, got, tc.want)
		}
	}
	no := false
	if e.api.analyticsInEffect(analyticsSite(&no, &store.Analytics{Enabled: true})) {
		t.Error("enabled setting must not override the plan")
	}
	if got := e.api.analyticsReason(analyticsSite(nil, &store.Analytics{})); got != "" {
		t.Errorf("reason = %q for a site that merely has analytics off, want empty", got)
	}
}

func TestAnalyticsReason(t *testing.T) {
	e := newEnv(t, nil)
	no := false
	locked := analyticsSite(nil, &store.Analytics{Enabled: true})
	locked.Meta.Locked = &store.SiteLock{}
	if got := e.api.analyticsReason(locked); got != analyticsReasonLocked {
		t.Errorf("locked: reason = %q", got)
	}
	if e.api.analyticsInEffect(locked) {
		t.Error("a locked site collects nothing")
	}
	if got := e.api.analyticsReason(analyticsSite(&no, nil)); got != analyticsReasonPlan {
		t.Errorf("plan: reason = %q", got)
	}
	off := newEnv(t, map[string]string{"SITEBIN_ANALYTICS": "false"})
	if got := off.api.analyticsReason(locked); got != analyticsReasonInstance {
		t.Errorf("instance switch: reason = %q, want it to win over locked", got)
	}
}
