//go:build ee

package containers

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/ittrail/sitebin.io/internal/ext"
	"github.com/ittrail/sitebin.io/internal/store"
)

// startExample runs the example project (app + db) and returns the rig.
func startExample(t *testing.T, own map[string]ext.ContainerLimits) *rig {
	t.Helper()
	r := newRig(t)
	r.sites.add(siteA, "acct", example).Limits = own
	r.reconcile(t, siteA, true)
	if st := r.sites.get(siteA).Observed.Status; st != store.ContainerRunning {
		t.Fatalf("project not running: %s", st)
	}
	return r
}

func (r *rig) hostConfig(service string) HostConfig {
	r.eng.mu.Lock()
	defer r.eng.mu.Unlock()
	return r.eng.containers["sb-"+siteA+"-"+service].body.HostConfig
}

func (r *rig) observedLimits(t *testing.T, service string) ext.ContainerLimits {
	t.Helper()
	for _, sv := range r.sites.get(siteA).Observed.Services {
		if sv.Name == service {
			return sv.Limits
		}
	}
	t.Fatalf("no observed service %s", service)
	return ext.ContainerLimits{}
}

func TestOwnLimitsAtCreation(t *testing.T) {
	r := startExample(t, map[string]ext.ContainerLimits{"db": {MemoryMB: 1024}})
	if hc := r.hostConfig("db"); hc.Memory != 1024<<20 || hc.MemorySwap != hc.Memory || hc.NanoCpus != 5e8 || hc.PidsLimit != 256 {
		t.Errorf("db host config = %+v", hc)
	}
	if hc := r.hostConfig("app"); hc.Memory != 512<<20 {
		t.Errorf("app memory = %d", hc.Memory)
	}
	if got := r.observedLimits(t, "db"); got != (ext.ContainerLimits{MemoryMB: 1024, CPUs: 0.5, Pids: 256}) {
		t.Errorf("db applied = %+v", got)
	}
	if got := r.observedLimits(t, "app"); got != envLimits {
		t.Errorf("app applied = %+v", got)
	}
	// Nothing differs, so the tick has nothing to change.
	r.reconcile(t, siteA, false)
	if len(r.eng.updates) != 0 {
		t.Errorf("updates on a converged project: %v", r.eng.updates)
	}
}

func TestDefaultsChangeUpdatesInPlace(t *testing.T) {
	r := startExample(t, nil)
	created := r.eng.created
	if err := r.m.Limits().SetDefaults(ext.ContainerLimits{CPUs: 1}); err != nil {
		t.Fatal(err)
	}
	r.reconcile(t, siteA, false)
	if len(r.eng.updates) != 2 || r.eng.created != created {
		t.Fatalf("updates %v, created %d -> %d: the change must not recreate", r.eng.updates, created, r.eng.created)
	}
	if hc := r.hostConfig("app"); hc.NanoCpus != 1e9 || hc.Memory != 512<<20 || hc.PidsLimit != 256 {
		t.Errorf("app host config = %+v", hc)
	}
	if got := r.observedLimits(t, "db"); got.CPUs != 1 {
		t.Errorf("db applied = %+v", got)
	}
	if st := r.sites.get(siteA).Observed.Status; st != store.ContainerRunning {
		t.Errorf("status = %s", st)
	}
	r.reconcile(t, siteA, false)
	if len(r.eng.updates) != 2 {
		t.Errorf("a converged project was updated again: %v", r.eng.updates)
	}
}

func TestOwnLimitsChangeUpdatesOnlyThatService(t *testing.T) {
	r := startExample(t, nil)
	r.sites.update(siteA, func(cs *ext.ContainerSite) {
		cs.Limits = map[string]ext.ContainerLimits{"db": {MemoryMB: 2048, Pids: 512}}
	})
	r.reconcile(t, siteA, false)
	if !slices.Equal(r.eng.updates, []string{"sb-" + siteA + "-db"}) {
		t.Fatalf("updates = %v", r.eng.updates)
	}
	if hc := r.hostConfig("db"); hc.Memory != 2048<<20 || hc.MemorySwap != 2048<<20 || hc.PidsLimit != 512 || hc.NanoCpus != 5e8 {
		t.Errorf("db host config = %+v", hc)
	}
}

// Containers that ran before limits were recorded are updated to their
// effective values once (on app.sitebin.io a no-op) and never recreated.
func TestLegacyContainerIsUpdatedNotRecreated(t *testing.T) {
	r := startExample(t, nil)
	r.sites.update(siteA, func(cs *ext.ContainerSite) {
		for i := range cs.Observed.Services {
			cs.Observed.Services[i].Limits = ext.ContainerLimits{}
		}
	})
	created := r.eng.created
	r.reconcile(t, siteA, false)
	if len(r.eng.updates) != 2 || r.eng.created != created {
		t.Fatalf("updates %v, created %d -> %d", r.eng.updates, created, r.eng.created)
	}
	if got := r.observedLimits(t, "app"); got != envLimits {
		t.Errorf("app applied = %+v", got)
	}
	if hc := r.hostConfig("app"); hc.Memory != 512<<20 || hc.NanoCpus != 5e8 || hc.PidsLimit != 256 {
		t.Errorf("app host config = %+v", hc)
	}
}

func TestLimitUpdateFailureKeepsTheProjectRunning(t *testing.T) {
	r := startExample(t, nil)
	r.eng.updateErr = errors.New("docker: 500 cgroup busy")
	r.m.Limits().SetDefaults(ext.ContainerLimits{MemoryMB: 1024})
	r.reconcile(t, siteA, false)
	if st := r.sites.get(siteA).Observed.Status; st != store.ContainerRunning {
		t.Errorf("status after a failed update = %s", st)
	}
	if _, ok := r.eng.containers["sb-"+siteA+"-app"]; !ok {
		t.Fatal("a failed update tore the project down")
	}
	if r.m.UpdateError(siteA) == "" {
		t.Error("the failure is not reported")
	}

	// Not retried on every tick...
	r.eng.updateErr = nil
	r.reconcile(t, siteA, false)
	if len(r.eng.updates) != 0 {
		t.Errorf("retried before the pause: %v", r.eng.updates)
	}
	// ...but at once when the site is kicked (the console saves and kicks).
	r.m.Kick(siteA)
	r.reconcile(t, siteA, true)
	if len(r.eng.updates) != 2 || r.m.UpdateError(siteA) != "" {
		t.Errorf("retry: updates %v, error %q", r.eng.updates, r.m.UpdateError(siteA))
	}
	if got := r.observedLimits(t, "db"); got.MemoryMB != 1024 {
		t.Errorf("db applied = %+v", got)
	}
}

func TestLimitUpdateOfAVanishedContainerReapplies(t *testing.T) {
	r := startExample(t, nil)
	created := r.eng.created
	delete(r.eng.containers, "sb-"+siteA+"-app")
	r.m.Limits().SetDefaults(ext.ContainerLimits{Pids: 512})
	r.reconcile(t, siteA, false)
	if r.eng.created != created+2 {
		t.Fatalf("created %d -> %d: the project was not started again", created, r.eng.created)
	}
	if hc := r.hostConfig("app"); hc.PidsLimit != 512 {
		t.Errorf("re-created app pids = %d", hc.PidsLimit)
	}
	if st := r.sites.get(siteA).Observed.Status; st != store.ContainerRunning {
		t.Errorf("status = %s", st)
	}
}

// The full scan's heal of a vanished container starts the project again with
// the site's own limits, not the defaults.
func TestRefreshReapplyKeepsOwnLimits(t *testing.T) {
	r := startExample(t, map[string]ext.ContainerLimits{"db": {MemoryMB: 1024}})
	created := r.eng.created
	delete(r.eng.containers, "sb-"+siteA+"-app")
	r.m.fullScan(context.Background())
	if r.eng.created == created {
		t.Fatal("the scan did not heal the project")
	}
	if hc := r.hostConfig("db"); hc.Memory != 1024<<20 {
		t.Errorf("re-created db memory = %d", hc.Memory)
	}
}

func TestNoUpdatesWhileDefaultsAreUnknown(t *testing.T) {
	r := startExample(t, nil)
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "containers"), 0o755)
	os.WriteFile(filepath.Join(dir, "containers", "limits.json"), []byte("{broken"), 0o644)
	r.m.limits = NewLimitBook(dir, envLimits)
	r.sites.update(siteA, func(cs *ext.ContainerSite) {
		cs.Limits = map[string]ext.ContainerLimits{"db": {MemoryMB: 2048}}
	})
	r.reconcile(t, siteA, false)
	if len(r.eng.updates) != 0 {
		t.Errorf("updated while the defaults are unknown: %v", r.eng.updates)
	}
}

func TestSetupReadsHostCapacity(t *testing.T) {
	r := newRig(t)
	r.eng.info = EngineInfo{NCPU: 6, MemTotal: 16 << 30}
	if !r.m.setup(context.Background()) {
		t.Fatal(r.m.setupErr)
	}
	if c, m := r.m.Limits().Capacity(); c != 6 || m != 16<<30 {
		t.Errorf("capacity = %d, %d", c, m)
	}
	// An Engine that does not say is no reason not to run.
	r.eng.infoErr = errors.New("no info")
	if !r.m.setup(context.Background()) {
		t.Error("setup failed on /info")
	}
}

// The egress interface is named, so the service's own stats say which
// counters are its internet traffic.
func TestEgressInterfaceIsNamed(t *testing.T) {
	r := startExample(t, nil)
	app := r.eng.containers["sb-"+siteA+"-app"]
	if got := r.eng.ifnames[egressName(siteA)+"|"+app.id]; got != "egress0" {
		t.Errorf("egress ifname = %q", got)
	}
	if got := r.eng.ifnames[networkName(siteA)+"|self-sitebin"]; got != "" {
		t.Errorf("Sitebin's own attachment was named %q", got)
	}
}
