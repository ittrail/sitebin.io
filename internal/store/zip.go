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
func (s *Store) ExtractZip(site *Site, r io.ReaderAt, size int64) error {
	zr, err := zip.NewReader(r, size)
	if err != nil {
		return fmt.Errorf("read zip: %w", err)
	}
	l := s.lockSite(site.ViewID)
	l.Lock()
	defer l.Unlock()

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
		rc, err := f.Open()
		if err != nil {
			return fmt.Errorf("zip entry %q: %w", f.Name, err)
		}
		err = s.saveFileLocked(site, rel, rc)
		rc.Close()
		if err != nil {
			return fmt.Errorf("zip entry %q: %w", f.Name, err)
		}
	}
	return nil
}
