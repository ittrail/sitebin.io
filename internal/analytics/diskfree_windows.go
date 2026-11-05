//go:build windows

package analytics

import "golang.org/x/sys/windows"

// DiskFree is the space available to the calling user on the volume that
// holds path, in bytes.
func DiskFree(path string) (uint64, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	var free uint64
	if err := windows.GetDiskFreeSpaceEx(p, &free, nil, nil); err != nil {
		return 0, err
	}
	return free, nil
}
