package snapshots

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// DirSource is a folder of archives `sitebin backup` wrote (<id>.tar.gz, or a
// plain <id>.tar): the README's cron recipe, for an instance without Borg.
// A snapshot's time is its file's modification time.
type DirSource struct{ Dir string }

var dirExts = []string{".tar.gz", ".tgz", ".tar"}

// List answers the archives in the folder, newest first.
func (d *DirSource) List(ctx context.Context) ([]Snapshot, error) {
	entries, err := os.ReadDir(d.Dir)
	if err != nil {
		return nil, err
	}
	var out []Snapshot
	for _, e := range entries {
		if !e.Type().IsRegular() {
			continue
		}
		for _, ext := range dirExts {
			id, ok := strings.CutSuffix(e.Name(), ext)
			if !ok || !ValidSnapshotID(id) {
				continue
			}
			fi, err := e.Info()
			if err != nil {
				break
			}
			out = append(out, Snapshot{ID: id, At: fi.ModTime().UTC(), Kind: KindOf(id)})
			break
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].At.After(out[j].At) })
	return out, nil
}

// open answers a tar reader over snapshot id and the file to close.
func (d *DirSource) open(id string) (*tar.Reader, io.Closer, error) {
	if !ValidSnapshotID(id) {
		return nil, nil, ErrNotFound
	}
	for _, ext := range dirExts {
		f, err := os.Open(filepath.Join(d.Dir, id+ext))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, nil, err
		}
		br := bufio.NewReader(f)
		var in io.Reader = br
		if magic, err := br.Peek(2); err == nil && magic[0] == 0x1f && magic[1] == 0x8b {
			gz, err := gzip.NewReader(br)
			if err != nil {
				f.Close()
				return nil, nil, err
			}
			in = gz
		}
		return tar.NewReader(in), f, nil
	}
	return nil, nil, ErrNotFound
}

// entryName is a tar entry's path relative to the data root.
func entryName(h *tar.Header) string {
	n := strings.TrimPrefix(h.Name, "./")
	return strings.TrimSuffix(path.Clean("/" + n)[1:], "/")
}

// under reports whether name is one of paths or inside one (all when nil).
func under(name string, paths []string) bool {
	if paths == nil {
		return true
	}
	for _, p := range paths {
		p = strings.TrimSuffix(p, "/")
		if name == p || strings.HasPrefix(name, p+"/") {
			return true
		}
	}
	return false
}

// Walk calls fn for every entry under prefix.
func (d *DirSource) Walk(ctx context.Context, id, prefix string, fn func(Item) error) error {
	tr, c, err := d.open(id)
	if err != nil {
		return err
	}
	defer c.Close()
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		name := entryName(h)
		if !strings.HasPrefix(name, prefix) && !strings.HasPrefix(name+"/", prefix) {
			continue
		}
		if err := fn(Item{Path: name, Size: h.Size, Mtime: h.ModTime, Regular: h.Typeflag == tar.TypeReg}); err != nil {
			return err
		}
	}
}

// Tar copies the entries at or under paths into a new tar on w.
func (d *DirSource) Tar(ctx context.Context, id string, paths []string, w io.Writer) error {
	tr, c, err := d.open(id)
	if err != nil {
		return err
	}
	defer c.Close()
	tw := tar.NewWriter(w)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return tw.Close()
		}
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if !under(entryName(h), paths) {
			continue
		}
		if err := tw.WriteHeader(h); err != nil {
			return err
		}
		if h.Typeflag == tar.TypeReg {
			if _, err := io.Copy(tw, tr); err != nil {
				return err
			}
		}
	}
}

// ReadFile answers one regular file of snapshot id.
func (d *DirSource) ReadFile(ctx context.Context, id, p string, max int64) ([]byte, error) {
	tr, c, err := d.open(id)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil, ErrNotFound
		}
		if err != nil {
			return nil, err
		}
		if entryName(h) != p || h.Typeflag != tar.TypeReg {
			continue
		}
		if h.Size > max {
			return nil, fmt.Errorf("%s is %d bytes, more than %d", p, h.Size, max)
		}
		return io.ReadAll(io.LimitReader(tr, max))
	}
}
