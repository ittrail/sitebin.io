package store

import "os"

// linkExists reports whether an index link entry exists (even if dangling).
func linkExists(linkPath string) bool {
	_, err := os.Lstat(linkPath)
	return err == nil
}

// linkDangling reports whether linkPath exists but its target does not.
func linkDangling(linkPath string) bool {
	if _, err := os.Lstat(linkPath); err != nil {
		return false
	}
	_, err := os.Stat(linkPath)
	return err != nil
}
