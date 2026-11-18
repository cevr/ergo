package drawing_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/constant"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const (
	module   = "github.com/egoist/mygo/drawing/"
	internal = "github.com/egoist/mygo/internal/"
)

// TestMirrorsInternal checks each package under drawing against the
// internal package it mirrors, by go/types: on this host's build, and
// on the build of each phone (android/arm64, ios/arm64), where files
// constrained to the phone hold what a phone host needs (the Android
// EGL window, for one). Every exported constant, type and function of
// the internal package has a counterpart of the same name that is the
// same constant, the same type (an alias) or a function of the
// identical signature whose body calls the internal function with its
// own parameters in order, and nothing else is exported. An internal
// name may be missing only when it is a variable or a function whose
// signature names an unexported type.
//
// Mutants it kills (drawgen, testdata/mutants.json in hosts/mygo): the
// constants left out; a wrapper that passes its arguments in another
// order; a file constrained to a phone left out (only the phone builds
// see it).
func TestMirrorsInternal(t *testing.T) {
	t.Run("host", func(t *testing.T) {
		pkgs := goList(t, nil, "-export")
		fset := token.NewFileSet()
		imp := importer.ForCompiler(fset, "gc", func(path string) (io.ReadCloser, error) {
			p, ok := pkgs[path]
			if !ok || p.Export == "" {
				return nil, os.ErrNotExist
			}
			return os.Open(p.Export)
		})
		mirrors(t, fset, imp, pkgs)
	})
	// A phone's build uses cgo, and this host may have no C compiler
	// for it, so there is no export data: the packages are checked from
	// source under the phone's build constraints, with import "C" left
	// to go/types' fake package.
	for _, target := range []string{"android/arm64", "ios/arm64"} {
		t.Run(target, func(t *testing.T) {
			goos, goarch, _ := strings.Cut(target, "/")
			pkgs := goList(t, []string{"GOOS=" + goos, "GOARCH=" + goarch, "CGO_ENABLED=1"})
			fset := token.NewFileSet()
			src := &sourceImporter{fset: fset, pkgs: pkgs, sizes: types.SizesFor("gc", goarch), done: map[string]*types.Package{}}
			mirrors(t, fset, src, pkgs)
			for _, err := range src.errs {
				t.Error(err)
			}
		})
	}
}

// mirrors checks every package of the drawing module in pkgs against its
// internal package, both resolved by imp.
func mirrors(t *testing.T, fset *token.FileSet, imp types.Importer, pkgs map[string]listed) {
	n := 0
	for path, p := range pkgs {
		rel, ok := strings.CutPrefix(path, module)
		if !ok || strings.HasPrefix(rel, "internal/") {
			continue
		}
		n++
		t.Run(rel, func(t *testing.T) {
			in, err := imp.Import(internal + rel)
			if err != nil {
				t.Fatal(err)
			}
			var files []*ast.File
			for _, f := range p.GoFiles {
				af, err := parser.ParseFile(fset, filepath.Join(p.Dir, f), nil, 0)
				if err != nil {
					t.Fatal(err)
				}
				files = append(files, af)
			}
			info := &types.Info{Uses: map[*ast.Ident]types.Object{}, Defs: map[*ast.Ident]types.Object{}}
			conf := &types.Config{Importer: imp}
			if s, ok := imp.(*sourceImporter); ok {
				conf.Sizes = s.sizes
			}
			out, err := conf.Check(path, fset, files, info)
			if err != nil {
				t.Fatal(err)
			}
			bodies := map[types.Object]*ast.FuncDecl{}
			for _, f := range files {
				for _, d := range f.Decls {
					if fd, ok := d.(*ast.FuncDecl); ok {
						bodies[info.Defs[fd.Name]] = fd
					}
				}
			}
			for _, name := range in.Scope().Names() {
				o := in.Scope().Lookup(name)
				if !o.Exported() {
					continue
				}
				m := out.Scope().Lookup(name)
				if m == nil {
					if !mayLeaveOut(o) {
						t.Errorf("%s is not mirrored", name)
					}
					continue
				}
				switch o := o.(type) {
				case *types.Const:
					mc, ok := m.(*types.Const)
					if !ok || !types.Identical(mc.Type(), o.Type()) || !constant.Compare(mc.Val(), token.EQL, o.Val()) {
						t.Errorf("const %s: %v, want %v", name, m, o)
					}
				case *types.TypeName:
					if !sameType(m, o) {
						t.Errorf("type %s: %v, want an alias of %v", name, m, o)
					}
				case *types.Func:
					mf, ok := m.(*types.Func)
					if !ok || !types.Identical(mf.Type(), o.Type()) {
						t.Errorf("func %s: %v, want %v", name, m, o)
						continue
					}
					if why := callsInOrder(bodies[mf], mf, o, info); why != "" {
						t.Errorf("func %s: %s", name, why)
					}
				default:
					t.Errorf("%s: %v is mirrored but is not a constant, type or function", name, o)
				}
			}
			for _, name := range out.Scope().Names() {
				if m := out.Scope().Lookup(name); m.Exported() && in.Scope().Lookup(name) == nil {
					t.Errorf("%s is not exported by %s", name, internal+rel)
				}
			}
		})
	}
	if n == 0 {
		t.Fatal("no package under drawing")
	}
}

// mayLeaveOut reports whether drawgen may leave the internal object out:
// a variable, or a function whose signature names an unexported type.
func mayLeaveOut(o types.Object) bool {
	switch o := o.(type) {
	case *types.Var:
		return true
	case *types.Func:
		sig := o.Type().(*types.Signature)
		for _, tup := range []*types.Tuple{sig.Params(), sig.Results()} {
			for v := range tup.Variables() {
				if namesUnexported(v.Type(), o.Pkg()) {
					return true
				}
			}
		}
	}
	return false
}

// namesUnexported reports whether t names an unexported type of pkg.
func namesUnexported(t types.Type, pkg *types.Package) bool {
	found := false
	var walk func(t types.Type)
	walk = func(t types.Type) {
		if found {
			return
		}
		switch t := t.(type) {
		case *types.Named:
			if o := t.Obj(); o.Pkg() == pkg && !o.Exported() {
				found = true
			}
			for a := range t.TypeArgs().Types() {
				walk(a)
			}
		case *types.Alias:
			if o := t.Obj(); o.Pkg() == pkg && !o.Exported() {
				found = true
			}
		case *types.Pointer:
			walk(t.Elem())
		case *types.Slice:
			walk(t.Elem())
		case *types.Array:
			walk(t.Elem())
		case *types.Map:
			walk(t.Key())
			walk(t.Elem())
		case *types.Chan:
			walk(t.Elem())
		case *types.Signature:
			for _, tup := range []*types.Tuple{t.Params(), t.Results()} {
				for v := range tup.Variables() {
					walk(v.Type())
				}
			}
		case *types.Struct:
			for f := range t.Fields() {
				walk(f.Type())
			}
		}
	}
	walk(t)
	return found
}

// sameType reports whether m is an alias that denotes o's type.
func sameType(m types.Object, o *types.TypeName) bool {
	mt, ok := m.(*types.TypeName)
	if !ok || !mt.IsAlias() {
		return false
	}
	return types.Identical(types.Unalias(mt.Type()), types.Unalias(o.Type()))
}

// callsInOrder returns why the body of fd, the mirror m of o, is not one
// call of o with m's parameters in order, or "".
func callsInOrder(fd *ast.FuncDecl, m, o *types.Func, info *types.Info) string {
	if fd == nil || fd.Body == nil || len(fd.Body.List) != 1 {
		return "the body is not one statement"
	}
	var call ast.Expr
	switch s := fd.Body.List[0].(type) {
	case *ast.ReturnStmt:
		if len(s.Results) == 1 {
			call = s.Results[0]
		}
	case *ast.ExprStmt:
		call = s.X
	}
	c, ok := call.(*ast.CallExpr)
	if !ok {
		return "the body is not a call"
	}
	sel, ok := c.Fun.(*ast.SelectorExpr)
	if !ok || info.Uses[sel.Sel] != types.Object(o) {
		return "the body does not call " + o.FullName()
	}
	sig := m.Type().(*types.Signature)
	if len(c.Args) != sig.Params().Len() {
		return "the call passes another number of arguments"
	}
	for i, a := range c.Args {
		id, ok := a.(*ast.Ident)
		if !ok || info.Uses[id] != types.Object(sig.Params().At(i)) {
			return "an argument is not the parameter in its place"
		}
	}
	return ""
}

type listed struct {
	ImportPath, Dir, Export string
	GoFiles, CgoFiles       []string
	ImportMap               map[string]string
}

// goList returns the packages of this module and their dependencies, by
// import path, as the go command lists them in env (added to this
// process's) with flags.
func goList(t *testing.T, env []string, flags ...string) map[string]listed {
	args := append([]string{"list"}, flags...)
	args = append(args, "-deps", "-json=ImportPath,Dir,Export,GoFiles,CgoFiles,ImportMap", "./...")
	cmd := exec.Command("go", args...)
	cmd.Env = append(os.Environ(), env...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	b, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list: %v\n%s", err, stderr.Bytes())
	}
	pkgs := map[string]listed{}
	dec := json.NewDecoder(bytes.NewReader(b))
	for dec.More() {
		var p listed
		if err := dec.Decode(&p); err != nil {
			t.Fatal(err)
		}
		pkgs[p.ImportPath] = p
	}
	return pkgs
}

// sourceImporter type-checks the listed packages from source, each once,
// with import "C" left to go/types' fake package. It keeps the errors of
// every package it checks that does not use cgo.
type sourceImporter struct {
	fset  *token.FileSet
	pkgs  map[string]listed
	sizes types.Sizes
	done  map[string]*types.Package
	errs  []error
}

func (s *sourceImporter) Import(path string) (*types.Package, error) {
	if path == "unsafe" {
		return types.Unsafe, nil
	}
	if pkg, ok := s.done[path]; ok {
		return pkg, nil
	}
	p, ok := s.pkgs[path]
	if !ok {
		return nil, fmt.Errorf("%s is not listed", path)
	}
	var files []*ast.File
	for _, f := range append(slices.Clone(p.GoFiles), p.CgoFiles...) {
		af, err := parser.ParseFile(s.fset, filepath.Join(p.Dir, f), nil, parser.SkipObjectResolution)
		if err != nil {
			return nil, err
		}
		files = append(files, af)
	}
	conf := &types.Config{
		FakeImportC: true,
		Sizes:       s.sizes,
		Importer: importerFunc(func(q string) (*types.Package, error) {
			if m, ok := p.ImportMap[q]; ok {
				q = m
			}
			return s.Import(q)
		}),
		Error: func(err error) {
			// The fake "C" package has no types, so in a package that
			// uses cgo an expression over one is untyped (net's
			// cgo_unix.go calls the _C_ functions cgo would write); only
			// those packages' errors are expected. None of the mirrored
			// packages uses cgo.
			if len(p.CgoFiles) > 0 {
				return
			}
			s.errs = append(s.errs, err)
		},
	}
	pkg, _ := conf.Check(path, s.fset, files, nil)
	s.done[path] = pkg
	return pkg, nil
}

type importerFunc func(path string) (*types.Package, error)

func (f importerFunc) Import(path string) (*types.Package, error) { return f(path) }
