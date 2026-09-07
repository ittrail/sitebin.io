//go:build !windows

package store

import "os"

// makeLink creates the index link at linkPath: a relative symlink to the site
// directory, exactly as the storage layout specifies. absTarget is unused
// here; the Windows build needs it for its junction fallback.
func makeLink(linkPath, relTarget, _ string) error {
	return os.Symlink(relTarget, linkPath)
}
