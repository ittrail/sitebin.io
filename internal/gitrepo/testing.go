package gitrepo

import (
	"os/exec"
	"testing"
)

// NeedGit skips the test when there is no git binary, and returns its path.
// It lives outside a _test file so other packages' tests can call it.
func NeedGit(t testing.TB) string {
	t.Helper()
	p, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git binary not available")
	}
	return p
}
