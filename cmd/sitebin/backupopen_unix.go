//go:build unix

package main

import (
	"os"
	"syscall"
)

// openNoFollow opens p for reading, refusing a link (O_NOFOLLOW) and never
// blocking on a FIFO (O_NONBLOCK, harmless on a regular file).
func openNoFollow(p string) (*os.File, error) {
	return os.OpenFile(p, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
}
