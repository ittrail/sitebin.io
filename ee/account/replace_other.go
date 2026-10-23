//go:build ee && !windows

package account

import "os"

// replaceFile renames tmp over dst; POSIX replaces an open file atomically.
// See replace_windows.go for why Windows needs more.
func replaceFile(tmp, dst string) error {
	return os.Rename(tmp, dst)
}
