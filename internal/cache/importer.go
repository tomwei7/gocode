package cache

import (
	"fmt"
	"go/build"
	"go/token"
	"go/types"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/tools/go/gcexportdata"
)

var ImporterCache = importerCache{
	fset:    token.NewFileSet(),
	imports: make(map[string]importCacheEntry),
}

func NewImporter(gbroot string, gbpaths []string) *importer {
	ImporterCache.cleanImporter()
	return &importer{
		gbroot:        gbroot,
		gbpaths:       gbpaths,
		importerCache: &ImporterCache,
	}
}

type importer struct {
	*importerCache
	gbroot  string
	gbpaths []string
}

type importerCache struct {
	sync.Mutex
	fset    *token.FileSet
	imports map[string]importCacheEntry
}

type importCacheEntry struct {
	pkg   *types.Package
	mtime time.Time
}

func (i *importer) Import(importPath string) (*types.Package, error) {
	return i.ImportFrom(importPath, "", 0)
}

func (i *importer) ImportFrom(importPath, srcDir string, mode types.ImportMode) (*types.Package, error) {
	filename, path := findExportData(importPath, srcDir, i.gbroot)
	log.Printf("filename: %v, path: %v", filename, path)
	fi, err := os.Stat(filename)
	if err != nil {
		return nil, err
	}

	entry := i.imports[path]
	if entry.mtime != fi.ModTime() {
		f, err := os.Open(filename)
		if err != nil {
			return nil, err
		}

		in, err := gcexportdata.NewReader(f)
		if err != nil {
			return nil, err
		}

		pkg, err := gcexportdata.Read(in, i.fset, make(map[string]*types.Package), path)
		if err != nil {
			return nil, err
		}

		entry = importCacheEntry{pkg, fi.ModTime()}
		i.imports[path] = entry
	}

	return entry.pkg, nil
}

// Delete random files to keep the cache at most 100 entries.
// Only call while holding the importer's mutex.
func (i *importerCache) cleanImporter() {
	for k := range i.imports {
		if len(i.imports) <= 100 {
			break
		}
		delete(i.imports, k)
	}
}

var pkgExts = [...]string{".x", ".a", ".o"}

func findExportData(path, srcDir, gbroot string) (filename, id string) {
	if path == "" {
		return
	}

	var noext string
	switch {
	default:
		// "x" -> "$GOPATH/pkg/$GOOS_$GOARCH/x.ext", "x"
		// Don't require the source files to be present.
		if abs, err := filepath.Abs(srcDir); err == nil { // see issue 14282
			srcDir = abs
		}
		log.Printf("path: %v, srcDir: %v", path, srcDir)
		log.Printf("gbroot: %v", gbroot)
		ctxt := &build.Context{
			GOPATH:        gbroot,
			GOROOT:        build.Default.GOROOT,
			Compiler:      build.Default.Compiler,
			GOOS:          build.Default.GOOS,
			GOARCH:        build.Default.GOARCH,
			SplitPathList: build.Default.SplitPathList,
			JoinPath:      build.Default.JoinPath,
		}
		bp, _ := ctxt.Import(path, srcDir, build.FindOnly|build.AllowBinary)
		if bp.PkgObj == "" {
			id = path // make sure we have an id to print in error message
			return
		}
		noext = strings.TrimSuffix(bp.PkgObj, ".a")
		id = bp.ImportPath

	case build.IsLocalImport(path):
		// "./x" -> "/this/directory/x.ext", "/this/directory/x"
		noext = filepath.Join(srcDir, path)
		id = noext

	case filepath.IsAbs(path):
		log.Printf("abs")
		// for completeness only - go/build.Import
		// does not support absolute imports
		// "/x" -> "/x.ext", "/x"
		noext = path
		id = path
	}

	if false { // for debugging
		if path != id {
			fmt.Printf("%s -> %s\n", path, id)
		}
	}

	// try extensions
	for _, ext := range pkgExts {
		filename = noext + ext
		if f, err := os.Stat(filename); err == nil && !f.IsDir() {
			return
		}
	}

	filename = "" // not found
	return
}
