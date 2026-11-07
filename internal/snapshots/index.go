package snapshots

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// Index caches each snapshot's per-site summaries as <Dir>/<id>.json, so a
// site's versions are a read of small files, not a walk of every archive.
// An index outlives its snapshot only until the next Prune.
type Index struct{ Dir string }

func (x Index) path(id string) string { return filepath.Join(x.Dir, id+".json") }

// Get answers the summaries of snapshot id, and whether it is indexed.
func (x Index) Get(id string) (map[string]SiteSummary, bool) {
	if !ValidSnapshotID(id) {
		return nil, false
	}
	b, err := os.ReadFile(x.path(id))
	if err != nil {
		return nil, false
	}
	var m map[string]SiteSummary
	if json.Unmarshal(b, &m) != nil {
		return nil, false
	}
	return m, true
}

// Put stores the summaries of snapshot id.
func (x Index) Put(id string, m map[string]SiteSummary) error {
	if !ValidSnapshotID(id) {
		return os.ErrInvalid
	}
	if err := os.MkdirAll(x.Dir, 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	tmp := x.path(id) + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, x.path(id))
}

// Prune deletes the index of every snapshot not in keep: what a snapshot
// held of a site must not outlive the snapshot (the 30-day promise).
func (x Index) Prune(keep []string) {
	k := map[string]bool{}
	for _, id := range keep {
		k[id] = true
	}
	entries, err := os.ReadDir(x.Dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		id, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok {
			if strings.HasSuffix(e.Name(), ".json.tmp") {
				os.Remove(filepath.Join(x.Dir, e.Name()))
			}
			continue
		}
		if !k[id] {
			os.Remove(filepath.Join(x.Dir, e.Name()))
		}
	}
}
