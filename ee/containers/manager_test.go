//go:build ee

package containers

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ittrail/sitebin.io/internal/ext"
	"github.com/ittrail/sitebin.io/internal/store"
)

// ---- a fake Docker Engine ----

type fakeContainer struct {
	id, name string
	body     CreateBody
	state    string
	nets     []string
}

type fakeEngine struct {
	mu         sync.Mutex
	seq        int
	containers map[string]*fakeContainer // by name
	networks   map[string]map[string]string
	netOpts    map[string]map[string]string
	internal   map[string]bool
	attached   map[string][]string // network -> containers
	images     map[string]bool
	pulled     []string
	created    int
	createErr  error
	pingErr    error
	mounts     []struct{ Type, Name, Source, Destination string }
}

func newFakeEngine() *fakeEngine {
	return &fakeEngine{
		containers: map[string]*fakeContainer{}, networks: map[string]map[string]string{},
		netOpts: map[string]map[string]string{}, internal: map[string]bool{},
		attached: map[string][]string{}, images: map[string]bool{},
		mounts: []struct{ Type, Name, Source, Destination string }{{"bind", "", "/srv/sitebin/data", "/data"}},
	}
}

func (f *fakeEngine) Ping(context.Context) error { return f.pingErr }
func (f *fakeEngine) ImageExists(_ context.Context, ref string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.images[ref], nil
}
func (f *fakeEngine) Pull(_ context.Context, repo, digest string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.images[repo+"@"+digest] = true
	f.pulled = append(f.pulled, repo)
	return nil
}
func hasLabels(have map[string]string, want []string) bool {
	for _, l := range want {
		k, v, _ := strings.Cut(l, "=")
		if have[k] != v {
			return false
		}
	}
	return true
}
func (f *fakeEngine) ContainerList(_ context.Context, labels ...string) ([]ContainerSummary, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []ContainerSummary
	for _, c := range f.containers {
		if hasLabels(c.body.Labels, labels) {
			out = append(out, ContainerSummary{ID: c.id, Names: []string{"/" + c.name}, State: c.state, Labels: c.body.Labels})
		}
	}
	return out, nil
}
func (f *fakeEngine) ContainerCreate(_ context.Context, name string, body CreateBody) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.createErr != nil {
		return "", f.createErr
	}
	if _, ok := f.containers[name]; ok {
		return "", &engineError{409, "name in use"}
	}
	f.seq++
	f.created++
	c := &fakeContainer{id: fmt.Sprintf("c%d", f.seq), name: name, body: body, state: "created", nets: []string{body.HostConfig.NetworkMode}}
	f.containers[name] = c
	return c.id, nil
}
func (f *fakeEngine) byID(id string) *fakeContainer {
	for _, c := range f.containers {
		if c.id == id || c.name == id {
			return c
		}
	}
	return nil
}
func (f *fakeEngine) ContainerStart(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.byID(id).state = "running"
	return nil
}
func (f *fakeEngine) ContainerRemove(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if c := f.byID(id); c != nil {
		delete(f.containers, c.name)
	}
	return nil
}
func (f *fakeEngine) ContainerInspect(_ context.Context, id string) (Inspect, error) {
	in := Inspect{ID: "self-" + id}
	for _, m := range f.mounts {
		in.Mounts = append(in.Mounts, struct {
			Type        string `json:"Type"`
			Name        string `json:"Name"`
			Source      string `json:"Source"`
			Destination string `json:"Destination"`
		}{m.Type, m.Name, m.Source, m.Destination})
	}
	return in, nil
}
func (f *fakeEngine) ContainerLogs(_ context.Context, id string, _ int) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.byID(id) == nil {
		return "", &engineError{404, "no such container"}
	}
	return "log of " + id, nil
}
func (f *fakeEngine) NetworkList(_ context.Context, labels ...string) ([]NetworkSummary, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []NetworkSummary
	for n, l := range f.networks {
		if hasLabels(l, labels) {
			out = append(out, NetworkSummary{ID: n, Name: n, Labels: l})
		}
	}
	return out, nil
}
func (f *fakeEngine) NetworkExists(_ context.Context, name string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.networks[name]
	return ok, nil
}
func (f *fakeEngine) NetworkCreate(_ context.Context, name string, internal bool, labels, options map[string]string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.networks[name], f.internal[name], f.netOpts[name] = labels, internal, options
	return nil
}
func (f *fakeEngine) NetworkRemove(_ context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.networks, name)
	delete(f.attached, name)
	return nil
}
func (f *fakeEngine) NetworkConnect(_ context.Context, network, container string, _ []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.networks[network]; !ok {
		return &engineError{404, "no such network"}
	}
	if !slices.Contains(f.attached[network], container) {
		f.attached[network] = append(f.attached[network], container)
	}
	return nil
}
func (f *fakeEngine) NetworkDisconnect(_ context.Context, network, container string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.attached[network] = slices.DeleteFunc(f.attached[network], func(c string) bool { return c == container })
	return nil
}

// ---- a fake core ----

type fakeSites struct {
	mu      sync.Mutex
	sites   map[string]*ext.ContainerSite
	bytes   map[string]int64
	domains map[string][]string
}

func newFakeSites() *fakeSites {
	return &fakeSites{sites: map[string]*ext.ContainerSite{}, bytes: map[string]int64{}, domains: map[string][]string{}}
}

func (s *fakeSites) add(id, owner, compose string) *ext.ContainerSite {
	s.mu.Lock()
	defer s.mu.Unlock()
	cs := &ext.ContainerSite{ViewID: id, Owner: owner, Container: true, Enabled: true, Compose: []byte(compose), MaxBytes: 1 << 30}
	s.sites[id] = cs
	return cs
}
func (s *fakeSites) update(id string, fn func(*ext.ContainerSite)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fn(s.sites[id])
}
func (s *fakeSites) get(id string) ext.ContainerSite {
	s.mu.Lock()
	defer s.mu.Unlock()
	return *s.sites[id]
}

func (s *fakeSites) ContainerSites() ([]ext.ContainerSite, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []ext.ContainerSite
	for _, cs := range s.sites {
		if cs.Container {
			out = append(out, *cs)
		}
	}
	return out, nil
}
func (s *fakeSites) ContainerSite(id string) (ext.ContainerSite, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cs, ok := s.sites[id]
	if !ok {
		return ext.ContainerSite{}, fmt.Errorf("%w: %s", ext.ErrSiteGone, id)
	}
	return *cs, nil
}
func (s *fakeSites) PrepareVolume(id, folder string) (string, error) {
	if !store.ValidVolumeFolder(folder) {
		return "", store.ErrBadPath
	}
	return "sites/" + id + "/files/" + folder, nil
}
func (s *fakeSites) SetContainerState(id string, st ext.ContainerState) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cs, ok := s.sites[id]
	if !ok {
		return ext.ErrSiteGone
	}
	cs.Observed = st
	return nil
}
func (s *fakeSites) SyncContainerDomains(id string, d []string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.domains[id] = d
	return nil, nil
}
func (s *fakeSites) Info(id string) (ext.SiteInfo, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return ext.SiteInfo{ViewID: id, Bytes: s.bytes[id]}, true
}
func (s *fakeSites) All() ([]ext.SiteInfo, error)              { return nil, nil }
func (s *fakeSites) SetExpiry(string, *time.Time) error        { return nil }
func (s *fakeSites) RotateEditPassword(string) (string, error) { return "", nil }
func (s *fakeSites) Delete(string) error                       { return nil }
func (s *fakeSites) ApplyQuota(string, ext.CreateGrant) error  { return nil }
func (s *fakeSites) CustomDomainCount() (int, error)           { return 0, nil }

// ---- harness ----

type rig struct {
	m      *Manager
	eng    *fakeEngine
	sites  *fakeSites
	caps   map[string]int
	capErr error
	mu     sync.Mutex
}

func newRig(t *testing.T) *rig {
	t.Helper()
	r := &rig{eng: newFakeEngine(), sites: newFakeSites(), caps: map[string]int{"acct": 20}}
	cfg := Config{DockerHost: "unix:///x", DataDir: "/data", Self: "sitebin", MemoryMB: 512, CPUs: 0.5, Pids: 256}
	r.m = newManager(cfg, r.eng, r.sites, func(owner string) (int, error) {
		r.mu.Lock()
		defer r.mu.Unlock()
		if r.capErr != nil {
			return 0, r.capErr
		}
		return r.caps[owner], nil
	})
	if !r.m.setup(context.Background()) {
		t.Fatalf("setup: %v", r.m.setupErr)
	}
	return r
}

func (r *rig) reconcile(t *testing.T, id string, kicked bool) {
	t.Helper()
	r.m.reconcileID(context.Background(), id, kicked)
}

const siteA = "aaaaaaaaaaaaaaaaaaaaaaaaaa"
const siteB = "bbbbbbbbbbbbbbbbbbbbbbbbbb"

func TestApplyTheExample(t *testing.T) {
	r := newRig(t)
	r.sites.add(siteA, "acct", example)
	r.reconcile(t, siteA, true)

	cs := r.sites.get(siteA)
	if cs.Observed.Status != store.ContainerRunning || cs.Observed.Message != "" {
		t.Fatalf("state = %+v", cs.Observed)
	}
	if len(cs.Observed.Services) != 2 || cs.Observed.Services[0].Host != "sb-"+siteA+"-app" {
		t.Fatalf("services = %+v", cs.Observed.Services)
	}
	if cs.Observed.AppliedHash == "" || cs.Observed.AppliedAt.IsZero() {
		t.Error("applied markers missing")
	}
	if got := r.sites.domains[siteA]; len(got) != 1 || got[0] != "my.custom.domain.com" {
		t.Errorf("domains synced = %v", got)
	}
	if len(r.eng.pulled) != 2 {
		t.Errorf("pulled = %v", r.eng.pulled)
	}

	app := r.eng.containers["sb-"+siteA+"-app"]
	db := r.eng.containers["sb-"+siteA+"-db"]
	if app == nil || db == nil || app.state != "running" || db.state != "running" {
		t.Fatalf("containers = %+v", r.eng.containers)
	}
	hc := app.body.HostConfig
	if app.body.User != fmt.Sprintf("%d:%d", r.m.uid, r.m.gid) || !hc.ReadonlyRootfs || !slices.Equal(hc.CapDrop, []string{"ALL"}) ||
		!slices.Contains(hc.SecurityOpt, "no-new-privileges:true") || hc.Memory != 512<<20 || hc.MemorySwap != hc.Memory ||
		hc.NanoCpus != 5e8 || hc.PidsLimit != 256 || hc.RestartPolicy.Name != "unless-stopped" || hc.LogConfig.Config["max-size"] != "10m" {
		t.Errorf("app host config = %+v, user %q", hc, app.body.User)
	}
	if len(hc.Mounts) != 1 || hc.Mounts[0].Type != "bind" || hc.Mounts[0].Source != "/srv/sitebin/data/sites/"+siteA+"/files/sitebin-rootfolder-1" || hc.Mounts[0].Target != "/usr/src/app" {
		t.Errorf("app mounts = %+v", hc.Mounts)
	}
	if app.body.WorkingDir != "/usr/src/app" || app.body.Image != catalog["alpine-node-22"].Ref() {
		t.Errorf("app workdir/image = %q %q", app.body.WorkingDir, app.body.Image)
	}
	if !slices.Contains(app.body.Env, "DB_HOST=db") || !slices.Contains(app.body.Env, "HOME=/tmp") {
		t.Errorf("app env = %v", app.body.Env)
	}
	// mysql's data path is mounted, so it is not a tmpfs; its run dir is.
	if _, ok := db.body.HostConfig.Tmpfs["/var/lib/mysql"]; ok {
		t.Error("a mounted data path got a tmpfs over it")
	}
	if _, ok := db.body.HostConfig.Tmpfs["/var/run/mysqld"]; !ok {
		t.Error("mysql has no run dir")
	}
	if db.body.Cmd[0] != "mysqld" {
		t.Errorf("db cmd = %v", db.body.Cmd)
	}

	// Networks: an internal one Sitebin is on, an egress one only app is on.
	net, egress := networkName(siteA), egressName(siteA)
	if !r.eng.internal[net] || r.eng.internal[egress] {
		t.Errorf("internal flags: %v", r.eng.internal)
	}
	if r.eng.netOpts[egress]["com.docker.network.bridge.enable_icc"] != "false" {
		t.Error("egress network allows inter-container traffic")
	}
	if br := r.eng.netOpts[egress]["com.docker.network.bridge.name"]; br != "sbe"+siteA[:12] || len(br) > 15 {
		t.Errorf("egress bridge name = %q: the firewall rule matches sbe+", br)
	}
	if !slices.Contains(r.eng.attached[net], "self-sitebin") {
		t.Errorf("Sitebin not attached: %v", r.eng.attached[net])
	}
	if !slices.Contains(r.eng.attached[egress], app.id) || slices.Contains(r.eng.attached[egress], db.id) {
		t.Errorf("egress attachments = %v", r.eng.attached[egress])
	}
}

func TestUnmountedDataPathIsATmpfs(t *testing.T) {
	r := newRig(t)
	r.sites.add(siteA, "acct", "services:\n  db:\n    image: mysql-8.4\n")
	r.reconcile(t, siteA, true)
	db := r.eng.containers["sb-"+siteA+"-db"]
	if db == nil || db.body.HostConfig.Tmpfs["/var/lib/mysql"] == "" {
		t.Fatalf("unmounted data path: %+v", db)
	}
	if _, ok := r.eng.networks[egressName(siteA)]; ok {
		t.Error("an egress network exists with no service needing it")
	}
	if db.body.WorkingDir != "/tmp" {
		t.Errorf("workdir without volumes = %q", db.body.WorkingDir)
	}
}

func TestNamedVolumeUsesSubpath(t *testing.T) {
	r := newRig(t)
	r.eng.mounts[0].Type, r.eng.mounts[0].Name = "volume", "sitebin_data"
	if !r.m.setup(context.Background()) {
		t.Fatal(r.m.setupErr)
	}
	r.sites.add(siteA, "acct", "services:\n  app:\n    image: alpine-node-22\n    volumes: ['app:/srv:ro']\n")
	r.reconcile(t, siteA, true)
	mt := r.eng.containers["sb-"+siteA+"-app"].body.HostConfig.Mounts[0]
	if mt.Type != "volume" || mt.Source != "sitebin_data" || mt.VolumeOptions == nil || mt.VolumeOptions.Subpath != "sites/"+siteA+"/files/app" || !mt.ReadOnly {
		t.Errorf("mount = %+v %+v", mt, mt.VolumeOptions)
	}
}

func TestChangesRestartAndNothingElseDoes(t *testing.T) {
	r := newRig(t)
	r.sites.add(siteA, "acct", example)
	r.reconcile(t, siteA, true)
	if r.eng.created != 2 {
		t.Fatalf("created = %d", r.eng.created)
	}
	r.reconcile(t, siteA, false)
	if r.eng.created != 2 {
		t.Errorf("an unchanged project was restarted: %d", r.eng.created)
	}

	r.sites.update(siteA, func(cs *ext.ContainerSite) {
		cs.Compose = []byte(strings.Replace(example, "development", "production", 1))
	})
	r.reconcile(t, siteA, false)
	if r.eng.created != 4 || len(r.eng.containers) != 2 {
		t.Errorf("a changed compose file did not restart: created=%d live=%d", r.eng.created, len(r.eng.containers))
	}
	if !slices.Contains(r.eng.containers["sb-"+siteA+"-app"].body.Env, "NODE_ENV=production") {
		t.Error("the restarted container runs the old environment")
	}

	r.sites.update(siteA, func(cs *ext.ContainerSite) { cs.RestartSeq++ })
	r.reconcile(t, siteA, false)
	if r.eng.created != 6 {
		t.Errorf("restart did not restart: %d", r.eng.created)
	}
}

func TestBrokenComposeIsReportedOnce(t *testing.T) {
	r := newRig(t)
	r.sites.add(siteA, "acct", example)
	r.reconcile(t, siteA, true)

	r.sites.update(siteA, func(cs *ext.ContainerSite) { cs.Compose = []byte("services:\n  app:\n    image: nginx\n") })
	r.reconcile(t, siteA, false)
	cs := r.sites.get(siteA)
	if cs.Observed.Status != store.ContainerError || !strings.Contains(cs.Observed.Message, "not one of Sitebin's images") {
		t.Fatalf("state = %+v", cs.Observed)
	}
	if len(r.eng.containers) != 0 {
		t.Error("the old project kept running under a broken file")
	}
	created := r.eng.created
	r.reconcile(t, siteA, false)
	if r.eng.created != created || r.sites.get(siteA).Observed.Message != cs.Observed.Message {
		t.Error("a broken file was retried")
	}

	r.sites.update(siteA, func(cs *ext.ContainerSite) { cs.Compose, cs.ComposeErr = nil, store.ComposeFile+" is missing" })
	r.reconcile(t, siteA, false)
	if msg := r.sites.get(siteA).Observed.Message; !strings.Contains(msg, "missing") {
		t.Errorf("missing file: %q", msg)
	}
}

func TestPlanCapCountsEveryProject(t *testing.T) {
	r := newRig(t)
	r.caps["acct"] = 3
	r.sites.add(siteA, "acct", example) // 2 services
	r.reconcile(t, siteA, true)
	if r.sites.get(siteA).Observed.Status != store.ContainerRunning {
		t.Fatal("first project did not start")
	}
	r.sites.add(siteB, "acct", example) // 2 more: 4 > 3
	r.reconcile(t, siteB, true)
	cs := r.sites.get(siteB)
	if cs.Observed.Status != store.ContainerError || !strings.Contains(cs.Observed.Message, "allows 3 container(s)") ||
		!strings.Contains(cs.Observed.Message, "needs 2") || !strings.Contains(cs.Observed.Message, "use 2") {
		t.Fatalf("over the cap: %+v", cs.Observed)
	}
	if _, ok := r.eng.containers["sb-"+siteB+"-app"]; ok {
		t.Error("a project over the cap was started")
	}

	r.caps["acct"] = 0
	r.sites.update(siteB, func(cs *ext.ContainerSite) { cs.RestartSeq++ })
	r.reconcile(t, siteB, true)
	if msg := r.sites.get(siteB).Observed.Message; msg != "your plan does not include containers" {
		t.Errorf("no containers on the plan: %q", msg)
	}
}

func TestUnknownPlanStartsNothingAndRetries(t *testing.T) {
	r := newRig(t)
	r.capErr = errors.New("paygate down")
	r.sites.add(siteA, "acct", example)
	r.reconcile(t, siteA, true)
	cs := r.sites.get(siteA)
	if len(r.eng.containers) != 0 || cs.Observed.Status != store.ContainerError || cs.Observed.AppliedHash != "" {
		t.Fatalf("unknown plan: %+v, %d containers", cs.Observed, len(r.eng.containers))
	}
	if r.m.retryAt[siteA].IsZero() {
		t.Error("no retry scheduled")
	}
	r.capErr = nil
	r.reconcile(t, siteA, false) // still backing off
	if len(r.eng.containers) != 0 {
		t.Error("the back-off was ignored")
	}
	r.m.now = func() time.Time { return time.Now().Add(2 * retryAfter) }
	r.reconcile(t, siteA, false)
	if r.sites.get(siteA).Observed.Status != store.ContainerRunning {
		t.Errorf("not started after the back-off: %+v", r.sites.get(siteA).Observed)
	}
}

func TestDeployFailureIsRetriedAndCleanedUp(t *testing.T) {
	r := newRig(t)
	r.eng.createErr = errors.New("no space left on device")
	r.sites.add(siteA, "acct", example)
	r.reconcile(t, siteA, true)
	cs := r.sites.get(siteA)
	if cs.Observed.Status != store.ContainerError || !strings.Contains(cs.Observed.Message, "no space left") || cs.Observed.AppliedHash != "" {
		t.Fatalf("state = %+v", cs.Observed)
	}
	if len(r.eng.networks) != 0 {
		t.Errorf("a failed deploy left networks: %v", r.eng.networks)
	}
}

func TestStopAndDisable(t *testing.T) {
	r := newRig(t)
	r.sites.add(siteA, "acct", example)
	r.reconcile(t, siteA, true)
	r.sites.update(siteA, func(cs *ext.ContainerSite) { cs.Enabled = false })
	if err := r.m.Stop(siteA); err != nil {
		t.Fatal(err)
	}
	if len(r.eng.containers) != 0 || len(r.eng.networks) != 0 {
		t.Errorf("after Stop: %d containers, networks %v", len(r.eng.containers), r.eng.networks)
	}
	if cs := r.sites.get(siteA); cs.Observed.Status != store.ContainerStopped {
		t.Errorf("state = %+v", cs.Observed)
	}
	r.reconcile(t, siteA, false)
	if r.eng.created != 2 {
		t.Error("a disabled project was started again")
	}
	if r.m.known[siteA] {
		t.Error("a stopped site stays on the tick")
	}

	// Expired: stopped even though enabled.
	r.sites.update(siteA, func(cs *ext.ContainerSite) { cs.Enabled, cs.Expired = true, true })
	r.reconcile(t, siteA, true)
	if cs := r.sites.get(siteA); cs.Observed.Status != store.ContainerStopped || cs.Observed.Message != "the site has expired" || len(r.eng.containers) != 0 {
		t.Errorf("expired: %+v", cs.Observed)
	}
}

func TestFullScanRemovesOrphans(t *testing.T) {
	r := newRig(t)
	r.sites.add(siteA, "acct", example)
	r.reconcile(t, siteA, true)
	// A site that was deleted without anyone telling the runtime.
	r.eng.containers["sb-zzz-app"] = &fakeContainer{id: "orphan", name: "sb-zzz-app", state: "running",
		body: CreateBody{Labels: map[string]string{labelManaged: "true", labelSite: "zzz", labelService: "app"}}}
	r.eng.networks["sb-zzz"] = map[string]string{labelManaged: "true", labelSite: "zzz"}
	// Something that is not Sitebin's at all.
	r.eng.containers["postgres"] = &fakeContainer{id: "p", name: "postgres", state: "running", body: CreateBody{Labels: map[string]string{}}}

	r.m.fullScan(context.Background())
	if _, ok := r.eng.containers["sb-zzz-app"]; ok {
		t.Error("orphan container survived")
	}
	if _, ok := r.eng.networks["sb-zzz"]; ok {
		t.Error("orphan network survived")
	}
	if _, ok := r.eng.containers["postgres"]; !ok {
		t.Error("the scan removed a container that is not Sitebin's")
	}
	if _, ok := r.eng.containers["sb-"+siteA+"-app"]; !ok {
		t.Error("the scan removed a wanted container")
	}
}

func TestFullScanRefreshesAndHeals(t *testing.T) {
	r := newRig(t)
	r.sites.add(siteA, "acct", example)
	r.reconcile(t, siteA, true)

	// A redeploy of Sitebin: the new container is on no project network.
	r.eng.attached[networkName(siteA)] = nil
	r.eng.containers["sb-"+siteA+"-db"].state = "restarting"
	r.m.fullScan(context.Background())
	if !slices.Contains(r.eng.attached[networkName(siteA)], "self-sitebin") {
		t.Error("Sitebin was not re-attached")
	}
	if st := r.sites.get(siteA).Observed.Services[1].State; st != "restarting" {
		t.Errorf("db state = %q", st)
	}

	// A container that vanished brings the project back.
	delete(r.eng.containers, "sb-"+siteA+"-app")
	r.m.fullScan(context.Background())
	if _, ok := r.eng.containers["sb-"+siteA+"-app"]; !ok {
		t.Error("a vanished container was not recreated")
	}
}

func TestDowngradeStopsNewestFirst(t *testing.T) {
	r := newRig(t)
	r.sites.add(siteA, "acct", example)
	r.reconcile(t, siteA, true)
	r.m.now = func() time.Time { return time.Now().Add(time.Hour) }
	r.sites.add(siteB, "acct", "services:\n  web:\n    image: alpine-node-22\n")
	r.reconcile(t, siteB, true)

	r.caps["acct"] = 2 // A (2) + B (1) = 3 > 2: B is newer and goes
	r.m.enforcePlans(context.Background())
	if st := r.sites.get(siteB).Observed; st.Status != store.ContainerStopped || !strings.Contains(st.Message, "allows 2") {
		t.Errorf("B = %+v", st)
	}
	if r.sites.get(siteA).Observed.Status != store.ContainerRunning {
		t.Error("the older project was stopped")
	}
	// Held off: the next tick does not start it again.
	r.reconcile(t, siteB, false)
	if _, ok := r.eng.containers["sb-"+siteB+"-web"]; ok {
		t.Error("a held-off project was restarted by the tick")
	}

	r.capErr = errors.New("down")
	r.caps["acct"] = 0
	r.m.enforcePlans(context.Background())
	if r.sites.get(siteA).Observed.Status != store.ContainerRunning {
		t.Error("an unknown plan stopped a project")
	}
}

func TestStorageCapStopsTheProject(t *testing.T) {
	r := newRig(t)
	r.sites.add(siteA, "acct", example)
	r.reconcile(t, siteA, true)
	r.sites.bytes[siteA] = 2 << 30
	r.m.enforceStorage(context.Background())
	st := r.sites.get(siteA).Observed
	if st.Status != store.ContainerStopped || !strings.Contains(st.Message, "storage") || len(r.eng.containers) != 0 {
		t.Errorf("over storage: %+v, %d containers", st, len(r.eng.containers))
	}
}

func TestGoneSiteIsTornDown(t *testing.T) {
	r := newRig(t)
	r.sites.add(siteA, "acct", example)
	r.reconcile(t, siteA, true)
	delete(r.sites.sites, siteA)
	r.reconcile(t, siteA, false)
	if len(r.eng.containers) != 0 || len(r.eng.networks) != 0 {
		t.Error("a deleted site kept its containers")
	}
}

func TestAllowed(t *testing.T) {
	r := newRig(t)
	if err := r.m.Allowed("acct"); err != nil {
		t.Errorf("paid owner: %v", err)
	}
	if err := r.m.Allowed(""); err == nil {
		t.Error("anonymous allowed")
	}
	if err := r.m.Allowed("free"); err == nil || !strings.Contains(err.Error(), "does not include") {
		t.Errorf("free owner: %v", err)
	}
	r.capErr = errors.New("x")
	if err := r.m.Allowed("acct"); err == nil {
		t.Error("unknown plan allowed")
	}
	r.capErr = nil
	r.eng.pingErr = errors.New("connection refused")
	r.m.setup(context.Background())
	if err := r.m.Allowed("acct"); err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Errorf("docker down: %v", err)
	}
}

func TestSetupNeedsTheDataMount(t *testing.T) {
	r := newRig(t)
	r.eng.mounts = nil
	if r.m.setup(context.Background()) {
		t.Fatal("setup succeeded without knowing where the data lives")
	}
	r.m.cfg.DataMount = "volume:sitebin_data"
	if !r.m.setup(context.Background()) || r.m.mount != (dataMount{"volume", "sitebin_data"}) {
		t.Errorf("override: %v %+v", r.m.setupErr, r.m.mount)
	}
	r.m.cfg.DataMount = "relative/path"
	if r.m.setup(context.Background()) {
		t.Error("a relative data mount was accepted")
	}
}

func TestLogs(t *testing.T) {
	r := newRig(t)
	r.sites.add(siteA, "acct", example)
	r.reconcile(t, siteA, true)
	out, err := r.m.Logs(context.Background(), siteA, "app", 10)
	if err != nil || !strings.Contains(out, "sb-"+siteA+"-app") {
		t.Errorf("logs = %q, %v", out, err)
	}
	if _, err := r.m.Logs(context.Background(), siteA, "nope", 10); err == nil {
		t.Error("logs of an unknown service")
	}
	if _, err := r.m.Logs(context.Background(), siteA, "../etc", 10); err == nil {
		t.Error("a hostile service name reached the engine")
	}
}

func TestDemux(t *testing.T) {
	frame := func(stream byte, s string) []byte {
		h := []byte{stream, 0, 0, 0, 0, 0, 0, byte(len(s))}
		return append(h, s...)
	}
	in := append(frame(1, "out\n"), frame(2, "err\n")...)
	got, err := demux(strings.NewReader(string(in)))
	if err != nil || got != "out\nerr\n" {
		t.Errorf("demux = %q, %v", got, err)
	}
}
