package store

import (
	"errors"
	"io/fs"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// DirEntry is one entry of a folder listing: a sub-folder or a regular file.
type DirEntry struct {
	Name string `json:"name"`
	Dir  bool   `json:"dir"`
	Size int64  `json:"size,omitempty"` // files only
}

// maxDirEntries caps one folder listing; a folder with more is cut there and
// reported truncated. A variable so a test can lower it.
var maxDirEntries = 10000

// ListDir lists the immediate children of the folder relDir ("" = the content
// root) — sub-folders first, then files, each sorted by name ignoring case.
// It powers the edit page's folder browser, one folder at a time, so that a
// container site of tens of thousands of files is navigable in full.
//
// The folder is read through the content root's os.Root, so a link a
// container planted can never lead the listing out of the site. Only
// directories and regular files are listed — links, sockets and pipes are not
// content, just as ListFiles counts regular files only — and Sitebin's own
// markers are left out. A bad path is ErrBadPath; a folder that does not
// exist, or is a file, ErrNotFound.
func (s *Store) ListDir(site *Site, relDir string) ([]DirEntry, bool, error) {
	rel, err := CleanDirPath(relDir)
	if err != nil {
		return nil, false, err
	}
	dir := "."
	if rel != "" {
		dir = filepath.FromSlash(rel)
	}
	root, err := openContent(site)
	if err != nil {
		return nil, false, err
	}
	defer root.Close()
	f, err := root.Open(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, false, ErrNotFound
		}
		return nil, false, err
	}
	defer f.Close()
	if fi, err := f.Stat(); err != nil {
		return nil, false, err
	} else if !fi.IsDir() {
		return nil, false, ErrNotFound // a file, not a folder
	}
	names, err := f.Readdirnames(-1)
	if err != nil {
		return nil, false, err
	}
	out := make([]DirEntry, 0, len(names))
	for _, name := range names {
		if dir == "." && (name == SPAMarker || name == TrustedMarker) {
			continue
		}
		// Lstat through the root, never the entry's own Info: that would
		// resolve the name against the process's working directory.
		fi, err := root.Lstat(filepath.Join(dir, name))
		if err != nil {
			continue // gone since the folder was read
		}
		switch {
		case fi.IsDir():
			out = append(out, DirEntry{Name: name, Dir: true})
		case fi.Mode().IsRegular():
			out = append(out, DirEntry{Name: name, Size: fi.Size()})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Dir != out[j].Dir {
			return out[i].Dir
		}
		a, b := strings.ToLower(out[i].Name), strings.ToLower(out[j].Name)
		if a != b {
			return a < b
		}
		return out[i].Name < out[j].Name
	})
	truncated := false
	if len(out) > maxDirEntries {
		out, truncated = out[:maxDirEntries], true
	}
	return out, truncated, nil
}

// CleanDirPath is the folder path ListDir addresses, slash-separated, "" for
// the content root.
func CleanDirPath(relDir string) (string, error) {
	if relDir == "" || relDir == "." || relDir == "/" {
		return "", nil
	}
	rel, err := CleanRelPath(strings.TrimSuffix(relDir, "/"))
	if err != nil {
		return "", err
	}
	return path.Clean(rel), nil
}
