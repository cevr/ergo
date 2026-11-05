package lower

import (
	"fmt"
	"go/ast"
	"go/types"
	"math/rand"
	"path/filepath"
	"strings"
	"testing"
)

// Round 4 (196b): the lowering reads the unit's and the checker's lists
// only through the canonical unit (canon.go), and a text function's
// identity is the Go type it takes.

// rawLists are the list fields whose order is how a caller or the checker
// listed the program: by the package and type that declare them.
var rawLists = map[string]map[string]bool{
	"ergo.local/prototype/internal/sketch/check.Package": {"Funcs": true, "Types": true, "Tests": true, "Laws": true},
	"ergo.local/prototype/internal/sketch/check.Info":    {"InitOrder": true},
	"ergo.local/prototype/internal/sketch/lower.Unit":    {"Files": true, "Sources": true, "Imports": true},
}

// TestLoweringReadsCanonicalLists resolves every field selection of the
// package's production files through go/types: a raw list is read only in
// canon.go, which builds the canonical unit, and in lower.go, whose
// Validate checks the Unit before it. So no lowering site can read a list
// in discovery order, under any spelling (an alias, a helper, a local
// copy all select the field somewhere).
func TestLoweringReadsCanonicalLists(t *testing.T) {
	src := typedPackage(t, ".")
	allowed := map[string]bool{"canon.go": true, "lower.go": true}
	var found []string
	for _, f := range src.files {
		file := filepath.Base(src.fset.Position(f.Pos()).Filename)
		ast.Inspect(f, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			field, ok := src.info.Uses[sel.Sel].(*types.Var)
			if !ok || !field.IsField() {
				return true
			}
			tv, ok := src.info.Types[sel.X]
			if !ok {
				return true
			}
			typ := tv.Type
			if p, ok := typ.(*types.Pointer); ok {
				typ = p.Elem()
			}
			named, ok := typ.(*types.Named)
			if !ok || named.Obj().Pkg() == nil {
				return true
			}
			owner := named.Obj().Pkg().Path() + "." + named.Obj().Name()
			if rawLists[owner][field.Name()] && !allowed[file] {
				found = append(found, fmt.Sprintf("%s: %s.%s", src.fset.Position(sel.Pos()), named.Obj().Name(), field.Name()))
			}
			return true
		})
	}
	if len(found) > 0 {
		t.Fatalf("raw list reads outside canon.go (read the canonical unit instead):\n%s", strings.Join(found, "\n"))
	}
}

// TestGeneratedOrderIsTotal: the naming sort accepts one entity met twice
// and refuses, as a compiler defect, two entities under one sort string,
// whose order would be partial.
func TestGeneratedOrderIsTotal(t *testing.T) {
	one := generatedEntry{pkgKey{"text", "named main.Hold@17"}, "text main.Hold@17", "textHold"}
	if got := sortGenerated([]generatedEntry{one, one}); len(got) != 2 {
		t.Fatalf("one entity met twice: %v", got)
	}
	for _, other := range []generatedEntry{
		{pkgKey{"text", "instance main.Hold@17"}, one.sort, one.base},
		{one.key, one.sort, "textHold2"},
	} {
		func() {
			defer func() {
				r, ok := recover().(refusal)
				if !ok || !strings.Contains(r.msg, "share the sort key") {
					t.Fatalf("two entities under one sort string: recovered %#v", r)
				}
			}()
			sortGenerated([]generatedEntry{one, other})
		}()
	}
}

// verifyCanonical requires every lowering of g's program, the same checked
// unit listed in other orders and fresh checks of its files in other
// orders, to emit the same files.
func verifyCanonical(t *testing.T, g genProgram) {
	t.Helper()
	u := g.check(t, nil)
	want := emitted(t, "probe", u)
	r := rand.New(rand.NewSource(99091))
	for i := 0; i < 30; i++ {
		sameEmission(t, fmt.Sprintf("lower %d", i), want, emitted(t, "probe", permuteUnit(r, u)))
		sameEmission(t, fmt.Sprintf("check %d", i), want, emitted(t, "probe", g.check(t, r)))
	}
}

// The round 3 verify's probe (M2): tests of two files run in canonical
// order, however the checker met the files.
func TestVerifyR3TestsAcrossFiles(t *testing.T) {
	verifyCanonical(t, genProgram{root: []pkgSource{
		{"/probe/a.ergo", "test A { assert 1 == 1 }\n"},
		{"/probe/z.ergo", "test Z { assert 2 == 2 }\nfunc main() {}\n"},
	}})
}

// The round 3 verify's r3-5 case: generic overloads in two files lower as
// package functions, named in canonical order.
func TestVerifyR3GenericOverloads(t *testing.T) {
	g := genProgram{libs: []genLib{{"x/lib", []pkgSource{
		{"/probe/lib/a.ergo", "type Q {}\nfunc Show[T](q Q, x T) T { x }\n"},
		{"/probe/lib/b.ergo", "type R {}\nfunc Show[T](r R, x T) T { x }\n"},
	}}}, root: []pkgSource{{"/probe/main.ergo", `import x/lib
func main() needs Console { Console.Println("\(lib.Q.Show(1)) \(lib.R.Show(2))") }
`}}}
	verifyCanonical(t, g)
	p := mustLowerUnit(t, "overloads", g.check(t, nil), false)
	buildAndRun(t, []*lowered{p}, map[string]string{"overloads": "1 2\n"})
}

// The round 3 verify's probe (M1): a type generic over a failure row only
// and its instance in a record's field erase to one Go type, which has one
// text function, in both builds.
func TestVerifyR3ErasedRows(t *testing.T) {
	u := checkPackage(t, "main", []pkgSource{{"/probe/rows.ergo", `type Bad {}
type Hold[E] { F func() fails E }
type Outer { V Hold[Bad] }
func bad() fails Bad { Bad }
func first() fails Hold[Bad] { Hold[Bad]{F: bad} }
func second() fails Outer { Outer{V: Hold[Bad]{F: bad}} }
func main() fails Outer { Outer{V: Hold[Bad]{F: bad}} }
test First { try first() }
test Second { try second() }
`}}, nil)
	for _, tests := range []bool{false, true} {
		t.Run(fmt.Sprint(tests), func(t *testing.T) {
			p := mustLowerUnit(t, "rows", u, tests)
			if n := strings.Count(p.mainGo(), "func textHold("); n != 1 {
				t.Fatalf("%d text functions of Hold, want 1", n)
			}
			exes := buildAll(t, []*lowered{p}, true)
			if exes == nil {
				t.Fatal("build failed")
			}
			out, stderr, code := runExe(t, exes[p.name])
			if code != 1 || !strings.Contains(out+stderr, "{F:<func>}") {
				t.Fatalf("exit %d stdout %q stderr %q: want the derived text {F:<func>}", code, out, stderr)
			}
		})
	}
}
