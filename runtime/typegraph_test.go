package runtimefiles

import (
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"slices"
	"strings"
	"testing"
)

// taskGraphPackages are the packages whose types may be reachable from
// rt.Task: the runtime's own and those whose types have no method that
// the Go runtime or the core calls through an interface. A type of package
// time is not among them: time.Time's String and Format, and
// time.Duration's String, stay in every program whose type graph reaches
// them from a type used in an interface (binsize plan 1.4, R3).
var taskGraphPackages = []string{"ergo.local/prototype/runtime/rt", "sync", "sync/atomic", "internal/sync", "unsafe"}

// typeGraph type-checks the runtime package path (one of packages) from
// its embedded source.
func typeGraph(t *testing.T, path string) *types.Package {
	t.Helper()
	fset := token.NewFileSet()
	var files []*ast.File
	for _, name := range packages[path].files {
		data, err := sources.ReadFile(strings.TrimPrefix(path, "runtime/") + "/" + name)
		if err != nil {
			t.Fatal(err)
		}
		f, err := parser.ParseFile(fset, name, data, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, f)
	}
	conf := types.Config{Importer: importer.ForCompiler(fset, "source", nil)}
	pkg, err := conf.Check(Module+"/"+path, fset, files, nil)
	if err != nil {
		t.Fatal(err)
	}
	return pkg
}

// walkTypes visits every type reachable from root as the Go linker marks
// the types of a type used in an interface: through a named type's
// underlying type and the signatures of its methods (value and pointer
// receivers), a pointer's, slice's, array's, map's and channel's
// elements, a struct's fields, a signature's parameters and results and an
// interface's methods. bad reports a named type outside allowed with the
// path from root, which is the path -dumpdep shows.
func walkTypes(root types.Type, allowed func(*types.Named) bool, bad func(path []string)) {
	seen := map[types.Type]bool{}
	var walk func(t types.Type, path []string)
	walk = func(t types.Type, path []string) {
		if t == nil || seen[t] {
			return
		}
		seen[t] = true
		switch t := t.(type) {
		case *types.Named:
			path = append(slices.Clone(path), t.String())
			if !allowed(t) {
				bad(path)
				return
			}
			walk(t.Underlying(), path)
			for _, mset := range []*types.MethodSet{types.NewMethodSet(t), types.NewMethodSet(types.NewPointer(t))} {
				for sel := range mset.Methods() {
					walk(sel.Type(), append(slices.Clone(path), "."+sel.Obj().Name()))
				}
			}
		case *types.Alias:
			walk(types.Unalias(t), path)
		case *types.Pointer:
			walk(t.Elem(), path)
		case *types.Slice:
			walk(t.Elem(), path)
		case *types.Array:
			walk(t.Elem(), path)
		case *types.Map:
			walk(t.Key(), path)
			walk(t.Elem(), path)
		case *types.Chan:
			walk(t.Elem(), path)
		case *types.Struct:
			for f := range t.Fields() {
				walk(f.Type(), append(slices.Clone(path), "."+f.Name()))
			}
		case *types.Signature:
			for v := range t.Params().Variables() {
				walk(v.Type(), path)
			}
			for v := range t.Results().Variables() {
				walk(v.Type(), path)
			}
		case *types.Interface:
			for m := range t.Methods() {
				walk(m.Type(), append(slices.Clone(path), "."+m.Name()))
			}
		}
	}
	walk(root, nil)
}

// walkFixture is a package whose root type reaches a type of package time
// by each edge walkTypes follows, one per root field.
const walkFixture = `package p

import "time"

type Root struct {
	byMethod   *M
	byElem     map[string][]chan [2]*time.Location
	byIface    I
	byFunc     func(int) time.Month
	byEmbedded struct{ time.Weekday }
}

type M struct{}

func (*M) At() time.Time { return time.Time{} }

type I interface{ Span() time.Duration }
`

// TestWalkTypesFollowsEveryEdge: walkTypes reaches a type through a
// method's signature, a map's, slice's, channel's, array's and pointer's
// elements, an interface's methods, a function type and an embedded field,
// as the linker's mark of a type used in an interface does.
// Mutant: bs3-walk-shallow (walkTypes skips method signatures).
func TestWalkTypesFollowsEveryEdge(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "p.go", walkFixture, 0)
	if err != nil {
		t.Fatal(err)
	}
	pkg, err := (&types.Config{Importer: importer.ForCompiler(fset, "source", nil)}).Check("p", fset, []*ast.File{f}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	walkTypes(pkg.Scope().Lookup("Root").Type(), func(n *types.Named) bool { return n.Obj().Pkg().Path() == "p" }, func(path []string) {
		got = append(got, path[len(path)-1])
	})
	slices.Sort(got)
	want := []string{"time.Duration", "time.Location", "time.Month", "time.Time", "time.Weekday"}
	if !slices.Equal(got, want) {
		t.Errorf("walkTypes reached %v; want %v", got, want)
	}
}

// TestTaskTypeGraphHasNoTime: no type of package time (or of any package
// but the runtime's, sync and unsafe) is reachable from rt.Task through
// fields, elements and method signatures (binsize BS3). Every program
// uses a type with a *rt.Task parameter in an interface, and the Go linker
// then keeps every method of every type that graph reaches whose name an
// interface call names: time.Time's String reached Format, 100 KB of each
// program. The failure names the path, as go build -ldflags=-dumpdep does.
// Mutants: bs3-virtual-time (rt.Virtual holds a time.Time again) and
// bs3-context-in-core (the task holds a context.Context).
func TestTaskTypeGraphHasNoTime(t *testing.T) {
	pkg := typeGraph(t, "runtime/rt")
	task := pkg.Scope().Lookup("Task")
	if task == nil {
		t.Fatal("runtime/rt declares no Task")
	}
	allowed := func(n *types.Named) bool {
		return n.Obj().Pkg() == nil || slices.Contains(taskGraphPackages, n.Obj().Pkg().Path())
	}
	walkTypes(task.Type(), allowed, func(path []string) {
		t.Errorf("rt.Task reaches %s: %s", path[len(path)-1], strings.Join(path, " "))
	})
}
