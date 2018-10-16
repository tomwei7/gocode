package cache

import (
	"fmt"
	"go/build"
	goimporter "go/importer"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/tools/go/gcexportdata"
)

// We need to mangle go/build.Default to make gcimporter work as
// intended, so use a lock to protect against concurrent accesses.
var buildDefaultLock sync.Mutex

// Mu must be held while using the cache importer.
var Mu sync.Mutex

var importCache = importerCache{
	fset:    token.NewFileSet(),
	imports: make(map[string]importCacheEntry),
}

func NewImporter(ctx *PackedContext, filename string) types.ImporterFrom {
	importCache.clean()

	imp := &importer{
		ctx:           ctx,
		importerCache: &importCache,
	}

	slashed := filepath.ToSlash(filename)
	i := strings.LastIndex(slashed, "/vendor/src/")
	if i < 0 {
		i = strings.LastIndex(slashed, "/src/")
	}
	if i > 0 {
		paths := filepath.SplitList(imp.ctx.GOPATH)

		gbroot := filepath.FromSlash(slashed[:i])
		gbvendor := filepath.Join(gbroot, "vendor")
		if SamePath(gbroot, imp.ctx.GOROOT) {
			goto Found
		}
		for _, path := range paths {
			if SamePath(path, gbroot) || SamePath(path, gbvendor) {
				goto Found
			}
		}

		imp.gbroot = gbroot
		imp.gbvendor = gbvendor
	Found:
	}

	return imp
}

type importer struct {
	*importerCache
	gbroot, gbvendor string
	ctx              *PackedContext
}

type importerCache struct {
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
	buildDefaultLock.Lock()
	defer buildDefaultLock.Unlock()

	origDef := build.Default
	defer func() { build.Default = origDef }()

	def := &build.Default
	// The gb root of a project can be used as a $GOPATH because it contains pkg/.
	def.GOPATH = i.ctx.GOPATH
	if i.gbroot != "" {
		def.GOPATH = i.gbroot
	}
	def.GOARCH = i.ctx.GOARCH
	def.GOOS = i.ctx.GOOS
	def.GOROOT = i.ctx.GOROOT
	def.CgoEnabled = i.ctx.CgoEnabled
	def.UseAllFiles = i.ctx.UseAllFiles
	def.Compiler = i.ctx.Compiler
	def.BuildTags = i.ctx.BuildTags
	def.ReleaseTags = i.ctx.ReleaseTags
	def.InstallSuffix = i.ctx.InstallSuffix
	def.SplitPathList = i.splitPathList
	def.JoinPath = i.joinPath

	filename, path := gcexportdata.Find(importPath, srcDir)
	entry, ok := i.imports[path]
	if filename == "" {
		// If there is no export data, check the cache.
		// TODO(rstambler): Develop a better heuristic for entry eviction.
		if ok && time.Since(entry.mtime) <= time.Minute*20 {
			return entry.pkg, nil
		}
		// If there is no cache entry, import and cache using the source importer.
		pkg, err := goimporter.For("source", nil).Import(path)
		if pkg != nil {
			entry = importCacheEntry{pkg, time.Now()}
			i.imports[path] = entry
		}
		return pkg, err
	}

	// If there is export data for the package.
	fi, err := os.Stat(filename)
	if err != nil {
		return nil, err
	}
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
func (i *importerCache) clean() {
	for k := range i.imports {
		if len(i.imports) <= 100 {
			break
		}
		delete(i.imports, k)
	}
}

func (i *importer) splitPathList(list string) []string {
	res := filepath.SplitList(list)
	if i.gbroot != "" {
		res = append(res, i.gbroot, i.gbvendor)
	}
	return res
}

func (i *importer) joinPath(elem ...string) string {
	res := filepath.Join(elem...)

	if i.gbroot != "" {
		// Want to rewrite "$GBROOT/(vendor/)?pkg/$GOOS_$GOARCH(_)?"
		// into "$GBROOT/pkg/$GOOS-$GOARCH(-)?".
		// Note: gb doesn't use vendor/pkg.
		if gbrel, err := filepath.Rel(i.gbroot, res); err == nil {
			gbrel = filepath.ToSlash(gbrel)
			gbrel, _ = match(gbrel, "vendor/")
			if gbrel, ok := match(gbrel, fmt.Sprintf("pkg/%s_%s", i.ctx.GOOS, i.ctx.GOARCH)); ok {
				gbrel, hasSuffix := match(gbrel, "_")

				// Reassemble into result.
				if hasSuffix {
					gbrel = "-" + gbrel
				}
				gbrel = fmt.Sprintf("pkg/%s-%s/", i.ctx.GOOS, i.ctx.GOARCH) + gbrel
				gbrel = filepath.FromSlash(gbrel)
				res = filepath.Join(i.gbroot, gbrel)
			}
		}
	}
	return res
}

func match(s, prefix string) (string, bool) {
	rest := strings.TrimPrefix(s, prefix)
	return rest, len(rest) < len(s)
}
