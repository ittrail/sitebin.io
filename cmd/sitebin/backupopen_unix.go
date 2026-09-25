//go:build unix

package main

import (
	"os"
	"syscall"
)

// openInRoot opens name of r for reading, never blocking on a FIFO
// (O_NONBLOCK, harmless on a regular file). The root keeps the open inside
// it; a file swapped for a link is caught by the SameFile check after it.
func openInRoot(r *os.Root, name string) (*os.File, error) {
	return r.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK, 0)
}
