package store

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
)

// makeLink creates the index link at linkPath pointing to the site directory.
// On Linux (production) it is a relative symlink, exactly as the PRD's storage
// layout specifies. On Windows — used only for local development and tests,
// where symlinks need elevation — it falls back to a directory junction, which
// resolves identically for our read paths.
func makeLink(linkPath, relTarget, absTarget string) error {
	err := os.Symlink(relTarget, linkPath)
	if err == nil || runtime.GOOS != "windows" {
		return err
	}
	out, jerr := exec.Command("cmd", "/c", "mklink", "/J", linkPath, absTarget).CombinedOutput()
	if jerr != nil {
		return fmt.Errorf("junction fallback: %v: %s", jerr, out)
	}
	return nil
}

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
