//go:build windows

package store

import (
	"fmt"
	"os"
	"os/exec"
)

// makeLink creates the index link at linkPath pointing to the site directory.
// Windows is a development and test platform only: creating a symlink needs
// elevation there, so when that fails the link is a directory junction, which
// resolves identically for every read path. The production (Linux) build has
// no shell-out at all — see links_other.go.
func makeLink(linkPath, relTarget, absTarget string) error {
	if err := os.Symlink(relTarget, linkPath); err == nil {
		return nil
	}
	out, err := exec.Command("cmd", "/c", "mklink", "/J", linkPath, absTarget).CombinedOutput()
	if err != nil {
		return fmt.Errorf("junction fallback: %v: %s", err, out)
	}
	return nil
}
