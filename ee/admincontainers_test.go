//go:build ee

package ee

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/ittrail/sitebin.io/ee/containers"
	"github.com/ittrail/sitebin.io/internal/ext"
	"github.com/ittrail/sitebin.io/internal/store"
)

const (
	shopSite = "ssssssssssssssssssssssssss"
	blogSite = "bbbbbbbbbbbbbbbbbbbbbbbbbb"
)

var envDefaults = ext.ContainerLimits{MemoryMB: 512, CPUs: 0.5, Pids: 256}

// setupContainersTab is the console with container mode on: an admin, a
// customer owning two container sites, and the instance's limit book.
func setupContainersTab(t *testing.T) (p *provider, host *fakeHost, mux http.Handler, admin *http.Cookie, adminID string) {
	t.Helper()
	p, host, mux = setupAdmin(t, "boss@example.com")
	admin, adminID = adminUser(t, p, mux, "boss@example.com", "admin")
	_, customer := adminUser(t, p, mux, "shop@example.com", "free")
	p.containerLimits = containers.NewLimitBook(t.TempDir(), envDefaults)
	p.containerLimits.SetCapacity(6, 16<<30)
	host.sites.containers = []ext.ContainerSite{
		{
			ViewID: shopSite, Owner: customer, Name: "Shop", Container: true, Enabled: true,
			Limits: map[string]ext.ContainerLimits{"db": {MemoryMB: 1024}},
			Observed: ext.ContainerState{Status: store.ContainerRunning, Services: []ext.ContainerService{
				{Name: "app", Image: "alpine-node-22", State: "running", Limits: envDefaults},
				{Name: "db", Image: "mysql-8.4", State: "running", Limits: envDefaults},
			}},
		},
		{
			ViewID: blogSite, Owner: customer, Container: true,
			Limits: map[string]ext.ContainerLimits{"old": {Pids: 64}},
			Observed: ext.ContainerState{Status: store.ContainerStopped, Services: []ext.ContainerService{
				{Name: "web", Image: "alpine-node-22"},
			}},
		},
	}
	return p, host, mux, admin, adminID
}

func TestContainersTabGate(t *testing.T) {
	p, _, mux := setupAdmin(t, "boss@example.com")
	boss, _ := adminUser(t, p, mux, "boss@example.com", "admin")
	nobody, _ := adminUser(t, p, mux, "nobody@example.com", "free")

	// Container mode off: there is no such page, and no tab to it.
	if w := getAs(mux, "/account/admin/containers", boss); w.Code != 404 {
		t.Errorf("containers off: %d", w.Code)
	}
	if strings.Contains(getAs(mux, "/account/admin", boss).Body.String(), `href="/account/admin/containers"`) {
		t.Error("the register links a tab that does not exist")
	}

	p.containerLimits = containers.NewLimitBook(t.TempDir(), envDefaults)
	if w := getAs(mux, "/account/admin/containers", boss); w.Code != 200 {
		t.Errorf("admin: %d", w.Code)
	}
	for _, c := range []*http.Cookie{nobody, nil} {
		if w := getAs(mux, "/account/admin/containers", c); w.Code != 404 {
			t.Errorf("not an admin (%v): %d", c != nil, w.Code)
		}
	}
	for _, page := range []string{"/account/admin", "/account/admin/reports"} {
		if !strings.Contains(getAs(mux, page, boss).Body.String(), `href="/account/admin/containers"`) {
			t.Errorf("%s has no Containers tab", page)
		}
	}
	if w := postAs(mux, "/account/admin/containers/defaults", nobody, url.Values{"csrf": {"x"}, "cpus": {"1"}}); w.Code != 404 {
		t.Errorf("a non-admin POST: %d", w.Code)
	}
}

func TestContainersTabShowsEveryService(t *testing.T) {
	_, _, mux, admin, _ := setupContainersTab(t)
	body := getAs(mux, "/account/admin/containers", admin).Body.String()
	for _, want := range []string{
		"Shop", shopSite, blogSite, "shop@example.com",
		">app<", ">db<", ">web<", "mysql-8.4",
		"1024 MB", // db's own memory
		"512 MB",  // everyone else's, the default
		"6 CPUs",  // the host
		"updating", // db runs with 512 MB until the runtime changes it
		">old<", "not in the project",
		`placeholder="512"`, // the defaults form shows the environment's
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the tab does not show %q", want)
		}
	}
}

func TestParseLimitsForm(t *testing.T) {
	parse := func(v url.Values) (ext.ContainerLimits, error) {
		r := httptest.NewRequest("POST", "/", strings.NewReader(v.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		return parseLimitsForm(r)
	}
	if l, err := parse(url.Values{"memory_mb": {""}, "cpus": {" "}, "pids": {""}}); err != nil || l != (ext.ContainerLimits{}) {
		t.Errorf("empty = %+v, %v", l, err)
	}
	if l, err := parse(url.Values{"memory_mb": {"1024"}, "cpus": {"0,5"}, "pids": {"512"}}); err != nil || l != (ext.ContainerLimits{MemoryMB: 1024, CPUs: 0.5, Pids: 512}) {
		t.Errorf("full = %+v, %v", l, err)
	}
	if l, _ := parse(url.Values{"cpus": {"0.333"}}); l.CPUs != 0.33 {
		t.Errorf("cpus rounded = %v", l.CPUs)
	}
	for _, bad := range []url.Values{
		{"memory_mb": {"0"}}, {"memory_mb": {"-1"}}, {"memory_mb": {"1.5"}}, {"memory_mb": {"abc"}},
		{"cpus": {"0"}}, {"cpus": {"0.001"}}, {"cpus": {"NaN"}}, {"cpus": {"Inf"}}, {"cpus": {"x"}},
		{"pids": {"0"}}, {"pids": {"-5"}},
	} {
		if l, err := parse(bad); err == nil {
			t.Errorf("%v accepted as %+v", bad, l)
		}
	}
}

func TestSaveContainerDefaults(t *testing.T) {
	p, _, mux, admin, adminID := setupContainersTab(t)
	acc, _ := p.accounts.ByID(adminID)
	csrf := p.csrf(acc)

	if w := postAs(mux, "/account/admin/containers/defaults", admin, url.Values{"cpus": {"1"}}); w.Code != http.StatusForbidden {
		t.Errorf("no CSRF: %d", w.Code)
	}
	w := postAs(mux, "/account/admin/containers/defaults", admin, url.Values{"csrf": {csrf}, "cpus": {"99"}})
	if w.Code != 400 || !strings.Contains(w.Body.String(), "host") || p.containerLimits.Saved() != (ext.ContainerLimits{}) {
		t.Errorf("99 CPUs on a 6 CPU host: %d %q, saved %+v", w.Code, w.Body, p.containerLimits.Saved())
	}
	w = postAs(mux, "/account/admin/containers/defaults", admin, url.Values{"csrf": {csrf}, "memory_mb": {"1024"}, "cpus": {"1"}, "pids": {""}})
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/account/admin/containers?flash=defaults" {
		t.Fatalf("save: %d %q", w.Code, w.Header().Get("Location"))
	}
	if got := p.containerLimits.Saved(); got != (ext.ContainerLimits{MemoryMB: 1024, CPUs: 1}) {
		t.Errorf("saved = %+v", got)
	}
	if body := getAs(mux, "/account/admin/containers?flash=defaults", admin).Body.String(); !strings.Contains(body, `class="flash"`) {
		t.Error("no confirmation after saving")
	}
	w = postAs(mux, "/account/admin/containers/defaults", admin, url.Values{"csrf": {csrf}, "reset": {"1"}, "memory_mb": {"2048"}})
	if w.Code != http.StatusSeeOther || p.containerLimits.Saved() != (ext.ContainerLimits{}) {
		t.Errorf("reset: %d, saved %+v", w.Code, p.containerLimits.Saved())
	}
}

func TestSaveContainerLimits(t *testing.T) {
	p, host, mux, admin, adminID := setupContainersTab(t)
	acc, _ := p.accounts.ByID(adminID)
	csrf := p.csrf(acc)
	path := "/account/admin/containers/sites/" + shopSite + "/limits"
	limitsOf := func(id string) map[string]ext.ContainerLimits {
		cs, _ := host.sites.ContainerSite(id)
		return cs.Limits
	}

	if w := postAs(mux, path, admin, url.Values{"service": {"app"}, "cpus": {"1"}}); w.Code != http.StatusForbidden {
		t.Errorf("no CSRF: %d", w.Code)
	}
	w := postAs(mux, path, admin, url.Values{"csrf": {csrf}, "service": {"app"}, "cpus": {"1"}, "pids": {"512"}})
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/account/admin/containers?flash=limits" {
		t.Fatalf("save: %d %q", w.Code, w.Body)
	}
	got := limitsOf(shopSite)
	if len(got) != 2 || got["app"] != (ext.ContainerLimits{CPUs: 1, Pids: 512}) || got["db"] != (ext.ContainerLimits{MemoryMB: 1024}) {
		t.Errorf("limits = %+v (db's own must be kept)", got)
	}

	// Every field empty is "inherit everything": the entry goes.
	postAs(mux, path, admin, url.Values{"csrf": {csrf}, "service": {"db"}, "memory_mb": {""}})
	if got := limitsOf(shopSite); len(got) != 1 || got["app"].CPUs != 1 {
		t.Errorf("after emptying db: %+v", got)
	}
	postAs(mux, path, admin, url.Values{"csrf": {csrf}, "service": {"app"}, "clear": {"1"}, "cpus": {"2"}})
	if got := limitsOf(shopSite); len(got) != 0 {
		t.Errorf("after clearing app: %+v", got)
	}

	// A value out of bounds changes nothing.
	if w := postAs(mux, path, admin, url.Values{"csrf": {csrf}, "service": {"app"}, "memory_mb": {"10"}}); w.Code != 400 || len(limitsOf(shopSite)) != 0 {
		t.Errorf("10 MB: %d, %+v", w.Code, limitsOf(shopSite))
	}
	// Only the project's services, or an entry to remove.
	if w := postAs(mux, path, admin, url.Values{"csrf": {csrf}, "service": {"nope"}, "cpus": {"1"}}); w.Code != 400 {
		t.Errorf("unknown service: %d", w.Code)
	}
	blog := "/account/admin/containers/sites/" + blogSite + "/limits"
	if w := postAs(mux, blog, admin, url.Values{"csrf": {csrf}, "service": {"old"}, "clear": {"1"}}); w.Code != http.StatusSeeOther || len(limitsOf(blogSite)) != 0 {
		t.Errorf("removing a stale entry: %d, %+v", w.Code, limitsOf(blogSite))
	}
	if w := postAs(mux, "/account/admin/containers/sites/zzzzzzzzzzzzzzzzzzzzzzzzzz/limits", admin, url.Values{"csrf": {csrf}, "service": {"app"}}); w.Code != 404 {
		t.Errorf("unknown site: %d", w.Code)
	}
}
