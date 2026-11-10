package lower

import (
	"go/ast"
	"go/build"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// typedSource is a production Go package of this module, parsed and
// type-checked, so an owner scan resolves names through go/types: an alias, a
// method value, a local copy or a helper cannot hide a use the way a spelling
// can.
type typedSource struct {
	fset  *token.FileSet
	files []*ast.File
	info  *types.Info
	pkg   *types.Package
}

var typedSources sync.Map // dir -> *typedSource

// typedImports is the one source importer of every typedPackage call, with
// its file set: each package the scanned packages import, the standard
// library's included, is type-checked once per test binary, not once per
// scanned package (fast-cpu, batch 9). The importer is not safe for
// concurrent use, so a call holds the lock while it checks.
var typedImports = struct {
	sync.Mutex
	fset     *token.FileSet
	importer types.Importer
}{fset: token.NewFileSet()}

// typedPackage type-checks the non-test files of dir (relative to this
// package) once per test binary.
func typedPackage(t *testing.T, dir string) *typedSource {
	t.Helper()
	if v, ok := typedSources.Load(dir); ok {
		return v.(*typedSource)
	}
	typedImports.Lock()
	defer typedImports.Unlock()
	if v, ok := typedSources.Load(dir); ok {
		return v.(*typedSource)
	}
	if typedImports.importer == nil {
		typedImports.importer = importer.ForCompiler(typedImports.fset, "source", nil)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	src := &typedSource{fset: typedImports.fset, info: &types.Info{
		Types: map[ast.Expr]types.TypeAndValue{},
		Defs:  map[*ast.Ident]types.Object{},
		Uses:  map[*ast.Ident]types.Object{},
	}}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		// Only the files of this host's build: runtime/v1 has one file per
		// platform for some bodies (exec_exit_linux.go, exec_exit_kqueue.go).
		if ok, err := build.Default.MatchFile(dir, name); err != nil || !ok {
			if err != nil {
				t.Fatal(err)
			}
			continue
		}
		f, err := parser.ParseFile(src.fset, dir+"/"+name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		src.files = append(src.files, f)
	}
	conf := types.Config{Importer: typedImports.importer}
	src.pkg, err = conf.Check(src.files[0].Name.Name, src.fset, src.files, src.info)
	if err != nil {
		t.Fatalf("type-check %s: %v", dir, err)
	}
	typedSources.Store(dir, src)
	return src
}

// owners visits each top-level declaration with its qualified owner name:
// "Recv.Method" for a method, the name for a function, "file:package"
// otherwise.
func (s *typedSource) owners(visit func(owner string, decl ast.Decl)) {
	for _, f := range s.files {
		for _, decl := range f.Decls {
			visit(declOwner(s.fset, decl), decl)
		}
	}
}

func declOwner(fset *token.FileSet, decl ast.Decl) string {
	fn, ok := decl.(*ast.FuncDecl)
	if !ok {
		name := fset.Position(decl.Pos()).Filename
		return name[strings.LastIndex(name, "/")+1:] + ":package"
	}
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return fn.Name.Name
	}
	recv := fn.Recv.List[0].Type
	if star, ok := recv.(*ast.StarExpr); ok {
		recv = star.X
	}
	if idx, ok := recv.(*ast.IndexExpr); ok {
		recv = idx.X
	}
	return types.ExprString(recv) + "." + fn.Name.Name
}

// method resolves the named method of a package type.
func (s *typedSource) method(t *testing.T, typ, name string) types.Object {
	t.Helper()
	tn, ok := s.pkg.Scope().Lookup(typ).(*types.TypeName)
	if !ok {
		t.Fatalf("%s: no type %s", s.pkg.Path(), typ)
	}
	obj, _, _ := types.LookupFieldOrMethod(types.NewPointer(tn.Type()), true, s.pkg, name)
	if obj == nil {
		t.Fatalf("%s.%s: no method %s", s.pkg.Path(), typ, name)
	}
	return obj
}

// users is the set of owners whose declarations refer to obj (a call, a
// method value or any other use).
func (s *typedSource) users(obj types.Object) map[string]bool {
	out := map[string]bool{}
	s.owners(func(owner string, decl ast.Decl) {
		ast.Inspect(decl, func(n ast.Node) bool {
			if id, ok := n.(*ast.Ident); ok && s.info.Uses[id] == obj {
				out[owner] = true
			}
			return true
		})
	})
	return out
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// stringLits returns the unquoted string literals under n.
func stringLits(t *testing.T, n ast.Node) []string {
	var out []string
	ast.Inspect(n, func(n ast.Node) bool {
		if s, ok := n.(*ast.BasicLit); ok && s.Kind == token.STRING {
			v, err := strconv.Unquote(s.Value)
			if err != nil {
				t.Fatal(err)
			}
			out = append(out, v)
		}
		return true
	})
	return out
}

// The env type's Go name has one source, (*lowerer).env. Every owner that
// reads it (a call, a method value, a local copy) is classified here; a new
// reader fails until it is. Of these, only envClosure writes a closure: no
// other reader may hold both "func(" and "{" in its literals, however the
// text is assembled (concatenation, fmt.Sprintf, a local). A helper that
// returns env-typed parameter text is a new reader, so it fails too.
func TestRuntime199EnvTypeReaders(t *testing.T) {
	src := typedPackage(t, ".")
	readers := src.users(src.method(t, "lowerer", "env"))
	classified := map[string]string{
		"funcCtx.envClosure":     "the closure owner",
		"lowerer.emitFuncBody":   "a declared function's header: func name(env *Env, ...)",
		"lowerer.initFunc":       "the package initializer's declared header",
		"lowerer.emitContext":    "a context builder's declared header",
		"lowerer.envField":       "names the type before its first field",
		"lowerer.entry":          "the root Env value in main",
		"lowerer.testEntry":      "the root Env value in the test build's main (196l)",
		"lowerer.testDecl":       "a test's declared header: func ergoTestName(env *Env, ...)",
		"lowerer.lawDecl":        "a law's declared header: func ergoLawName(env *Env, ...) (bool, error) (#162)",
		"lowerer.testRunnerHead": "the test runner's declared header: func ergoTests(env *Env, seed uint64)",
		"funcCtx.operations":     "an Env value of the caller's task for an operation table",
		"funcCtx.witnessMethod":  "an Env value of the caller's task for a witness adapter",
		"typeCtx.funcType":       "a function type, which has no body",
		"lowerer.machinePlan":    "a machine plan's declared Step header: func (X) Step(env *Env, ...)",
		"lowerer.genFunc":        "a generator's assertion of the runner's Env that a constructor runs in (fu6a)",
		"funcCtx.contextValue":   "the zero Env a Context value's providers build into where no environment is bound: a variable, no closure (its closure is envClosure's)",
	}
	for _, owner := range sortedKeys(readers) {
		if _, ok := classified[owner]; !ok {
			t.Errorf("%s reads the env type name and is not classified; only envClosure writes Env closures", owner)
		}
	}
	for owner := range classified {
		if !readers[owner] {
			t.Errorf("%s is classified as an env type reader but no longer reads it", owner)
		}
	}
	// A reader that writes a Go closure whose parameter is not an Env.
	otherClosure := map[string]string{
		"lowerer.entry":     "main's root task body takes the task and builds the root Env inside",
		"lowerer.testEntry": "the test build's root task body, as lowerer.entry's",
	}
	src.owners(func(owner string, decl ast.Decl) {
		if !readers[owner] || owner == "funcCtx.envClosure" || otherClosure[owner] != "" {
			return
		}
		open, brace := false, false
		for _, s := range stringLits(t, decl) {
			open = open || strings.Contains(s, "func(")
			brace = brace || strings.Contains(s, "{")
		}
		if open && brace {
			t.Errorf("%s reads the env type and writes closure text; route it through envClosure", owner)
		}
	})
}

// Only newFrame makes a frame. Resolved through go/types, any value of type
// frame outside it is a construction or a copy: a composite literal (through
// an alias too), a dereference, a variable or field of type frame, or
// new(frame).
func TestRuntime199FrameValues(t *testing.T) {
	src := typedPackage(t, ".")
	frameT := src.pkg.Scope().Lookup("frame").Type()
	src.owners(func(owner string, decl ast.Decl) {
		if owner == "newFrame" {
			return
		}
		ast.Inspect(decl, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.Ident:
				if v, ok := src.info.Defs[n].(*types.Var); ok && types.Identical(v.Type(), frameT) {
					t.Errorf("%s declares %s of type frame outside newFrame", owner, n.Name)
				}
			case *ast.CallExpr:
				if id, ok := n.Fun.(*ast.Ident); ok {
					if b, ok := src.info.Uses[id].(*types.Builtin); ok && b.Name() == "new" {
						if p, ok := src.info.Types[n].Type.(*types.Pointer); ok && types.Identical(p.Elem(), frameT) {
							t.Errorf("%s allocates a frame outside newFrame", owner)
						}
					}
				}
			}
			if x, ok := n.(ast.Expr); ok {
				if tv, ok := src.info.Types[x]; ok && tv.IsValue() && types.Identical(tv.Type, frameT) {
					t.Errorf("%s makes or copies a frame value (%s) outside newFrame", owner, types.ExprString(x))
				}
			}
			return true
		})
	})
}
