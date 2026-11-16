//go:build ee

package ee

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/ittrail/sitebin.io/internal/ext"
)

// An operator gives a plan away through a tier with no price (the stack
// refuses to put anyone on a PRICED tier without a subscription to change),
// and such a gift must not be on offer to everybody else: not on the stack's
// price list, not among the plans Sitebin's own pages show, and not
// self-selectable. `"listed": false` says so in tiers.json.
const listedTiersJSON = `[
  {"id":"free","label":"Free","max_site_bytes":104857600,"max_files":500,"max_sites":10,"webdav":true,"max_expiry_days":7},
  {"id":"pro","label":"Pro","max_sites":100,"webdav":true,"custom_domains":5,"featured":true,
   "price":{"monthly":"6.00","annual":"60.00","currency":"EUR"}},
  {"id":"pro-gift","label":"Pro (gift)","max_sites":100,"webdav":true,"custom_domains":5,"listed":false},
  {"id":"studio","label":"Studio","max_sites":500,"webdav":true,"custom_domains":25,
   "price":{"monthly":"19.00","annual":"190.00","currency":"EUR"}}
]`

// The stack keeps a tier off its public price list and its widgets only when
// the declaration says `"listed": false`; it stores `listed !== false` on
// every registration, so leaving the field out lists the tier. Every other
// tier must not carry the field at all, so a listed tier's declaration is
// byte for byte what it was.
func TestStackDeclarationKeepsAnUnlistedTierOffThePriceList(t *testing.T) {
	reg := stackProviderTiers(t, listedTiersJSON, "").stackDeclaration("sitebin")
	raw, err := json.Marshal(reg.Billing.Tiers)
	if err != nil {
		t.Fatal(err)
	}
	var declared []map[string]any
	if err := json.Unmarshal(raw, &declared); err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, d := range declared {
		listed, has := d["listed"]
		if d["key"] == "pro-gift" {
			seen++
			if !has || listed != false {
				t.Errorf("pro-gift declared listed=%v (present %v), want false", listed, has)
			}
			continue
		}
		if has {
			t.Errorf("%v declared listed=%v; a listed tier must not carry the field", d["key"], listed)
		}
	}
	if seen != 1 {
		t.Fatalf("pro-gift declared %d times in %s", seen, raw)
	}
}

func listedProvider(t *testing.T) (*provider, *http.ServeMux) {
	t.Helper()
	t.Setenv("SITEBIN_ACCOUNT_MODE", "tiers")
	t.Setenv("SITEBIN_TIERS", listedTiersJSON)
	t.Setenv("SITEBIN_DEFAULT_TIER", "free")
	// Self-select renders the plan lists without a live billing backend, and
	// it is the one way a customer could pick a free tier on their own.
	t.Setenv("SITEBIN_TIER_SELF_SELECT", "true")
	p := newProvider()
	if err := initT(t, p, &fakeHost{dir: t.TempDir(), sites: &fakeSites{infos: map[string]ext.SiteInfo{}}}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	mux := http.NewServeMux()
	for pat, h := range p.PublicRoutes() {
		mux.Handle(pat, h)
	}
	return p, mux
}

func statePlanTiers(t *testing.T, mux http.Handler, cookie *http.Cookie) map[string]tierOption {
	t.Helper()
	w := getAs(mux, "/account/api/state", cookie)
	if w.Code != http.StatusOK {
		t.Fatalf("state = %d %s", w.Code, w.Body)
	}
	var st wsState
	if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	out := map[string]tierOption{}
	for _, o := range st.Plan.Tiers {
		out[o.ID] = o
	}
	return out
}

func TestPlanListsLeaveOutAnUnlistedTier(t *testing.T) {
	p, mux := listedProvider(t)
	cookie, _ := adminUser(t, p, mux, "someone@example.com", "free")

	body := getAs(mux, "/account", cookie).Body.String()
	if strings.Contains(body, "Pro (gift)") {
		t.Error("the dashboard offers the unlisted tier to an account that does not hold it")
	}
	if !strings.Contains(body, "Studio") {
		t.Fatal("the dashboard shows no plan list at all; the test proves nothing")
	}

	tiers := statePlanTiers(t, mux, cookie)
	if _, ok := tiers["pro-gift"]; ok {
		t.Error("the workspace state lists the unlisted tier for an account that does not hold it")
	}
	if _, ok := tiers["studio"]; !ok {
		t.Fatalf("the workspace state lists %v; the test proves nothing", tiers)
	}
}

// The friend who was given the plan still sees it, as the current one: an
// account must always be able to read which plan it is on.
func TestPlanListsShowAnUnlistedTierToItsHolder(t *testing.T) {
	p, mux := listedProvider(t)
	cookie, _ := adminUser(t, p, mux, "friend@example.com", "pro-gift")

	body := getAs(mux, "/account", cookie).Body.String()
	if !strings.Contains(body, "Pro (gift)") {
		t.Error("the dashboard hides the plan its own holder is on")
	}

	tiers := statePlanTiers(t, mux, cookie)
	if o, ok := tiers["pro-gift"]; !ok || !o.Current {
		t.Errorf("the workspace state shows the holder's own plan as %+v (present %v)", o, ok)
	}
}

// Self-select takes any tier without a price. An unlisted one is the
// operator's to give, so naming it in the form must not be enough.
func TestAnUnlistedTierCannotBeSelfSelected(t *testing.T) {
	p, mux := listedProvider(t)
	cookie, id := adminUser(t, p, mux, "taker@example.com", "free")
	acc, err := p.accounts.ByID(id)
	if err != nil {
		t.Fatal(err)
	}

	req := form(url.Values{"csrf": {p.csrf(acc)}, "tier": {"pro-gift"}})
	req.URL.Path = "/account/tier"
	req.AddCookie(cookie)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("self-select to an unlisted tier = %d, want 403", w.Code)
	}
	if acc, _ = p.accounts.ByID(id); acc.Tier != "free" {
		t.Errorf("the account moved to %q", acc.Tier)
	}
}
