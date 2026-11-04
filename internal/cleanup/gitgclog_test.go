package cleanup

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
)

// A collection cancelled on purpose — a delete, a lock, the shutdown — is
// not an error in the log.
func TestCollectGitCancelledIsNotAnError(t *testing.T) {
	st, r := collectFixture(t)
	site := gitSite(t, st, r)
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	collectOne(ctx, st, r, site)

	out := buf.String()
	if strings.Contains(out, "level=ERROR") {
		t.Errorf("a cancelled collection logged an error:\n%s", out)
	}
	if !strings.Contains(out, "level=DEBUG") || !strings.Contains(out, "git gc stopped") {
		t.Errorf("a cancelled collection is not logged at debug:\n%s", out)
	}
}
