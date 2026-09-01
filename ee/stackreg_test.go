//go:build ee

package ee

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/ittrail/sitebin.io/internal/ext"
)

// The catalogue the hosted instance actually ships: amounts, no provider price
// ids, one featured plan, cheapest first.
const stackTiersJSON = `[
  {"id":"drop","label":"Drop","max_site_bytes":26214400,"max_files":200,"max_expiry_days":1},
  {"id":"free","label":"Free","max_site_bytes":104857600,"max_files":500,"max_sites":10,"webdav":true,"max_expiry_days":7},
  {"id":"pro","label":"Pro","max_sites":100,"webdav":true,"custom_domains":5,"featured":true,
   "price":{"monthly":"6.00","annual":"60.00","currency":"EUR"}},
  {"id":"studio","label":"Studio","max_sites":500,"webdav":true,"custom_domains":25,
   "price":{"monthly":"19.00","annual":"190.00","currency":"EUR"}}
]`

func stackProvider(t *testing.T, licensing string) *provider {
	t.Helper()
	t.Setenv("SITEBIN_ACCOUNT_MODE", "tiers")
	t.Setenv("SITEBIN_TIERS", stackTiersJSON)
	t.Setenv("SITEBIN_DEFAULT_TIER", "free")
	t.Setenv("SITEBIN_STACK_URL", "https://platform.example")
	t.Setenv("SITEBIN_STACK_APP_ID", "sitebin")
	t.Setenv("SITEBIN_STACK_ADMIN_KEY", "padm_test")
	if licensing != "" {
		t.Setenv("SITEBIN_STACK_LICENSING", licensing)
	}
	p := newProvider()
	if err := p.Init(&fakeHost{dir: t.TempDir(), sites: &fakeSites{infos: map[string]ext.SiteInfo{}}}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	return p
}

// Tier order on the stack's hosted plan page is the order of this array — the
// stack derives the sort from it and has no sortOrder field — and `featured`
// says which plan the page leads with. Declaring neither left both accidental.
func TestStackDeclarationCarriesOrderAndEmphasis(t *testing.T) {
	reg := stackProvider(t, "").stackDeclaration("sitebin")

	var order []string
	featured := map[string]bool{}
	for _, st := range reg.Billing.Tiers {
		order = append(order, st.Key)
		featured[st.Key] = st.Featured
	}
	want := []string{"drop", "free", "pro", "studio"}
	if len(order) != len(want) {
		t.Fatalf("declared %v, want %v", order, want)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("declared order %v, want the catalogue's own order %v", order, want)
		}
	}
	if !featured["pro"] {
		t.Error("pro is the featured plan and must be declared as one")
	}
	for _, k := range []string{"drop", "free", "studio"} {
		if featured[k] {
			t.Errorf("%q was not declared featured; the declaration must not invent emphasis", k)
		}
	}
}

// A priced tier must carry its amount, and a free one must carry none: a tier
// with no amount is how PayGate is told to create no payment product.
func TestStackDeclarationCarriesAmounts(t *testing.T) {
	reg := stackProvider(t, "").stackDeclaration("sitebin")
	byKey := map[string]stackTier{}
	for _, st := range reg.Billing.Tiers {
		byKey[st.Key] = st
	}
	if pro := byKey["pro"]; pro.MonthlyPrice != "6.00" || pro.AnnualPrice != "60.00" || pro.Currency != "EUR" {
		t.Errorf("pro declared %+v", pro)
	}
	if free := byKey["free"]; free.MonthlyPrice != "" || free.Currency != "" {
		t.Errorf("a free tier must declare no amount, got %+v", free)
	}
}

// Absent entitlements mean UNLIMITED, so an instance that declares no licensing
// block sells a custom-domain-capped licence tier that caps nothing.
func TestStackDeclarationCarriesLicensing(t *testing.T) {
	t.Run("declared when configured", func(t *testing.T) {
		reg := stackProvider(t, `{"graceMonths":3,"plans":{"team":{"max_custom_domains":25},"platform":{}}}`).
			stackDeclaration("sitebin")
		if reg.Licensing == nil {
			t.Fatal("configured licensing was not declared")
		}
		if reg.Licensing.GraceMonths != 3 {
			t.Errorf("graceMonths = %d", reg.Licensing.GraceMonths)
		}
		if got := reg.Licensing.Plans["team"]["max_custom_domains"]; got != 25 {
			t.Errorf("team max_custom_domains = %d, want 25", got)
		}

		// The wire shape is the stack's, verbatim.
		b, err := json.Marshal(reg)
		if err != nil {
			t.Fatal(err)
		}
		var wire struct {
			Licensing *struct {
				GraceMonths int                       `json:"graceMonths"`
				Plans       map[string]map[string]int `json:"plans"`
			} `json:"licensing"`
		}
		if err := json.Unmarshal(b, &wire); err != nil {
			t.Fatal(err)
		}
		if wire.Licensing == nil || wire.Licensing.GraceMonths != 3 ||
			wire.Licensing.Plans["team"]["max_custom_domains"] != 25 {
			t.Fatalf("licensing did not survive the wire: %s", b)
		}
		if _, ok := wire.Licensing.Plans["platform"]; !ok {
			t.Error("a plan with no entitlements is unlimited, not absent; it must still be sent")
		}
	})

	// Registration is convergent and the stack MERGES: a block that is absent
	// keeps what the app already has, an empty one would erase it.
	t.Run("omitted entirely when unconfigured", func(t *testing.T) {
		reg := stackProvider(t, "").stackDeclaration("sitebin")
		if reg.Licensing != nil {
			t.Fatal("an unconfigured licensing block must not be declared")
		}
		b, err := json.Marshal(reg)
		if err != nil {
			t.Fatal(err)
		}
		var wire map[string]json.RawMessage
		if err := json.Unmarshal(b, &wire); err != nil {
			t.Fatal(err)
		}
		if _, present := wire["licensing"]; present {
			t.Errorf("licensing must be absent from the payload, not empty: %s", b)
		}
	})
}

// The defect this proves against: the dashboard printed Price.Display, which a
// PayGate catalogue does not carry, so every paid plan was offered at a blank
// price. Self-select is on so the plan cards render without a live backend.
func TestDashboardShowsThePriceOfAPayGateTier(t *testing.T) {
	t.Setenv("SITEBIN_ACCOUNT_MODE", "tiers")
	t.Setenv("SITEBIN_TIERS", stackTiersJSON)
	t.Setenv("SITEBIN_DEFAULT_TIER", "free")
	t.Setenv("SITEBIN_TIER_SELF_SELECT", "true")
	p := newProvider()
	if err := p.Init(&fakeHost{dir: t.TempDir(), sites: &fakeSites{infos: map[string]ext.SiteInfo{}}}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	mux := http.NewServeMux()
	for pat, h := range p.PublicRoutes() {
		mux.Handle(pat, h)
	}
	cookie, _ := adminUser(t, p, mux, "buyer@example.com", "free")

	body := getAs(mux, "/account", cookie).Body.String()
	for _, want := range []string{"€6.00/mo", "€19.00/mo"} {
		if !strings.Contains(body, want) {
			t.Errorf("the dashboard does not show %q; a paid plan must never be offered at a blank price", want)
		}
	}
}

// The stack gates sign-in on two documents — the platform's and the app's —
// and an app that declares no `terms` block contributes nothing to the second.
// Sitebin renders no terms screen of its own, so this declaration is the only
// place its terms of service exist as far as the sign-in is concerned.
func TestStackDeclarationCarriesTerms(t *testing.T) {
	t.Run("declared when configured", func(t *testing.T) {
		t.Setenv("SITEBIN_STACK_TERMS",
			`{"version":"2026-09-01","url":"https://sitebin.io/terms","title":{"en":"Sitebin Terms of Service","de":"Nutzungsbedingungen"}}`)
		reg := stackProvider(t, "").stackDeclaration("sitebin")
		if reg.Terms == nil {
			t.Fatal("configured terms were not declared")
		}
		if reg.Terms.Version != "2026-09-01" || reg.Terms.URL != "https://sitebin.io/terms" {
			t.Errorf("terms = %+v", reg.Terms)
		}

		// The wire shape is the stack's, verbatim.
		b, err := json.Marshal(reg)
		if err != nil {
			t.Fatal(err)
		}
		var wire struct {
			Terms *struct {
				Version string            `json:"version"`
				URL     string            `json:"url"`
				Title   map[string]string `json:"title"`
			} `json:"terms"`
		}
		if err := json.Unmarshal(b, &wire); err != nil {
			t.Fatal(err)
		}
		if wire.Terms == nil || wire.Terms.Version != "2026-09-01" ||
			wire.Terms.URL != "https://sitebin.io/terms" ||
			wire.Terms.Title["de"] != "Nutzungsbedingungen" {
			t.Fatalf("terms did not survive the wire: %s", b)
		}
	})

	// Same rule as licensing: the stack MERGES, so an absent block keeps what
	// the app already declared and an empty one would replace real terms with
	// a version and a URL of nothing.
	t.Run("omitted entirely when unconfigured", func(t *testing.T) {
		reg := stackProvider(t, "").stackDeclaration("sitebin")
		if reg.Terms != nil {
			t.Fatal("unconfigured terms must not be declared")
		}
		b, err := json.Marshal(reg)
		if err != nil {
			t.Fatal(err)
		}
		var wire map[string]json.RawMessage
		if err := json.Unmarshal(b, &wire); err != nil {
			t.Fatal(err)
		}
		if _, present := wire["terms"]; present {
			t.Errorf("terms must be absent from the payload, not empty: %s", b)
		}
	})
}
