package provenance

import (
	"path/filepath"
	"testing"
	"time"
)

// Every push is its own entry: merged, a second push's refs were folded into
// the first's detail (and cut at its length), and "the last push" read from
// the log showed the older one.
func TestRecordNeverMergesGitPushes(t *testing.T) {
	p := filepath.Join(t.TempDir(), "p.jsonl")
	push := func(at time.Time, detail string) Entry {
		return Entry{Time: at, Action: ActionGitPush, Surface: SurfaceGit, Auth: AuthPassword, IP: "198.51.100.1", UA: "git/2.54.0", Detail: detail}
	}
	if err := Record(p, push(t0, "refs/heads/main 1111111 created, 1.2 KB received")); err != nil {
		t.Fatal(err)
	}
	if err := Record(p, push(t0.Add(time.Minute), "refs/heads/main 1111111..2222222, 300 B received")); err != nil {
		t.Fatal(err)
	}
	got, err := Read(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 entries for two pushes, got %d: %+v", len(got), got)
	}
	if got[0].Times() != 1 || got[1].Times() != 1 || got[1].Detail != "refs/heads/main 1111111..2222222, 300 B received" {
		t.Fatalf("entries: %+v", got)
	}
}
