package snapshots

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"
)

// BorgSource is a Borg repository, reached through the borg command with the
// environment the agent runs in (BORG_REPO, BORG_PASSCOMMAND,
// BORG_REMOTE_PATH — the agent's EnvironmentFile) plus Env. Every command
// waits up to two minutes for the repository lock a backup run may hold.
type BorgSource struct {
	Bin string   // the borg binary; "borg" when empty
	Env []string // added to the process environment
	// Location is the zone Borg's timestamps are in: it prints them without
	// one, in the host's. time.Local when nil.
	Location *time.Location
}

const borgLockWait = "120"

// borgTime is how borg --json prints a time.
const borgTime = "2006-01-02T15:04:05.999999"

func (b *BorgSource) loc() *time.Location {
	if b.Location != nil {
		return b.Location
	}
	return time.Local
}

func (b *BorgSource) command(ctx context.Context, args ...string) (*exec.Cmd, *bytes.Buffer) {
	bin := b.Bin
	if bin == "" {
		bin = "borg"
	}
	c := exec.CommandContext(ctx, bin, args...)
	c.Env = append(os.Environ(), b.Env...)
	var stderr bytes.Buffer
	c.Stderr = &limitedBuffer{b: &stderr, max: 64 << 10}
	return c, &stderr
}

// limitedBuffer keeps the first max bytes of what borg says on stderr.
type limitedBuffer struct {
	b   *bytes.Buffer
	max int
}

func (l *limitedBuffer) Write(p []byte) (int, error) {
	if room := l.max - l.b.Len(); room > 0 {
		l.b.Write(p[:min(len(p), room)])
	}
	return len(p), nil
}

// borgErr turns a failed borg run into ErrNotFound where it means that, and
// otherwise into an error carrying borg's last line.
func borgErr(err error, stderr *bytes.Buffer) error {
	if err == nil {
		return nil
	}
	msg := strings.TrimSpace(stderr.String())
	if strings.Contains(msg, "does not exist") || strings.Contains(msg, "never matched") {
		return ErrNotFound
	}
	if i := strings.LastIndexByte(msg, '\n'); i >= 0 {
		msg = msg[i+1:]
	}
	if msg == "" {
		return fmt.Errorf("borg: %w", err)
	}
	return fmt.Errorf("borg: %s (%w)", msg, err)
}

func (b *BorgSource) archive(id string) (string, error) {
	if !ValidSnapshotID(id) {
		return "", ErrNotFound
	}
	return "::" + id, nil
}

// List answers the repository's archives, newest first.
func (b *BorgSource) List(ctx context.Context) ([]Snapshot, error) {
	c, stderr := b.command(ctx, "list", "--lock-wait", borgLockWait, "--json")
	out, err := c.Output()
	if err != nil {
		return nil, borgErr(err, stderr)
	}
	var doc struct {
		Archives []struct {
			Name  string `json:"name"`
			Start string `json:"start"`
		} `json:"archives"`
	}
	if err := json.Unmarshal(out, &doc); err != nil {
		return nil, fmt.Errorf("borg list: %w", err)
	}
	var snaps []Snapshot
	for _, a := range doc.Archives {
		if !ValidSnapshotID(a.Name) || strings.HasSuffix(a.Name, ".checkpoint") {
			continue
		}
		at, err := time.ParseInLocation(borgTime, a.Start, b.loc())
		if err != nil {
			continue
		}
		snaps = append(snaps, Snapshot{ID: a.Name, At: at.UTC(), Kind: KindOf(a.Name)})
	}
	sort.Slice(snaps, func(i, j int) bool { return snaps[i].At.After(snaps[j].At) })
	return snaps, nil
}

// Walk calls fn for every entry of archive id under prefix.
func (b *BorgSource) Walk(ctx context.Context, id, prefix string, fn func(Item) error) error {
	a, err := b.archive(id)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	args := []string{"list", "--lock-wait", borgLockWait, "--json-lines", a}
	if prefix != "" {
		args = append(args, prefix) // an empty path would match nothing
	}
	c, stderr := b.command(ctx, args...)
	out, err := c.StdoutPipe()
	if err != nil {
		return err
	}
	if err := c.Start(); err != nil {
		return err
	}
	sc := bufio.NewScanner(out)
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	var fnErr error
	for sc.Scan() {
		var e struct {
			Type  string `json:"type"`
			Path  string `json:"path"`
			Size  int64  `json:"size"`
			Mtime string `json:"mtime"`
		}
		if json.Unmarshal(sc.Bytes(), &e) != nil {
			continue
		}
		mt, _ := time.ParseInLocation(borgTime, e.Mtime, b.loc())
		if fnErr = fn(Item{Path: strings.TrimPrefix(e.Path, "./"), Size: e.Size, Mtime: mt.UTC(), Regular: e.Type == "-"}); fnErr != nil {
			cancel()
			break
		}
	}
	scanErr := sc.Err()
	io.Copy(io.Discard, out)
	waitErr := c.Wait()
	if fnErr != nil {
		return fnErr
	}
	if waitErr != nil {
		return borgErr(waitErr, stderr)
	}
	return scanErr
}

// Tar writes borg export-tar of archive id, limited to paths, to w.
func (b *BorgSource) Tar(ctx context.Context, id string, paths []string, w io.Writer) error {
	a, err := b.archive(id)
	if err != nil {
		return err
	}
	// Borg 1.2 writes GNU tar, its times cut to whole seconds (no PAX before
	// 1.4): a file restored from an hourly archive keeps its second, not its
	// fraction, so the version may not show as the current one afterwards.
	args := append([]string{"export-tar", "--lock-wait", borgLockWait, a, "-"}, paths...)
	c, stderr := b.command(ctx, args...)
	c.Stdout = w
	return borgErr(c.Run(), stderr)
}

// ReadFile answers one file of archive id through borg extract --stdout.
func (b *BorgSource) ReadFile(ctx context.Context, id, p string, max int64) ([]byte, error) {
	a, err := b.archive(id)
	if err != nil {
		return nil, err
	}
	c, stderr := b.command(ctx, "extract", "--lock-wait", borgLockWait, "--stdout", a, p)
	out, err := c.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := c.Start(); err != nil {
		return nil, err
	}
	data, readErr := io.ReadAll(io.LimitReader(out, max+1))
	io.Copy(io.Discard, out)
	if err := borgErr(c.Wait(), stderr); err != nil {
		return nil, err
	}
	if readErr != nil {
		return nil, readErr
	}
	if int64(len(data)) > max {
		return nil, fmt.Errorf("%s is more than %d bytes", p, max)
	}
	if len(data) == 0 && strings.Contains(stderr.String(), "never matched") {
		return nil, ErrNotFound
	}
	return data, nil
}
