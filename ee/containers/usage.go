//go:build ee

package containers

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ittrail/sitebin.io/internal/ext"
)

// Usage: what every running container uses — memory, CPU, processes, and its
// PUBLIC traffic. A service's interface on its project network carries both
// its visitors' requests (from Sitebin's proxy) and what it exchanges with
// the project's other services, so public traffic is taken where the two are
// apart: Sitebin's own interface on the project network (the visitors) and
// the service's egress interface (the internet). Kept in memory only. See
// docs/superpowers/specs/2026-10-25-container-usage-design.md.

// Timings. Vars so the tests can shorten them.
var (
	sampleEvery   = 10 * time.Second
	usageMaxAge   = 60 * time.Second
	statsTimeout  = 5 * time.Second
	statsParallel = 4
)

// ifaceReader reads Sitebin's own network interfaces' counters, by MAC
// address.
type ifaceReader interface {
	Counters() (map[string]IfCounters, error)
}

// sysNet reads /sys/class/net inside Sitebin's container.
type sysNet struct{ root string }

func (s sysNet) Counters() (map[string]IfCounters, error) {
	entries, err := os.ReadDir(s.root)
	if err != nil {
		return nil, err
	}
	out := map[string]IfCounters{}
	for _, e := range entries {
		dir := filepath.Join(s.root, e.Name())
		mac, err := os.ReadFile(filepath.Join(dir, "address"))
		if err != nil {
			continue
		}
		rx, err1 := readUint(filepath.Join(dir, "statistics", "rx_bytes"))
		tx, err2 := readUint(filepath.Join(dir, "statistics", "tx_bytes"))
		if err1 != nil || err2 != nil {
			continue
		}
		out[strings.ToLower(strings.TrimSpace(string(mac)))] = IfCounters{Rx: rx, Tx: tx}
	}
	return out, nil
}

func readUint(path string) (uint64, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	return strconv.ParseUint(strings.TrimSpace(string(b)), 10, 64)
}

// rawSample is one container's counters at one moment.
type rawSample struct {
	at  time.Time
	cpu uint64
	net map[string]IfCounters
}

// measured is one running container as last measured.
type measured struct {
	mem    int64
	pids   int
	cpu    *float64
	ifaces map[string]*ext.Traffic // rates by interface; nil entry = no rate yet
}

type siteMeasure struct {
	at       time.Time
	services map[string]measured
	web      *ext.Traffic // Sitebin's traffic with the project; nil = no rate yet
	hasWeb   bool         // Sitebin's interface on the project network was found
}

type usageBook struct {
	mu      sync.Mutex
	prev    map[string]rawSample // by container name
	prevWeb map[string]rawSample // by site: Sitebin's interface on its network
	sites   map[string]*siteMeasure
}

func newUsageBook() *usageBook {
	return &usageBook{prev: map[string]rawSample{}, prevWeb: map[string]rawSample{}, sites: map[string]*siteMeasure{}}
}

// sampleLoop measures every running container until ctx ends.
func (m *Manager) sampleLoop(ctx context.Context) {
	t := time.NewTicker(sampleEvery)
	defer t.Stop()
	for {
		m.sample(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// sample takes one measurement of every running managed container and of
// Sitebin's own interfaces, and turns them into figures against the last.
func (m *Manager) sample(ctx context.Context) {
	list, err := m.eng.ContainerList(ctx, labelManaged+"=true")
	if err != nil {
		slog.Warn("containers: usage: list containers", "err", err)
		return
	}
	type got struct {
		site, service, name string
		st                  Stats
	}
	var (
		mu      sync.Mutex
		results []got
		wg      sync.WaitGroup
		sem     = make(chan struct{}, statsParallel)
	)
	for _, c := range list {
		site, service := c.Labels[labelSite], c.Labels[labelService]
		if c.State != "running" || site == "" || service == "" {
			continue
		}
		name := containerName(site, service)
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer func() { <-sem; wg.Done() }()
			cctx, cancel := context.WithTimeout(ctx, statsTimeout)
			defer cancel()
			st, err := m.eng.ContainerStats(cctx, name)
			if err != nil {
				slog.Debug("containers: usage: stats", "container", name, "err", err)
				return
			}
			mu.Lock()
			results = append(results, got{site, service, name, st})
			mu.Unlock()
		}()
	}
	wg.Wait()
	web := m.webCounters(ctx)
	now := m.now()

	b := m.usage
	b.mu.Lock()
	defer b.mu.Unlock()
	prev, prevWeb := b.prev, b.prevWeb
	b.prev, b.prevWeb = map[string]rawSample{}, map[string]rawSample{}
	sites := map[string]*siteMeasure{}
	for _, r := range results {
		at := r.st.Read
		if at.IsZero() {
			at = now
		}
		cur := rawSample{at: at, cpu: r.st.CPU.Usage.Total, net: r.st.Networks}
		b.prev[r.name] = cur
		mem := int64(r.st.Memory.Usage)
		inactive := r.st.Memory.Stats["inactive_file"]
		if inactive == 0 {
			inactive = r.st.Memory.Stats["total_inactive_file"]
		}
		if int64(inactive) < mem {
			mem -= int64(inactive)
		}
		ms := measured{mem: mem, pids: int(r.st.Pids.Current), ifaces: map[string]*ext.Traffic{}}
		p, seen := prev[r.name]
		dt := cur.at.Sub(p.at).Seconds()
		ok := seen && dt > 0
		if ok && cur.cpu >= p.cpu {
			cores := float64(cur.cpu-p.cpu) / 1e9 / dt
			ms.cpu = &cores
		}
		for name, c := range cur.net {
			if pc, had := p.net[name]; ok && had {
				ms.ifaces[name] = rates(pc, c, dt)
			} else {
				ms.ifaces[name] = nil
			}
		}
		s := sites[r.site]
		if s == nil {
			s = &siteMeasure{at: now, services: map[string]measured{}}
			sites[r.site] = s
		}
		s.services[r.service] = ms
	}
	for site, s := range sites {
		c, found := web[site]
		if !found {
			continue
		}
		s.hasWeb = true
		cur := rawSample{at: now, net: map[string]IfCounters{"": c}}
		b.prevWeb[site] = cur
		if p, seen := prevWeb[site]; seen {
			if dt := now.Sub(p.at).Seconds(); dt > 0 {
				// Sitebin sends its visitors' requests in, and receives the
				// project's responses out.
				if r := rates(p.net[""], c, dt); r != nil {
					s.web = &ext.Traffic{In: r.Out, Out: r.In}
				}
			}
		}
	}
	b.sites = sites
}

// rates are the per-second deltas between two counters as In (received) and
// Out (sent); nil when a counter went backwards (the interface was made
// again).
func rates(prev, cur IfCounters, dt float64) *ext.Traffic {
	if cur.Rx < prev.Rx || cur.Tx < prev.Tx || dt <= 0 {
		return nil
	}
	return &ext.Traffic{In: float64(cur.Rx-prev.Rx) / dt, Out: float64(cur.Tx-prev.Tx) / dt}
}

// webCounters are Sitebin's own counters on each project network, by site.
func (m *Manager) webCounters(ctx context.Context) map[string]IfCounters {
	m.mu.Lock()
	self := m.self
	m.mu.Unlock()
	if self == "" || m.ifaces == nil {
		return nil
	}
	in, err := m.eng.ContainerInspect(ctx, self)
	if err != nil {
		slog.Debug("containers: usage: inspect Sitebin", "err", err)
		return nil
	}
	byMAC, err := m.ifaces.Counters()
	if err != nil {
		slog.Debug("containers: usage: own interfaces", "err", err)
		return nil
	}
	out := map[string]IfCounters{}
	for name, ep := range in.NetworkSettings.Networks {
		site, ok := strings.CutPrefix(name, "sb-")
		if !ok || strings.HasSuffix(site, "-egress") {
			continue
		}
		if c, ok := byMAC[strings.ToLower(ep.MacAddress)]; ok {
			out[site] = c
		}
	}
	return out
}

// Usage is what the site's running services use, as last measured, against
// the limits applied to them.
func (m *Manager) Usage(viewID string) (ext.ContainerUsage, bool) {
	m.usage.mu.Lock()
	s := m.usage.sites[viewID]
	m.usage.mu.Unlock()
	if s == nil || m.now().Sub(s.at) > usageMaxAge {
		return ext.ContainerUsage{}, false
	}
	cs, err := m.sites.ContainerSite(viewID)
	if err != nil {
		if !errors.Is(err, ext.ErrSiteGone) {
			slog.Error("containers: usage: read site", "site", viewID, "err", err)
		}
		return ext.ContainerUsage{}, false
	}
	var webServices []string
	for _, sv := range cs.Observed.Services {
		if len(sv.Ports) > 0 {
			webServices = append(webServices, sv.Name)
		}
	}
	out := ext.ContainerUsage{At: s.at}
	if len(webServices) > 1 && s.web != nil {
		w := *s.web
		out.Web = &w
	}
	for _, sv := range cs.Observed.Services {
		ms, running := s.services[sv.Name]
		if !running {
			continue
		}
		u := ext.ServiceUsage{
			Name: sv.Name, MemoryBytes: ms.mem, MemoryLimitBytes: int64(sv.Limits.MemoryMB) << 20,
			CPU: ms.cpu, CPULimit: sv.Limits.CPUs, Pids: ms.pids, PidsLimit: sv.Limits.Pids,
		}
		// Public traffic is the sum of the parts this service has; it is
		// known once every part has a rate.
		in, outB, known := 0.0, 0.0, true
		add := func(t *ext.Traffic) {
			if t == nil {
				known = false
				return
			}
			in += t.In
			outB += t.Out
		}
		if t, ok := ms.ifaces[egressIf]; ok {
			add(t)
		} else if t, ok := ms.ifaces[legacyEgressIf]; ok && sv.Egress {
			add(t)
		}
		if len(webServices) == 1 && webServices[0] == sv.Name {
			if s.hasWeb {
				add(s.web)
			} else {
				known = false
			}
		}
		if known {
			u.In, u.Out = &in, &outB
		}
		out.Services = append(out.Services, u)
	}
	return out, true
}
