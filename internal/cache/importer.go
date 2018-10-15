package cache

import (
	"go/token"
	"go/types"
	"log"
	"os"
	"sync"
	"time"

	"golang.org/x/tools/go/gcexportdata"
)

var Importer = importer{
	fset:    token.NewFileSet(),
	imports: make(map[string]importCacheEntry),
}

type importer struct {
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
	filename, path := gcexportdata.Find(importPath, srcDir)
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
func (i *importer) Cleanup() {
	for k := range i.imports {
		if len(i.imports) <= 100 {
			break
		}
		delete(i.imports, k)
	}
}
