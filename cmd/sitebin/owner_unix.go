//go:build unix

package main

import (
	"os"
	"syscall"
)

// ownerOf answers the uid and gid owning path: the agent gives a restored
// tree, and its socket, to the container's user.
func ownerOf(path string) *[2]int {
	fi, err := os.Stat(path)
	if err != nil {
		return nil
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return nil
	}
	return &[2]int{int(st.Uid), int(st.Gid)}
}
