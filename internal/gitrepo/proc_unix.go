//go:build !windows

package gitrepo

import (
	"os/exec"
	"syscall"
)

// ownProcessGroup puts git in its own process group and makes a cancelled
// context kill the whole group, so index-pack and unpack-objects die with
// receive-pack instead of keeping its pipes open.
func ownProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
