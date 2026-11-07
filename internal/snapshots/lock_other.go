//go:build !unix

package snapshots

import "time"

// lockFile is a no-op where there is no flock (the agent runs on Linux;
// this keeps the package building for tests elsewhere).
func lockFile(string, time.Duration) (func(), error) { return func() {}, nil }
