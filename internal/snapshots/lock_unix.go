//go:build unix

package snapshots

import (
	"fmt"
	"os"
	"syscall"
	"time"
)

// lockFile takes an exclusive flock on path — the file the backup script
// locks — waiting up to wait. "" takes nothing.
func lockFile(path string, wait time.Duration) (func(), error) {
	if path == "" {
		return func() {}, nil
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(wait)
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return func() { syscall.Flock(int(f.Fd()), syscall.LOCK_UN); f.Close() }, nil
		}
		if time.Now().After(deadline) {
			f.Close()
			return nil, fmt.Errorf("%s still locked after %s", path, wait)
		}
		time.Sleep(2 * time.Second)
	}
}
