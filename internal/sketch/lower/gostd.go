package lower

import (
	"go/ast"
	"go/build"
	"go/parser"
	"go/token"
	"go/types"
	"path/filepath"
	goruntime "runtime"
	"slices"
	"strings"
	"sync"
)

// stdTypes types Go's standard library from GOROOT's source, as a target
// compiles it (cgo off), for the closure walk (LBK-TYPED-WALK). A call on a
// value of a std type (c.value.Load() on an atomic.Int64) then resolves to
// std's method, which the walk does not enter, instead of taking every
// method of that name in the world's packages. A std package that does not
// load or type is an empty one, as before: its names are taken by name.
// An interface method call stays by name wherever the interface is
// declared (the walk's object), so a world value behind io.Writer still
// takes every Write.
type stdTypes struct {
	mu   sync.Mutex
	ctx  build.Context
	fset *token.FileSet
	pkgs map[string]*types.Package // by the directory's import path
	dirs map[string]string         // import path from a directory -> resolved path
}

// stdTypesFor are the std types of each target, by GoTarget.ID.
var stdTypesFor sync.Map

func stdTypesOf(t GoTarget) *stdTypes {
	if s, ok := stdTypesFor.Load(t.ID()); ok {
		return s.(*stdTypes)
	}
	ctx := build.Default
	ctx.GOOS, ctx.GOARCH, ctx.CgoEnabled = t.GOOS, t.GOARCH, false
	ctx.ToolTags = slices.Clone(ctx.ToolTags)
	for _, e := range t.Experiments {
		ctx.ToolTags = append(ctx.ToolTags, "goexperiment."+e)
	}
	ctx.BuildTags = nil
	s, _ := stdTypesFor.LoadOrStore(t.ID(), &stdTypes{ctx: ctx, fset: token.NewFileSet(), pkgs: map[string]*types.Package{}, dirs: map[string]string{}})
	return s.(*stdTypes)
}

// isStdPath reports whether p is a path of Go's standard library: no dot
// in its first element.
func isStdPath(p string) bool {
	return p != "" && p != "C" && !strings.Contains(strings.Split(p, "/")[0], ".")
}

// imported is std package p, typed; nil when it does not load.
func (s *stdTypes) imported(p string) *types.Package {
	if s == nil || s.ctx.GOROOT == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t, _ := s.ImportFrom(p, "", 0)
	return t
}

// Import and ImportFrom are the types.ImporterFrom of std's own imports;
// the caller holds s.mu.
func (s *stdTypes) Import(p string) (*types.Package, error) { return s.ImportFrom(p, "", 0) }

func (s *stdTypes) ImportFrom(p, dir string, _ types.ImportMode) (*types.Package, error) {
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
			s.pkgs[ip] = nil // a cycle reads an empty package
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

// hostStd is the std types of a build of the host's platform with no
// experiments; a test replaces it.
var hostStd = func() *stdTypes { return stdTypesOf(GoTarget{GOOS: goruntime.GOOS, GOARCH: goruntime.GOARCH}) }

// stdUntypedWhy is the reason a stale or missing ledger note gives when
// this process cannot type Go's standard library (no GOROOT, or none with
// std's source): the closure walk then takes std's names by name, so the
// keys differ from those of a ledger written where std was typed, and its
// entries read stale or missing here though nothing changed. "" when std
// is typed.
func stdUntypedWhy() string {
	s := hostStd()
	if s.ctx.GOROOT == "" {
		return " (no GOROOT: Go's standard library is read untyped, so the keys differ from a ledger written where it is typed; set GOROOT to the toolchain's)"
	}
	if _, err := s.ctx.Import("sync/atomic", "", build.FindOnly); err != nil {
		return " (Go's standard library has no source at GOROOT " + s.ctx.GOROOT + ": it is read untyped, so the keys differ from a ledger written where it is typed; set GOROOT to the toolchain's)"
	}
	return ""
}
