//go:build ee && windows

package account

import (
	"errors"
	"os"
	"syscall"
	"time"
)

// replaceFile renames tmp over dst. On Windows a rename onto a file another
// handle has open fails with "access denied" (or a sharing violation) for as
// long as that handle lives -- and ByID reads account.json without the
// account lock, so a concurrent read made an Update fail and its change was
// lost (TestConcurrentUpdate, 2026-10-21). The reader is gone within
// microseconds, so the rename is retried briefly; any other error, or one that
// outlasts the window, is returned as before. Linux replaces an open file
// without complaint, which is why this file is Windows-only.
func replaceFile(tmp, dst string) error {
	deadline := time.Now().Add(time.Second)
	for wait := time.Millisecond; ; wait *= 2 {
		err := os.Rename(tmp, dst)
		if err == nil || !transientRenameError(err) || time.Now().After(deadline) {
			return err
		}
		if wait > 50*time.Millisecond {
			wait = 50 * time.Millisecond
		}
		time.Sleep(wait)
	}
}

// errorSharingViolation is ERROR_SHARING_VIOLATION, which the syscall package
// does not name.
const errorSharingViolation = syscall.Errno(32)

func transientRenameError(err error) bool {
	return errors.Is(err, syscall.ERROR_ACCESS_DENIED) || errors.Is(err, errorSharingViolation)
}
