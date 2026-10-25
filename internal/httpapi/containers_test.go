package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/ittrail/sitebin.io/internal/ext"
	"github.com/ittrail/sitebin.io/internal/store"
)

// fakeRuntime records what the core asked of the container runtime.
type fakeRuntime struct {
	mu       sync.Mutex
	allowErr error
	stopErr  error
	kicked   []string
	stopped  []string
	logs     string
	usage    map[string]ext.ContainerUsage
}

func (r *fakeRuntime) Allowed(string) error { return r.allowErr }
func (r *fakeRuntime) Kick(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.kicked = append(r.kicked, id)
}
func (r *fakeRuntime) Stop(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stopped = append(r.stopped, id)
	return r.stopErr
}
func (r *fakeRuntime) Usage(viewID string) (ext.ContainerUsage, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	u, ok := r.usage[viewID]
	return u, ok
}
func (r *fakeRuntime) Logs(_ context.Context, _, service string, _ int) (string, error) {
	if service != "app" {
		return "", errors.New("no service " + service)
	}
	return r.logs, nil
}

// containerProvider is the fake provider plus a container runtime.
type containerProvider struct {
	*fakeProvider
	rt *fakeRuntime
}

func (c containerProvider) Containers() ext.ContainerRuntime {
	if c.rt == nil {
		return nil
	}
	return c.rt
}

func withContainers(t *testing.T, rt *fakeRuntime) {
	t.Helper()
	ext.Register(containerProvider{fakeProvider: &fakeProvider{enabled: true, owner: "acct-1", domainsOK: true}, rt: rt})
	t.Cleanup(ext.Reset)
}

func (e *env) putJSON(t *testing.T, editID, pw string, body any) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(body)
	req := httptest.NewRequest("PUT", "/api/sites/"+editID, bytes.NewReader(b))
	req.Header.Set("X-Edit-Password", pw)
	req.Header.Set("Content-Type", "application/json")
	return e.public(t, req)
}

func (e *env) call(t *testing.T, method, path, pw string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var rd *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, rd)
	req.Header.Set("X-Edit-Password", pw)
	return e.public(t, req)
}

func TestContainerModeRefusedInCommunityBuild(t *testing.T) {
	e := newEnv(t, nil)
	c := e.createSite(t, nil, map[string]string{"index.html": "x"})
	w := e.putJSON(t, editIDFrom(t, c.EditURL), c.EditPassword, map[string]any{"mode": "container"})
	if w.Code != 403 || !strings.Contains(w.Body.String(), "Enterprise") {
		t.Fatalf("community: %d %s", w.Code, w.Body)
	}
	w = e.call(t, "POST", "/api/sites/"+editIDFrom(t, c.EditURL)+"/containers/start", c.EditPassword, nil)
	if w.Code != 403 {
		t.Errorf("community start: %d", w.Code)
	}
}

func TestContainerModeRefusedWithoutRuntime(t *testing.T) {
	ext.Register(containerProvider{fakeProvider: &fakeProvider{enabled: true, owner: "acct-1"}})
	defer ext.Reset()
	e := newEnv(t, nil)
	c := e.createSite(t, nil, nil)
	w := e.putJSON(t, editIDFrom(t, c.EditURL), c.EditPassword, map[string]any{"mode": "container"})
	if w.Code != 403 {
		t.Fatalf("runtime off: %d %s", w.Code, w.Body)
	}
}

func TestContainerModeRefusedForAnonymousAndByPlan(t *testing.T) {
	rt := &fakeRuntime{}
	ext.Register(containerProvider{fakeProvider: &fakeProvider{enabled: true}, rt: rt})
	e := newEnv(t, nil)
	c := e.createSite(t, nil, nil)
	w := e.putJSON(t, editIDFrom(t, c.EditURL), c.EditPassword, map[string]any{"mode": "container"})
	if w.Code != 403 || !strings.Contains(w.Body.String(), "account") {
		t.Fatalf("anonymous: %d %s", w.Code, w.Body)
	}
	ext.Reset()

	rt = &fakeRuntime{allowErr: errors.New("your Free plan has no containers")}
	withContainers(t, rt)
	c = e.createSite(t, nil, nil)
	w = e.putJSON(t, editIDFrom(t, c.EditURL), c.EditPassword, map[string]any{"mode": "container"})
	if w.Code != 403 || !strings.Contains(w.Body.String(), "Free plan") {
		t.Fatalf("plan: %d %s", w.Code, w.Body)
	}
	if len(rt.kicked) != 0 {
		t.Error("a refused site was kicked")
	}
}

func TestEnterAndLeaveContainerMode(t *testing.T) {
	rt := &fakeRuntime{}
	withContainers(t, rt)
	e := newEnv(t, nil)
	c := e.createSite(t, nil, map[string]string{"index.html": "x"})
	edit := editIDFrom(t, c.EditURL)

	w := e.putJSON(t, edit, c.EditPassword, map[string]any{"mode": "container"})
	if w.Code != 200 {
		t.Fatalf("enter: %d %s", w.Code, w.Body)
	}
	site, _ := e.st.ByViewID(c.ID)
	if site.Meta.Mode != store.ModeContainer || site.Meta.Container == nil || !site.Meta.Container.Enabled {
		t.Fatalf("meta after enter: %+v", site.Meta.Container)
	}
	if len(rt.kicked) != 1 || rt.kicked[0] != c.ID {
		t.Errorf("kicked = %v", rt.kicked)
	}
	var payload struct {
		Container struct {
			Available bool   `json:"available"`
			Enabled   bool   `json:"enabled"`
			Status    string `json:"status"`
		} `json:"container"`
		Usage struct {
			MaxFiles int `json:"max_files"`
		} `json:"usage"`
	}
	json.Unmarshal(w.Body.Bytes(), &payload)
	if !payload.Container.Available || !payload.Container.Enabled || payload.Container.Status != "starting" {
		t.Errorf("payload container = %+v", payload.Container)
	}
	if payload.Usage.MaxFiles != 0 {
		t.Errorf("container site reports a file cap of %d", payload.Usage.MaxFiles)
	}

	// A link a container left behind must be gone before Caddy serves files.
	linked := os.Symlink(t.TempDir(), filepath.Join(site.FilesDir(), "out")) == nil

	w = e.putJSON(t, edit, c.EditPassword, map[string]any{"mode": "webserver"})
	if w.Code != 200 {
		t.Fatalf("leave: %d %s", w.Code, w.Body)
	}
	if len(rt.stopped) != 1 {
		t.Errorf("stopped = %v", rt.stopped)
	}
	site, _ = e.st.ByViewID(c.ID)
	if site.Meta.Mode != store.ModeWebserver || site.Meta.Container.Enabled {
		t.Errorf("meta after leave: mode=%s %+v", site.Meta.Mode, site.Meta.Container)
	}
	if linked {
		if _, err := os.Lstat(filepath.Join(site.FilesDir(), "out")); !os.IsNotExist(err) {
			t.Error("a container's link survived leaving container mode")
		}
	}
}

func TestLeavingContainerModeWaitsForTheContainers(t *testing.T) {
	rt := &fakeRuntime{}
	withContainers(t, rt)
	e := newEnv(t, nil)
	c := e.createSite(t, map[string]string{"mode": "container"}, nil)
	rt.stopErr = errors.New("docker is down")
	w := e.putJSON(t, editIDFrom(t, c.EditURL), c.EditPassword, map[string]any{"mode": "webserver"})
	if w.Code != 503 {
		t.Fatalf("leave with docker down: %d %s", w.Code, w.Body)
	}
	site, _ := e.st.ByViewID(c.ID)
	if site.Meta.Mode != store.ModeContainer {
		t.Error("the mode changed although the containers are still running")
	}
}

// running marks a site's project as running with the given services, the
// way the runtime would report it.
func running(t *testing.T, e *env, viewID string, services ...ext.ContainerService) {
	t.Helper()
	if err := e.api.SiteService().SetContainerState(viewID, ext.ContainerState{Status: store.ContainerRunning, Services: services}); err != nil {
		t.Fatal(err)
	}
}

func authzFor(t *testing.T, e *env, host string, hdr map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("GET", "/internal/authz", nil)
	req.Header.Set("X-Forwarded-Host", host)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	return e.internal(t, req)
}

func TestAuthzRoutesContainerSites(t *testing.T) {
	rt := &fakeRuntime{}
	withContainers(t, rt)
	e := newEnv(t, nil)
	c := e.createSite(t, map[string]string{"mode": "container"}, nil)
	viewHost := c.ID + "." + e.cfg.ViewDomain

	// Enabled but not yet running: never served from the files.
	if w := authzFor(t, e, viewHost, nil); w.Code != 503 || w.Header().Get(upstreamHeader) != "" {
		t.Fatalf("starting: %d %q", w.Code, w.Header().Get(upstreamHeader))
	}

	running(t, e, c.ID,
		ext.ContainerService{Name: "app", Host: "sb-" + c.ID + "-app", Ports: []int{3000, 3001}},
		ext.ContainerService{Name: "api", Host: "sb-" + c.ID + "-api", Ports: []int{8080}},
		ext.ContainerService{Name: "db", Host: "sb-" + c.ID + "-db"},
	)
	if err := e.api.SiteService().SyncContainerEndpoints(c.ID, []string{"app:3000", "app:3001", "api:8080"}); err != nil {
		t.Fatal(err)
	}
	w := authzFor(t, e, viewHost, nil)
	if w.Code != 200 || w.Header().Get(upstreamHeader) != "sb-"+c.ID+"-app:3000" {
		t.Fatalf("running: %d %q", w.Code, w.Header().Get(upstreamHeader))
	}
	// Every further port has a generated address of its own.
	site, _ := e.st.ByViewID(c.ID)
	for endpoint, want := range map[string]string{"app:3001": "sb-" + c.ID + "-app:3001", "api:8080": "sb-" + c.ID + "-api:8080"} {
		var label string
		for _, a := range site.Meta.Addresses {
			if a.Endpoint == endpoint {
				label = a.Label
			}
		}
		if label == "" || label == c.ID {
			t.Fatalf("%s has no address of its own: %+v", endpoint, site.Meta.Addresses)
		}
		if w := authzFor(t, e, label+"."+e.cfg.ViewDomain, nil); w.Code != 200 || w.Header().Get(upstreamHeader) != want {
			t.Errorf("%s: %d %q, want %s", endpoint, w.Code, w.Header().Get(upstreamHeader), want)
		}
	}
	if w := authzFor(t, e, "zzzzzzzzzzzzzzzzzzzzzzzzzz."+e.cfg.ViewDomain, nil); w.Code != 404 || w.Header().Get(upstreamHeader) != "" {
		t.Errorf("an unknown label: %d %q", w.Code, w.Header().Get(upstreamHeader))
	}
	// Path views never proxy.
	if w := authzFor(t, e, "sitebin.example", map[string]string{"X-Sitebin-View": c.ID}); w.Code != 404 {
		t.Errorf("path view: %d", w.Code)
	}
	// Stopped: 503 again.
	e.st.Update(site, func(m *store.Meta) error { m.Container.Enabled = false; return nil })
	if w := authzFor(t, e, viewHost, nil); w.Code != 503 {
		t.Errorf("stopped: %d", w.Code)
	}
}

func TestAuthzContainerSiteWithNothingMappedIsNotServed(t *testing.T) {
	withContainers(t, &fakeRuntime{})
	e := newEnv(t, nil)
	c := e.createSite(t, map[string]string{"mode": "container"}, nil)
	running(t, e, c.ID, ext.ContainerService{Name: "db", Host: "sb-" + c.ID + "-db"})
	w := authzFor(t, e, c.ID+"."+e.cfg.ViewDomain, nil)
	if w.Code != 404 || w.Header().Get(upstreamHeader) != "" {
		t.Fatalf("unmapped: %d %q", w.Code, w.Header().Get(upstreamHeader))
	}
}

func TestAuthzFileSitesUnchanged(t *testing.T) {
	e := newEnv(t, nil)
	c := e.createSite(t, nil, map[string]string{"index.html": "x"})
	w := authzFor(t, e, c.ID+"."+e.cfg.ViewDomain, nil)
	if w.Code != 200 || w.Header().Get(upstreamHeader) != "" {
		t.Fatalf("file site: %d %q", w.Code, w.Header().Get(upstreamHeader))
	}
}

func TestContainerActionsAndLogs(t *testing.T) {
	rt := &fakeRuntime{logs: "listening on 3000\n"}
	withContainers(t, rt)
	e := newEnv(t, nil)
	web := e.createSite(t, nil, nil)
	if w := e.call(t, "POST", "/api/sites/"+editIDFrom(t, web.EditURL)+"/containers/start", web.EditPassword, nil); w.Code != 409 {
		t.Errorf("start on a web site: %d", w.Code)
	}

	c := e.createSite(t, map[string]string{"mode": "container"}, nil)
	base := "/api/sites/" + editIDFrom(t, c.EditURL) + "/containers/"
	if w := e.call(t, "POST", base+"restart", c.EditPassword, nil); w.Code != 202 {
		t.Fatalf("restart: %d %s", w.Code, w.Body)
	}
	site, _ := e.st.ByViewID(c.ID)
	if site.Meta.Container.RestartSeq != 1 {
		t.Errorf("restart_seq = %d", site.Meta.Container.RestartSeq)
	}
	if w := e.call(t, "POST", base+"stop", c.EditPassword, nil); w.Code != 202 {
		t.Fatalf("stop: %d %s", w.Code, w.Body)
	}
	site, _ = e.st.ByViewID(c.ID)
	if site.Meta.Container.Enabled || len(rt.stopped) != 1 {
		t.Errorf("after stop: enabled=%v stopped=%v", site.Meta.Container.Enabled, rt.stopped)
	}
	if w := e.call(t, "POST", base+"start", c.EditPassword, nil); w.Code != 202 {
		t.Fatalf("start: %d", w.Code)
	}
	site, _ = e.st.ByViewID(c.ID)
	if !site.Meta.Container.Enabled || site.Meta.Container.RestartSeq != 2 {
		t.Errorf("after start: %+v", site.Meta.Container)
	}
	if w := e.call(t, "POST", base+"explode", c.EditPassword, nil); w.Code != 404 {
		t.Errorf("unknown action: %d", w.Code)
	}
	w := e.call(t, "GET", base+"app/logs?tail=50", c.EditPassword, nil)
	if w.Code != 200 || w.Body.String() != "listening on 3000\n" {
		t.Errorf("logs: %d %q", w.Code, w.Body)
	}
	if w := e.call(t, "GET", base+"nope/logs", c.EditPassword, nil); w.Code != 404 {
		t.Errorf("logs of unknown service: %d", w.Code)
	}
	if w := e.call(t, "POST", base+"start", "wrong", nil); w.Code != 401 {
		t.Errorf("wrong password: %d", w.Code)
	}

	// The plan is checked again on start.
	rt.allowErr = errors.New("plan limit")
	if w := e.call(t, "POST", base+"start", c.EditPassword, nil); w.Code != 403 {
		t.Errorf("start past the plan: %d", w.Code)
	}
}

// A container site's domains are managed like a file site's — the edit page,
// the API, MCP — and each serves one of the app's HTTP ports. The compose
// file never names one. See
// docs/superpowers/specs/2026-10-16-container-addresses-design.md.
func TestContainerSiteDomainsServePorts(t *testing.T) {
	withContainers(t, &fakeRuntime{})
	e := newEnv(t, nil)
	c := e.createSite(t, map[string]string{"mode": "container"}, nil)
	edit := editIDFrom(t, c.EditURL)
	running(t, e, c.ID,
		ext.ContainerService{Name: "app", Host: "sb-app", Ports: []int{3000}},
		ext.ContainerService{Name: "api", Host: "sb-api", Ports: []int{8080}},
	)
	ss := e.api.SiteService()
	if err := ss.SyncContainerEndpoints(c.ID, []string{"app:3000", "api:8080"}); err != nil {
		t.Fatal(err)
	}
	upstream := func(host string) (int, string) {
		w := authzFor(t, e, host, nil)
		return w.Code, w.Header().Get(upstreamHeader)
	}

	// Added without a target: the port of the site's own address.
	if w := e.call(t, "POST", "/api/sites/"+edit+"/domains", c.EditPassword, map[string]string{"domain": "a.example.com"}); w.Code != 200 {
		t.Fatalf("add on a container site: %d %s", w.Code, w.Body)
	}
	if w := e.call(t, "POST", "/api/sites/"+edit+"/domains", c.EditPassword, map[string]string{"domain": "b.example.com", "target": "api:8080"}); w.Code != 200 {
		t.Fatalf("add with a target: %d %s", w.Code, w.Body)
	}
	if w := e.call(t, "POST", "/api/sites/"+edit+"/domains", c.EditPassword, map[string]string{"domain": "c.example.com", "target": "db:3306"}); w.Code != 400 || !strings.Contains(w.Body.String(), "app:3000, api:8080") {
		t.Errorf("an undeclared target: %d %s", w.Code, w.Body)
	}
	if site, _ := e.st.ByViewID(c.ID); site.HasDomainClaim("c.example.com") {
		t.Error("a refused target still claimed the domain")
	}
	if code, up := upstream("a.example.com"); code != 200 || up != "sb-app:3000" {
		t.Errorf("a: %d %q", code, up)
	}
	if code, up := upstream("b.example.com"); code != 200 || up != "sb-api:8080" {
		t.Errorf("b: %d %q", code, up)
	}

	// Pointed at another port, without touching the compose file.
	if w := e.call(t, "PUT", "/api/sites/"+edit+"/domains/b.example.com/target", c.EditPassword, map[string]string{"endpoint": "app:3000"}); w.Code != 200 {
		t.Fatalf("target: %d %s", w.Code, w.Body)
	}
	if code, up := upstream("b.example.com"); code != 200 || up != "sb-app:3000" {
		t.Errorf("b after the target change: %d %q", code, up)
	}
	if w := e.call(t, "PUT", "/api/sites/"+edit+"/domains/b.example.com/target", c.EditPassword, map[string]string{"endpoint": "nope:1"}); w.Code != 400 {
		t.Errorf("an undeclared target: %d %s", w.Code, w.Body)
	}

	// Redirects and the generated addresses work as on a file site — before
	// the gate.
	if w := e.call(t, "PUT", "/api/sites/"+edit+"/domains/b.example.com/redirect", c.EditPassword, map[string]any{"to": "a.example.com"}); w.Code != 200 {
		t.Fatalf("redirect: %d %s", w.Code, w.Body)
	}
	if w := e.putJSON(t, edit, c.EditPassword, map[string]any{"view_password": "sesame"}); w.Code != 200 {
		t.Fatal(w.Body)
	}
	if w := authzFor(t, e, "b.example.com", map[string]string{"X-Forwarded-Uri": "/x?y=1"}); w.Code != 301 || w.Header().Get("Location") != e.api.cfg.SiteURL("a.example.com")+"/x?y=1" {
		t.Errorf("b → a: %d %q", w.Code, w.Header().Get("Location"))
	}
	if code, _ := upstream("a.example.com"); code != 401 {
		t.Errorf("the target is gated: %d", code)
	}
	if w := e.putJSON(t, edit, c.EditPassword, map[string]any{"view_password": ""}); w.Code != 200 {
		t.Fatal(w.Body)
	}
	if w := e.call(t, "PUT", "/api/sites/"+edit+"/view-address", c.EditPassword, map[string]any{"mode": "off", "endpoint": "api:8080"}); w.Code != 400 {
		t.Errorf("off for a port no domain serves: %d %s", w.Code, w.Body)
	}
	if w := e.call(t, "PUT", "/api/sites/"+edit+"/view-address", c.EditPassword, map[string]any{"mode": "off"}); w.Code != 200 {
		t.Fatalf("own address off: %d %s", w.Code, w.Body)
	}
	if code, _ := upstream(c.ID + "." + e.cfg.ViewDomain); code != 404 {
		t.Errorf("an own address that is off: %d", code)
	}
	w := e.call(t, "PUT", "/api/sites/"+edit+"/view-address", c.EditPassword, map[string]any{"mode": "redirect", "to": "a.example.com", "code": 302, "endpoint": "api:8080"})
	if w.Code != 200 {
		t.Fatalf("api's address → a: %d %s", w.Code, w.Body)
	}
	var p struct {
		ViewAddress struct{ Mode string } `json:"view_address"`
		Addresses   []struct {
			Endpoint, URL, Mode, To string
			Own                     bool
			Code                    int
			InEffect                bool `json:"in_effect"`
		} `json:"addresses"`
		DomainTargets map[string]struct {
			Endpoint string
			Declared bool
		} `json:"domain_targets"`
		Container struct {
			Services []struct {
				Name  string
				Ports []int
			}
		}
	}
	if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	if p.ViewAddress.Mode != "off" || len(p.Addresses) != 2 || !p.Addresses[0].Own || p.Addresses[0].Mode != "off" ||
		p.Addresses[1].Endpoint != "api:8080" || p.Addresses[1].Mode != "redirect" || p.Addresses[1].Code != 302 || !p.Addresses[1].InEffect {
		t.Errorf("payload addresses = %+v, view_address %+v", p.Addresses, p.ViewAddress)
	}
	if p.DomainTargets["a.example.com"].Endpoint != "app:3000" || !p.DomainTargets["a.example.com"].Declared {
		t.Errorf("payload targets = %+v", p.DomainTargets)
	}
	if len(p.Container.Services) != 2 || len(p.Container.Services[1].Ports) != 1 || p.Container.Services[1].Ports[0] != 8080 {
		t.Errorf("payload services = %+v", p.Container.Services)
	}
	apiHost := strings.TrimPrefix(strings.TrimPrefix(strings.TrimSuffix(p.Addresses[1].URL, "/"), "http://"), "https://")
	if w := authzFor(t, e, apiHost, nil); w.Code != 302 || !strings.HasPrefix(w.Header().Get("Location"), e.api.cfg.SiteURL("a.example.com")+"/") {
		t.Errorf("api's address %s: %d %q", apiHost, w.Code, w.Header().Get("Location"))
	}

	// MCP: the same paths.
	cs := mcpClient(t, e, nil)
	res := mcpCall(t, cs, "add_domain", map[string]any{"edit_id": edit, "edit_password": c.EditPassword, "domain": "d.example.com", "target": "api:8080"})
	if res.IsError || !strings.Contains(mcpText(res), `"domain_targets"`) || !strings.Contains(mcpText(res), `"addresses"`) {
		t.Errorf("MCP add_domain with a target: %s", mcpText(res))
	}
	if res := mcpCall(t, cs, "set_domain_target", map[string]any{"edit_id": edit, "edit_password": c.EditPassword, "domain": "d.example.com", "target": "app:3000"}); res.IsError {
		t.Errorf("MCP set_domain_target: %s", mcpText(res))
	}
	if res := mcpCall(t, cs, "set_view_address", map[string]any{"edit_id": edit, "edit_password": c.EditPassword, "mode": "serve", "endpoint": "api:8080"}); res.IsError {
		t.Errorf("MCP set_view_address with an endpoint: %s", mcpText(res))
	}
	if code, up := upstream("d.example.com"); code != 200 || up != "sb-app:3000" {
		t.Errorf("d: %d %q", code, up)
	}

	// The file drops api:8080: a domain pointed at it stays and serves
	// nothing; nothing is moved to another service.
	if w := e.call(t, "PUT", "/api/sites/"+edit+"/domains/d.example.com/target", c.EditPassword, map[string]string{"endpoint": "api:8080"}); w.Code != 200 {
		t.Fatal(w.Body)
	}
	if err := ss.SyncContainerEndpoints(c.ID, []string{"app:3000"}); err != nil {
		t.Fatal(err)
	}
	if w := authzFor(t, e, "d.example.com", nil); w.Code != 404 || w.Header().Get(upstreamHeader) != "" {
		t.Errorf("a domain whose port went: %d %q", w.Code, w.Header().Get(upstreamHeader))
	}
	if site, _ := e.st.ByViewID(c.ID); !slices.Contains(site.Meta.CustomDomains, "d.example.com") {
		t.Error("a domain whose port went was detached")
	}

	// Domains are removed like any other.
	if w := e.call(t, "DELETE", "/api/sites/"+edit+"/domains/d.example.com", c.EditPassword, nil); w.Code != 200 {
		t.Errorf("remove on a container site: %d %s", w.Code, w.Body)
	}
}

// Leaving container mode takes the ports' addresses with it; the domains stay
// and serve the files.
func TestLeavingContainerModeDropsAddresses(t *testing.T) {
	rt := &fakeRuntime{}
	withContainers(t, rt)
	e := newEnv(t, nil)
	c := e.createSite(t, map[string]string{"mode": "container"}, map[string]string{"index.html": "x"})
	edit := editIDFrom(t, c.EditURL)
	if err := e.api.SiteService().SyncContainerEndpoints(c.ID, []string{"app:3000", "api:8080"}); err != nil {
		t.Fatal(err)
	}
	if w := e.call(t, "POST", "/api/sites/"+edit+"/domains", c.EditPassword, map[string]string{"domain": "a.example.com"}); w.Code != 200 {
		t.Fatal(w.Body)
	}
	site, _ := e.st.ByViewID(c.ID)
	var label string
	for _, a := range site.Meta.Addresses {
		if a.Endpoint == "api:8080" {
			label = a.Label
		}
	}
	if w := e.putJSON(t, edit, c.EditPassword, map[string]any{"mode": "webserver"}); w.Code != 200 {
		t.Fatalf("leave: %d %s", w.Code, w.Body)
	}
	site, _ = e.st.ByViewID(c.ID)
	if site.Meta.Addresses != nil || site.Meta.DomainTargets != nil || len(site.Meta.CustomDomains) != 1 {
		t.Errorf("after leaving: %+v %+v %v", site.Meta.Addresses, site.Meta.DomainTargets, site.Meta.CustomDomains)
	}
	if w := authzFor(t, e, label+"."+e.cfg.ViewDomain, nil); w.Code != 404 {
		t.Errorf("a port's address after leaving the mode: %d", w.Code)
	}
	if w := authzFor(t, e, "a.example.com", nil); w.Code != 200 || w.Header().Get(upstreamHeader) != "" {
		t.Errorf("the domain serves the files: %d %q", w.Code, w.Header().Get(upstreamHeader))
	}
}

func TestContainerSiteServiceSeam(t *testing.T) {
	withContainers(t, &fakeRuntime{})
	e := newEnv(t, nil)
	c := e.createSite(t, map[string]string{"mode": "container"}, map[string]string{store.ComposeFile: "services: {}\n"})
	e.createSite(t, nil, nil) // a web site the scan must skip
	ss := e.api.SiteService()

	all, err := ss.ContainerSites()
	if err != nil || len(all) != 1 || all[0].ViewID != c.ID {
		t.Fatalf("ContainerSites = %+v, %v", all, err)
	}
	cs := all[0]
	if !cs.Container || !cs.Enabled || cs.Owner != "acct-1" || string(cs.Compose) != "services: {}\n" || cs.ComposeErr != "" {
		t.Errorf("container site = %+v", cs)
	}

	rel, err := ss.PrepareVolume(c.ID, "db")
	if err != nil || rel != "sites/"+c.ID+"/files/db" {
		t.Errorf("PrepareVolume = %q, %v", rel, err)
	}
	if _, err := ss.ContainerSite("aaaaaaaaaaaaaaaaaaaaaaaaaa"); !errors.Is(err, ext.ErrSiteGone) {
		t.Errorf("gone site: %v", err)
	}

	// Observed state round-trips and leaves the desired half alone.
	st := ext.ContainerState{Status: store.ContainerError, Message: "bad", AppliedHash: "h", AppliedSeq: 3,
		Services: []ext.ContainerService{{Name: "app", Image: "alpine-node-22", Host: "h", Ports: []int{80}}}}
	if err := ss.SetContainerState(c.ID, st); err != nil {
		t.Fatal(err)
	}
	cs, _ = ss.ContainerSite(c.ID)
	if !cs.Enabled || cs.Observed.Status != store.ContainerError || cs.Observed.AppliedSeq != 3 || len(cs.Observed.Services) != 1 {
		t.Errorf("after SetContainerState: %+v", cs)
	}

	// The compose file missing is said, not guessed.
	site, _ := e.st.ByViewID(c.ID)
	e.st.DeleteFile(site, store.ComposeFile)
	cs, _ = ss.ContainerSite(c.ID)
	if cs.Compose != nil || !strings.Contains(cs.ComposeErr, "missing") {
		t.Errorf("no compose: %+v", cs)
	}
}

func TestDeletingAContainerSiteStopsItFirst(t *testing.T) {
	rt := &fakeRuntime{}
	withContainers(t, rt)
	e := newEnv(t, nil)
	c := e.createSite(t, map[string]string{"mode": "container"}, nil)
	if w := e.call(t, "DELETE", "/api/sites/"+editIDFrom(t, c.EditURL), c.EditPassword, nil); w.Code != 200 {
		t.Fatalf("delete: %d", w.Code)
	}
	if len(rt.stopped) != 1 || rt.stopped[0] != c.ID {
		t.Errorf("stopped = %v", rt.stopped)
	}
}

// The operator's per-service limits are desired state: the core writes them,
// the runtime reads them, and recording what was observed leaves them alone.
func TestSetContainerLimitsIsDesiredState(t *testing.T) {
	withContainers(t, &fakeRuntime{})
	e := newEnv(t, nil)
	c := e.createSite(t, map[string]string{"mode": "container"}, map[string]string{store.ComposeFile: "services: {}\n"})
	ss := e.api.SiteService()

	want := map[string]ext.ContainerLimits{"db": {MemoryMB: 1024}, "app": {CPUs: 1, Pids: 512}, "gone": {}}
	if err := ss.SetContainerLimits(c.ID, want); err != nil {
		t.Fatal(err)
	}
	cs, _ := ss.ContainerSite(c.ID)
	if len(cs.Limits) != 2 || cs.Limits["db"] != (ext.ContainerLimits{MemoryMB: 1024}) || cs.Limits["app"] != (ext.ContainerLimits{CPUs: 1, Pids: 512}) {
		t.Fatalf("limits = %+v (an all-zero entry must be dropped)", cs.Limits)
	}

	applied := ext.ContainerLimits{MemoryMB: 1024, CPUs: 0.5, Pids: 256}
	st := ext.ContainerState{Status: store.ContainerRunning, Services: []ext.ContainerService{{Name: "db", Image: "mysql-8.4", Limits: applied}}}
	if err := ss.SetContainerState(c.ID, st); err != nil {
		t.Fatal(err)
	}
	cs, _ = ss.ContainerSite(c.ID)
	if len(cs.Limits) != 2 || cs.Observed.Services[0].Limits != applied {
		t.Errorf("after SetContainerState: limits %+v, observed %+v", cs.Limits, cs.Observed.Services)
	}

	if err := ss.SetContainerLimits(c.ID, nil); err != nil {
		t.Fatal(err)
	}
	cs, _ = ss.ContainerSite(c.ID)
	if len(cs.Limits) != 0 {
		t.Errorf("cleared limits = %+v", cs.Limits)
	}
	site, _ := e.st.ByViewID(c.ID)
	raw, _ := os.ReadFile(filepath.Join(site.Dir(), "meta.json"))
	var m struct {
		Container struct {
			Limits map[string]any `json:"limits"`
		} `json:"container"`
	}
	json.Unmarshal(raw, &m)
	if m.Container.Limits != nil {
		t.Errorf("meta.json container.limits = %v", m.Container.Limits)
	}

	if err := ss.SetContainerLimits("aaaaaaaaaaaaaaaaaaaaaaaaaa", want); !errors.Is(err, ext.ErrSiteGone) {
		t.Errorf("gone site: %v", err)
	}
}

// A container site nobody gave limits writes no limits at all: older sites
// and new ones look the same on disk.
func TestContainerLimitsOmittedWhenUnset(t *testing.T) {
	b, err := json.Marshal(store.ContainerMeta{Enabled: true, Services: []store.ContainerService{{Name: "app", Image: "alpine-node-22"}}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "limits") {
		t.Errorf("unset limits are written: %s", b)
	}
}
