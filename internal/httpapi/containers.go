package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/ittrail/sitebin.io/internal/ext"
	"github.com/ittrail/sitebin.io/internal/store"
)

// Container sites: the core half. The core records what a container site
// should be doing, routes requests to it, and keeps its files safe; the
// extension's runtime runs it. See
// docs/superpowers/specs/2026-09-22-container-sites-design.md.

// containerRuntime returns the registered runtime, if the instance has one.
// The community build registers no provider, so it never does.
func containerRuntime() (ext.ContainerRuntime, bool) {
	p, ok := ext.Get()
	if !ok {
		return nil, false
	}
	cp, ok := p.(ext.ContainerProvider)
	if !ok {
		return nil, false
	}
	rt := cp.Containers()
	return rt, rt != nil
}

// errNoContainers is the answer wherever container mode is asked for and the
// instance cannot run it.
var errNoContainers = &apiError{403, "container sites are an Enterprise feature and are not enabled on this instance"}

// containerModeAllowed decides whether site may enter container mode.
func containerModeAllowed(site *store.Site) error {
	rt, ok := containerRuntime()
	if !ok {
		return errNoContainers
	}
	if site.Meta.OwnerAccountID == "" {
		return &apiError{403, "container sites belong to an account: sign in and create the site there"}
	}
	if err := rt.Allowed(site.Meta.OwnerAccountID); err != nil {
		return &apiError{403, err.Error()}
	}
	return nil
}

// enterContainerMode is the side effect of switching a site INTO container
// mode: the project is enabled, and the runtime told to start it.
func (a *API) enterContainerMode(site *store.Site) error {
	err := a.st.Update(site, func(m *store.Meta) error {
		if m.Container == nil {
			m.Container = &store.ContainerMeta{}
		}
		m.Container.Enabled = true
		m.Container.Status = store.ContainerStarting
		m.Container.Message = ""
		return nil
	})
	if err != nil {
		return err
	}
	if rt, ok := containerRuntime(); ok {
		rt.Kick(site.ViewID)
	}
	return nil
}

// leaveContainerMode is the side effect of switching a site OUT of container
// mode, and runs before the mode is written. The containers are removed
// synchronously first, then every symlink in the tree: Caddy's file server
// follows links, only a container can have made one, and a container still
// running could make another after the purge.
func (a *API) leaveContainerMode(site *store.Site) error {
	if rt, ok := containerRuntime(); ok {
		if err := rt.Stop(site.ViewID); err != nil {
			a.log.Error("stop containers on mode change", "id", site.ViewID, "err", err)
			return &apiError{503, "the site's containers could not be stopped, so its mode was not changed; try again in a moment"}
		}
	}
	if n, err := a.st.PurgeSymlinks(site); err != nil {
		return err
	} else if n > 0 {
		a.log.Info("removed links left by containers", "id", site.ViewID, "count", n)
	}
	err := a.st.Update(site, func(m *store.Meta) error {
		if m.Container != nil {
			m.Container.Enabled = false
			m.Container.Status = store.ContainerStopped
			m.Container.Services = nil
		}
		return nil
	})
	if err != nil {
		return err
	}
	// The ports' generated addresses go with the containers; the custom
	// domains stay, and serve the files.
	return a.st.ClearAddresses(site)
}

// stopContainersBeforeDelete removes a container site's containers before its
// files go, so nothing keeps writing into a deleted directory. Best effort:
// the runtime's scan removes the containers of a site that no longer exists
// anyway.
func (a *API) stopContainersBeforeDelete(site *store.Site) {
	if site.Meta.Mode != store.ModeContainer {
		return
	}
	if rt, ok := containerRuntime(); ok {
		if err := rt.Stop(site.ViewID); err != nil {
			a.log.Error("stop containers before delete", "id", site.ViewID, "err", err)
		}
	}
}

// ---- API: /api/sites/{editID}/containers/... ----

func (a *API) requireContainerSite(w http.ResponseWriter, site *store.Site) (ext.ContainerRuntime, bool) {
	rt, ok := containerRuntime()
	if !ok {
		writeError(w, errNoContainers.code, errNoContainers.msg)
		return nil, false
	}
	if site.Meta.Mode != store.ModeContainer {
		writeError(w, 409, `this site is not in container mode; set "mode": "container" first`)
		return nil, false
	}
	return rt, true
}

// containerAction handles start, stop and restart. Start and restart are
// re-checked against the plan, exactly like entering the mode: a site that
// entered container mode on a plan the owner has since left must not be
// restartable by hand.
func (a *API) containerAction(w http.ResponseWriter, r *http.Request, site *store.Site) {
	rt, ok := a.requireContainerSite(w, site)
	if !ok {
		return
	}
	action := r.PathValue("action")
	switch action {
	case "start", "restart":
		if err := containerModeAllowed(site); err != nil {
			respondErr(w, err)
			return
		}
	case "stop":
	default:
		writeError(w, 404, "unknown action: use start, stop or restart")
		return
	}
	err := a.st.Update(site, func(m *store.Meta) error {
		if m.Container == nil {
			m.Container = &store.ContainerMeta{}
		}
		switch action {
		case "start", "restart":
			m.Container.Enabled = true
			// A bump, even for start: a project that failed on this very
			// compose file is only retried when something changes, and the
			// customer pressing Start is that something.
			m.Container.RestartSeq++
			m.Container.Status = store.ContainerStarting
			m.Container.Message = ""
		case "stop":
			m.Container.Enabled = false
		}
		return nil
	})
	if err != nil {
		respondErr(w, err)
		return
	}
	if action == "stop" {
		if err := rt.Stop(site.ViewID); err != nil {
			a.log.Error("stop containers", "id", site.ViewID, "err", err)
			writeError(w, 503, "the containers could not be stopped; the runtime will retry")
			return
		}
	} else {
		rt.Kick(site.ViewID)
	}
	noteOf(r).detail = action
	a.log.Info("container action", "id", site.ViewID, "owner", site.Meta.OwnerAccountID, "action", action)
	writeJSON(w, 202, a.sitePayload(site))
}

// maxLogTail bounds how much of a service's log one request returns.
const maxLogTail = 1000

func (a *API) containerLogs(w http.ResponseWriter, r *http.Request, site *store.Site) {
	rt, ok := a.requireContainerSite(w, site)
	if !ok {
		return
	}
	tail := 200
	if v := r.URL.Query().Get("tail"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			writeError(w, 400, "tail must be a positive number")
			return
		}
		tail = min(n, maxLogTail)
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	out, err := rt.Logs(ctx, site.ViewID, r.PathValue("service"), tail)
	if err != nil {
		writeError(w, 404, err.Error())
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	w.Write([]byte(out))
}

// containerStats answers what the site's running services use — memory,
// CPU, processes and public traffic — as the runtime last measured it. Its
// own route, not the site payload: MCP clients cache that one's schema. A
// rate not yet measured (one sample so far) is null.
func (a *API) containerStats(w http.ResponseWriter, r *http.Request, site *store.Site) {
	rt, ok := a.requireContainerSite(w, site)
	if !ok {
		return
	}
	out := map[string]any{"sampled_at": nil, "services": []any{}, "web": nil}
	if u, ok := rt.Usage(site.ViewID); ok {
		out["sampled_at"] = u.At.UTC()
		services := make([]map[string]any, 0, len(u.Services))
		for _, s := range u.Services {
			services = append(services, map[string]any{
				"name": s.Name, "memory_bytes": s.MemoryBytes, "memory_limit_bytes": s.MemoryLimitBytes,
				"cpu": s.CPU, "cpu_limit": s.CPULimit, "pids": s.Pids, "pids_limit": s.PidsLimit,
				"in_bps": s.In, "out_bps": s.Out,
			})
		}
		out["services"] = services
		if u.Web != nil {
			out["web"] = map[string]any{"in_bps": u.Web.In, "out_bps": u.Web.Out}
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, out)
}

// containerPayload is the container half of the site payload.
func (a *API) containerPayload(site *store.Site) map[string]any {
	_, available := containerRuntime()
	out := map[string]any{"available": available, "compose_file": store.ComposeFile}
	c := site.Meta.Container
	if c == nil {
		out["enabled"] = false
		out["status"] = store.ContainerStopped
		out["services"] = []any{}
		return out
	}
	out["enabled"] = c.Enabled
	out["status"] = c.Status
	out["message"] = c.Message
	services := make([]map[string]any, 0, len(c.Services))
	for _, s := range c.Services {
		ports := s.Ports
		if ports == nil {
			ports = []int{}
		}
		services = append(services, map[string]any{
			"name": s.Name, "image": s.Image, "egress": s.Egress,
			"volumes": s.Volumes, "ports": ports, "state": s.State,
		})
	}
	out["services"] = services
	if c.ObservedAt != nil {
		out["observed_at"] = c.ObservedAt
	}
	return out
}

// ---- ext.SiteService: what the runtime reads and reports ----

func (s siteService) containerSiteOf(site *store.Site) ext.ContainerSite {
	cs := ext.ContainerSite{
		ViewID:    site.ViewID,
		Owner:     site.Meta.OwnerAccountID,
		Container: site.Meta.Mode == store.ModeContainer,
		Expired:   site.Meta.Expired(time.Now()),
		Locked:    site.Meta.IsLocked(),
		MaxBytes:  s.a.st.EffMaxBytes(site),
		Name:      site.Meta.Name,
	}
	if c := site.Meta.Container; c != nil {
		cs.Enabled = c.Enabled
		cs.RestartSeq = c.RestartSeq
		cs.Observed = observedOf(c)
		for name, l := range c.Limits {
			if cs.Limits == nil {
				cs.Limits = map[string]ext.ContainerLimits{}
			}
			cs.Limits[name] = extLimits(l)
		}
	}
	if cs.Container {
		b, err := s.a.st.ReadCompose(site)
		switch {
		case err == nil:
			cs.Compose = b
		case errors.Is(err, store.ErrNotFound):
			cs.ComposeErr = store.ComposeFile + " is missing from the site's root"
		case errors.Is(err, store.ErrTooLarge):
			cs.ComposeErr = fmt.Sprintf("%s is larger than %d KB", store.ComposeFile, store.MaxComposeBytes>>10)
		case errors.Is(err, store.ErrBadPath):
			cs.ComposeErr = store.ComposeFile + " is not a regular file"
		default:
			cs.ComposeErr = "could not read " + store.ComposeFile + ": " + err.Error()
		}
	}
	return cs
}

func (s siteService) ContainerSites() ([]ext.ContainerSite, error) {
	sites, err := s.a.st.AllSites()
	if err != nil {
		return nil, err
	}
	var out []ext.ContainerSite
	for _, site := range sites {
		if site.Meta.Mode == store.ModeContainer {
			out = append(out, s.containerSiteOf(site))
		}
	}
	return out, nil
}

func (s siteService) ContainerSite(viewID string) (ext.ContainerSite, error) {
	site, err := s.a.st.ByViewID(viewID)
	if err != nil {
		return ext.ContainerSite{}, mapSiteGone(err, viewID)
	}
	return s.containerSiteOf(site), nil
}

func (s siteService) PrepareVolume(viewID, folder string) (string, error) {
	site, err := s.a.st.ByViewID(viewID)
	if err != nil {
		return "", mapSiteGone(err, viewID)
	}
	return s.a.st.PrepareVolume(site, folder)
}

func (s siteService) SetContainerState(viewID string, st ext.ContainerState) error {
	site, err := s.a.st.ByViewID(viewID)
	if err != nil {
		return mapSiteGone(err, viewID)
	}
	now := time.Now().UTC()
	err = s.a.st.Update(site, func(m *store.Meta) error {
		if m.Container == nil {
			m.Container = &store.ContainerMeta{}
		}
		c := m.Container
		c.Status = st.Status
		c.Message = st.Message
		c.AppliedHash = st.AppliedHash
		c.AppliedSeq = st.AppliedSeq
		c.AppliedAt = nil
		if !st.AppliedAt.IsZero() {
			at := st.AppliedAt.UTC()
			c.AppliedAt = &at
		}
		c.Services = make([]store.ContainerService, 0, len(st.Services))
		for _, sv := range st.Services {
			c.Services = append(c.Services, store.ContainerService{
				Name: sv.Name, Image: sv.Image, Egress: sv.Egress, Volumes: sv.Volumes,
				Ports: slices.Clone(sv.Ports), State: sv.State, Host: sv.Host, Limits: storeLimits(sv.Limits),
			})
		}
		c.ObservedAt = &now
		return nil
	})
	return mapSiteGone(err, viewID)
}

func (s siteService) SyncContainerEndpoints(viewID string, endpoints []string) error {
	site, err := s.a.st.ByViewID(viewID)
	if err != nil {
		return mapSiteGone(err, viewID)
	}
	return mapSiteGone(s.a.st.SyncAddresses(site, endpoints), viewID)
}

func observedOf(c *store.ContainerMeta) ext.ContainerState {
	st := ext.ContainerState{Status: c.Status, Message: c.Message, AppliedHash: c.AppliedHash, AppliedSeq: c.AppliedSeq}
	if c.AppliedAt != nil {
		st.AppliedAt = *c.AppliedAt
	}
	for _, sv := range c.Services {
		st.Services = append(st.Services, ext.ContainerService{
			Name: sv.Name, Image: sv.Image, Egress: sv.Egress, Volumes: sv.Volumes,
			Ports: slices.Clone(sv.Ports), State: sv.State, Host: sv.Host, Limits: extLimits(sv.Limits),
		})
	}
	return st
}

func (s siteService) SetContainerLimits(viewID string, limits map[string]ext.ContainerLimits) error {
	site, err := s.a.st.ByViewID(viewID)
	if err != nil {
		return mapSiteGone(err, viewID)
	}
	err = s.a.st.Update(site, func(m *store.Meta) error {
		var keep map[string]store.ContainerLimits
		for name, l := range limits {
			if l == (ext.ContainerLimits{}) {
				continue
			}
			if keep == nil {
				keep = map[string]store.ContainerLimits{}
			}
			keep[name] = storeLimits(l)
		}
		if m.Container == nil {
			if keep == nil {
				return nil
			}
			m.Container = &store.ContainerMeta{}
		}
		m.Container.Limits = keep
		return nil
	})
	return mapSiteGone(err, viewID)
}

func extLimits(l store.ContainerLimits) ext.ContainerLimits {
	return ext.ContainerLimits{MemoryMB: l.MemoryMB, CPUs: l.CPUs, Pids: l.Pids}
}

func storeLimits(l ext.ContainerLimits) store.ContainerLimits {
	return store.ContainerLimits{MemoryMB: l.MemoryMB, CPUs: l.CPUs, Pids: l.Pids}
}
