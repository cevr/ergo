package lower

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"math/rand"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/lower/testdata/rawhelper"
	"ergo.local/prototype/internal/sketch/syntax"
)

// Round 6 (196b): the checker hands out its lists in canonical order
// (check/canon.go), so a reader of a raw list by any route gets a function
// of the program. The guards of this package prove three narrower facts
// about the lowering's own code (canon.go, "What the tests of this package
// prove"): no direct read of a list field outside canon.go and Validate
// (TestLoweringReadsCanonicalLists), no Unit value outside the entry
// functions (TestLoweringUnitStaysAtEntries), and no import of reflect or
// unsafe (TestLoweringImportsNoReflection).

const lowerPath = "ergo.local/prototype/internal/sketch/lower"

// unitEntries are the functions that may hold a Unit, by file: they check
// it and convert it into the canonical unit. canon.go may hold one
// anywhere: it is that conversion.
var unitEntries = map[string][]string{
	"lower.go":  {"Validate", "validateFiles"},
	"module.go": {"Lower", "lowerPackage", "newLowerer", "newBuildLowerer"},
	"tests.go":  {"LowerTests"},
}

// mentions reports whether t is u or is built from u.
func mentions(t types.Type, u types.Type, seen map[types.Type]bool) bool {
	if t == nil || seen[t] {
		return false
	}
	seen[t] = true
	if types.Identical(t, u) {
		return true
	}
	switch t := t.(type) {
	case *types.Pointer:
		return mentions(t.Elem(), u, seen)
	case *types.Slice:
		return mentions(t.Elem(), u, seen)
	case *types.Array:
		return mentions(t.Elem(), u, seen)
	case *types.Map:
		return mentions(t.Key(), u, seen) || mentions(t.Elem(), u, seen)
	case *types.Chan:
		return mentions(t.Elem(), u, seen)
	case *types.Tuple:
		for i := 0; i < t.Len(); i++ {
			if mentions(t.At(i).Type(), u, seen) {
				return true
			}
		}
	case *types.Signature:
		return mentions(t.Params(), u, seen) || mentions(t.Results(), u, seen)
	case *types.Struct:
		for i := 0; i < t.NumFields(); i++ {
			if mentions(t.Field(i).Type(), u, seen) {
				return true
			}
		}
	}
	return false
}

// unitHolders returns each top-level declaration of src, outside canon.go
// and the entry functions, that has an expression whose type is or is
// built from lower.Unit. The Unit's own declaration is not one.
func unitHolders(t *testing.T, src *typedSource) []string {
	t.Helper()
	var unit types.Type
	for _, pkg := range append([]*types.Package{src.pkg}, src.pkg.Imports()...) {
		if pkg.Path() == lowerPath || pkg == src.pkg && pkg.Path() == "lower" {
			unit = pkg.Scope().Lookup("Unit").Type()
		}
	}
	if unit == nil {
		t.Fatal("no type lower.Unit")
	}
	var found []string
	for _, f := range src.files {
		file := filepath.Base(src.fset.Position(f.Pos()).Filename)
		if file == "canon.go" {
			continue
		}
		for _, decl := range f.Decls {
			name := ""
			switch d := decl.(type) {
			case *ast.FuncDecl:
				name = d.Name.Name
			case *ast.GenDecl:
				if d.Tok == token.IMPORT {
					continue
				}
				for _, s := range d.Specs {
					if ts, ok := s.(*ast.TypeSpec); ok {
						name = ts.Name.Name
					}
				}
				if name == "Unit" {
					continue
				}
			}
			if slices.Contains(unitEntries[file], name) {
				continue
			}
			held := false
			ast.Inspect(decl, func(n ast.Node) bool {
				if e, ok := n.(ast.Expr); ok && !held {
					held = mentions(src.info.TypeOf(e), unit, map[types.Type]bool{})
				}
				return !held
			})
			if held {
				found = append(found, file+":"+name)
			}
		}
	}
	slices.Sort(found)
	return found
}

// TestLoweringUnitStaysAtEntries: after the entry functions, the lowering
// holds no Unit, so no route (a helper, a conversion to a struct of the
// same fields) can read the caller's order of files or imports. The
// fixture testdata/rawunits holds those routes; each is reported.
func TestLoweringUnitStaysAtEntries(t *testing.T) {
	if found := unitHolders(t, typedPackage(t, ".")); len(found) > 0 {
		t.Fatalf("a Unit outside the entry functions (pass the canonical unit instead):\n%s", strings.Join(found, "\n"))
	}
	want := []string{
		"module.go:escInModuleGo",
		"probe.go:count",
		"probe.go:escConvert",
		"probe.go:escHelper",
		"probe.go:escHolder",
		"probe.go:escImports",
	}
	if got := unitHolders(t, typedPackage(t, "testdata/rawunits")); !slices.Equal(got, want) {
		t.Fatalf("fixture holders:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// reflectionImports returns each import of reflect or unsafe in src's
// files, which reach a field with no identifier the guards could resolve.
func reflectionImports(t *testing.T, src *typedSource) []string {
	t.Helper()
	var found []string
	for _, f := range src.files {
		for _, imp := range f.Imports {
			path, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				t.Fatal(err)
			}
			if path == "reflect" || path == "unsafe" {
				found = append(found, filepath.Base(src.fset.Position(f.Pos()).Filename)+" "+path)
			}
		}
	}
	slices.Sort(found)
	return found
}

// TestLoweringImportsNoReflection: package lower imports neither reflect
// nor unsafe (a depguard rule). The fixture testdata/rawimports holds the
// round 5 verify's reflection and unsafe escapes; both imports are found.
func TestLoweringImportsNoReflection(t *testing.T) {
	if found := reflectionImports(t, typedPackage(t, ".")); len(found) > 0 {
		t.Fatalf("package lower imports %s", strings.Join(found, ", "))
	}
	want := []string{"probe.go reflect", "probe.go unsafe"}
	if got := reflectionImports(t, typedPackage(t, "testdata/rawimports")); !slices.Equal(got, want) {
		t.Fatalf("fixture imports %v, want %v", got, want)
	}
}

// TestRawTestsReaderIsCanonical: the round 5 verify's mutation, runTests
// routed through a reader of the raw p.Tests in another package, emits
// the same files for every listing and fresh check of the program, as the
// checker hands the list out in canonical order. In the second program the
// checker's order (by file content) is not the path order.
func TestRawTestsReaderIsCanonical(t *testing.T) {
	defer func(old func(*canonical, *check.Package) []*check.Test) { testsRead = old }(testsRead)
	testsRead = func(_ *canonical, p *check.Package) []*check.Test { return rawhelper.Tests(p) }
	for _, root := range [][]pkgSource{
		{
			{"/probe/a.ergo", "test A { assert 1 == 1 }\n"},
			{"/probe/z.ergo", "test Z { assert 2 == 2 }\nfunc main() {}\n"},
		},
		{
			{"/probe/a.ergo", "test Z { assert 2 == 2 }\n"},
			{"/probe/m.ergo", "test M { assert 3 == 3 }\nfunc main() {}\n"},
			{"/probe/z.ergo", "test A { assert 1 == 1 }\n"},
		},
	} {
		verifyCanonical(t, genProgram{root: root})
	}
}

// declKeys names each element of a raw list by its source path, offset
// and name; the sources are u's and its imports'.
func declKeys[T any](u Unit, list []T, at func(T) (syntax.Node, string)) []string {
	path := map[syntax.Node]string{}
	for _, unit := range append([]Unit{u}, u.Imports...) {
		for i, f := range unit.Files {
			syntax.Inspect(f, func(n syntax.Node) bool {
				if n != nil {
					path[n] = unit.Sources[i].Path
				}
				return true
			})
		}
	}
	var out []string
	for _, x := range list {
		n, name := at(x)
		out = append(out, fmt.Sprintf("%s@%d %s", path[n], n.Pos(), name))
	}
	return out
}

// escapedLists are the root's and each import's lists, read by the round
// 5 escapes that name no field of package lower: the external helper and
// reflection.
func escapedLists(u Unit) map[string][]string {
	out := map[string][]string{}
	field := func(p *check.Package, name string) any {
		return reflect.ValueOf(p).Elem().FieldByName(name).Interface()
	}
	for _, unit := range append([]Unit{u}, u.Imports...) {
		p := unit.Package
		funcs := func(f *check.Func) (syntax.Node, string) { return f.Decl.Name, f.Name }
		types := func(tn *check.TypeName) (syntax.Node, string) { return tn.Decl.Name, tn.Name }
		tests := func(x *check.Test) (syntax.Node, string) { return x.Decl.Name, x.Name }
		laws := func(l *check.Law) (syntax.Node, string) { return l.Decl.Name, l.Name }
		out[p.Path+" helper Funcs"] = declKeys(u, rawhelper.Funcs(p), funcs)
		out[p.Path+" helper Types"] = declKeys(u, rawhelper.Types(p), types)
		out[p.Path+" helper Tests"] = declKeys(u, rawhelper.Tests(p), tests)
		out[p.Path+" helper Laws"] = declKeys(u, rawhelper.Laws(p), laws)
		out[p.Path+" reflect Funcs"] = declKeys(u, field(p, "Funcs").([]*check.Func), funcs)
		out[p.Path+" reflect Types"] = declKeys(u, field(p, "Types").([]*check.TypeName), types)
		out[p.Path+" reflect Tests"] = declKeys(u, field(p, "Tests").([]*check.Test), tests)
		out[p.Path+" reflect Laws"] = declKeys(u, field(p, "Laws").([]*check.Law), laws)
	}
	return out
}

// TestRawListEscapesAreCanonical: the lists that the round 5 escapes read
// (an external helper, reflection) are the same, element by element, for
// fresh checks of each generated program with its files and import lines
// in other orders. A Unit's own lists are the caller's and are not
// canonical; TestLoweringUnitStaysAtEntries and
// TestLoweringImportsNoReflection keep the lowering from reading them
// after its entry.
func TestRawListEscapesAreCanonical(t *testing.T) {
	for seed := int64(0); seed < 4; seed++ {
		g := generateProgram(seed)
		want := escapedLists(g.check(t, nil))
		r := rand.New(rand.NewSource(seed))
		for i := 0; i < 6; i++ {
			got := escapedLists(g.check(t, r))
			for key, w := range want {
				if !slices.Equal(w, got[key]) {
					t.Fatalf("seed %d, check %d: %s differ:\n%s\nwant:\n%s", seed, i, key, strings.Join(got[key], "\n"), strings.Join(w, "\n"))
				}
			}
		}
	}
}
