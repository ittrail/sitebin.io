//go:build ee

package eeconfig

import (
	"strings"
	"testing"
)

func tiersEnv(extra map[string]string) map[string]string {
	m := map[string]string{
		"SITEBIN_ACCOUNT_MODE": "tiers",
		"SITEBIN_TIERS":        `[{"id":"free","max_sites":3},{"id":"pro","max_sites":100,"max_containers":3}]`,
		"SITEBIN_DEFAULT_TIER": "free",
	}
	for k, v := range extra {
		m[k] = v
	}
	return m
}

func TestContainersOffByDefault(t *testing.T) {
	cfg, err := Load(env(tiersEnv(nil)), noFile)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Containers != nil {
		t.Errorf("containers on by default: %+v", cfg.Containers)
	}
	free, _ := cfg.Tier("free")
	pro, _ := cfg.Tier("pro")
	if free.MaxContainers != 0 || pro.MaxContainers != 3 {
		t.Errorf("max_containers: free=%d pro=%d", free.MaxContainers, pro.MaxContainers)
	}
}

func TestContainersDefaults(t *testing.T) {
	cfg, err := Load(env(tiersEnv(map[string]string{"SITEBIN_CONTAINERS": "docker"})), noFile)
	if err != nil {
		t.Fatal(err)
	}
	c := cfg.Containers
	if c == nil || c.DockerHost != "unix:///var/run/docker.sock" || c.MemoryMB != 512 || c.CPUs != 0.5 || c.Pids != 256 || c.DataMount != "" {
		t.Errorf("defaults = %+v", c)
	}
}

func TestContainersSettings(t *testing.T) {
	cfg, err := Load(env(tiersEnv(map[string]string{
		"SITEBIN_CONTAINERS":             "docker",
		"SITEBIN_CONTAINERS_DOCKER_HOST": "tcp://socket-proxy:2375",
		"SITEBIN_CONTAINERS_DATA_MOUNT":  "volume:sitebin_data",
		"SITEBIN_CONTAINERS_RUNTIME":     "runsc",
		"SITEBIN_CONTAINER_MEMORY_MB":    "1024",
		"SITEBIN_CONTAINER_CPUS":         "1.5",
		"SITEBIN_CONTAINER_PIDS":         "512",
	})), noFile)
	if err != nil {
		t.Fatal(err)
	}
	c := cfg.Containers
	if c.DockerHost != "tcp://socket-proxy:2375" || c.DataMount != "volume:sitebin_data" || c.Runtime != "runsc" ||
		c.MemoryMB != 1024 || c.CPUs != 1.5 || c.Pids != 512 {
		t.Errorf("settings = %+v", c)
	}
}

func TestContainersRefusals(t *testing.T) {
	cases := map[string]struct {
		env  map[string]string
		want string
	}{
		"not tiers":      {map[string]string{"SITEBIN_ACCOUNT_MODE": "accounts", "SITEBIN_CONTAINERS": "docker"}, "needs SITEBIN_ACCOUNT_MODE=tiers"},
		"bad switch":     {tiersEnv(map[string]string{"SITEBIN_CONTAINERS": "podman"}), "want off|docker"},
		"bad host":       {tiersEnv(map[string]string{"SITEBIN_CONTAINERS": "docker", "SITEBIN_CONTAINERS_DOCKER_HOST": "npipe:////./pipe/docker"}), "unix:// or tcp://"},
		"bad mount":      {tiersEnv(map[string]string{"SITEBIN_CONTAINERS": "docker", "SITEBIN_CONTAINERS_DATA_MOUNT": "data"}), "absolute host path"},
		"tiny memory":    {tiersEnv(map[string]string{"SITEBIN_CONTAINERS": "docker", "SITEBIN_CONTAINER_MEMORY_MB": "8"}), "at least 64"},
		"bad cpus":       {tiersEnv(map[string]string{"SITEBIN_CONTAINERS": "docker", "SITEBIN_CONTAINER_CPUS": "lots"}), "number of CPUs"},
		"too few pids":   {tiersEnv(map[string]string{"SITEBIN_CONTAINERS": "docker", "SITEBIN_CONTAINER_PIDS": "2"}), "at least 16"},
		"volume no name": {tiersEnv(map[string]string{"SITEBIN_CONTAINERS": "docker", "SITEBIN_CONTAINERS_DATA_MOUNT": "volume:"}), "absolute host path"},
	}
	for name, c := range cases {
		_, err := Load(env(c.env), noFile)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want %q", name, err, c.want)
		}
	}
}
