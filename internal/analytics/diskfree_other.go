//go:build !linux && !darwin && !freebsd && !windows

package analytics

import "errors"

// DiskFree cannot be asked on this platform: the recorder's free-space
// floor treats the error as unknown and keeps writing.
func DiskFree(string) (uint64, error) { return 0, errors.ErrUnsupported }
