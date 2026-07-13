// Package supervisor runs Caddy as a child of the backend for the all-in-one
// container: one image, one process tree, graceful shutdown for both.
package supervisor

import (
	"context"
	"os"
	"os/exec"
	"runtime"
	"time"
)

// StartCaddy launches caddy with the given Caddyfile. The returned channel
// receives the process exit error (nil on clean exit). Cancelling ctx asks
// Caddy to shut down gracefully (SIGTERM) before it is killed.
func StartCaddy(ctx context.Context, caddyfile string) (<-chan error, error) {
	cmd := exec.CommandContext(ctx, "caddy", "run", "--config", caddyfile, "--adapter", "caddyfile")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if runtime.GOOS != "windows" {
		cmd.Cancel = func() error { return cmd.Process.Signal(os.Interrupt) }
	}
	cmd.WaitDelay = 15 * time.Second // SIGKILL backstop after graceful ask
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	return done, nil
}
