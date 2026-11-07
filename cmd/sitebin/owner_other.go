//go:build !unix

package main

// ownerOf has no owner to answer where there are no uids.
func ownerOf(string) *[2]int { return nil }
