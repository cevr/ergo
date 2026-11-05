package lower

import (
	"fmt"
	"go/ast"
	"go/types"
	"math/rand"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
)

// Round 4 (196b): the lowering reads the unit's and the checker's lists
// only through the canonical unit (canon.go), and a text function's
// identity is the Go type it takes.

// rawLists are the list fields whose order is how a caller or the checker
// listed the program, by the package path and the type that declare them.
var rawLists = map[string]map[string][]string{
	"ergo.local/prototype/internal/sketch/check": {"Package": {"Funcs", "Types", "Tests", "Laws"}, "Info": {"InitOrder"}},
	"ergo.local/prototype/internal/sketch/lower": {"Unit": {"Files", "Sources", "Imports"}},
}

// rawSets are the sets of the check result (check/canon.go, kind b): the
// fields and the method (Row.Elems) that enumerate one in the checker's
// intern order, by the type that declares them. Round 7.
var rawSets = map[string][]string{
	"Union":        {"Variants"},
	"Match":        {"Subjects"},
	"ContextEntry": {"Uses"},
	"Row":          {"Elems"},
}

// setAllowed are the reads of a set field that may stay, by "file:func
// object", each with its reason. A read of an unlisted one is reported, and
// an entry that matches no read is stale (TestLoweringReadsCanonicalLists).
var setAllowed = map[string]string{
	"decls.go:typeDecl Union.Variants":       "a declared union's variants, in declaration order (kind a)",
	"decls.go:optionVariants Union.Variants": "the prelude's declared Option: its length and membership by name",
	"literal.go:litTypes Union.Variants":     "a declared union (a Named's underlying), declaration order",
	"literal.go:litFunc Union.Variants":      "a declared union (a Named's underlying), declaration order",
	"tests.go:genNamed Union.Variants":       "a declared union (a Named's underlying), declaration order",
	"tests.go:genFunc Union.Variants":        "a declared union (a Named's underlying), declaration order",
	"text.go:textVariants Union.Variants":    "a declared union (a Named's underlying), declaration order",
	"text.go:instancesOf Union.Variants":     "a declared union's variants; the row value's go through CanonVariants",
	"expr.go:patternCatch Union.Variants":    "builds the row value from CanonCatchSubjects (a literal key, not a read)",
}

// rawListReads returns each use, in src, of a raw list field or a set
// outside the places that may read one: canon.go, which builds the
// canonical unit, Validate and validateFiles in lower.go, which check the
// Unit before it, and the entries of allowed, which it reports as used. A
// use is matched by the object itself (the *types.Var or *types.Func that
// go/types resolves the selector to), not by how the receiver's type is
// written. An alias, a defined type over the struct, an embedded struct (a
// promoted field) and a local copy type all resolve to the same object.
func rawListReads(t *testing.T, src *typedSource, allowed map[string]string, used map[string]bool) []string {
	t.Helper()
	raw := map[types.Object]string{}
	for _, pkg := range append([]*types.Package{src.pkg}, src.pkg.Imports()...) {
		path := pkg.Path()
		if pkg == src.pkg && path == "lower" {
			path = "ergo.local/prototype/internal/sketch/lower" // checked under its name
		}
		for typ, fields := range rawLists[path] {
			st := pkg.Scope().Lookup(typ).Type().Underlying().(*types.Struct)
			for i := 0; i < st.NumFields(); i++ {
				if slices.Contains(fields, st.Field(i).Name()) {
					raw[st.Field(i)] = typ + "." + st.Field(i).Name()
				}
			}
		}
		if path != "ergo.local/prototype/internal/sketch/check" {
			continue
		}
		for typ, names := range rawSets {
			for _, name := range names {
				obj, _, _ := types.LookupFieldOrMethod(types.NewPointer(pkg.Scope().Lookup(typ).Type()), false, pkg, name)
				if obj == nil {
					t.Fatalf("no %s.%s", typ, name)
				}
				raw[obj] = typ + "." + name
			}
		}
	}
	if want := 8 + len(rawSets); len(raw) != want {
		t.Fatalf("found %d of the %d raw list and set objects", len(raw), want)
	}
	var found []string
	for _, f := range src.files {
		file := filepath.Base(src.fset.Position(f.Pos()).Filename)
		for _, decl := range f.Decls {
			fn := ""
			if d, ok := decl.(*ast.FuncDecl); ok {
				fn = d.Name.Name
			}
			if file == "canon.go" || file == "lower.go" && (fn == "Validate" || fn == "validateFiles") {
				continue
			}
			ast.Inspect(decl, func(n ast.Node) bool {
				if id, ok := n.(*ast.Ident); ok {
					if name, ok := raw[src.info.Uses[id]]; ok {
						site := fmt.Sprintf("%s:%s %s", file, fn, name)
						if _, ok := allowed[site]; ok {
							used[site] = true
						} else {
							found = append(found, site)
						}
					}
				}
				return true
			})
		}
	}
	sort.Strings(found)
	return found
}

// TestLoweringReadsCanonicalLists: no code of package lower names a raw
// list field outside canon.go and Validate. That is the whole claim: it
// matches direct field selections in this package's files. A helper in
// another package, a conversion to a struct of the same fields,
// reflection and unsafe name no such field; the producer's order
// (check/canon.go), TestLoweringUnitStaysAtEntries and
// TestLoweringImportsNoReflection cover those routes (canon.go). The
// fixture testdata/rawreads holds the round 4 verify's escape probe and a
// read in lower.go outside Validate; the guard reports each of them and
// nothing else. Round 7: a direct read of a set (rawSets) is reported too,
// but for the reads setAllowed names with a reason; the fixture
// testdata/rawsets holds one read of each set object.
func TestLoweringReadsCanonicalLists(t *testing.T) {
	used := map[string]bool{}
	if found := rawListReads(t, typedPackage(t, "."), setAllowed, used); len(found) > 0 {
		t.Fatalf("raw list or set reads outside canon.go (read the canonical unit or view instead):\n%s", strings.Join(found, "\n"))
	}
	for site := range setAllowed {
		if !used[site] {
			t.Errorf("setAllowed lists %s, which reads no set: remove it", site)
		}
	}
	sets := []string{
		"probe.go:escCatch Match.Subjects",
		"probe.go:escEntry ContextEntry.Uses",
		"probe.go:escRow Row.Elems",
		"probe.go:escRowValue Union.Variants",
	}
	if got := rawListReads(t, typedPackage(t, "testdata/rawsets"), nil, nil); !slices.Equal(got, sets) {
		t.Fatalf("set fixture reads:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(sets, "\n"))
	}
	want := []string{
		"lower.go:escInLowerGo Unit.Sources",
		"probe.go:escDirect Package.Laws",
		"probe.go:escInitOrder Info.InitOrder",
		"probe.go:escPromoted Package.Tests",
		"probe.go:escViaAlias Package.Funcs",
		"probe.go:escViaDefined Package.Types",
		"probe.go:escViaMirror Unit.Files",
	}
	if got := rawListReads(t, typedPackage(t, "testdata/rawreads"), nil, nil); !slices.Equal(got, want) {
		t.Fatalf("fixture reads:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
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
