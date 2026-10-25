//go:build ee

package containers

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ittrail/sitebin.io/internal/ext"
)

var envLimits = ext.ContainerLimits{MemoryMB: 512, CPUs: 0.5, Pids: 256}

func TestDefaultsLayerOverTheEnvironment(t *testing.T) {
	dir := t.TempDir()
	b := NewLimitBook(dir, envLimits)
	if b.Defaults() != envLimits || b.Saved() != (ext.ContainerLimits{}) || b.Err() != nil || !b.Known() {
		t.Fatalf("fresh book: defaults %+v saved %+v err %v", b.Defaults(), b.Saved(), b.Err())
	}
	if err := b.SetDefaults(ext.ContainerLimits{MemoryMB: 1024}); err != nil {
		t.Fatal(err)
	}
	if want := (ext.ContainerLimits{MemoryMB: 1024, CPUs: 0.5, Pids: 256}); b.Defaults() != want {
		t.Errorf("defaults = %+v, want %+v", b.Defaults(), want)
	}
	if again := NewLimitBook(dir, envLimits); again.Saved() != (ext.ContainerLimits{MemoryMB: 1024}) {
		t.Errorf("a new book reads %+v", again.Saved())
	}
	if err := b.SetDefaults(ext.ContainerLimits{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "containers", "limits.json")); !os.IsNotExist(err) {
		t.Errorf("saving nothing left the file: %v", err)
	}
	if b.Defaults() != envLimits || b.Env() != envLimits {
		t.Errorf("after reset: %+v", b.Defaults())
	}
}

func TestForPrefersTheServicesOwn(t *testing.T) {
	b := NewLimitBook(t.TempDir(), envLimits)
	b.SetDefaults(ext.ContainerLimits{CPUs: 1})
	own := map[string]ext.ContainerLimits{"db": {MemoryMB: 2048}}
	if got := b.For(own, "db"); got != (ext.ContainerLimits{MemoryMB: 2048, CPUs: 1, Pids: 256}) {
		t.Errorf("db = %+v", got)
	}
	if got := b.For(own, "app"); got != (ext.ContainerLimits{MemoryMB: 512, CPUs: 1, Pids: 256}) {
		t.Errorf("app = %+v", got)
	}
	if got := b.For(nil, "app"); got != b.Defaults() {
		t.Errorf("no own limits = %+v", got)
	}
}

func TestCheckBounds(t *testing.T) {
	b := NewLimitBook(t.TempDir(), envLimits)
	for _, l := range []ext.ContainerLimits{{MemoryMB: 63}, {CPUs: 0.001}, {Pids: 15}, {MemoryMB: -1}, {CPUs: -1}, {Pids: -1}, {Pids: 4194305}, {CPUs: 65}, {MemoryMB: 1<<20 + 1}} {
		if err := b.Check(l); err == nil {
			t.Errorf("%+v accepted", l)
		}
	}
	for _, l := range []ext.ContainerLimits{{}, {MemoryMB: 64, CPUs: 0.01, Pids: 16}, {Pids: 4194304}, {CPUs: 64}} {
		if err := b.Check(l); err != nil {
			t.Errorf("%+v refused: %v", l, err)
		}
	}
	if err := b.SetDefaults(ext.ContainerLimits{MemoryMB: 10}); err == nil || b.Saved() != (ext.ContainerLimits{}) {
		t.Errorf("an out-of-bounds default was saved: %v, %+v", err, b.Saved())
	}
}

// Docker refuses more CPUs than the host has, and a limit above the host's
// memory limits nothing: both are refused before they are stored, so they
// cannot fail every start afterwards.
func TestCheckAgainstTheHost(t *testing.T) {
	b := NewLimitBook(t.TempDir(), envLimits)
	b.SetCapacity(6, 16<<30)
	if c, m := b.Capacity(); c != 6 || m != 16<<30 {
		t.Errorf("capacity = %d, %d", c, m)
	}
	for _, l := range []ext.ContainerLimits{{CPUs: 6.5}, {MemoryMB: 17000}} {
		if err := b.Check(l); err == nil {
			t.Errorf("%+v accepted on a 6 CPU / 16 GiB host", l)
		}
	}
	if err := b.Check(ext.ContainerLimits{CPUs: 6, MemoryMB: 16384}); err != nil {
		t.Errorf("the host's own size refused: %v", err)
	}
}

// A defaults file that cannot be read is unknown, not "the environment's
// values": the runtime must not converge running containers to them.
func TestUnreadableDefaultsAreUnknown(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "containers"), 0o755)
	os.WriteFile(filepath.Join(dir, "containers", "limits.json"), []byte("{not json"), 0o644)
	b := NewLimitBook(dir, envLimits)
	if b.Err() == nil || b.Known() {
		t.Fatal("a garbage file read as known")
	}
	if b.Defaults() != envLimits {
		t.Errorf("defaults while unknown = %+v", b.Defaults())
	}
	if err := b.SetDefaults(ext.ContainerLimits{Pids: 512}); err != nil {
		t.Fatal(err)
	}
	if b.Err() != nil || !b.Known() || b.Defaults().Pids != 512 {
		t.Errorf("saving did not clear the error: %v %+v", b.Err(), b.Defaults())
	}

	// A file whose values are out of bounds is no better than garbage.
	os.WriteFile(filepath.Join(dir, "containers", "limits.json"), []byte(`{"memory_mb":1}`), 0o644)
	if NewLimitBook(dir, envLimits).Known() {
		t.Error("an out-of-bounds file read as known")
	}
}

// A value saved before the host's size was known, or on a larger host before
// a move, is used at the host's size: Docker refuses more CPUs than the host
// has, and would refuse every start.
func TestForFitsTheHost(t *testing.T) {
	b := NewLimitBook(t.TempDir(), envLimits)
	if err := b.SetDefaults(ext.ContainerLimits{CPUs: 8}); err != nil {
		t.Fatal(err)
	}
	b.SetCapacity(6, 16<<30)
	if got := b.For(map[string]ext.ContainerLimits{"db": {MemoryMB: 20000}}, "db"); got.CPUs != 6 || got.MemoryMB != 16384 || got.Pids != 256 {
		t.Errorf("db = %+v", got)
	}
	if got := b.Defaults(); got.CPUs != 6 {
		t.Errorf("defaults = %+v", got)
	}
	if b.Saved().CPUs != 8 {
		t.Errorf("the admin's saved value was rewritten: %+v", b.Saved())
	}
}
