package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
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
		ext.ContainerService{Name: "app", Host: "sb-" + c.ID + "-app", Domains: []ext.ContainerDomain{{Domain: "*", Port: 3000}}},
		ext.ContainerService{Name: "db", Host: "sb-" + c.ID + "-db"},
	)
	w := authzFor(t, e, viewHost, nil)
	if w.Code != 200 || w.Header().Get(upstreamHeader) != "sb-"+c.ID+"-app:3000" {
		t.Fatalf("running: %d %q", w.Code, w.Header().Get(upstreamHeader))
	}
	// Path views never proxy.
	if w := authzFor(t, e, "sitebin.example", map[string]string{"X-Sitebin-View": c.ID}); w.Code != 404 {
		t.Errorf("path view: %d", w.Code)
	}
	// Stopped: 503 again.
	site, _ := e.st.ByViewID(c.ID)
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

func TestContainerSiteDomainsComeFromTheComposeFile(t *testing.T) {
	withContainers(t, &fakeRuntime{})
	e := newEnv(t, nil)
	c := e.createSite(t, map[string]string{"mode": "container"}, nil)
	edit := editIDFrom(t, c.EditURL)

	if w := e.call(t, "POST", "/api/sites/"+edit+"/domains", c.EditPassword, map[string]string{"domain": "a.example.com"}); w.Code != 409 {
		t.Fatalf("add on container site: %d %s", w.Code, w.Body)
	}

	warn, err := e.api.SiteService().SyncContainerDomains(c.ID, []string{"a.example.com", "B.example.com"})
	if err != nil || len(warn) != 0 {
		t.Fatalf("sync: %v %v", warn, err)
	}
	site, _ := e.st.ByViewID(c.ID)
	if !site.HasDomainClaim("a.example.com") || !site.HasDomainClaim("b.example.com") {
		t.Fatalf("claims after sync: %+v %v", site.Meta.DomainClaims, site.Meta.CustomDomains)
	}
	// The edit page's "check now" re-posts a claimed domain: allowed.
	if w := e.call(t, "POST", "/api/sites/"+edit+"/domains", c.EditPassword, map[string]string{"domain": "a.example.com"}); w.Code != 200 && w.Code != 202 {
		t.Errorf("re-check: %d %s", w.Code, w.Body)
	}
	if w := e.call(t, "DELETE", "/api/sites/"+edit+"/domains/a.example.com", c.EditPassword, nil); w.Code != 409 {
		t.Errorf("remove on container site: %d", w.Code)
	}

	if _, err := e.api.SiteService().SyncContainerDomains(c.ID, []string{"b.example.com"}); err != nil {
		t.Fatal(err)
	}
	site, _ = e.st.ByViewID(c.ID)
	if site.HasDomainClaim("a.example.com") || !site.HasDomainClaim("b.example.com") {
		t.Errorf("claims after dropping one: %+v %v", site.Meta.DomainClaims, site.Meta.CustomDomains)
	}

	// A domain another site holds comes back as a warning, not an error.
	other := e.createSite(t, nil, nil)
	os, _ := e.st.ByViewID(other.ID)
	if err := e.st.AddDomain(os, "taken.example.com"); err != nil {
		t.Fatal(err)
	}
	warn, err = e.api.SiteService().SyncContainerDomains(c.ID, []string{"taken.example.com"})
	if err != nil || len(warn) != 1 {
		t.Errorf("taken domain: %v %v", warn, err)
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
		Services: []ext.ContainerService{{Name: "app", Image: "alpine-node-22", Host: "h", Domains: []ext.ContainerDomain{{Domain: "*", Port: 80}}}}}
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
