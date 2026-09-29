package store

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// The write-path census. Every call that can create, overwrite or move a
// file in the packages that handle site content is listed here with the
// reason it is allowed. A new one fails this test until someone has decided
// whether it writes site content — and if it does, routed it through the
// abuse guard (writeFileIn, a Replacement, a StagedFile or RenameChecked;
// see guard.go) and added a row to TestEveryWriteSurfaceIsScanned in
// internal/httpapi. A row whose call is gone fails too, so the list stays
// true.
var writeCensus = map[string]string{
	// the guarded paths themselves
	"internal/store/files.go:writeFileIn:OpenFile":              "the temp file of every guarded write",
	"internal/store/files.go:writeFileIn:Rename":                "the rename after the verdict is settled",
	"internal/store/staged.go:Store.OpenStaged:OpenFile":        "the temp file of a staged (WebDAV, FTP) write",
	"internal/store/staged.go:Store.commitStagedLocked:Rename":  "the rename after the verdict is settled",
	"internal/store/staged.go:Store.RenameChecked:Rename":       "the rename after the verdict is settled",
	"internal/store/replace.go:Replacement.Commit:commitRename": "moves staged files whose verdict Commit settled first",
	// surfaces that reach site content only through the guarded paths
	"internal/httpapi/webdavfs.go:siteFS.OpenFile:OpenFile":   "read-only opens; writes go to Store.OpenStaged above it",
	"internal/ftp/quota.go:quotaFs.Create:OpenFile":           "goes through quotaFs.OpenFile",
	"internal/ftp/quota.go:quotaFs.OpenFile:OpenFile":         "reads, and the guard-less fallback of the quota unit tests; FTPAuth always sets the guard",
	"internal/ftp/quota.go:quotaFs.Rename:Rename":             "Guard.Rename, and the guard-less fallback of the quota unit tests",
	"internal/ftp/rootfs.go:rootFs.Create:OpenFile":           "the confinement layer under quotaFs, reached only through it",
	"internal/ftp/rootfs.go:rootFs.Open:OpenFile":             "read-only",
	"internal/ftp/rootfs.go:rootFs.OpenFile:OpenFile":         "the confinement layer under quotaFs, reached only through it",
	"internal/ftp/rootfs.go:rootFs.Rename:Rename":             "the confinement layer under quotaFs, reached only through it",
	"internal/httpapi/sites.go:API.extractZipPart:CreateTemp": "the zip spool under tmp/, extracted through the store",
	// not site content
	"internal/httpapi/sites.go:API.createSiteWith:Create":   "store.Create: a new, empty site",
	"internal/store/files.go:Store.ZipContent:Create":       "an entry of the download archive",
	"internal/store/links_other.go:makeLink:Symlink":        "an index link",
	"internal/store/links_windows.go:makeLink:Symlink":      "an index link",
	"internal/store/meta.go:writeMeta:WriteFile":            "meta.json",
	"internal/store/meta.go:writeMeta:Rename":               "meta.json",
	"internal/store/reports.go:Store.AddReport:WriteFile":   "an abuse report",
	"internal/store/stats.go:Store.writeStats:WriteFile":    "stats.json",
	"internal/store/stats.go:Store.writeStats:Rename":       "stats.json",
	"internal/store/store.go:Store.SetTrusted:OpenFile":     "Sitebin's own empty trust marker",
	"internal/store/store.go:Store.WriteSPAMarker:OpenFile": "Sitebin's own empty SPA marker",
	"internal/store/zones.go:Store.writeZone:WriteFile":     "an account zone record",
	"internal/store/zones.go:Store.writeZone:Rename":        "an account zone record",
	"internal/viewer/viewer.go:generate:WriteFile":          "the viewer's own wrapper page",
	"internal/viewer/viewer.go:Apply:Rename":                "a mode switch moving files into _raw/ under their own names; scanned when written",
	"internal/viewer/viewer.go:Remove:Rename":               "a mode switch moving files out of _raw/ under their own names; scanned when written",
}

// writeCalls are the calls the census looks for: by method or package
// function name, and the store's own swappable rename.
var writeCalls = map[string]bool{
	"OpenFile": true, "Create": true, "WriteFile": true, "Rename": true,
	"Symlink": true, "Link": true, "CreateTemp": true, "commitRename": true,
}

func TestWritePathCensus(t *testing.T) {
	found := map[string]bool{}
	for _, pkg := range []string{"internal/store", "internal/httpapi", "internal/ftp", "internal/viewer"} {
		files, err := filepath.Glob(filepath.Join("..", "..", pkg, "*.go"))
		if err != nil || len(files) == 0 {
			t.Fatalf("no sources for %s: %v", pkg, err)
		}
		for _, f := range files {
			if strings.HasSuffix(f, "_test.go") {
				continue
			}
			af, err := parser.ParseFile(token.NewFileSet(), f, nil, 0)
			if err != nil {
				t.Fatal(err)
			}
			base := filepath.Base(f)
			for _, d := range af.Decls {
				fd, ok := d.(*ast.FuncDecl)
				if !ok {
					continue
				}
				fn := fd.Name.Name
				if fd.Recv != nil {
					rt := fd.Recv.List[0].Type
					if s, ok := rt.(*ast.StarExpr); ok {
						rt = s.X
					}
					if id, ok := rt.(*ast.Ident); ok {
						fn = id.Name + "." + fn
					}
				}
				ast.Inspect(fd, func(n ast.Node) bool {
					c, ok := n.(*ast.CallExpr)
					if !ok {
						return true
					}
					name := ""
					switch fun := c.Fun.(type) {
					case *ast.SelectorExpr:
						name = fun.Sel.Name
					case *ast.Ident:
						name = fun.Name
					}
					if writeCalls[name] {
						found[fmt.Sprintf("%s/%s:%s:%s", pkg, base, fn, name)] = true
					}
					return true
				})
			}
		}
	}
	var unknown, stale []string
	for k := range found {
		if _, ok := writeCensus[k]; !ok {
			unknown = append(unknown, k)
		}
	}
	for k := range writeCensus {
		if !found[k] {
			stale = append(stale, k)
		}
	}
	sort.Strings(unknown)
	sort.Strings(stale)
	for _, k := range unknown {
		t.Errorf("a new call that can write files: %s — if it writes site content, route it through the abuse guard (guard.go) and add it to TestEveryWriteSurfaceIsScanned; then list it here with why it is safe", k)
	}
	for _, k := range stale {
		t.Errorf("the census lists a call that is gone: %s", k)
	}
}
