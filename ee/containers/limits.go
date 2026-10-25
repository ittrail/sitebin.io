//go:build ee

package containers

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/ittrail/sitebin.io/internal/ext"
)

// Bounds of a container's limits. The floors are the environment variables'
// own; the ceilings are the host's where the Engine said what it has.
const (
	minMemoryMB = 64
	maxMemoryMB = 1 << 20 // 1 TiB
	minCPUs     = 0.01
	maxCPUs     = 64
	minPids     = 16
	maxPids     = 1 << 22 // the kernel's pid ceiling on 64-bit
)

// limitsFile is where the console's instance defaults live, under the data
// directory.
const limitsFile = "containers/limits.json"

// LimitBook holds the instance's default container limits: the environment's
// (SITEBIN_CONTAINER_*), and over them, field by field, the ones an admin
// saved in the console. It also knows the host's size, which bounds both.
// See docs/superpowers/specs/2026-10-25-container-limits-admin-design.md.
type LimitBook struct {
	path string
	env  ext.ContainerLimits

	mu    sync.Mutex
	saved ext.ContainerLimits
	err   error // the saved defaults could not be read: unknown
	cpus  int   // the host's, 0 = not known
	mem   int64
}

// limitsJSON is the file's form; a field absent or 0 is the environment's.
type limitsJSON struct {
	MemoryMB int     `json:"memory_mb,omitempty"`
	CPUs     float64 `json:"cpus,omitempty"`
	Pids     int     `json:"pids,omitempty"`
}

// NewLimitBook reads the saved defaults under dataDir. A missing file is
// none saved; one that cannot be read or holds values out of bounds is
// recorded as Err, and the book is not Known until defaults are saved again.
func NewLimitBook(dataDir string, env ext.ContainerLimits) *LimitBook {
	b := &LimitBook{path: filepath.Join(dataDir, filepath.FromSlash(limitsFile)), env: env}
	raw, err := os.ReadFile(b.path)
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		b.err = err
	default:
		var f limitsJSON
		if err := json.Unmarshal(raw, &f); err != nil {
			b.err = fmt.Errorf("%s: %w", limitsFile, err)
			break
		}
		l := ext.ContainerLimits{MemoryMB: f.MemoryMB, CPUs: f.CPUs, Pids: f.Pids}
		if err := b.Check(l); err != nil {
			b.err = fmt.Errorf("%s: %w", limitsFile, err)
			break
		}
		b.saved = l
	}
	return b
}

// Env is the environment's defaults.
func (b *LimitBook) Env() ext.ContainerLimits { return b.env }

// Saved is what an admin saved; a zero field is the environment's.
func (b *LimitBook) Saved() ext.ContainerLimits {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.saved
}

// Defaults is what a container gets without its own limits. While the saved
// defaults are unknown it is the environment's, which is what a NEW
// container starts with; running ones are left as they are (Known).
func (b *LimitBook) Defaults() ext.ContainerLimits {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.saved.Or(b.env)
}

// Err is why the saved defaults could not be read, or nil.
func (b *LimitBook) Err() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.err
}

// Known reports whether the defaults are known, and running containers may
// be converged to them.
func (b *LimitBook) Known() bool { return b.Err() == nil }

// For is the effective limits of one service: its own, then the defaults.
func (b *LimitBook) For(own map[string]ext.ContainerLimits, service string) ext.ContainerLimits {
	return own[service].Or(b.Defaults())
}

// SetCapacity records the host's CPUs and memory (bytes), as the Engine
// reports them.
func (b *LimitBook) SetCapacity(cpus int, memBytes int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.cpus, b.mem = cpus, memBytes
}

// Capacity is the host's CPUs and memory in bytes; 0 when not known.
func (b *LimitBook) Capacity() (cpus int, memBytes int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.cpus, b.mem
}

// Check reports whether every field l sets is within bounds. Its message is
// shown to the admin as it is.
func (b *LimitBook) Check(l ext.ContainerLimits) error {
	cpus, mem := b.Capacity()
	maxMem, maxCPU := maxMemoryMB, float64(maxCPUs)
	memWhy, cpuWhy := "", ""
	if mb := int(mem >> 20); mb > 0 && mb < maxMem {
		maxMem, memWhy = mb, ", the host's memory"
	}
	if cpus > 0 && float64(cpus) < maxCPU {
		maxCPU, cpuWhy = float64(cpus), ", the host's CPUs"
	}
	switch {
	case l.MemoryMB < 0 || (l.MemoryMB > 0 && l.MemoryMB < minMemoryMB):
		return fmt.Errorf("memory: at least %d MB", minMemoryMB)
	case l.MemoryMB > maxMem:
		return fmt.Errorf("memory: at most %d MB%s", maxMem, memWhy)
	case l.CPUs < 0 || (l.CPUs > 0 && l.CPUs < minCPUs):
		return fmt.Errorf("CPUs: at least %v", minCPUs)
	case l.CPUs > maxCPU:
		return fmt.Errorf("CPUs: at most %v%s", maxCPU, cpuWhy)
	case l.Pids < 0 || (l.Pids > 0 && l.Pids < minPids):
		return fmt.Errorf("processes: at least %d", minPids)
	case l.Pids > maxPids:
		return fmt.Errorf("processes: at most %d", maxPids)
	}
	return nil
}

// SetDefaults saves the admin's defaults (a zero field is the
// environment's; all zero removes the file) and makes them known.
func (b *LimitBook) SetDefaults(l ext.ContainerLimits) error {
	if err := b.Check(l); err != nil {
		return err
	}
	if l == (ext.ContainerLimits{}) {
		if err := os.Remove(b.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	} else if err := writeFileAtomic(b.path, limitsJSON{MemoryMB: l.MemoryMB, CPUs: l.CPUs, Pids: l.Pids}); err != nil {
		return err
	}
	b.mu.Lock()
	b.saved, b.err = l, nil
	b.mu.Unlock()
	return nil
}

// writeFileAtomic writes v as JSON next to path and renames it into place,
// so a reader never sees half a file.
func writeFileAtomic(path string, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".limits-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, werr := f.Write(append(raw, '\n'))
	cerr := f.Close()
	if err := errors.Join(werr, cerr); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Chmod(tmp, 0o644); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}
