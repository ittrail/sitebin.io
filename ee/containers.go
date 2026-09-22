//go:build ee

package ee

import (
	"context"
	"errors"
	"log/slog"

	"github.com/ittrail/sitebin.io/ee/account"
	"github.com/ittrail/sitebin.io/ee/containers"
	"github.com/ittrail/sitebin.io/internal/ext"
)

// The enterprise extension runs container sites: it implements the optional
// ext.ContainerProvider. See
// docs/superpowers/specs/2026-09-22-container-sites-design.md.
var _ ext.ContainerProvider = (*provider)(nil)

// Containers returns the runtime, or nil when SITEBIN_CONTAINERS is off.
func (p *provider) Containers() ext.ContainerRuntime {
	if p.containers == nil {
		return nil // a typed nil would read as "on" across the interface
	}
	return p.containers
}

// initContainers starts the runtime when the instance has it on. A Docker
// Engine that does not answer does not stop the start: the runtime keeps
// trying and the mode says why it is unavailable, like the licence does.
func (p *provider) initContainers() error {
	c := p.cfg.Containers
	if c == nil {
		return nil
	}
	m, err := containers.New(containers.Config{
		DockerHost: c.DockerHost, DataDir: p.host.DataDir(), DataMount: c.DataMount, Self: c.Self,
		Runtime: c.Runtime, MemoryMB: c.MemoryMB, CPUs: c.CPUs, Pids: c.Pids,
	}, p.host.Sites(), p.containerCap)
	if err != nil {
		return err
	}
	p.containers = m
	go m.Run(context.Background())
	slog.Info("containers enabled", "docker", c.DockerHost, "memory_mb", c.MemoryMB, "cpus", c.CPUs, "pids", c.Pids)
	return nil
}

// containerCap is an account's max_containers. It resolves the tier strictly:
// the runtime starts and stops projects on this answer, so an unknown plan
// is an error, never a guess.
func (p *provider) containerCap(owner string) (int, error) {
	acc, err := p.accounts.ByID(owner)
	if errors.Is(err, account.ErrNotFound) {
		return 0, nil // no account, no containers
	}
	if err != nil {
		return 0, err
	}
	t, err := p.effectiveTierStrict(acc)
	if err != nil {
		return 0, err
	}
	return t.MaxContainers, nil
}
