//go:build windows

package gitrepo

import (
	"os/exec"
	"strconv"
)

// ownProcessGroup makes a cancelled context end git's whole process tree.
// Windows kills only the process CommandContext started; git's children
// (index-pack, pack-objects) would run on, holding open the pack they write,
// and a delete could not remove the repository. taskkill /T ends the tree.
// Windows is a test host only: the image is Linux (proc_unix.go).
func ownProcessGroup(cmd *exec.Cmd) {
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		if err := exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid)).Run(); err != nil {
			return cmd.Process.Kill()
		}
		return nil
	}
}
