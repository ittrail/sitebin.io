//go:build windows

package gitrepo

import "os/exec"

// ownProcessGroup is a no-op on Windows: CommandContext's kill of the git
// process is all a test host needs.
func ownProcessGroup(cmd *exec.Cmd) {}
