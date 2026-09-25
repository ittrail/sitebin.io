package store

import (
	"errors"
	"io/fs"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// ErrUnreadable is a folder the server may not read — one a container made
// unreadable, say.
var ErrUnreadable = errors.New("this folder cannot be read")

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
		if fi, lerr := root.Lstat(dir); lerr == nil && fi.Mode()&fs.ModeSymlink != 0 {
			return nil, false, ErrNotFound // a link, which the root refuses to follow out
		}
		if errors.Is(err, fs.ErrPermission) {
			return nil, false, ErrUnreadable
		}
		return nil, false, err
	}
	defer f.Close()
	if fi, err := f.Stat(); err != nil {
		return nil, false, err
	} else if !fi.IsDir() {
		return nil, false, ErrNotFound // a file, not a folder
	}
	// ReadDir of a folder opened through the root gets each entry's type
	// from the listing and its size relative to the open folder, not by
	// resolving every path again from the top.
	entries, err := f.ReadDir(-1)
	if err != nil {
		if errors.Is(err, fs.ErrPermission) {
			return nil, false, ErrUnreadable
		}
		return nil, false, err
	}
	out := make([]DirEntry, 0, len(entries))
	for _, e := range entries {
		name := e.Name()
		if dir == "." && (name == SPAMarker || name == TrustedMarker) {
			continue
		}
		// A name the API could not address — one a container made, say a
		// top-level meta.json or a name with a backslash — is not offered:
		// the page could neither open nor delete it.
		if _, err := CleanRelPath(path.Join(rel, name)); err != nil {
			continue
		}
		switch t := e.Type(); {
		case t.IsDir():
			out = append(out, DirEntry{Name: name, Dir: true})
		case t.IsRegular():
			fi, err := e.Info()
			if err != nil {
				continue // gone since the folder was read
			}
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
