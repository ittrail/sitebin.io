package snapshots

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The test binary doubles as a fake borg: with FAKE_BORG_LOG set it records
// its arguments, answers from canned output and exits like borg would.
func TestMain(m *testing.M) {
	if log := os.Getenv("FAKE_BORG_LOG"); log != "" {
		os.Exit(fakeBorg(log, os.Args[1:]))
	}
	// …and as the agent's backup-now command: it writes a new archive.
	if dir := os.Getenv("FAKE_BACKUP_DIR"); dir != "" {
		if os.Getenv("FAKE_BACKUP_FAIL") != "" {
			fmt.Fprintln(os.Stderr, "repository locked")
			os.Exit(2)
		}
		os.Exit(fakeBackup(dir))
	}
	os.Exit(m.Run())
}

func fakeBorg(log string, args []string) int {
	f, _ := os.OpenFile(log, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	fmt.Fprintln(f, strings.Join(args, " "))
	f.Close()
	cmd := ""
	for _, a := range args {
		if !strings.HasPrefix(a, "-") {
			cmd = a
			break
		}
	}
	last := args[len(args)-1]
	switch {
	case cmd == "list" && hasArg(args, "--json"):
		fmt.Print(`{"archives":[{"name":"hourly-2026-11-07T0917Z","start":"2026-11-07T09:17:01.000000","time":"2026-11-07T09:17:01.000000"},` +
			`{"name":"nightly-2026-11-07T0320Z","start":"2026-11-07T03:20:00.000000"},{"name":"bad name","start":"2026-11-07T03:00:00.000000"}]}`)
	case cmd == "list" && hasArg(args, "--json-lines"):
		if strings.Contains(strings.Join(args, " "), "::gone") {
			fmt.Fprintln(os.Stderr, "Archive gone does not exist")
			return 2
		}
		fmt.Println(`{"type":"d","path":"sites","size":0,"mtime":"2026-11-07T09:00:00.000000"}`)
		fmt.Println(`{"type":"-","path":"sites/` + siteA + `/meta.json","size":12,"mtime":"2026-11-07T09:00:00.000000"}`)
		fmt.Println(`{"type":"-","path":"sites/` + siteA + `/files/index.html","size":5,"mtime":"2026-11-07T09:00:00.123456"}`)
		fmt.Println(`{"type":"l","path":"sites/` + siteA + `/files/out","size":0,"mtime":"2026-11-07T09:00:00.000000"}`)
	case cmd == "extract":
		if strings.HasSuffix(last, "missing.json") {
			fmt.Fprintln(os.Stderr, "Include pattern '"+last+"' never matched.")
			return 1
		}
		fmt.Print(`{"mode":"x"}`)
	case cmd == "export-tar":
		fmt.Print("TARDATA")
	}
	return 0
}

func hasArg(args []string, a string) bool {
	for _, x := range args {
		if x == a {
			return true
		}
	}
	return false
}

func fakeSource(t *testing.T) (*BorgSource, string) {
	t.Helper()
	log := filepath.Join(t.TempDir(), "argv.log")
	return &BorgSource{Bin: os.Args[0], Env: []string{"FAKE_BORG_LOG=" + log}, Location: time.UTC}, log
}

func argvLog(t *testing.T, log string) []string {
	b, _ := os.ReadFile(log)
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

func TestBorgSourceListParsesArchives(t *testing.T) {
	src, log := fakeSource(t)
	got, err := src.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != "hourly-2026-11-07T0917Z" || got[0].Kind != "hourly" || got[1].Kind != "nightly" {
		t.Fatalf("List = %+v (an invalid name must be left out, newest first)", got)
	}
	if !got[0].At.Equal(time.Date(2026, 11, 7, 9, 17, 1, 0, time.UTC)) {
		t.Errorf("At = %v", got[0].At)
	}
	if a := argvLog(t, log); !strings.Contains(a[0], "list") || !strings.Contains(a[0], "--json") || !strings.Contains(a[0], "--lock-wait") {
		t.Errorf("argv = %q", a[0])
	}
}

func TestBorgSourceWalkReadTar(t *testing.T) {
	src, log := fakeSource(t)
	ctx := context.Background()
	var items []Item
	if err := src.Walk(ctx, "hourly-1", "sites/", func(it Item) error { items = append(items, it); return nil }); err != nil {
		t.Fatal(err)
	}
	if len(items) != 4 || !items[2].Regular || items[3].Regular || items[2].Size != 5 || items[2].Path != "sites/"+siteA+"/files/index.html" {
		t.Fatalf("items = %+v", items)
	}
	if items[2].Mtime.Nanosecond() != 123456000 {
		t.Errorf("mtime = %v", items[2].Mtime)
	}
	if err := src.Walk(ctx, "gone", "sites/", func(Item) error { return nil }); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown archive: %v", err)
	}
	b, err := src.ReadFile(ctx, "hourly-1", "sites/"+siteA+"/meta.json", 1<<20)
	if err != nil || string(b) != `{"mode":"x"}` {
		t.Fatalf("ReadFile = %q %v", b, err)
	}
	if _, err := src.ReadFile(ctx, "hourly-1", "sites/x/missing.json", 1<<20); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing file: %v", err)
	}
	var sb strings.Builder
	if err := src.Tar(ctx, "hourly-1", []string{"sites/" + siteA + "/files"}, &sb); err != nil || sb.String() != "TARDATA" {
		t.Fatalf("Tar = %q %v", sb.String(), err)
	}
	a := argvLog(t, log)
	want := []string{
		"list --lock-wait 120 --json-lines ::hourly-1 sites/",
		"extract --lock-wait 120 --stdout ::hourly-1 sites/" + siteA + "/meta.json",
		"export-tar --lock-wait 120 ::hourly-1 - sites/" + siteA + "/files",
	}
	for _, w := range want {
		found := false
		for _, l := range a {
			if l == w {
				found = true
			}
		}
		if !found {
			t.Errorf("no call %q in %q", w, a)
		}
	}
	// an id that was never listed is refused before borg runs
	if err := src.Tar(ctx, "-x", nil, io.Discard); err == nil {
		t.Error("an invalid snapshot id reached borg")
	}
}
