//go:build !unix

package main

import "os"

// openNoFollow is a plain open where the platform has no O_NOFOLLOW; the
// SameFile check after the open still catches a swapped file.
func openNoFollow(p string) (*os.File, error) { return os.Open(p) }
