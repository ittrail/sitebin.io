package store

import (
	"archive/zip"
	"compress/flate"
	"errors"
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
	maxBytes, maxFiles := s.EffMaxBytes(site), s.EffMaxFiles(site)
	entries, err := zipEntries(r, size, maxFiles)
	if err != nil {
		return err
	}
	l := s.lockSite(site.ViewID)
	l.Lock()
	defer l.Unlock()
	used, count, err := usage(site.ContentDir())
	if err != nil {
		return err
	}
	root, err := openContent(site)
	if err != nil {
		return err
	}
	defer root.Close()
	if _, _, err := extractEntries(root, entries, used, count, maxBytes, maxFiles); err != nil {
		return err
	}
	return s.renewExpiryLocked(site)
}

type zipEntry struct {
	f   *zip.File
	rel string
}

// zipEntries opens an archive and bounds it before anything is written: a
// symlink, a bad path, a name twice or more entries than maxFiles is refused.
func zipEntries(r io.ReaderAt, size int64, maxFiles int) ([]zipEntry, error) {
	zr, err := zip.NewReader(r, size)
	if err != nil {
		if damagedEntry(err) {
			return nil, fmt.Errorf("%w: %v", ErrBadArchive, err)
		}
		return nil, fmt.Errorf("read zip: %w", err) // the spool, not the upload
	}
	entries := make([]zipEntry, 0, len(zr.File))
	seen := make(map[string]bool, len(zr.File))
	for _, f := range zr.File {
		name := strings.ReplaceAll(f.Name, `\`, "/") // tolerate Windows-built zips
		if strings.HasSuffix(name, "/") {
			continue // directories materialize via file writes
		}
		if f.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("%w: zip entry %q is a symlink", ErrBadPath, f.Name)
		}
		rel, err := CleanRelPath(name)
		if err != nil {
			return nil, fmt.Errorf("zip entry %q: %w", f.Name, err)
		}
		if seen[rel] {
			return nil, fmt.Errorf("%w: zip entry %q appears twice", ErrBadPath, f.Name)
		}
		seen[rel] = true
		entries = append(entries, zipEntry{f: f, rel: rel})
	}
	if len(entries) > maxFiles {
		return nil, fmt.Errorf("%w: the archive holds %d files, the site allows %d", ErrTooManyFiles, len(entries), maxFiles)
	}
	return entries, nil
}

// extractEntries writes validated entries into root, keeping the byte and
// file budgets in a running counter rather than re-walking the tree per
// entry. It returns the updated totals.
func extractEntries(root *os.Root, entries []zipEntry, used int64, count int, maxBytes int64, maxFiles int) (int64, int, error) {
	for _, e := range entries {
		rc, err := e.f.Open()
		if err != nil {
			if damagedEntry(err) {
				return used, count, fmt.Errorf("%w: zip entry %q: %v", ErrBadArchive, e.f.Name, err)
			}
			return used, count, fmt.Errorf("zip entry %q: %w", e.f.Name, err)
		}
		written, existing, err := writeFileIn(root, e.rel, rc, used, count, maxBytes, maxFiles)
		rc.Close()
		if err != nil {
			if damagedEntry(err) {
				return used, count, fmt.Errorf("%w: zip entry %q: %v", ErrBadArchive, e.f.Name, err)
			}
			return used, count, fmt.Errorf("zip entry %q: %w", e.f.Name, err)
		}
		used += written - existing
		if existing == 0 {
			count++
		}
	}
	return used, count, nil
}

// damagedEntry reports whether reading an archive or an entry failed because
// the archive is damaged — not a zip, a checksum mismatch, a corrupt deflate
// stream, data cut short — rather than because of the site's caps or a read
// error on the server's own spool file, which stays an internal error (and
// whose message, naming a server path, is never shown to the uploader).
func damagedEntry(err error) bool {
	var corrupt flate.CorruptInputError
	return errors.Is(err, zip.ErrChecksum) || errors.Is(err, zip.ErrFormat) ||
		errors.Is(err, zip.ErrAlgorithm) || errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, io.EOF) || errors.As(err, &corrupt)
}
