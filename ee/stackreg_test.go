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
	t.Setenv("SITEBIN_STACK_GDPR_SECRET", testGDPRSecret)
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

const testGDPRSecret = "gdpr-e2e-secret-0123456789abcdef0123456789"

// The stack gates sign-in on the platform's document and then on each of the
// app's own, and an app that declares no `consents` contributes nothing.
// Sitebin renders no consent screen of its own, so this declaration is the
// only place its terms and its DPA exist as far as the sign-in is concerned.
func TestStackDeclarationCarriesConsents(t *testing.T) {
	t.Run("declared in order when configured", func(t *testing.T) {
		t.Setenv("SITEBIN_STACK_CONSENTS", `[
		  {"key":"terms","version":"2026-09-08","url":"https://sitebin.io/terms/","title":{"en":"Sitebin Terms of Service","de":"Sitebin Nutzungsbedingungen"}},
		  {"key":"dpa","version":"2026-09-08","url":"https://sitebin.io/dpa/","title":{"en":"Data Processing Agreement","de":"Auftragsverarbeitungsvertrag"}}
		]`)
		reg := stackProvider(t, "").stackDeclaration("sitebin")
		if len(reg.Consents) != 2 {
			t.Fatalf("consents = %+v", reg.Consents)
		}

		// The wire shape is the stack's `consents` block, verbatim — and
		// NEVER its `terms` shorthand: a payload carrying both is a 400, and
		// the shorthand has room for one document.
		b, err := json.Marshal(reg)
		if err != nil {
			t.Fatal(err)
		}
		var wire struct {
			Consents []struct {
				Key      string            `json:"key"`
				Version  string            `json:"version"`
				URL      string            `json:"url"`
				Title    map[string]string `json:"title"`
				Required *bool             `json:"required"`
			} `json:"consents"`
			Terms json.RawMessage `json:"terms"`
		}
		if err := json.Unmarshal(b, &wire); err != nil {
			t.Fatal(err)
		}
		if wire.Terms != nil {
			t.Fatalf("the payload must never carry `terms` beside `consents`: %s", b)
		}
		if len(wire.Consents) != 2 || wire.Consents[0].Key != "terms" || wire.Consents[1].Key != "dpa" {
			t.Fatalf("consents did not survive the wire in order: %s", b)
		}
		if wire.Consents[0].Version != "2026-09-08" || wire.Consents[0].URL != "https://sitebin.io/terms/" ||
			wire.Consents[0].Title["de"] != "Sitebin Nutzungsbedingungen" {
			t.Errorf("terms = %+v", wire.Consents[0])
		}
		if wire.Consents[1].URL != "https://sitebin.io/dpa/" || wire.Consents[1].Title["en"] != "Data Processing Agreement" {
			t.Errorf("dpa = %+v", wire.Consents[1])
		}
		// `required` unstated is `required` unsent: the default is the stack's.
		if wire.Consents[0].Required != nil || wire.Consents[1].Required != nil {
			t.Errorf("an unstated required must be absent from the wire: %s", b)
		}
	})

	// Same rule as licensing: the stack MERGES, so an absent list keeps what
	// the app already declared, and an empty one would tell the stack this
	// app asks for nothing.
	t.Run("omitted entirely when unconfigured", func(t *testing.T) {
		reg := stackProvider(t, "").stackDeclaration("sitebin")
		if reg.Consents != nil {
			t.Fatal("unconfigured consents must not be declared")
		}
		b, err := json.Marshal(reg)
		if err != nil {
			t.Fatal(err)
		}
		var wire map[string]json.RawMessage
		if err := json.Unmarshal(b, &wire); err != nil {
			t.Fatal(err)
		}
		for _, k := range []string{"consents", "terms"} {
			if _, present := wire[k]; present {
				t.Errorf("%s must be absent from the payload, not empty: %s", k, b)
			}
		}
	})
}

// The stack can only order a deletion or an export where it has been told to
// send it and what to sign it with. The URLs come from the SAME base URL as
// the OIDC callback, so the declared endpoint is the served one.
func TestStackDeclarationCarriesGDPR(t *testing.T) {
	t.Run("declared with the secret", func(t *testing.T) {
		reg := stackProvider(t, "").stackDeclaration("sitebin")
		if reg.GDPR == nil {
			t.Fatal("the gdpr block was not declared")
		}
		if reg.GDPR.DeleteUserURL != "http://sitebin.example/account/gdpr/delete" ||
			reg.GDPR.ExportUserDataURL != "http://sitebin.example/account/gdpr/export" ||
			reg.GDPR.WebhookSecret != testGDPRSecret {
			t.Errorf("gdpr = %+v", reg.GDPR)
		}
		b, err := json.Marshal(reg)
		if err != nil {
			t.Fatal(err)
		}
		var wire struct {
			GDPR *struct {
				Delete string `json:"deleteUserUrl"`
				Export string `json:"exportUserDataUrl"`
				Secret string `json:"webhookSecret"`
			} `json:"gdpr"`
		}
		if err := json.Unmarshal(b, &wire); err != nil {
			t.Fatal(err)
		}
		if wire.GDPR == nil || wire.GDPR.Delete == "" || wire.GDPR.Export == "" || wire.GDPR.Secret != testGDPRSecret {
			t.Fatalf("gdpr did not survive the wire in the stack's field names: %s", b)
		}
	})

	// No secret, no block — and no routes either (see gdpr_test.go). An
	// instance the stack could call but that could verify nothing must not
	// advertise itself.
	t.Run("omitted without a secret", func(t *testing.T) {
		t.Setenv("SITEBIN_ACCOUNT_MODE", "tiers")
		t.Setenv("SITEBIN_TIERS", stackTiersJSON)
		t.Setenv("SITEBIN_DEFAULT_TIER", "free")
		p := newProvider()
		if err := p.Init(&fakeHost{dir: t.TempDir(), sites: &fakeSites{infos: map[string]ext.SiteInfo{}}}); err != nil {
			t.Fatalf("Init: %v", err)
		}
		reg := p.stackDeclaration("sitebin")
		if reg.GDPR != nil {
			t.Fatal("a gdpr block with no secret must not be declared")
		}
		b, _ := json.Marshal(reg)
		var wire map[string]json.RawMessage
		json.Unmarshal(b, &wire)
		if _, present := wire["gdpr"]; present {
			t.Errorf("gdpr must be absent from the payload: %s", b)
		}
	})
}

// The stack's hosted surfaces -- the consent gate, the account console and
// the plan page -- paint themselves from the app's declared theme. An
// instance that declares none is rendered in the stack's stock grey, which
// a customer reads as somebody else's product asking for their card.
// The account console draws "Back to Sitebin" only when referrer_uri is an
// address the client registered, so the dashboard's address is declared
// beside the callback.
func TestStackDeclarationRegistersTheDashboardAsAReturnAddress(t *testing.T) {
	p := stackProvider(t, "")
	reg := p.stackDeclaration("sitebin")
	want := p.baseURL() + "/account"
	found := false
	for _, u := range reg.Auth.RedirectURIs {
		if u == want {
			found = true
		}
	}
	if !found {
		t.Errorf("redirect URIs %v do not include the dashboard %s, so the console cannot link back", reg.Auth.RedirectURIs, want)
	}
}

func TestStackDeclarationCarriesSitebinsOwnBrand(t *testing.T) {
	p := stackProvider(t, "")
	reg := p.stackDeclaration("sitebin")
	if reg.Theme == nil {
		t.Fatal("the declaration carries no theme; the plan page would not be branded as Sitebin")
	}
	if reg.Theme.DisplayName != "Sitebin" {
		t.Errorf("displayName = %q, want Sitebin", reg.Theme.DisplayName)
	}
	if reg.Theme.PrimaryColor != "#f5b84d" || reg.Theme.BackgroundColor != "#0a0e18" {
		t.Errorf("colours = %q on %q, want the amber claim-ticket accent #f5b84d on #0a0e18",
			reg.Theme.PrimaryColor, reg.Theme.BackgroundColor)
	}
	wantIcon := p.baseURL() + "/_sitebin/assets/static/favicon.svg"
	if reg.Theme.FaviconURL != wantIcon {
		t.Errorf("faviconUrl = %q, want %q (the icon the instance itself serves)", reg.Theme.FaviconURL, wantIcon)
	}
	body, _ := json.Marshal(reg)
	if !strings.Contains(string(body), `"theme":{`) {
		t.Errorf("the theme is not serialised into the registration: %s", body)
	}
}
