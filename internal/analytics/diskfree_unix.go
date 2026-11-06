//go:build linux || darwin || freebsd

package analytics

import "syscall"

// DiskFree is the space available to an unprivileged writer on the volume
// that holds path, in bytes.
func DiskFree(path string) (uint64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, err
	}
	return uint64(st.Bavail) * uint64(st.Bsize), nil
}

// DiskSize is the size of the volume that holds path, in bytes: with
// DiskFree, what the operator's health view states as used of total.
func DiskSize(path string) (uint64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, err
	}
	return uint64(st.Blocks) * uint64(st.Bsize), nil
}
