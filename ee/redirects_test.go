//go:build ee

package ee

import (
	"strings"
	"testing"

	"github.com/ittrail/sitebin.io/internal/ext"
)

// The dashboard says which domain redirects where, and shows a generated
// address that does not serve the site without a link.
func TestDashboardShowsRedirectsAndTheGeneratedAddress(t *testing.T) {
	p, host, mux, acc, cookie := namedAccount(t)
	if err := p.accounts.LinkSite(acc, ownedSite); err != nil {
		t.Fatal(err)
	}
	viewURL := "http://" + ownedSite + ".sitebin.example"
	host.sites.infos[ownedSite] = ext.SiteInfo{
		ViewID: ownedSite, Mode: "webserver", ViewURL: viewURL, EditURL: "http://sitebin.example/e/one",
		ViewOff: true, ViewRedirectTo: "example.at",
		Domains: []string{"example.at", "example.de"},
		DomainLinks: []ext.DomainLink{
			{Domain: "example.at", URL: "https://example.at"},
			{Domain: "example.de", URL: "https://example.de", RedirectTo: "example.at"},
		},
	}
	body := getAs(mux, "/account", cookie).Body.String()
	if strings.Contains(body, `href="`+viewURL+`"`) {
		t.Error("a generated address that does not serve the site is still a link")
	}
	if !strings.Contains(body, viewURL+" · &rarr; example.at") {
		t.Error("the dashboard does not say where the generated address redirects")
	}
	if !strings.Contains(body, `title="Redirects to example.at"`) || !strings.Contains(body, "example.de</a> &rarr; example.at") {
		t.Error("the dashboard does not show the redirecting domain's target")
	}

	info := host.sites.infos[ownedSite]
	info.ViewRedirectTo = ""
	host.sites.infos[ownedSite] = info
	if body := getAs(mux, "/account", cookie).Body.String(); !strings.Contains(body, viewURL+" · off") {
		t.Error("the dashboard does not mark the generated address off")
	}
}
