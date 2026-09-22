//go:build ee

package containers

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ittrail/sitebin.io/internal/ext"
	"github.com/ittrail/sitebin.io/internal/store"
)

// Config is the instance's container configuration (SITEBIN_CONTAINERS_*).
type Config struct {
	DockerHost string
	// DataDir is Sitebin's data directory as Sitebin sees it (/data).
	DataDir string
	// DataMount overrides where DataDir lives for Docker: a host path, or
	// "volume:<name>". Empty means: inspect Sitebin's own container.
	DataMount string
	// Self overrides Sitebin's own container id. Empty means: detect it.
	Self     string
	Runtime  string
	MemoryMB int
	CPUs     float64
	Pids     int
}

// engine is the part of the Docker Engine the manager uses; *Client
// implements it, and the tests fake it.
type engine interface {
	Ping(ctx context.Context) error
	ImageExists(ctx context.Context, ref string) (bool, error)
	Pull(ctx context.Context, repo, digest string) error
	ContainerList(ctx context.Context, labels ...string) ([]ContainerSummary, error)
	ContainerCreate(ctx context.Context, name string, body CreateBody) (string, error)
	ContainerStart(ctx context.Context, id string) error
	ContainerRemove(ctx context.Context, id string) error
	ContainerInspect(ctx context.Context, id string) (Inspect, error)
	ContainerLogs(ctx context.Context, id string, tail int) (string, error)
	NetworkList(ctx context.Context, labels ...string) ([]NetworkSummary, error)
	NetworkExists(ctx context.Context, name string) (bool, error)
	NetworkCreate(ctx context.Context, name string, internal bool, labels, options map[string]string) error
	NetworkRemove(ctx context.Context, name string) error
	NetworkConnect(ctx context.Context, network, container string, aliases []string) error
	NetworkDisconnect(ctx context.Context, network, container string) error
}

// Labels every object the manager creates carries. Orphan cleanup finds its
// own objects by them and touches nothing else on the host.
const (
	labelManaged = "io.sitebin.managed"
	labelSite    = "io.sitebin.site"
	labelService = "io.sitebin.service"
)

// Timings. Vars so the tests can shorten them.
var (
	tickEvery      = 5 * time.Second
	scanEvery      = 60 * time.Second
	quotaEvery     = 5 * time.Minute
	retryAfter     = 60 * time.Second
	setupRetry     = 30 * time.Second
	applyTimeout   = 10 * time.Minute
	stopTimeout    = 2 * time.Minute
	maxLogTailSize = 1000
)

func containerName(site, service string) string { return "sb-" + site + "-" + service }
func networkName(site string) string            { return "sb-" + site }
func egressName(site string) string             { return "sb-" + site + "-egress" }

// Manager runs container sites: a reconciler that converges the Docker
// Engine to the desired state the core records, and reports what it saw.
// It is the only writer of a site's observed container state.
type Manager struct {
	cfg    Config
	eng    engine
	sites  ext.SiteService
	capFor func(owner string) (int, error)
	uid    int
	gid    int
	now    func() time.Time

	mu        sync.Mutex
	setupErr  error // nil once Sitebin's own container and data mount are known
	self      string
	mount     dataMount
	known     map[string]bool
	busy      map[string]bool
	retryAt   map[string]time.Time
	locks     map[string]*sync.Mutex
	cancels   map[string]context.CancelFunc
	lastQuota time.Time
	kick      chan string
}

// New builds a manager. capFor answers an account's max_containers; an error
// means the plan is unknown right now, and nothing is started or stopped on
// that answer.
func New(cfg Config, sites ext.SiteService, capFor func(owner string) (int, error)) (*Manager, error) {
	c, err := NewClient(cfg.DockerHost)
	if err != nil {
		return nil, err
	}
	return newManager(cfg, c, sites, capFor), nil
}

func newManager(cfg Config, eng engine, sites ext.SiteService, capFor func(string) (int, error)) *Manager {
	uid, gid := os.Getuid(), os.Getgid()
	if uid < 0 { // Windows, where Sitebin is only ever developed
		uid, gid = 1000, 1000
	}
	return &Manager{
		cfg: cfg, eng: eng, sites: sites, capFor: capFor, uid: uid, gid: gid, now: time.Now,
		setupErr: errors.New("the container runtime is starting"),
		known:    map[string]bool{}, busy: map[string]bool{}, retryAt: map[string]time.Time{},
		locks: map[string]*sync.Mutex{}, cancels: map[string]context.CancelFunc{},
		kick: make(chan string, 64),
	}
}

// ---- ext.ContainerRuntime ----

// Allowed reports whether owner may use container mode.
func (m *Manager) Allowed(owner string) error {
	m.mu.Lock()
	serr := m.setupErr
	m.mu.Unlock()
	if serr != nil {
		return fmt.Errorf("containers are unavailable on this instance right now: %v", serr)
	}
	if owner == "" {
		return errors.New("container sites belong to an account")
	}
	n, err := m.capFor(owner)
	if err != nil {
		return errors.New("your plan could not be checked just now; try again in a moment")
	}
	if n <= 0 {
		return errors.New("your plan does not include containers; upgrade to run them")
	}
	return nil
}

// Kick converges viewID now.
func (m *Manager) Kick(viewID string) {
	m.mu.Lock()
	m.known[viewID] = true
	delete(m.retryAt, viewID)
	m.mu.Unlock()
	select {
	case m.kick <- viewID:
	default: // the next tick picks it up
	}
}

// Stop removes the site's containers and networks and records it stopped.
// An apply in flight for the site is cancelled first, so the caller does not
// wait out an image pull.
func (m *Manager) Stop(viewID string) error {
	m.mu.Lock()
	if cancel := m.cancels[viewID]; cancel != nil {
		cancel()
	}
	m.mu.Unlock()
	l := m.siteLock(viewID)
	l.Lock()
	defer l.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), stopTimeout)
	defer cancel()
	if err := m.teardown(ctx, viewID); err != nil {
		return err
	}
	cs, err := m.sites.ContainerSite(viewID)
	if err != nil {
		return nil // the site is gone; nothing left to record
	}
	obs := cs.Observed
	obs.Status, obs.Message = store.ContainerStopped, ""
	obs.Services = stateless(obs.Services, "")
	return m.sites.SetContainerState(viewID, obs)
}

var serviceRe = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)

// Logs returns the recent output of one service.
func (m *Manager) Logs(ctx context.Context, viewID, service string, tail int) (string, error) {
	if !serviceRe.MatchString(service) {
		return "", fmt.Errorf("no service %q", service)
	}
	if tail < 1 || tail > maxLogTailSize {
		tail = maxLogTailSize
	}
	out, err := m.eng.ContainerLogs(ctx, containerName(viewID, service), tail)
	if errors.Is(err, errNotFound) {
		return "", fmt.Errorf("service %q is not running", service)
	}
	return out, err
}

// ---- the loop ----

// Run reconciles until ctx ends.
func (m *Manager) Run(ctx context.Context) {
	for !m.setup(ctx) {
		select {
		case <-ctx.Done():
			return
		case <-time.After(setupRetry):
		}
	}
	m.fullScan(ctx)
	tick := time.NewTicker(tickEvery)
	scan := time.NewTicker(scanEvery)
	defer tick.Stop()
	defer scan.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case id := <-m.kick:
			m.spawn(ctx, id, true)
		case <-tick.C:
			m.mu.Lock()
			ids := make([]string, 0, len(m.known))
			for id := range m.known {
				ids = append(ids, id)
			}
			m.mu.Unlock()
			for _, id := range ids {
				m.spawn(ctx, id, false)
			}
		case <-scan.C:
			m.fullScan(ctx)
		}
	}
}

// spawn reconciles one site in its own goroutine, unless one already runs.
// An apply can pull an image for minutes; no other site waits for it.
func (m *Manager) spawn(ctx context.Context, id string, kicked bool) {
	m.mu.Lock()
	if m.busy[id] {
		m.mu.Unlock()
		if kicked { // try again once the running pass is done
			go func() { time.Sleep(time.Second); m.Kick(id) }()
		}
		return
	}
	m.busy[id] = true
	m.mu.Unlock()
	go func() {
		defer func() {
			m.mu.Lock()
			delete(m.busy, id)
			m.mu.Unlock()
		}()
		m.reconcileID(ctx, id, kicked)
	}()
}

// setup finds Sitebin's own container and where its data directory lives on
// the Docker host. Neither failing stops Sitebin: the mode reports it.
func (m *Manager) setup(ctx context.Context) bool {
	err := m.resolveSetup(ctx)
	m.mu.Lock()
	m.setupErr = err
	m.mu.Unlock()
	if err != nil {
		slog.Warn("containers: runtime not ready", "err", err)
		return false
	}
	slog.Info("containers: runtime ready", "self", m.self, "data", m.mount.describe())
	return true
}

func (m *Manager) resolveSetup(ctx context.Context) error {
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := m.eng.Ping(cctx); err != nil {
		return fmt.Errorf("the Docker Engine does not answer at %s: %v", m.cfg.DockerHost, err)
	}
	self := m.cfg.Self
	if self == "" {
		self = detectSelf()
	}
	if self == "" {
		return errors.New("Sitebin is not running in a container on this Docker Engine (set SITEBIN_CONTAINERS_SELF)")
	}
	in, err := m.eng.ContainerInspect(cctx, self)
	if err != nil {
		return fmt.Errorf("cannot inspect Sitebin's own container %q: %v", self, err)
	}
	mount, err := parseDataMount(m.cfg.DataMount)
	if err != nil {
		return err
	}
	if mount.kind == "" {
		for _, mt := range in.Mounts {
			if path.Clean(mt.Destination) == path.Clean(m.cfg.DataDir) {
				switch mt.Type {
				case "bind":
					mount = dataMount{kind: "bind", source: mt.Source}
				case "volume":
					mount = dataMount{kind: "volume", source: mt.Name}
				}
			}
		}
	}
	if mount.kind == "" {
		return fmt.Errorf("%s is not a bind mount or a named volume of Sitebin's container (set SITEBIN_CONTAINERS_DATA_MOUNT)", m.cfg.DataDir)
	}
	m.mu.Lock()
	m.self, m.mount = in.ID, mount
	m.mu.Unlock()
	return nil
}

// detectSelf finds this process's container id: /proc/self/mountinfo names
// it in the path of the /etc/hostname bind; the hostname is Docker's default
// otherwise.
func detectSelf() string {
	if f, err := os.Open("/proc/self/mountinfo"); err == nil {
		defer f.Close()
		re := regexp.MustCompile(`/containers/([0-9a-f]{64})/`)
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			if mm := re.FindStringSubmatch(sc.Text()); mm != nil {
				return mm[1]
			}
		}
	}
	h, _ := os.Hostname()
	return h
}

// dataMount is where Sitebin's data directory lives for Docker.
type dataMount struct {
	kind   string // bind or volume
	source string // host path, or volume name
}

func parseDataMount(s string) (dataMount, error) {
	switch {
	case s == "":
		return dataMount{}, nil
	case strings.HasPrefix(s, "volume:") && len(s) > len("volume:"):
		return dataMount{kind: "volume", source: strings.TrimPrefix(s, "volume:")}, nil
	case strings.HasPrefix(s, "/"):
		return dataMount{kind: "bind", source: s}, nil
	}
	return dataMount{}, fmt.Errorf("SITEBIN_CONTAINERS_DATA_MOUNT %q: use an absolute host path or volume:<name>", s)
}

func (d dataMount) describe() string { return d.kind + ":" + d.source }

// mountFor mounts rel (a path under the data directory) at target.
func (d dataMount) mountFor(rel, target string, ro bool) Mount {
	if d.kind == "volume" {
		return Mount{Type: "volume", Source: d.source, Target: target, ReadOnly: ro,
			VolumeOptions: &VolumeOptions{NoCopy: true, Subpath: rel}}
	}
	return Mount{Type: "bind", Source: strings.TrimSuffix(d.source, "/") + "/" + rel, Target: target, ReadOnly: ro}
}

func (m *Manager) siteLock(id string) *sync.Mutex {
	m.mu.Lock()
	defer m.mu.Unlock()
	l, ok := m.locks[id]
	if !ok {
		l = &sync.Mutex{}
		m.locks[id] = l
	}
	return l
}

// ---- reconciling one site ----

func composeHash(cs ext.ContainerSite) string {
	src := cs.Compose
	if cs.ComposeErr != "" {
		src = []byte("error:" + cs.ComposeErr)
	}
	sum := sha256.Sum256(src)
	return hex.EncodeToString(sum[:8])
}

func (m *Manager) reconcileID(ctx context.Context, id string, kicked bool) {
	l := m.siteLock(id)
	l.Lock()
	defer l.Unlock()
	cs, err := m.sites.ContainerSite(id)
	if errors.Is(err, ext.ErrSiteGone) {
		m.forget(id)
		m.teardownLogged(ctx, id)
		return
	}
	if err != nil {
		slog.Error("containers: read site", "site", id, "err", err)
		return
	}
	m.reconcile(ctx, cs, kicked, nil)
}

// reconcile converges one site. present is the site's containers when the
// caller already listed them (the full scan), or nil.
func (m *Manager) reconcile(ctx context.Context, cs ext.ContainerSite, kicked bool, present []ContainerSummary) {
	id := cs.ViewID
	if !cs.Container {
		m.forget(id)
		m.teardownLogged(ctx, id)
		return
	}
	obs := cs.Observed

	if !cs.Enabled || cs.Expired {
		// A stopped site leaves the tick: nothing about it changes until
		// someone presses Start, which kicks it back in. The full scan still
		// removes anything left of it.
		m.forget(id)
		msg := ""
		if cs.Enabled && cs.Expired {
			msg = "the site has expired"
		}
		if obs.Status != store.ContainerStopped || obs.Message != msg || len(present) > 0 {
			m.teardownLogged(ctx, id)
			obs.Status, obs.Message = store.ContainerStopped, msg
			obs.Services = stateless(obs.Services, "")
			m.report(id, obs)
		}
		return
	}
	m.mu.Lock()
	m.known[id] = true
	m.mu.Unlock()

	hash := composeHash(cs)
	if obs.AppliedHash == hash && obs.AppliedSeq == cs.RestartSeq {
		if present != nil && obs.Status == store.ContainerRunning {
			m.refresh(ctx, cs, present)
		}
		return
	}
	m.mu.Lock()
	wait := !kicked && m.now().Before(m.retryAt[id])
	m.mu.Unlock()
	if wait {
		return
	}
	actx, cancel := context.WithTimeout(ctx, applyTimeout)
	m.mu.Lock()
	m.cancels[id] = cancel
	m.mu.Unlock()
	defer func() {
		cancel()
		m.mu.Lock()
		delete(m.cancels, id)
		m.mu.Unlock()
	}()
	m.apply(actx, cs, hash)
}

func (m *Manager) forget(id string) {
	m.mu.Lock()
	delete(m.known, id)
	delete(m.retryAt, id)
	m.mu.Unlock()
}

func (m *Manager) report(id string, st ext.ContainerState) {
	if err := m.sites.SetContainerState(id, st); err != nil && !errors.Is(err, ext.ErrSiteGone) {
		slog.Error("containers: record state", "site", id, "err", err)
	}
}

// stateless copies services with State set to s.
func stateless(in []ext.ContainerService, s string) []ext.ContainerService {
	out := slices.Clone(in)
	for i := range out {
		out[i].State = s
	}
	return out
}

// apply (re)starts a project from its compose file.
//
// A failure the customer must fix — the file, the plan — is PERMANENT: it is
// recorded against this file and restart sequence, and not retried until
// one of them changes. Anything else — Docker, the plan lookup — is
// transient and retried after a pause.
func (m *Manager) apply(ctx context.Context, cs ext.ContainerSite, hash string) {
	id := cs.ViewID
	obs := cs.Observed
	fail := func(msg string, permanent bool) {
		st := ext.ContainerState{Status: store.ContainerError, Message: msg, AppliedHash: obs.AppliedHash, AppliedSeq: obs.AppliedSeq, AppliedAt: obs.AppliedAt}
		if permanent {
			st.AppliedHash, st.AppliedSeq = hash, cs.RestartSeq
		} else {
			m.mu.Lock()
			m.retryAt[id] = m.now().Add(retryAfter)
			m.mu.Unlock()
		}
		m.report(id, st)
		slog.Info("containers: not started", "site", id, "owner", cs.Owner, "permanent", permanent, "reason", msg)
	}
	m.mu.Lock()
	serr := m.setupErr
	m.mu.Unlock()
	if serr != nil {
		fail("containers are unavailable on this instance right now: "+serr.Error(), false)
		return
	}
	if cs.ComposeErr != "" {
		m.teardownLogged(ctx, id)
		fail(cs.ComposeErr, true)
		return
	}
	spec, err := Parse(cs.Compose)
	if err != nil {
		m.teardownLogged(ctx, id)
		fail(err.Error(), true)
		return
	}
	limit, err := m.capFor(cs.Owner)
	if err != nil {
		fail("your plan could not be checked just now; retrying", false)
		return
	}
	others, err := m.usedByOthers(cs.Owner, id)
	if err != nil {
		fail("could not count your other projects; retrying", false)
		return
	}
	if others+len(spec.Services) > limit {
		m.teardownLogged(ctx, id)
		fail(capMessage(limit, len(spec.Services), others), true)
		return
	}

	services := describe(id, spec)
	m.report(id, ext.ContainerState{Status: store.ContainerStarting, AppliedHash: obs.AppliedHash, AppliedSeq: obs.AppliedSeq, AppliedAt: obs.AppliedAt, Services: stateless(services, "created")})
	if err := m.deploy(ctx, id, spec); err != nil {
		m.teardownLogged(context.Background(), id)
		if ctx.Err() != nil {
			return // stopped or superseded while starting; Stop records the state
		}
		slog.Error("containers: deploy", "site", id, "owner", cs.Owner, "err", err)
		fail("could not start: "+err.Error(), false)
		return
	}
	warnings, err := m.sites.SyncContainerDomains(id, spec.CustomDomains())
	if err != nil {
		warnings = append(warnings, "custom domains: "+err.Error())
	}
	m.report(id, ext.ContainerState{
		Status: store.ContainerRunning, Message: strings.Join(warnings, "; "),
		AppliedHash: hash, AppliedSeq: cs.RestartSeq, AppliedAt: m.now().UTC(),
		Services: stateless(services, "running"),
	})
	m.mu.Lock()
	delete(m.retryAt, id)
	m.mu.Unlock()
	slog.Info("containers: started", "site", id, "owner", cs.Owner, "services", len(spec.Services))
}

func capMessage(limit, need, others int) string {
	if limit <= 0 {
		return "your plan does not include containers"
	}
	return fmt.Sprintf("your plan allows %d container(s) across all your projects; this project needs %d and your other projects use %d", limit, need, others)
}

// usedByOthers counts the services the owner runs in OTHER projects.
func (m *Manager) usedByOthers(owner, except string) (int, error) {
	all, err := m.sites.ContainerSites()
	if err != nil {
		return 0, err
	}
	n := 0
	for _, s := range all {
		if s.ViewID != except && s.Owner == owner && counts(s) {
			n += len(s.Observed.Services)
		}
	}
	return n, nil
}

// counts reports whether a site's services count against its owner's cap.
func counts(s ext.ContainerSite) bool {
	return s.Container && s.Enabled && !s.Expired &&
		(s.Observed.Status == store.ContainerRunning || s.Observed.Status == store.ContainerStarting)
}

// describe is the spec as the core records it.
func describe(id string, spec *Spec) []ext.ContainerService {
	out := make([]ext.ContainerService, 0, len(spec.Services))
	for _, sv := range spec.Services {
		d := ext.ContainerService{Name: sv.Name, Image: sv.Image.Name, Egress: sv.Egress, Host: containerName(id, sv.Name)}
		for _, v := range sv.Volumes {
			d.Volumes = append(d.Volumes, v.String())
		}
		for _, dm := range sv.Domains {
			d.Domains = append(d.Domains, ext.ContainerDomain{Domain: dm.Host, Port: dm.Port})
		}
		out = append(out, d)
	}
	return out
}

// deploy replaces the project's containers with ones built from spec.
func (m *Manager) deploy(ctx context.Context, id string, spec *Spec) error {
	for _, img := range uniqueImages(spec) {
		ok, err := m.eng.ImageExists(ctx, img.Ref())
		if err != nil {
			return err
		}
		if !ok {
			slog.Info("containers: pulling", "image", img.Display)
			if err := m.eng.Pull(ctx, img.Repo, img.Digest); err != nil {
				return err
			}
		}
	}
	if err := m.removeContainers(ctx, id); err != nil {
		return err
	}
	labels := map[string]string{labelManaged: "true", labelSite: id}
	net := networkName(id)
	if err := m.ensureNetwork(ctx, net, true, labels, nil); err != nil {
		return err
	}
	egress := slices.ContainsFunc(spec.Services, func(s Service) bool { return s.Egress })
	if egress {
		// No inter-container traffic on it: services talk over the internal
		// network, and this one exists only to reach out.
		if err := m.ensureNetwork(ctx, egressName(id), false, labels, map[string]string{"com.docker.network.bridge.enable_icc": "false"}); err != nil {
			return err
		}
	} else if err := m.eng.NetworkRemove(ctx, egressName(id)); err != nil {
		return err
	}
	m.mu.Lock()
	self, mount := m.self, m.mount
	m.mu.Unlock()
	if err := m.eng.NetworkConnect(ctx, net, self, nil); err != nil {
		return fmt.Errorf("attach Sitebin to the project network: %w", err)
	}
	for _, sv := range spec.Services {
		body, err := m.createBody(id, sv, mount)
		if err != nil {
			return err
		}
		cid, err := m.eng.ContainerCreate(ctx, containerName(id, sv.Name), body)
		if err != nil {
			return fmt.Errorf("service %s: %w", sv.Name, err)
		}
		if sv.Egress {
			if err := m.eng.NetworkConnect(ctx, egressName(id), cid, nil); err != nil {
				return fmt.Errorf("service %s: %w", sv.Name, err)
			}
		}
		if err := m.eng.ContainerStart(ctx, cid); err != nil {
			return fmt.Errorf("service %s: %w", sv.Name, err)
		}
	}
	return nil
}

func uniqueImages(spec *Spec) []Image {
	var out []Image
	for _, sv := range spec.Services {
		if !slices.ContainsFunc(out, func(i Image) bool { return i.Name == sv.Image.Name }) {
			out = append(out, sv.Image)
		}
	}
	return out
}

func (m *Manager) ensureNetwork(ctx context.Context, name string, internal bool, labels, options map[string]string) error {
	ok, err := m.eng.NetworkExists(ctx, name)
	if err != nil || ok {
		return err
	}
	return m.eng.NetworkCreate(ctx, name, internal, labels, options)
}

// createBody is one service's container: Sitebin's uid, no capabilities, no
// privilege escalation, a read-only root with bounded scratch space, the
// instance's fixed limits, capped logs, and no published port.
func (m *Manager) createBody(id string, sv Service, mount dataMount) (CreateBody, error) {
	img := sv.Image
	var mounts []Mount
	for _, v := range sv.Volumes {
		rel, err := m.sites.PrepareVolume(id, v.Folder)
		if err != nil {
			return CreateBody{}, fmt.Errorf("service %s: volume %s: %w", sv.Name, v.Folder, err)
		}
		mounts = append(mounts, mount.mountFor(rel, v.Target, v.ReadOnly))
	}
	tmpfs := map[string]string{}
	for p, o := range img.Tmpfs {
		tmpfs[p] = o
	}
	for _, p := range img.DataPaths {
		covered := slices.ContainsFunc(sv.Volumes, func(v Volume) bool {
			return v.Target == p || strings.HasPrefix(p, v.Target+"/")
		})
		if !covered {
			tmpfs[p] = img.DataTmpfs
		}
	}
	for _, v := range sv.Volumes {
		delete(tmpfs, v.Target) // a folder mounted over a scratch path wins
	}

	keys := make([]string, 0, len(img.Env))
	for k := range img.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	env := make([]string, 0, len(keys)+len(sv.Env))
	for _, k := range keys {
		env = append(env, k+"="+img.Env[k])
	}
	env = append(env, sv.Env...) // the service's own values come last and win

	cmd := sv.Command
	if len(cmd) == 0 {
		cmd = img.Cmd
	}
	wd := sv.WorkingDir
	if wd == "" {
		wd = "/tmp"
		if len(sv.Volumes) > 0 {
			wd = sv.Volumes[0].Target
		}
	}
	mem := int64(m.cfg.MemoryMB) << 20
	return CreateBody{
		Image:      img.Ref(),
		Hostname:   sv.Name,
		User:       fmt.Sprintf("%d:%d", m.uid, m.gid),
		Env:        env,
		Cmd:        cmd,
		WorkingDir: wd,
		Labels:     map[string]string{labelManaged: "true", labelSite: id, labelService: sv.Name},
		HostConfig: HostConfig{
			Memory:         mem,
			MemorySwap:     mem,
			NanoCpus:       int64(m.cfg.CPUs * 1e9),
			PidsLimit:      int64(m.cfg.Pids),
			CapDrop:        []string{"ALL"},
			SecurityOpt:    []string{"no-new-privileges:true"},
			ReadonlyRootfs: true,
			Tmpfs:          tmpfs,
			Mounts:         mounts,
			RestartPolicy:  RestartPolicy{Name: "unless-stopped"},
			LogConfig:      LogConfig{Type: "json-file", Config: map[string]string{"max-size": "10m", "max-file": "2"}},
			NetworkMode:    networkName(id),
			Runtime:        m.cfg.Runtime,
			Init:           true,
		},
		NetworkingConfig: &NetworkingConfig{EndpointsConfig: map[string]EndpointConfig{
			networkName(id): {Aliases: []string{sv.Name}},
		}},
	}, nil
}

// removeContainers removes the site's containers.
func (m *Manager) removeContainers(ctx context.Context, id string) error {
	list, err := m.eng.ContainerList(ctx, labelManaged+"=true", labelSite+"="+id)
	if err != nil {
		return err
	}
	var errs []error
	for _, c := range list {
		if err := m.eng.ContainerRemove(ctx, c.ID); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// teardown removes everything the site had: containers, Sitebin's
// attachment, networks. The site's files are never touched.
func (m *Manager) teardown(ctx context.Context, id string) error {
	if err := m.removeContainers(ctx, id); err != nil {
		return err
	}
	m.mu.Lock()
	self := m.self
	m.mu.Unlock()
	var errs []error
	for _, n := range []string{networkName(id), egressName(id)} {
		if self != "" {
			if err := m.eng.NetworkDisconnect(ctx, n, self); err != nil {
				errs = append(errs, err)
			}
		}
		if err := m.eng.NetworkRemove(ctx, n); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (m *Manager) teardownLogged(ctx context.Context, id string) {
	if err := m.teardown(ctx, id); err != nil {
		slog.Error("containers: tear down", "site", id, "err", err)
	}
}

// refresh keeps a running project honest: Sitebin attached to its network
// (a redeploy creates a Sitebin container that is on none), each service's
// Docker state recorded, and a project whose containers vanished re-applied.
func (m *Manager) refresh(ctx context.Context, cs ext.ContainerSite, present []ContainerSummary) {
	id := cs.ViewID
	state := map[string]string{}
	for _, c := range present {
		state[c.Labels[labelService]] = c.State
	}
	obs := cs.Observed
	for _, sv := range obs.Services {
		if _, ok := state[sv.Name]; !ok {
			slog.Warn("containers: a service's container is gone, starting the project again", "site", id, "service", sv.Name)
			obs.AppliedHash = ""
			m.apply(ctx, ext.ContainerSite{ViewID: id, Owner: cs.Owner, Container: true, Enabled: true,
				RestartSeq: cs.RestartSeq, Compose: cs.Compose, ComposeErr: cs.ComposeErr, Observed: obs}, composeHash(cs))
			return
		}
	}
	m.mu.Lock()
	self := m.self
	m.mu.Unlock()
	if err := m.eng.NetworkConnect(ctx, networkName(id), self, nil); err != nil {
		slog.Error("containers: re-attach Sitebin", "site", id, "err", err)
	}
	changed := false
	for i, sv := range obs.Services {
		if s := state[sv.Name]; s != sv.State {
			obs.Services[i].State = s
			changed = true
		}
	}
	if changed {
		m.report(id, obs)
	}
}

// fullScan reconciles every container site, removes what belongs to no
// running site, holds each owner to their plan, and now and then measures
// running sites against their storage cap.
func (m *Manager) fullScan(ctx context.Context) {
	all, err := m.sites.ContainerSites()
	if err != nil {
		slog.Error("containers: list sites", "err", err)
		return
	}
	present, err := m.eng.ContainerList(ctx, labelManaged+"=true")
	if err != nil {
		slog.Error("containers: list containers", "err", err)
		return
	}
	bySite := map[string][]ContainerSummary{}
	for _, c := range present {
		bySite[c.Labels[labelSite]] = append(bySite[c.Labels[labelSite]], c)
	}
	wanted := map[string]bool{}
	for _, cs := range all {
		if cs.Container && cs.Enabled && !cs.Expired {
			wanted[cs.ViewID] = true
		}
	}

	// What belongs to no wanted site goes: a deleted site, one switched out
	// of container mode, stopped or expired.
	orphans := map[string]bool{}
	for site := range bySite {
		if !wanted[site] {
			orphans[site] = true
		}
	}
	if nets, err := m.eng.NetworkList(ctx, labelManaged+"=true"); err == nil {
		for _, n := range nets {
			if site := n.Labels[labelSite]; site != "" && !wanted[site] {
				orphans[site] = true
			}
		}
	}
	for site := range orphans {
		l := m.siteLock(site)
		l.Lock()
		m.teardownLogged(ctx, site)
		l.Unlock()
	}

	for _, cs := range all {
		m.mu.Lock()
		busy := m.busy[cs.ViewID]
		if !busy {
			m.busy[cs.ViewID] = true
		}
		m.mu.Unlock()
		if busy {
			continue
		}
		l := m.siteLock(cs.ViewID)
		l.Lock()
		m.reconcile(ctx, cs, false, nonNil(bySite[cs.ViewID]))
		l.Unlock()
		m.mu.Lock()
		delete(m.busy, cs.ViewID)
		m.mu.Unlock()
	}

	m.enforcePlans(ctx)
	m.mu.Lock()
	measure := m.now().Sub(m.lastQuota) >= quotaEvery
	if measure {
		m.lastQuota = m.now()
	}
	m.mu.Unlock()
	if measure {
		m.enforceStorage(ctx)
	}
}

func nonNil(s []ContainerSummary) []ContainerSummary {
	if s == nil {
		return []ContainerSummary{}
	}
	return s
}

// holdOff stops a running project for a reason only its owner can fix, and
// records it against the current file and sequence so it is not simply
// started again on the next tick: pressing Start is the way back.
func (m *Manager) holdOff(ctx context.Context, cs ext.ContainerSite, msg string) {
	l := m.siteLock(cs.ViewID)
	l.Lock()
	defer l.Unlock()
	if err := m.teardown(ctx, cs.ViewID); err != nil {
		slog.Error("containers: stop", "site", cs.ViewID, "err", err)
		return
	}
	obs := cs.Observed
	obs.Status, obs.Message = store.ContainerStopped, msg
	obs.AppliedHash, obs.AppliedSeq = composeHash(cs), cs.RestartSeq
	obs.Services = stateless(obs.Services, "")
	m.report(cs.ViewID, obs)
	slog.Info("containers: stopped", "site", cs.ViewID, "owner", cs.Owner, "reason", msg)
}

// enforcePlans stops projects of owners who now run more services than their
// plan allows — after a downgrade — newest first. A plan that cannot be
// resolved stops nothing.
func (m *Manager) enforcePlans(ctx context.Context) {
	all, err := m.sites.ContainerSites()
	if err != nil {
		return
	}
	byOwner := map[string][]ext.ContainerSite{}
	for _, cs := range all {
		if counts(cs) {
			byOwner[cs.Owner] = append(byOwner[cs.Owner], cs)
		}
	}
	for owner, sites := range byOwner {
		limit, err := m.capFor(owner)
		if err != nil {
			continue
		}
		total := 0
		for _, s := range sites {
			total += len(s.Observed.Services)
		}
		if total <= limit {
			continue
		}
		sort.Slice(sites, func(i, j int) bool { return sites[i].Observed.AppliedAt.After(sites[j].Observed.AppliedAt) })
		for _, s := range sites {
			if total <= limit {
				break
			}
			m.holdOff(ctx, s, capMessage(limit, len(s.Observed.Services), total-len(s.Observed.Services))+"; it was stopped")
			total -= len(s.Observed.Services)
		}
	}
}

// enforceStorage stops running projects whose site is over its byte cap. A
// container cannot be refused mid-write the way an upload is; this is the
// bound. Nothing is deleted.
func (m *Manager) enforceStorage(ctx context.Context) {
	all, err := m.sites.ContainerSites()
	if err != nil {
		return
	}
	for _, cs := range all {
		if !counts(cs) || cs.MaxBytes <= 0 {
			continue
		}
		info, ok := m.sites.Info(cs.ViewID)
		if !ok || info.Bytes <= cs.MaxBytes {
			continue
		}
		m.holdOff(ctx, cs, fmt.Sprintf("the project uses %s of its %s storage, so it was stopped; free some space and press Start",
			store.HumanBytes(info.Bytes), store.HumanBytes(cs.MaxBytes)))
	}
}
