package store

import (
	"archive/zip"
	"fmt"
	"io"
	"os"
	"strings"
)

// ExtractZip unpacks an uploaded zip archive into the site's content root,
// applying the same path sanitation and quota rules as direct uploads.
// Symlink entries are rejected outright and byte budgets are enforced on the
// actual decompressed stream (zip headers are not trusted).
//
// The archive is bounded BEFORE anything is written — more entries than the
// file cap, or a name twice, is refused outright — and the byte and file
// budgets are kept in a running counter across entries rather than re-walking
// the site for each one. An earlier version walked the whole site per entry
// and bounded each entry by the remaining budget alone, so a 1 MB archive of
// 5,000 entries decompressing to 100 MB each cost 5,000 directory walks and
// 500 GB of zeros, all inside the site lock.
func (s *Store) ExtractZip(site *Site, r io.ReaderAt, size int64) error {
	zr, err := zip.NewReader(r, size)
	if err != nil {
		return fmt.Errorf("read zip: %w", err)
	}
	l := s.lockSite(site.ViewID)
	l.Lock()
	defer l.Unlock()

	maxBytes, maxFiles := s.EffMaxBytes(site), s.EffMaxFiles(site)
	type entry struct {
		f   *zip.File
		rel string
	}
	entries := make([]entry, 0, len(zr.File))
	seen := make(map[string]bool, len(zr.File))
	for _, f := range zr.File {
		name := strings.ReplaceAll(f.Name, `\`, "/") // tolerate Windows-built zips
		if strings.HasSuffix(name, "/") {
			continue // directories materialize via file writes
		}
		if f.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: zip entry %q is a symlink", ErrBadPath, f.Name)
		}
		rel, err := CleanRelPath(name)
		if err != nil {
			return fmt.Errorf("zip entry %q: %w", f.Name, err)
		}
		if seen[rel] {
			return fmt.Errorf("%w: zip entry %q appears twice", ErrBadPath, f.Name)
		}
		seen[rel] = true
		entries = append(entries, entry{f: f, rel: rel})
	}
	if len(entries) > maxFiles {
		return fmt.Errorf("%w: the archive holds %d files, the site allows %d", ErrTooManyFiles, len(entries), maxFiles)
	}

	dir := site.ContentDir()
	used, count, err := usage(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		rc, err := e.f.Open()
		if err != nil {
			return fmt.Errorf("zip entry %q: %w", e.f.Name, err)
		}
		written, existing, err := s.writeFileLocked(site, e.rel, rc, used, count, maxBytes, maxFiles)
		rc.Close()
		if err != nil {
			return fmt.Errorf("zip entry %q: %w", e.f.Name, err)
		}
		used += written - existing
		if existing == 0 {
			count++
		}
	}
	return s.renewExpiryLocked(site)
}
