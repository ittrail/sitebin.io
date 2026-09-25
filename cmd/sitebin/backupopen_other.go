//go:build !unix

package main

import "os"

// openInRoot is a plain open inside r where the platform has no O_NONBLOCK;
// the SameFile check after the open still catches a swapped file.
func openInRoot(r *os.Root, name string) (*os.File, error) { return r.Open(name) }
