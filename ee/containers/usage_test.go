//go:build ee

package containers

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/ittrail/sitebin.io/internal/ext"
)

// fakeIfaces is Sitebin's own /sys/class/net: counters by MAC address.
type fakeIfaces struct {
	mu sync.Mutex
	m  map[string]IfCounters
}

func (f *fakeIfaces) Counters() (map[string]IfCounters, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]IfCounters{}
	for k, v := range f.m {
		out[k] = v
	}
	return out, nil
}

const sitebinMAC = "02:42:ac:11:00:09"

// usageRig runs the example (app: HTTP + egress; db: neither) and gives the
// sampler a clock, Sitebin's interface on the project network, and counters.
type usageRig struct {
	*rig
	clock  time.Time
	ifaces *fakeIfaces
}

func newUsageRig(t *testing.T) *usageRig {
	t.Helper()
	r := startExample(t, nil)
	u := &usageRig{rig: r, clock: time.Date(2026, 10, 25, 9, 0, 0, 0, time.UTC), ifaces: &fakeIfaces{m: map[string]IfCounters{}}}
	r.m.now = func() time.Time { return u.clock }
	r.m.ifaces = u.ifaces
	r.eng.selfNets = map[string]Endpoint{networkName(siteA): {MacAddress: sitebinMAC}}
	return u
}

// set gives a container its counters at the rig's clock.
func (u *usageRig) set(service string, mem, cpu, pids uint64, nets map[string]IfCounters) {
	st := Stats{Read: u.clock, Networks: nets}
	st.Memory.Usage = mem
	st.Memory.Stats = map[string]uint64{"inactive_file": 1 << 20}
	st.CPU.Usage.Total = cpu
	st.Pids.Current = pids
	u.eng.mu.Lock()
	u.eng.stats["sb-"+siteA+"-"+service] = st
	u.eng.mu.Unlock()
}

func (u *usageRig) web(in, out uint64) {
	u.ifaces.mu.Lock()
	u.ifaces.m[sitebinMAC] = IfCounters{Rx: out, Tx: in} // Sitebin sends the requests in, receives the responses
	u.ifaces.mu.Unlock()
}

func (u *usageRig) usage(t *testing.T) (ext.ContainerUsage, map[string]ext.ServiceUsage) {
	t.Helper()
	cu, ok := u.m.Usage(siteA)
	if !ok {
		t.Fatal("no usage")
	}
	by := map[string]ext.ServiceUsage{}
	for _, s := range cu.Services {
		by[s.Name] = s
	}
	return cu, by
}

func eq(p *float64, want float64) bool { return p != nil && *p > want-1e-6 && *p < want+1e-6 }

// first is the sample every test starts from.
func (u *usageRig) first() {
	u.set("app", 100<<20, 1e9, 10, map[string]IfCounters{"eth0": {Rx: 500, Tx: 500}, "egress0": {Rx: 1000, Tx: 2000}})
	u.set("db", 300<<20, 5e9, 30, map[string]IfCounters{"eth0": {Rx: 9000, Tx: 9000}})
	u.web(10000, 20000)
	u.m.sample(context.Background())
}

func TestUsageFromTwoSamples(t *testing.T) {
	u := newUsageRig(t)
	u.first()
	u.clock = u.clock.Add(10 * time.Second)
	// app: 0.2 cores, 1000/2000 B/s to and from the internet; the project's
	// visitors 5000 B/s in, 10000 B/s out; db talks to app a lot, which is
	// not public.
	u.set("app", 129<<20, 3e9, 12, map[string]IfCounters{"eth0": {Rx: 900500, Tx: 900500}, "egress0": {Rx: 11000, Tx: 22000}})
	u.set("db", 301<<20, 6e9, 31, map[string]IfCounters{"eth0": {Rx: 909000, Tx: 909000}})
	u.web(60000, 120000)
	u.m.sample(context.Background())

	cu, by := u.usage(t)
	app, db := by["app"], by["db"]
	if !cu.At.Equal(u.clock) || cu.Web != nil {
		t.Errorf("at %v, project web %+v (one web service: it is app's)", cu.At, cu.Web)
	}
	if app.MemoryBytes != 128<<20 || app.MemoryLimitBytes != 512<<20 || !eq(app.CPU, 0.2) || app.CPULimit != 0.5 || app.Pids != 12 || app.PidsLimit != 256 {
		t.Errorf("app = %+v cpu %v", app, app.CPU)
	}
	if !eq(app.In, 1000+5000) || !eq(app.Out, 2000+10000) {
		t.Errorf("app public in %v out %v, want 6000 / 12000", deref(app.In), deref(app.Out))
	}
	if !eq(db.CPU, 0.1) || !eq(db.In, 0) || !eq(db.Out, 0) {
		t.Errorf("db cpu %v in %v out %v: a service with no public path has no public traffic", deref(db.CPU), deref(db.In), deref(db.Out))
	}
}

func deref(p *float64) any {
	if p == nil {
		return nil
	}
	return *p
}

func TestFirstSampleHasNoRates(t *testing.T) {
	u := newUsageRig(t)
	if _, ok := u.m.Usage(siteA); ok {
		t.Error("usage before any sample")
	}
	u.first()
	_, by := u.usage(t)
	app := by["app"]
	if app.MemoryBytes != 99<<20 || app.CPU != nil || app.In != nil || app.Out != nil {
		t.Errorf("first sample: %+v", app)
	}
}

func TestCounterResetGivesNoRate(t *testing.T) {
	u := newUsageRig(t)
	u.first()
	u.clock = u.clock.Add(10 * time.Second)
	// The app's container was recreated: its counters start again.
	u.set("app", 50<<20, 1e6, 3, map[string]IfCounters{"eth0": {Rx: 10, Tx: 10}, "egress0": {Rx: 5, Tx: 5}})
	u.set("db", 301<<20, 6e9, 31, map[string]IfCounters{"eth0": {Rx: 9000, Tx: 9000}})
	u.web(60000, 120000)
	u.m.sample(context.Background())
	_, by := u.usage(t)
	if app := by["app"]; app.CPU != nil || app.In != nil || app.Out != nil {
		t.Errorf("after a reset: cpu %v in %v out %v", deref(app.CPU), deref(app.In), deref(app.Out))
	}
	// The next sample has rates again.
	u.clock = u.clock.Add(10 * time.Second)
	u.set("app", 50<<20, 1e9+1e6, 3, map[string]IfCounters{"eth0": {Rx: 10, Tx: 10}, "egress0": {Rx: 10005, Tx: 5}})
	u.set("db", 301<<20, 6e9, 31, map[string]IfCounters{"eth0": {Rx: 9000, Tx: 9000}})
	u.m.sample(context.Background())
	if _, by := u.usage(t); !eq(by["app"].CPU, 0.1) || !eq(by["app"].In, 1000) {
		t.Errorf("after the reset: %+v", by["app"])
	}
}

// With two services serving HTTP the visitors' traffic cannot be given to
// one of them: it is the project's, and each service keeps only its own
// internet traffic.
func TestWebTrafficOfSeveralWebServicesStaysWithTheProject(t *testing.T) {
	u := newUsageRig(t)
	u.sites.update(siteA, func(cs *ext.ContainerSite) {
		cs.Observed.Services[1].Ports = []int{8080} // db now serves HTTP too
	})
	u.first()
	u.clock = u.clock.Add(10 * time.Second)
	u.set("app", 100<<20, 1e9, 10, map[string]IfCounters{"eth0": {Rx: 500, Tx: 500}, "egress0": {Rx: 1000, Tx: 2000}})
	u.set("db", 300<<20, 5e9, 30, map[string]IfCounters{"eth0": {Rx: 9000, Tx: 9000}})
	u.web(60000, 120000)
	u.m.sample(context.Background())
	cu, by := u.usage(t)
	if cu.Web == nil || cu.Web.In != 5000 || cu.Web.Out != 10000 {
		t.Errorf("project web = %+v", cu.Web)
	}
	if !eq(by["app"].In, 0) || !eq(by["db"].In, 0) {
		t.Errorf("a service got the project's web traffic: app %v db %v", deref(by["app"].In), deref(by["db"].In))
	}
}

func TestSampleSurvivesAFailingContainer(t *testing.T) {
	u := newUsageRig(t)
	u.eng.statsErr["sb-"+siteA+"-db"] = errors.New("docker: 500")
	u.first()
	_, by := u.usage(t)
	if _, ok := by["db"]; ok {
		t.Error("a container whose stats failed has figures")
	}
	if by["app"].MemoryBytes == 0 {
		t.Error("the other container was not sampled")
	}
	// Figures too old are not shown.
	u.clock = u.clock.Add(61 * time.Second)
	if _, ok := u.m.Usage(siteA); ok {
		t.Error("stale figures are shown")
	}
}

// A container created before its egress interface was named has it as eth1.
func TestLegacyEgressInterface(t *testing.T) {
	u := newUsageRig(t)
	u.set("app", 100<<20, 1e9, 10, map[string]IfCounters{"eth0": {}, "eth1": {Rx: 1000, Tx: 2000}})
	u.set("db", 300<<20, 5e9, 30, map[string]IfCounters{"eth0": {}})
	u.web(0, 0)
	u.m.sample(context.Background())
	u.clock = u.clock.Add(10 * time.Second)
	u.set("app", 100<<20, 1e9, 10, map[string]IfCounters{"eth0": {}, "eth1": {Rx: 3000, Tx: 4000}})
	u.set("db", 300<<20, 5e9, 30, map[string]IfCounters{"eth0": {Rx: 70000}})
	u.m.sample(context.Background())
	_, by := u.usage(t)
	if !eq(by["app"].In, 200) || !eq(by["app"].Out, 200) {
		t.Errorf("legacy egress: in %v out %v", deref(by["app"].In), deref(by["app"].Out))
	}
	if !eq(by["db"].In, 0) {
		t.Errorf("db (no egress) took eth1 or eth0 as public: %v", deref(by["db"].In))
	}
}
