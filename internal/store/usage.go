package store

import (
	"errors"
	"io/fs"
	"path/filepath"
)

// Usage is what a site holds against its byte cap: its files and its Git
// repository, which share the one cap. Files counts content files only —
// the file cap is about what was uploaded, and a pack file is not that.
type Usage struct {
	FileBytes int64
	GitBytes  int64
	Files     int
}

// Bytes is the figure counted against the site's byte cap and reported
// beside it: files and repository together.
func (u Usage) Bytes() int64 { return u.FileBytes + u.GitBytes }

// SiteUsage is the central byte function: every write's cap check counts a
// site's bytes through it (byteChecks in usage_test.go lists them), and so
// does every size report (the edit page, get_site, the dashboard), so none
// can forget the repository. It walks the content directory as
// usage does and the repository (GitDir) the same way — regular files only,
// links neither counted nor followed. A site with no repository has
// GitBytes 0. Every answer is noted for the account storage cap
// (accountusage.go), which is how the figures of sites in use stay current.
//
// Store.Usage stays files-only where the figure is about files, not the
// cap: file COUNTS (WebDAV's new-file check), the MCP download bound (a
// download is of the files), and the container runtime's stop rule
// (enforceStorage, through ext.SiteInfo.Bytes), which must never stop an app
// for a push.
func (s *Store) SiteUsage(site *Site) (Usage, error) {
	fb, n, err := usage(site.ContentDir())
	if err != nil {
		return Usage{}, err
	}
	gb, err := repoBytes(site.GitDir())
	if err != nil {
		return Usage{}, err
	}
	u := Usage{FileBytes: fb, GitBytes: gb, Files: n}
	s.acct.note(site, u.Bytes()) // the account storage cap's figure
	return u, nil
}

// repoBytes sums the regular files under a repository (0 if it is missing).
// A file that vanishes between being listed and being read — gc replacing
// packs, a push moving its temporary pack into place — is skipped rather
// than failing the count, or counting the whole repository as empty.
func repoBytes(dir string) (int64, error) {
	var n int64
	err := filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if !d.Type().IsRegular() {
			return nil
		}
		fi, err := d.Info()
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		n += fi.Size()
		return nil
	})
	return n, err
}
