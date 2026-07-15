// Package cleanup deletes expired sites and repairs the filesystem indexes.
// Between expiry and deletion (a 24h grace window) the authz endpoint serves
// 410 Gone, so visitors see a clear signal before the site disappears.
package cleanup

import (
	"context"
	"log/slog"
	"os"
	"time"

	"github.com/ittrail/sitebin.io/internal/store"
)

// grace is how long an expired site is kept (serving 410) before deletion.
const grace = 24 * time.Hour

// Sweep removes sites expired for longer than the grace period and prunes
// index links whose target no longer exists. It returns the number of sites
// deleted.
func Sweep(st *store.Store, now time.Time) (int, error) {
	sites, err := st.AllSites()
	if err != nil {
		return 0, err
	}
	removed := 0
	for _, site := range sites {
		if site.Meta.ExpiresAt != nil && now.After(site.Meta.ExpiresAt.Add(grace)) {
			if err := st.Delete(site); err != nil {
				slog.Error("cleanup: delete site", "id", site.ViewID, "err", err)
				continue
			}
			slog.Info("cleanup: deleted expired site", "id", site.ViewID)
			removed++
		}
	}
	dangling, err := st.DanglingIndexLinks()
	if err != nil {
		return removed, err
	}
	for _, link := range dangling {
		if err := os.Remove(link); err != nil {
			slog.Error("cleanup: prune link", "link", link, "err", err)
		} else {
			slog.Info("cleanup: pruned dangling link", "link", link)
		}
	}
	return removed, nil
}

// Run sweeps on the given interval until ctx is cancelled.
func Run(ctx context.Context, st *store.Store, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if _, err := Sweep(st, time.Now()); err != nil {
				slog.Error("cleanup sweep", "err", err)
			}
		}
	}
}
