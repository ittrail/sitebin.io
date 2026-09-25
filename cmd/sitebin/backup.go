package main

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// backup writes a gzip-compressed tar of the data directory to outPath.
func backup(outPath string) error { return backupData(mustConfig().DataDir, outPath) }

// restore extracts a backup into the data directory.
func restore(inPath string) error { return restoreData(mustConfig().DataDir, inPath) }

// openForBackup opens a file for reading without following a link or
// blocking on a FIFO, where the platform can (see openNoFollow). Swappable so
// a test can change a file between the walk listing it and the backup reading
// it.
var openForBackup = openNoFollow

// inFlight reports whether rel (slash-separated, relative to the data root)
// is an upload in progress rather than data: tmp/ (the zip spool and replace
// staging) and a site's .replace-* commit directory — directly in the site
// folder, never a user's own folder of that name inside files/. They change
// under the walk, and a restore could do nothing with them.
func inFlight(rel string, isDir bool) bool {
	if rel == "tmp" {
		return true
	}
	return isDir && strings.HasPrefix(path.Base(rel), ".replace-") && path.Dir(path.Dir(rel)) == "sites"
}

// inSiteContent reports whether rel (slash-separated) lies inside a site's
// files/ — content its owner, or a container, controls.
func inSiteContent(rel string) bool {
	parts := strings.SplitN(rel, "/", 4)
	return len(parts) == 4 && parts[0] == "sites" && parts[2] == "files"
}

// backupData writes a gzip-compressed tar of root to outPath (or stdout when
// empty/"-"). Symlinks (the edit/domain indexes) are preserved. Uploads in
// flight are left out, and a file that disappears while the walk runs — a
// temp file renamed into place — is skipped rather than failing the backup.
func backupData(root, outPath string) error {
	var w io.Writer = os.Stdout
	if outPath != "" && outPath != "-" {
		f, err := os.Create(outPath)
		if err != nil {
			return err
		}
		defer f.Close()
		w = f
	}
	gz := gzip.NewWriter(w)
	defer gz.Close()
	tw := tar.NewWriter(gz)
	defer tw.Close()

	count := 0
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, walkErr error) error {
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		if walkErr != nil {
			if errors.Is(walkErr, fs.ErrNotExist) && p != root {
				return nil // vanished between being listed and being read
			}
			if errors.Is(walkErr, fs.ErrPermission) && inSiteContent(filepath.ToSlash(rel)) {
				fmt.Fprintf(os.Stderr, "skipped %s: %v\n", filepath.ToSlash(rel), walkErr)
				return nil
			}
			return walkErr
		}
		if rel == "." {
			return nil
		}
		// A skipped entry the walk saw as a directory must return SkipDir: the
		// walk trusts its own listing, so a directory swapped for a link since
		// would otherwise still be descended into — out of the site.
		skip := func() error {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if inFlight(filepath.ToSlash(rel), d.IsDir()) {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		info, err := d.Info()
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		// skip the backup file itself if written inside the data dir
		if outPath != "" && p == outPath {
			return nil
		}
		// A container site's folders are written by containers, which can
		// leave sockets, pipes and links pointing anywhere. A socket would
		// abort the whole archive (tar has no type for it), and a link out of
		// the data root would make restore refuse the whole archive. Neither
		// is content Sitebin manages, so both are left out, and said so.
		var link string
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			if link, err = os.Readlink(p); err != nil {
				if errors.Is(err, fs.ErrNotExist) {
					return nil // an index link removed with its site mid-walk
				}
				return err
			}
			if err := linkStaysUnder(root, p, link); err != nil {
				fmt.Fprintf(os.Stderr, "skipped %s: %v\n", filepath.ToSlash(rel), err)
				return skip()
			}
		case !info.Mode().IsRegular() && !info.IsDir():
			fmt.Fprintf(os.Stderr, "skipped %s: not a file, directory or link\n", filepath.ToSlash(rel))
			return skip()
		}
		// A regular file is opened before its header is written: if it has
		// gone, or been swapped for a link or a FIFO since the walk saw it (a
		// container can do that), it is skipped — never followed out of the
		// site, never waited on — instead of leaving a header without its
		// content in the archive. The size comes from the open file.
		var f *os.File
		if info.Mode().IsRegular() {
			f, err = openForBackup(p)
			if err != nil {
				if errors.Is(err, fs.ErrNotExist) {
					return nil // gone since the walk listed it
				}
				// A container runs with Sitebin's uid and can chmod its own
				// files; one it made unreadable must not stop the backup of
				// every other site. Reported, skipped.
				if errors.Is(err, fs.ErrPermission) && inSiteContent(filepath.ToSlash(rel)) {
					fmt.Fprintf(os.Stderr, "skipped %s: %v\n", filepath.ToSlash(rel), err)
					return nil
				}
				if fi, lerr := os.Lstat(p); lerr != nil || !fi.Mode().IsRegular() {
					fmt.Fprintf(os.Stderr, "skipped %s: changed while the backup ran\n", filepath.ToSlash(rel))
					return nil
				}
				return fmt.Errorf("back up %s: %w", filepath.ToSlash(rel), err)
			}
			defer f.Close()
			opened, err := f.Stat()
			if err != nil {
				return fmt.Errorf("back up %s: %w", filepath.ToSlash(rel), err)
			}
			if !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
				fmt.Fprintf(os.Stderr, "skipped %s: changed while the backup ran\n", filepath.ToSlash(rel))
				return nil
			}
			info = opened
		}
		hdr, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		hdr.Name = filepath.ToSlash(rel)
		if link != "" {
			hdr.Typeflag = tar.TypeSymlink
			hdr.Linkname = link
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if f != nil {
			short, err := copyPadded(tw, f, hdr.Size)
			if err != nil {
				return fmt.Errorf("back up %s: %w", filepath.ToSlash(rel), err)
			}
			if short {
				fmt.Fprintf(os.Stderr, "%s shrank while it was read; its entry is zero-padded\n", filepath.ToSlash(rel))
			}
		}
		count++
		return nil
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "backed up %d entries from %s\n", count, root)
	return nil
}

// copyPadded copies exactly n bytes of r to w — the size the tar header
// already promised. A file that shrank after its size was read yields fewer;
// the rest is zero-filled so the archive stays well-formed (what GNU tar does),
// and short reports it. A file that grew is cut at n.
func copyPadded(w io.Writer, r io.Reader, n int64) (short bool, err error) {
	copied, err := io.CopyN(w, r, n)
	if err == io.EOF {
		if _, err := io.CopyN(w, zeroReader{}, n-copied); err != nil {
			return true, err
		}
		return true, nil
	}
	return false, err
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}

// linkStaysUnder refuses a symlink whose target, resolved from the link's own
// directory, leaves root: absolute targets and enough "..". A symlink inside
// the data dir only ever points at a sibling (the indexes point at
// ../sites/<id>), so nothing legitimate is lost.
func linkStaysUnder(root, link, linkname string) error {
	if filepath.IsAbs(linkname) || strings.HasPrefix(filepath.ToSlash(linkname), "/") {
		return fmt.Errorf("absolute link target %q", linkname)
	}
	resolved := filepath.Clean(filepath.Join(filepath.Dir(link), filepath.FromSlash(linkname)))
	rootClean := filepath.Clean(root)
	if resolved != rootClean && !strings.HasPrefix(resolved, rootClean+string(os.PathSeparator)) {
		return fmt.Errorf("link target %q escapes the data root", linkname)
	}
	return nil
}

// parentStaysUnder resolves the nearest existing ancestor of target through
// any symlinks and checks it is still under root, so a write can never be
// redirected outside by a link an earlier entry created.
func parentStaysUnder(root, target string) error {
	rootReal, err := filepath.EvalSymlinks(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // nothing exists yet, nothing can redirect
		}
		return err
	}
	dir := filepath.Dir(target)
	for {
		real, err := filepath.EvalSymlinks(dir)
		if err == nil {
			if real != rootReal && !strings.HasPrefix(real, rootReal+string(os.PathSeparator)) {
				return fmt.Errorf("parent %q resolves outside the data root", dir)
			}
			return nil
		}
		if !os.IsNotExist(err) {
			return err
		}
		next := filepath.Dir(dir)
		if next == dir {
			return nil
		}
		dir = next
	}
}

// restoreData extracts a backup (from backupData) into root.
func restoreData(root, inPath string) error {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return err
	}
	f, err := os.Open(inPath)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)

	rootClean := filepath.Clean(root) + string(os.PathSeparator)
	count := 0
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		target := filepath.Join(root, filepath.FromSlash(hdr.Name))
		// zip-slip guard: the target must stay under the data root
		if !strings.HasPrefix(filepath.Clean(target)+string(os.PathSeparator), rootClean) &&
			filepath.Clean(target) != filepath.Clean(root) {
			return fmt.Errorf("refusing unsafe path in archive: %q", hdr.Name)
		}
		// The lexical guard sees a path under the root; the filesystem follows
		// symlinks. An earlier symlink entry pointing outside, followed by a
		// regular entry beneath it, would land outside — so the parent is
		// resolved for real before anything is written, and no link may point
		// out of the root in the first place.
		if hdr.Typeflag != tar.TypeDir {
			if err := parentStaysUnder(root, target); err != nil {
				return fmt.Errorf("refusing archive entry %q: %w", hdr.Name, err)
			}
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, os.FileMode(hdr.Mode)); err != nil {
				return err
			}
		case tar.TypeSymlink:
			if err := linkStaysUnder(root, target, hdr.Linkname); err != nil {
				return fmt.Errorf("refusing symlink %q in archive: %w", hdr.Name, err)
			}
			os.MkdirAll(filepath.Dir(target), 0o755)
			os.Remove(target)
			if err := os.Symlink(hdr.Linkname, target); err != nil {
				return err
			}
		case tar.TypeReg:
			os.MkdirAll(filepath.Dir(target), 0o755)
			out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, os.FileMode(hdr.Mode))
			if err != nil {
				return err
			}
			_, err = io.Copy(out, tr)
			out.Close()
			if err != nil {
				return err
			}
		}
		count++
	}
	fmt.Fprintf(os.Stderr, "restored %d entries into %s\n", count, root)
	return nil
}
