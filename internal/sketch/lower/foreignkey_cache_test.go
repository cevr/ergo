package lower

import (
	"go/ast"
	"go/build"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	goruntime "runtime"
	"slices"
	"strings"
	"testing"

	runtimefiles "ergo.local/prototype/runtime"
)

// referenceStdTypes is the unoptimized baseline importer (full function body
// type-checking, build.Import mode 0, no import pruning) used solely in tests
// to prove bit-for-bit closure equivalence with the optimized stdTypes.
type referenceStdTypes struct {
	ctx  build.Context
	fset *token.FileSet
	pkgs map[string]*types.Package
	dirs map[string]string
}

func newReferenceStdTypes(t GoTarget) *referenceStdTypes {
	ctx := build.Default
	ctx.GOOS, ctx.GOARCH, ctx.CgoEnabled = t.GOOS, t.GOARCH, false
	ctx.ToolTags = slices.Clone(ctx.ToolTags)
	for _, e := range t.Experiments {
		ctx.ToolTags = append(ctx.ToolTags, "goexperiment."+e)
	}
	ctx.BuildTags = nil
	return &referenceStdTypes{
		ctx:  ctx,
		fset: token.NewFileSet(),
		pkgs: map[string]*types.Package{},
		dirs: map[string]string{},
	}
}

func (s *referenceStdTypes) Import(p string) (*types.Package, error) { return s.ImportFrom(p, "", 0) }

func (s *referenceStdTypes) ImportFrom(p, dir string, _ types.ImportMode) (*types.Package, error) {
	if p == "unsafe" {
		return types.Unsafe, nil
	}
	key := dir + "\x00" + p
	ip, ok := s.dirs[key]
	if !ok {
		bp, err := s.ctx.Import(p, dir, 0)
		if err != nil || !bp.Goroot {
			s.dirs[key] = ""
			return nil, err
		}
		ip = bp.ImportPath
		s.dirs[key] = ip
		if _, done := s.pkgs[ip]; !done {
			s.pkgs[ip] = nil
			var files []*ast.File
			for _, name := range bp.GoFiles {
				f, err := parser.ParseFile(s.fset, filepath.Join(bp.Dir, name), nil, parser.SkipObjectResolution)
				if err == nil {
					files = append(files, f)
				}
			}
			conf := types.Config{Importer: s, Error: func(error) {}, FakeImportC: true}
			t, _ := conf.Check(ip, s.fset, files, nil)
			s.pkgs[ip] = t
		}
	}
	if ip == "" || s.pkgs[ip] == nil {
		t := types.NewPackage(p, filepath.Base(p))
		t.MarkComplete()
		return t, nil
	}
	return s.pkgs[ip], nil
}

func TestStdTypesOptimizedMatchesReference(t *testing.T) {
	target := GoTarget{GOOS: goruntime.GOOS, GOARCH: goruntime.GOARCH}
	opt := stdTypesOf(target)
	if opt.ctx.GOROOT == "" {
		t.Skip("no GOROOT")
	}
	ref := newReferenceStdTypes(target)

	// Verify exported method sets & interface resolution match between optimized
	// and full-body reference checker across real stdlib packages used by runtime/v1.
	for _, pkgPath := range []string{"sync/atomic", "io", "os", "fmt", "bytes", "encoding/json", "net"} {
		optPkg := opt.imported(pkgPath)
		refPkg, err := ref.Import(pkgPath)
		if err != nil || optPkg == nil || refPkg == nil {
			t.Fatalf("import %s: opt=%v ref=%v err=%v", pkgPath, optPkg, refPkg, err)
		}
		for _, name := range refPkg.Scope().Names() {
			if !ast.IsExported(name) {
				continue
			}
			refObj := refPkg.Scope().Lookup(name)
			optObj := optPkg.Scope().Lookup(name)
			if optObj == nil {
				t.Errorf("%s.%s: missing in optimized stdTypes", pkgPath, name)
				continue
			}
			if _, isRefType := refObj.(*types.TypeName); isRefType {
				refIface := types.IsInterface(refObj.Type())
				optIface := types.IsInterface(optObj.Type())
				if refIface != optIface {
					t.Errorf("%s.%s: IsInterface opt=%v ref=%v", pkgPath, name, optIface, refIface)
				}
			}
		}
	}
}

func TestPruneStdFileKeepsSignatureAndUnusualPackageImports(t *testing.T) {
	const src = `package sample

import (
	_ "embed"
	"io"
	"strings"
	"crypto/internal/fips140deps"
	aliasBuf "bytes"
	unusedAlias "path"
)

type Reader interface {
	ReadFrom(r io.Reader) int64
}

var Buf *aliasBuf.Buffer
var Dep = fipsdeps.Marker

func OnlyInBody(s string) string {
	return strings.ToUpper(s)
}
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "sample.go", src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	pruneStdFile(f)

	kept := map[string]bool{}
	for _, im := range f.Imports {
		kept[strings.Trim(im.Path.Value, `"`)] = true
	}
	if kept["embed"] {
		t.Errorf("blank import _ \"embed\" should be pruned")
	}
	if !kept["io"] {
		t.Errorf("signature import \"io\" must be kept")
	}
	if !kept["bytes"] {
		t.Errorf("aliased var-type import \"bytes\" must be kept")
	}
	if !kept["crypto/internal/fips140deps"] {
		t.Errorf("unusual package-name import \"crypto/internal/fips140deps\" must be conservatively kept when fipsdeps.Marker is used in a var decl")
	}

	// Now test a file with NO unexplained selectors: body-only import "strings"
	// and unused alias "path" MUST be pruned.
	const cleanSrc = `package clean

import (
	_ "embed"
	"io"
	"strings"
	unusedAlias "path"
)

func Wrap(r io.Reader) int {
	_ = strings.ToUpper("x")
	_ = unusedAlias.Base("a/b")
	return 0
}
`
	f2, err := parser.ParseFile(fset, "clean.go", cleanSrc, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("ParseFile clean: %v", err)
	}
	pruneStdFile(f2)
	kept2 := map[string]bool{}
	for _, im := range f2.Imports {
		kept2[strings.Trim(im.Path.Value, `"`)] = true
	}
	if !kept2["io"] {
		t.Errorf("clean: signature import \"io\" must be kept")
	}
	if kept2["strings"] || kept2["path"] || kept2["embed"] {
		t.Errorf("clean: body-only/blank imports should be pruned, got kept=%v", kept2)
	}
}

func TestForeignKeyCacheLocalInvalidationOnEdit(t *testing.T) {
	cacheDir := t.TempDir()
	UseForeignKeyCache(cacheDir)
	t.Cleanup(func() { UseForeignKeyCache("") })

	pkgDir := t.TempDir()
	aDir := filepath.Join(pkgDir, "a")
	bDir := filepath.Join(pkgDir, "b")
	if err := os.MkdirAll(aDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(bDir, 0o755); err != nil {
		t.Fatal(err)
	}
	aFile := filepath.Join(aDir, "a.go")
	bFile := filepath.Join(bDir, "b.go")
	if err := os.WriteFile(aFile, []byte("package a\n\nimport \"ex.test/local/b\"\n\nfunc F() int { return b.G() }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bFile, []byte("package b\n\nfunc G() int { return 1 }\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	readDir := func(dir string) []GoSourceFile {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return nil
		}
		var out []GoSourceFile
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
				continue
			}
			data, err := os.ReadFile(filepath.Join(dir, e.Name()))
			if err != nil {
				return nil
			}
			out = append(out, GoSourceFile{Name: e.Name(), Data: data})
		}
		return out
	}

	target := GoTarget{GOOS: goruntime.GOOS, GOARCH: goruntime.GOARCH}
	makeSrc := func() ForeignKeySource {
		return ForeignKeySource{
			Toolchain: "go1.27.4",
			Module: GoModule{
				Modules: []string{"ex.test/local"},
				Replace: []GoReplace{{Old: "ex.test/local", New: pkgDir}},
			},
			ReadDir: readDir,
			Target:  &target,
		}
	}

	// 1. Cold run populates disk cache.
	k1 := makeSrc().Keys()("ex.test/local/a.F")
	if !strings.HasPrefix(k1.Key, "closure ") {
		t.Fatalf("unexpected key: %+v", k1)
	}

	// 2. Fresh ForeignKeySource + cleared in-memory mirror hits disk cache and returns exact key.
	fkDiskCache.setRoot("")
	fkDiskCache.setRoot(cacheDir)
	k2 := makeSrc().Keys()("ex.test/local/a.F")
	if k2 != k1 {
		t.Fatalf("warm cache key %+v != cold key %+v", k2, k1)
	}

	// 3. Edit transitive dependency b.go: cache MUST invalidate immediately!
	if err := os.WriteFile(bFile, []byte("package b\n\nfunc G() int { return 2 }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	k3 := makeSrc().Keys()("ex.test/local/a.F")
	if k3.Key == k1.Key {
		t.Fatalf("stale cache hit after editing b.go: k3=%+v == k1=%+v", k3, k1)
	}
}

func TestForeignKeyCacheRuntimeRoundTripAndCorruptRejection(t *testing.T) {
	cacheDir := t.TempDir()
	UseForeignKeyCache(cacheDir)
	t.Cleanup(func() { UseForeignKeyCache("") })

	linux := GoTarget{GOOS: "linux", GOARCH: "amd64"}
	src := ForeignKeySource{
		Toolchain: "go1.27.4",
		Target:    &linux,
		Variants: []BuildVariant{
			{Tag: "linux/amd64", Target: GoTarget{GOOS: "darwin", GOARCH: "arm64"}},
		},
	}
	target := runtimefiles.Module + "/runtime/v1.ConfigFlagName"

	// Clear in-memory runtimeKeys for this memo so we exercise disk cache cleanly.
	memo := linux.ID() + "|linux/amd64=darwin/arm64 |always=|" + target
	runtimeKeys.Delete(memo)

	k1 := src.Keys()(target)
	if !strings.HasPrefix(k1.Key, "closure ") || k1.Build != "" {
		t.Fatalf("unexpected runtime key: %+v", k1)
	}

	// Verify cache file was written and reloads identically into a fresh in-memory cache.
	ns := runtimeCacheNamespace()
	cachePath := fkDiskCache.filePath(ns)
	if _, err := os.Stat(cachePath); err != nil {
		t.Fatalf("expected runtime cache file at %s: %v", cachePath, err)
	}

	runtimeKeys.Delete(memo)
	fkDiskCache.setRoot("")
	fkDiskCache.setRoot(cacheDir)
	k2 := src.Keys()(target)
	if k2 != k1 {
		t.Fatalf("reloaded runtime key %+v != original %+v", k2, k1)
	}

	// Corrupt the cache file with a poisoned key + wrong namespace/version and verify rejection.
	poisoned := `{"version":"fk-v0-stale","namespace":"` + ns + `","runtime":{"` + memo + `":{"Key":"closure deadbeef","Group":"runtime/v1"}}}`
	if err := os.WriteFile(cachePath, []byte(poisoned), 0o644); err != nil {
		t.Fatal(err)
	}
	runtimeKeys.Delete(memo)
	fkDiskCache.setRoot("")
	fkDiskCache.setRoot(cacheDir)
	k3 := src.Keys()(target)
	if k3 != k1 {
		t.Fatalf("poisoned version cache was accepted: got %+v, want %+v", k3, k1)
	}

	// Also verify changing GOROOT changes stdEnvFingerprint (so typed vs untyped
	// or two different GOROOT paths never collide, even when VERSION/go.mod are absent).
	fpTyped := stdEnvFingerprint()
	saved := hostStd
	t.Cleanup(func() { hostStd = saved })
	setGOROOT := func(dir string) {
		hostStd = func() *stdTypes {
			ctx := build.Default
			ctx.GOROOT = dir
			return &stdTypes{ctx: ctx}
		}
	}
	setGOROOT("")
	fpUntyped := stdEnvFingerprint()
	if fpTyped == fpUntyped {
		t.Fatalf("stdEnvFingerprint must differ when GOROOT changes (both %s)", fpTyped)
	}
	setGOROOT("/nonexistent/goroot-a")
	fpA := stdEnvFingerprint()
	setGOROOT("/nonexistent/goroot-b")
	fpB := stdEnvFingerprint()
	if fpA == fpB {
		t.Fatalf("stdEnvFingerprint must distinguish different GOROOT paths (both %s)", fpA)
	}
}
