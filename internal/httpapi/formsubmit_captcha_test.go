package httpapi

import (
	"net/url"
	"testing"

	"github.com/ittrail/sitebin.io/internal/store"
)

// TestSubmitCaptchaSpentSolutionStaysSpentAcrossOtherPaths is a review
// regression probe: a spent solution must stay spent however it is
// re-posted afterwards, not just on its own form's own replay path.
func TestSubmitCaptchaSpentSolutionStaysSpentAcrossOtherPaths(t *testing.T) {
	e, rs := formsEnv(t, nil)
	site, f := activeForm(t, e, store.Form{Captcha: true})
	field := solvedAltcha(t, e, site, f)
	if w := submit(t, e, viewHost(site), f.Key, "message=hi&altcha="+field, nil); w.Code != 303 || rs.count() != 1 {
		t.Fatalf("first = %d", w.Code)
	}
	// 1. Replayed twice: the 403 path must not release.
	for i := 0; i < 2; i++ {
		if w := submit(t, e, viewHost(site), f.Key, "message=hi&altcha="+field, nil); w.Code != 403 {
			t.Fatalf("replay %d = %d, want 403", i, w.Code)
		}
	}
	// 2. Posted empty to a captcha-OFF form elsewhere: 400, must not release.
	plain, g := activeForm(t, e, store.Form{})
	if w := submit(t, e, viewHost(plain), g.Key, "message=+&altcha="+field, nil); w.Code != 400 {
		t.Fatalf("empty on plain form = %d", w.Code)
	}
	// 3. Posted empty to ANOTHER captcha-on form: 403 (binding), must not release.
	other, h := activeForm(t, e, store.Form{Captcha: true})
	if w := submit(t, e, viewHost(other), h.Key, "message=+&altcha="+field, nil); w.Code != 403 {
		t.Fatalf("other captcha form = %d", w.Code)
	}
	raw, _ := url.QueryUnescape(field)
	if err := e.api.forms.captcha.Verify(raw, site.ViewID, f.Key); err == nil {
		t.Fatal("a spent solution became acceptable again")
	}
	if w := submit(t, e, viewHost(site), f.Key, "message=hi&altcha="+field, nil); w.Code != 403 || rs.count() != 1 {
		t.Fatalf("final replay = %d mails=%d, want 403 and 1", w.Code, rs.count())
	}
}
