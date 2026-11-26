package lower

import (
	"bufio"
	"bytes"
	"go/ast"
	"go/build"
	"go/parser"
	"go/token"
	"go/types"
	"io"
	"os"
	"path"
	"path/filepath"
	goruntime "runtime"
	"slices"
	"sort"
	"strconv"
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

// Process-wide read-only caches for GOROOT standard library source files so
// multiple platform targets (linux/amd64, linux/arm64, darwin/amd64,
// darwin/arm64) never re-read or re-parse identical GOROOT .go files.
var (
	stdSharedFset = token.NewFileSet()
	stdDirEntries sync.Map // dir -> []string (sorted non-test .go basenames)
	stdFileBytes  sync.Map // fullPath -> []byte
	stdParsedASTs sync.Map // fullPath -> *ast.File (bodies stripped, body-only imports pruned)
)

func stdTypesOf(t GoTarget) *stdTypes {
	if s, ok := stdTypesFor.Load(t.ID()); ok {
		return s.(*stdTypes)
	}
	s, _ := stdTypesFor.LoadOrStore(t.ID(), &stdTypes{ctx: stdContext(build.Default, t), fset: stdSharedFset, pkgs: map[string]*types.Package{}, dirs: map[string]string{}})
	return s.(*stdTypes)
}

// stdContext is the build context std is typed in for t, from the
// process's default ctx: t's platform and experiments, cgo off, and the
// GOROOT of stdRoot when ctx has none.
func stdContext(ctx build.Context, t GoTarget) build.Context {
	if ctx.GOROOT == "" {
		ctx.GOROOT = stdRoot.get()
	}
	ctx.GOOS, ctx.GOARCH, ctx.CgoEnabled = t.GOOS, t.GOARCH, false
	ctx.ToolTags = slices.Clone(ctx.ToolTags)
	for _, e := range t.Experiments {
		ctx.ToolTags = append(ctx.ToolTags, "goexperiment."+e)
	}
	ctx.BuildTags = nil
	return ctx
}

// stdRoot is the GOROOT the closure walk types Go's standard library from
// when the process has none of its own: a binary built with -trimpath
// (internal/ergobuild) records no GOROOT, and with no GOROOT in the
// environment build.Default has none.
var stdRoot lazyRoot

// lazyRoot is a GOROOT resolved once, on first use.
type lazyRoot struct {
	mu      sync.Mutex
	resolve func() string
	root    string
	done    bool
}

// UseStdRoot makes resolve the source of the GOROOT std is typed from in
// a process that has none of its own (build.Default.GOROOT is empty):
// the release the process's builds run. It is called at most once, on
// the first closure walk that needs std's types.
func UseStdRoot(resolve func() string) {
	stdRoot.mu.Lock()
	defer stdRoot.mu.Unlock()
	stdRoot.resolve, stdRoot.root, stdRoot.done = resolve, "", false
}

func (s *lazyRoot) get() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.done && s.resolve != nil {
		s.root, s.done = s.resolve(), true
	}
	return s.root
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
		bp, err := s.ctx.Import(p, dir, build.FindOnly)
		if err != nil || !bp.Goroot {
			s.dirs[key] = ""
			return nil, err
		}
		ip = bp.ImportPath
		s.dirs[key] = ip
		if _, done := s.pkgs[ip]; !done {
			s.pkgs[ip] = nil // a cycle reads an empty package
			goFiles := s.matchStdDir(bp.Dir)
			var files []*ast.File
			for _, name := range goFiles {
				if f := stdParseFile(filepath.Join(bp.Dir, name)); f != nil {
					files = append(files, f)
				}
			}
			// Stdlib packages are imported solely for their package-level type,
			// interface, method, func, var, and const signatures; function
			// bodies never affect exported types or method sets.
			conf := types.Config{
				Importer:         s,
				Error:            func(error) {},
				FakeImportC:      true,
				IgnoreFuncBodies: true,
			}
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

// matchStdDir returns the sorted non-test .go basenames in dir that match
// s.ctx's GOOS/GOARCH/ToolTags/CgoEnabled=false constraints, without opening
// *_test.go files or running go/build's import scanner.
func (s *stdTypes) matchStdDir(dir string) []string {
	names := stdListGoFiles(dir)
	if len(names) == 0 {
		return nil
	}
	out := make([]string, 0, len(names))
	for _, name := range names {
		full := filepath.Join(dir, name)
		data := stdReadFile(full)
		if data == nil {
			continue
		}
		if !needsStdBuildMatch(name, data) {
			out = append(out, name)
			continue
		}
		matchCtx := s.ctx
		matchCtx.OpenFile = func(string) (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(data)), nil
		}
		if ok, err := matchCtx.MatchFile(dir, name); err == nil && ok {
			out = append(out, name)
		}
	}
	return out
}

func stdListGoFiles(dir string) []string {
	if v, ok := stdDirEntries.Load(dir); ok {
		return v.([]string)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		stdDirEntries.Store(dir, []string(nil))
		return nil
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		n := e.Name()
		if !strings.HasSuffix(n, ".go") || strings.HasSuffix(n, "_test.go") || strings.HasPrefix(n, "_") || strings.HasPrefix(n, ".") {
			continue
		}
		names = append(names, n)
	}
	sort.Strings(names)
	stdDirEntries.Store(dir, names)
	return names
}

func stdReadFile(fullPath string) []byte {
	if v, ok := stdFileBytes.Load(fullPath); ok {
		return v.([]byte)
	}
	data, err := os.ReadFile(fullPath)
	if err != nil {
		stdFileBytes.Store(fullPath, []byte(nil))
		return nil
	}
	stdFileBytes.Store(fullPath, data)
	return data
}

// needsStdBuildMatch reports whether name/data may depend on build constraints
// or cgo (so ctx.MatchFile must evaluate it). Plain .go files with no filename
// underscore, no //go:build or // +build header directive, and no import "C"
// compile unconditionally on every target.
func needsStdBuildMatch(name string, data []byte) bool {
	if strings.ContainsRune(name, '_') {
		return true
	}
	if bytes.Contains(data, []byte(`"C"`)) {
		return true
	}
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "package ") {
			return false
		}
		if strings.HasPrefix(line, "//go:build") || strings.HasPrefix(line, "// +build") || strings.HasPrefix(line, "//+build") {
			return true
		}
	}
	return false
}

// stdParseFile parses a GOROOT .go file once per process, strips function
// bodies, and prunes imports that are referenced only inside function bodies.
func stdParseFile(fullPath string) *ast.File {
	if v, ok := stdParsedASTs.Load(fullPath); ok {
		return v.(*ast.File)
	}
	data := stdReadFile(fullPath)
	if data == nil {
		stdParsedASTs.Store(fullPath, (*ast.File)(nil))
		return nil
	}
	f, err := parser.ParseFile(stdSharedFset, fullPath, data, parser.SkipObjectResolution)
	if err != nil {
		stdParsedASTs.Store(fullPath, (*ast.File)(nil))
		return nil
	}
	pruneStdFile(f)
	stdParsedASTs.Store(fullPath, f)
	return f
}

// pruneStdFile clears every top-level function/method body (which
// IgnoreFuncBodies skips anyway) and drops imports that cannot be referenced
// by any package-level type, const, var, or function signature in f.
func pruneStdFile(f *ast.File) {
	if f == nil {
		return
	}
	for _, d := range f.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok {
			fd.Body = nil
		}
	}
	if len(f.Imports) == 0 {
		return
	}

	usedIdents := map[string]bool{}
	selRoots := map[string]bool{}
	declared := map[string]bool{}
	for _, d := range f.Decls {
		if gd, ok := d.(*ast.GenDecl); ok && gd.Tok == token.IMPORT {
			continue
		}
		switch d := d.(type) {
		case *ast.FuncDecl:
			if d.Recv == nil && d.Name != nil {
				declared[d.Name.Name] = true
			}
			collectFieldNames(d.Recv, declared)
			if d.Type != nil {
				collectFieldNames(d.Type.Params, declared)
				collectFieldNames(d.Type.Results, declared)
				collectFieldNames(d.Type.TypeParams, declared)
			}
		case *ast.GenDecl:
			for _, sp := range d.Specs {
				switch sp := sp.(type) {
				case *ast.TypeSpec:
					if sp.Name != nil {
						declared[sp.Name.Name] = true
					}
					collectFieldNames(sp.TypeParams, declared)
				case *ast.ValueSpec:
					for _, id := range sp.Names {
						declared[id.Name] = true
					}
				}
			}
		}
		ast.Inspect(d, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.Ident:
				usedIdents[n.Name] = true
			case *ast.SelectorExpr:
				if x, ok := n.X.(*ast.Ident); ok {
					selRoots[x.Name] = true
				}
			}
			return true
		})
	}

	// Determine which imports are directly matched by alias or inferred
	// package base name. If any selector root outside local/package declarations
	// does not match any kept import (e.g. a package whose declared name differs
	// from its directory name, like crypto/internal/fips140deps -> fipsdeps),
	// conservatively keep all non-blank imports.
	keepSpec := make(map[*ast.ImportSpec]bool, len(f.Imports))
	matchedImportNames := map[string]bool{}
	hasDot := false
	for _, im := range f.Imports {
		if im.Name != nil {
			switch im.Name.Name {
			case "_":
				continue
			case ".":
				keepSpec[im] = true
				hasDot = true
				continue
			default:
				if usedIdents[im.Name.Name] {
					keepSpec[im] = true
					matchedImportNames[im.Name.Name] = true
				}
				continue
			}
		}
		ip, err := strconv.Unquote(im.Path.Value)
		if err != nil {
			keepSpec[im] = true
			continue
		}
		base := stdImportBase(ip)
		if usedIdents[base] {
			keepSpec[im] = true
			matchedImportNames[base] = true
		}
	}

	if !hasDot {
		unexplainedSelector := false
		for root := range selRoots {
			if !matchedImportNames[root] && !declared[root] {
				unexplainedSelector = true
				break
			}
		}
		if unexplainedSelector {
			for _, im := range f.Imports {
				if im.Name == nil || im.Name.Name != "_" {
					keepSpec[im] = true
				}
			}
		}
	}

	newDecls := make([]ast.Decl, 0, len(f.Decls))
	newImports := make([]*ast.ImportSpec, 0, len(keepSpec))
	for _, d := range f.Decls {
		gd, ok := d.(*ast.GenDecl)
		if !ok || gd.Tok != token.IMPORT {
			newDecls = append(newDecls, d)
			continue
		}
		specs := make([]ast.Spec, 0, len(gd.Specs))
		for _, sp := range gd.Specs {
			if im, ok := sp.(*ast.ImportSpec); ok && keepSpec[im] {
				specs = append(specs, im)
				newImports = append(newImports, im)
			}
		}
		if len(specs) > 0 {
			gd.Specs = specs
			newDecls = append(newDecls, gd)
		}
	}
	f.Decls = newDecls
	f.Imports = newImports
}

func collectFieldNames(fl *ast.FieldList, out map[string]bool) {
	if fl == nil {
		return
	}
	for _, f := range fl.List {
		for _, id := range f.Names {
			out[id.Name] = true
		}
	}
}

func stdImportBase(ip string) string {
	base := path.Base(ip)
	if isVersionSegment(base) && strings.Contains(ip, "/") {
		return path.Base(path.Dir(ip))
	}
	return base
}

func isVersionSegment(s string) bool {
	if len(s) < 2 || s[0] != 'v' {
		return false
	}
	for i := 1; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && c != '.' {
			return false
		}
	}
	return true
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
