package gbimporter

import (
	"fmt"
	"go/build"
	"go/types"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"sync"

	"github.com/mdempsky/gocode/internal/cache"
)

type installedInfo struct {
	Target string
	MTime  int64
}

var installedMap map[string]*installedInfo

func init() {
	installedMap = make(map[string]*installedInfo)
}

// We need to mangle go/build.Default to make gcimporter work as
// intended, so use a lock to protect against concurrent accesses.
var buildDefaultLock sync.Mutex

// importer implements types.ImporterFrom and provides transparent
// support for gb-based projects.
type importer struct {
	ctx        *cache.PackedContext
	gbroot     string
	gbpaths    []string
	underlying types.ImporterFrom
	logf       func(string, ...interface{})
}

func New(ctx *cache.PackedContext, filename string, underlying types.Importer, logger func(string, ...interface{})) types.ImporterFrom {
	imp := &importer{
		ctx:        ctx,
		underlying: underlying.(types.ImporterFrom),
		logf:       logger,
	}

	gbroot, gbvendor := cache.GetGbProjectPaths(ctx, filename)
	if gbroot != "" {
		imp.gbroot = gbroot
		imp.gbpaths = append(filepath.SplitList(imp.ctx.GOPATH), gbroot, gbvendor)
	}
	return imp
}

func (i *importer) Import(path string) (*types.Package, error) {
	return i.ImportFrom(path, "", 0)
}

func (i *importer) ImportFrom(path, srcDir string, mode types.ImportMode) (*types.Package, error) {
	i.tryInstallPackage(path, srcDir)
	buildDefaultLock.Lock()
	defer buildDefaultLock.Unlock()

	origDef := build.Default
	defer func() { build.Default = origDef }()

	def := &build.Default
	def.GOARCH = i.ctx.GOARCH
	def.GOOS = i.ctx.GOOS
	def.GOROOT = i.ctx.GOROOT
	def.GOPATH = i.ctx.GOPATH
	def.CgoEnabled = i.ctx.CgoEnabled
	def.UseAllFiles = i.ctx.UseAllFiles
	def.Compiler = i.ctx.Compiler
	def.BuildTags = i.ctx.BuildTags
	def.ReleaseTags = i.ctx.ReleaseTags
	def.InstallSuffix = i.ctx.InstallSuffix

	def.SplitPathList = i.splitPathList
	def.JoinPath = i.joinPath

	pkg, err := i.underlying.ImportFrom(path, srcDir, mode)
	if pkg == nil {
		i.logf("no package found for %s: %v", path, err)
		return nil, err
	}
	return pkg, nil
}

func (i *importer) tryInstallPackage(pkgPath, srcDir string) {
	goPath := strings.Split(i.ctx.GOPATH, ":")[0]
	target := path.Join(goPath, "src", pkgPath)
	for dir := srcDir; dir != "/" && dir != "."; dir = path.Dir(dir) {
		tryDir := path.Join(dir, "vendor", pkgPath)
		if stat, err := os.Stat(tryDir); err == nil && stat.IsDir() {
			target = tryDir
			break
		}
	}
	mtime, err := newest(target, ".go")
	if err != nil || mtime == 0 {
		log.Printf("newest error %s", err)
		return
	}
	// check build
	if gprel, err := filepath.Rel(filepath.Join(goPath, "src"), target); err == nil {
		pkgPath := filepath.Join(goPath, "pkg", fmt.Sprintf("%s_%s", i.ctx.GOOS, i.ctx.GOARCH), gprel+".a")
		pkgMTime := modTime(pkgPath)
		if pkgMTime > mtime {
			installedMap[target] = &installedInfo{Target: target, MTime: mtime}
			return
		}
	}
	info, ok := installedMap[target]
	if !ok || info.MTime == 0 || info.MTime < mtime {
		if stat, err := os.Stat(target); err == nil && stat.IsDir() {
			if err := exec.Command("go", "install", target).Run(); err != nil {
				log.Printf("try go install error: %s", err)
			}
			installedMap[target] = &installedInfo{Target: target, MTime: mtime}
		}
	}
}

func newest(target, suffix string) (int64, error) {
	infos, err := ioutil.ReadDir(target)
	if err != nil {
		return 0, err
	}
	var n int64
	for _, info := range infos {
		if strings.HasSuffix(info.Name(), suffix) {
			mtime := info.ModTime().Unix()
			if mtime > n {
				n = mtime
			}
		}
	}
	return n, nil
}

func modTime(name string) int64 {
	info, err := os.Stat(name)
	if err != nil {
		return 0
	}
	return info.ModTime().Unix()
}

func (i *importer) splitPathList(list string) []string {
	if i.gbroot != "" {
		return i.gbpaths
	}
	return filepath.SplitList(list)
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
