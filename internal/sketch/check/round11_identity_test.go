package check

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"math/rand/v2"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// structural is the structural identity the interned ids must agree with
// (the checker's identical before class E): a named type by its origin and
// identical type arguments, a row by its set, a signature by its parameters,
// results and rows, an interface by method names, a struct by field names
// and types, a union and a machine only by the object.
func structural(a, b Type) bool {
	if a == b {
		return true
	}
	switch a := a.(type) {
	case *Named:
		b, ok := b.(*Named)
		if !ok || a.origin() != b.origin() || len(a.TArgs) != len(b.TArgs) {
			return false
		}
		for i := range a.TArgs {
			if !structural(a.TArgs[i], b.TArgs[i]) {
				return false
			}
		}
		return true
	case *RowArg:
		b, ok := b.(*RowArg)
		return ok && structuralRow(a.Row, b.Row)
	case *RowVar:
		// A use as a row element: one variable, identical bindings.
		b, ok := b.(*RowVar)
		if !ok || a.root() != b.root() {
			return false
		}
		bind := func(v *RowVar, tp *TypeParam) Type {
			if x, ok := v.m[tp]; ok {
				return x
			}
			return tp
		}
		for _, tp := range a.root().Fn.Sig.TParams {
			if !structural(bind(a, tp), bind(b, tp)) {
				return false
			}
		}
		return true
	case *Slice:
		b, ok := b.(*Slice)
		return ok && structural(a.Elem, b.Elem)
	case *Map:
		b, ok := b.(*Map)
		return ok && structural(a.Key, b.Key) && structural(a.Elem, b.Elem)
	case *Pointer:
		b, ok := b.(*Pointer)
		return ok && structural(a.Elem, b.Elem)
	case *Signature:
		b, ok := b.(*Signature)
		if !ok || len(a.Params) != len(b.Params) || len(a.Results) != len(b.Results) || a.NonEscaping != b.NonEscaping {
			return false
		}
		for i := range a.Params {
			if !structural(a.Params[i].Type, b.Params[i].Type) {
				return false
			}
		}
		for i := range a.Results {
			if !structural(a.Results[i], b.Results[i]) {
				return false
			}
		}
		return structuralRow(a.Errors, b.Errors) && structuralRow(a.Uses, b.Uses)
	case *Interface:
		b, ok := b.(*Interface)
		if !ok || a.Set != b.Set || len(a.Methods) != len(b.Methods) {
			return false
		}
		for _, m := range a.Methods {
			o := b.method(m.Name)
			if o == nil || !structural(m.Sig, o.Sig) {
				return false
			}
		}
		return true
	case *Struct:
		b, ok := b.(*Struct)
		if !ok || len(a.Fields) != len(b.Fields) {
			return false
		}
		for i := range a.Fields {
			if a.Fields[i].Name != b.Fields[i].Name || !structural(a.Fields[i].Type, b.Fields[i].Type) {
				return false
			}
		}
		return true
	case *Tuple:
		b, ok := b.(*Tuple)
		if !ok || len(a.Types) != len(b.Types) {
			return false
		}
		for i := range a.Types {
			if !structural(a.Types[i], b.Types[i]) {
				return false
			}
		}
		return true
	}
	return false
}

// structuralRow compares two rows as sets, element by element, a row
// variable read as its solution with its use's substitution (class F).
func structuralRow(a, b *Row) bool {
	flat := func(r *Row) []Type {
		var out []Type
		for _, e := range r.Elems() {
			if v, ok := e.(*RowVar); ok {
				out = append(out, v.Solution().Elems()...)
				continue
			}
			out = append(out, e)
		}
		return out
	}
	in := func(xs []Type, t Type) bool {
		for _, e := range xs {
			if structural(e, t) {
				return true
			}
		}
		return false
	}
	fa, fb := flat(a), flat(b)
	for _, e := range fa {
		if !in(fb, e) {
			return false
		}
	}
	for _, e := range fb {
		if !in(fa, e) {
			return false
		}
	}
	return true
}

// collectTypes gathers every type the check recorded and their subterms.
func collectTypes(c *checker) []Type {
	seen := map[Type]bool{}
	var out []Type
	var walk func(t Type)
	walk = func(t Type) {
		if t == nil || seen[t] {
			return
		}
		seen[t] = true
		out = append(out, t)
		switch t := t.(type) {
		case *Named:
			for _, a := range t.TArgs {
				walk(a)
			}
		case *RowArg:
			for _, e := range t.Row.Elems() {
				walk(e)
			}
		case *Slice:
			walk(t.Elem)
		case *Map:
			walk(t.Key)
			walk(t.Elem)
		case *Pointer:
			walk(t.Elem)
		case *Tuple:
			for _, x := range t.Types {
				walk(x)
			}
		case *Struct:
			for _, f := range t.Fields {
				walk(f.Type)
			}
		case *Interface:
			for _, m := range t.Methods {
				walk(m.Sig)
			}
		case *Signature:
			for _, p := range t.Params {
				walk(p.Type)
			}
			for _, r := range t.Results {
				walk(r)
			}
			for _, e := range t.Errors.Elems() {
				walk(e)
			}
			for _, e := range t.Uses.Elems() {
				walk(e)
			}
		}
	}
	for _, t := range c.pkg.Info.Types {
		walk(t)
	}
	for _, s := range c.pkg.Info.Literals {
		walk(s)
	}
	for _, o := range c.pkg.Info.Uses {
		if f, ok := o.(*Func); ok && f.Sig != nil {
			walk(f.Sig)
		}
	}
	for _, inst := range c.instance {
		walk(inst)
		walk(under(inst))
	}
	return out
}

// identitySource is decided source (#196). The decided language has no
// anonymous struct or interface types, fixed arrays or channels, so the
// permuted-field, permuted-method and channel-direction parameters (c, d
// and the arrays of e) are gone; the fixtures cover named types.
const identitySource = `type Fault {}
type Other {}
type G[T] { v T }
type Pair[A, B] {
	a A,
	b B,
}
type Box[T] { f func(T) fails Fault | Other }
type Shape = V[int] | V[string]
type V[T] {}
type Store { Get(k string) int fails Fault }
type Log { Put(s string) }

func a(x G[int], y G[string], z G[int], p Pair[int, G[int]], q Pair[G[int], int])                 {}
func b(f func(int) fails Fault | Other, g func(int) fails Other | Fault, h func(int) fails Fault) {}
func e(m Map[string, []G[int]], n Map[string, []G[int]])                                          {}
func f(w Box[int], v Box[int], u Box[string])                                                     {}
func g(x Shape) int {
	match x {
	case V[int]:
		return 1
	case _:
		return 0
	}
}
func h() needs Store, Log {}
func k() needs Log, Store {}
func l(fn func() needs Store, gn func() needs Store, hn func() needs Log) {}
`

// TestIdentityMatchesStructure: equal interned ids exactly when the types
// are structurally identical, over every type and subterm of a source with
// generic instances, rows in both orders, permuted interface methods and
// struct fields, channel directions and capability rows, and of every
// fixture.
func TestIdentityMatchesStructure(t *testing.T) {
	sources := map[string]string{"identity": identitySource}
	for _, dir := range []string{"positive", "spec", "negative", "../classf"} {
		files, _ := filepath.Glob(filepath.Join("testdata", "fixtures", dir, "*.ergo"))
		for _, file := range files {
			data, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			sources[dir+"/"+filepath.Base(file)] = string(data)
		}
	}
	names := make([]string, 0, len(sources))
	for name := range sources {
		names = append(names, name)
	}
	sort.Strings(names)
	pairs, same := 0, 0
	for _, name := range names {
		c, diags := checkFor(sources[name])
		if c == nil {
			t.Fatalf("%s: %v", name, diags)
		}
		if name == "identity" && len(diags) > 0 {
			t.Fatalf("identity source: %v", diags)
		}
		if len(c.widened) > 0 {
			t.Errorf("%s: a reporting pass computed a row wider than the settled one for %v", name, c.widened)
		}
		types := collectTypes(c)
		if len(types) > 400 {
			types = types[:400]
		}
		for i, x := range types {
			for _, y := range types[i:] {
				pairs++
				want, have := structural(x, y), c.identical(x, y)
				if want {
					same++
				}
				if want != have {
					t.Errorf("%s: %s and %s: structural %v, interned ids %v", name, x, y, want, have)
				}
			}
		}
	}
	t.Logf("%d sources, %d pairs, %d identical", len(names), pairs, same)
	if same == pairs {
		t.Fatal("no distinct pair compared")
	}
}

// genericInferred writes generic callees whose rows are inferred, each used
// three ways (round 12 M1): a concrete call, an explicit instantiation and a
// saved instantiated value (a capability-row parameter is implicit, so it
// takes a call and a saved function value instead). Callee i passes on a row parameter's failure,
// fails with an error type over its type parameter, or charges a
// capability-row parameter; every caller writes the row it must get.
func genericInferred(seed uint64) string {
	rng := rand.New(rand.NewPCG(seed, seed^0x9e37))
	var b strings.Builder
	b.WriteString("type Bang {}\ntype Fault[T] { V T }\ntype Cap { M() }\n")
	// fail and work are function values below: ordinary functions, since
	// an extern is call-only (check-v1 stage E4, P3).
	b.WriteString("func fail() fails Bang { return Bang }\nfunc work() needs Cap { Cap.M() }\nextern go fixture.mark func mark[T]() fails Fault[T]\n")
	types := []string{"int", "string", "Bang", "[]int"}
	for i := range 6 {
		ty := types[rng.IntN(len(types))]
		switch rng.IntN(3) {
		case 0:
			fmt.Fprintf(&b, "func pass%d[E](f func() fails E) { return f() }\n", i)
			fmt.Fprintf(&b, "func C%d() fails Bang { return pass%d(fail) }\n", i, i)
			fmt.Fprintf(&b, "func X%d() fails Bang { return pass%d[Bang](fail) }\n", i, i)
			fmt.Fprintf(&b, "let saved%d = pass%d[Bang]\nfunc S%d() fails Bang { return saved%d(fail) }\n", i, i, i, i)
		case 1:
			fmt.Fprintf(&b, "func raise%d[T](x T) { return Fault{x} }\n", i)
			fmt.Fprintf(&b, "func C%d(x %s) fails Fault[%s] { return raise%d(x) }\n", i, ty, ty, i)
			fmt.Fprintf(&b, "func X%d(x %s) fails Fault[%s] { return raise%d[%s](x) }\n", i, ty, ty, i, ty)
			fmt.Fprintf(&b, "let saved%d = raise%d[%s]\nfunc S%d(x %s) fails Fault[%s] { return saved%d(x) }\n", i, i, ty, i, ty, ty, i)
		default:
			fmt.Fprintf(&b, "func run%d[R](f func() needs R) { f() }\n", i)
			fmt.Fprintf(&b, "func C%d() needs Cap { run%d(work) }\n", i, i)
			// An implicit capability-row parameter takes no explicit argument:
			// the saved form is a function value passed on.
			fmt.Fprintf(&b, "func S%d() needs Cap { let g = work; run%d(g) }\n", i, i)
		}
	}
	return decided(b.String())
}

// TestIdentityGenericInferred: the generic-callee family is accepted, and
// the interned ids agree with the structural oracle on every recorded type,
// the variable uses read with their substitutions.
func TestIdentityGenericInferred(t *testing.T) {
	pairs := 0
	for seed := range uint64(40) {
		src := genericInferred(seed)
		c, diags := checkFor(src)
		if c == nil || len(diags) > 0 {
			t.Fatalf("seed %d: %v\n%s", seed, diags, src)
		}
		if len(c.widened) > 0 {
			t.Errorf("seed %d: wider reporting rows for %v", seed, c.widened)
		}
		types := collectTypes(c)
		for i, x := range types {
			for _, y := range types[i:] {
				pairs++
				if want, have := structural(x, y), c.identical(x, y); want != have {
					t.Errorf("seed %d: %s and %s: structural %v, interned ids %v", seed, x, y, want, have)
				}
			}
		}
	}
	t.Logf("40 sources, %d pairs", pairs)
}

// TestTypeStringOnlyPrints: typeString builds a whole type's text, so it is
// for printing only (class E): no comparison, cache or index of the checker
// may key on it. A call outside the printing functions below, or any use of
// typeString as a value, fails.
func TestTypeStringOnlyPrints(t *testing.T) {
	allowed := map[string]bool{
		"typeString": true, // its own recursion
		"Row.list":   true, // the print order of a row's elements
	}
	files, _ := filepath.Glob("*.go")
	fset := token.NewFileSet()
	calls := 0
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, file, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range f.Decls {
			name := "package scope"
			if fd, ok := decl.(*ast.FuncDecl); ok {
				name = funcName(fd)
			}
			callee := map[*ast.Ident]bool{}
			ast.Inspect(decl, func(n ast.Node) bool {
				if call, ok := n.(*ast.CallExpr); ok {
					if id, ok := call.Fun.(*ast.Ident); ok && id.Name == "typeString" {
						callee[id] = true
					}
				}
				return true
			})
			ast.Inspect(decl, func(n ast.Node) bool {
				id, ok := n.(*ast.Ident)
				if !ok || id.Name != "typeString" {
					return true
				}
				if fd, ok := decl.(*ast.FuncDecl); ok && fd.Name == id {
					return true // the declaration itself
				}
				switch {
				case !callee[id]:
					t.Errorf("%s: %s uses typeString as a value", fset.Position(id.Pos()), name)
				case !allowed[name]:
					t.Errorf("%s: %s calls typeString; compare, cache and index by interned id (intern.go)", fset.Position(id.Pos()), name)
				default:
					calls++
				}
				return true
			})
		}
	}
	if calls == 0 {
		t.Fatal("no typeString call found: the scan reads the wrong files")
	}
}

// TestRowVariablesAfterInference: interned types never change (class F).
// Initializer discovery meets f before its row is inferred, so the old
// values hold f's row variable: Box[func() ρ_f] and a row
// error[Fault[func() ρ_f]]. Once f's component is final, they are revisited:
// each has the id of the same type written with f's solved row, and none
// has the id of the type written with an empty row.
func TestRowVariablesAfterInference(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("testdata", "classf", "cache-witness.ergo"))
	if err != nil {
		t.Fatal(err)
	}
	c, _ := checkFor(string(src))
	tn := func(name string) Type { return c.pkg.Scope.names[name].(*TypeName).Type }
	old := c.pkg.Scope.names["old"].(*Var).Type
	box, bang := tn("Box").(*Named), tn("Bang")
	fails := c.instantiate(box, []Type{&Signature{Errors: c.types.row(bang), Uses: newRow()}})
	empty := c.instantiate(box, []Type{&Signature{Errors: newRow(), Uses: newRow()}})
	if !c.identical(old, fails) || !structural(old, fails) {
		t.Errorf("old value %s: not identical to Box[func() error[Bang]]", old)
	}
	if c.identical(old, empty) || structural(old, empty) {
		t.Errorf("old value %s: identical to Box[func()]", old)
	}
	if c.instantiate(box, []Type{&Signature{Errors: newRow(), Uses: newRow()}}) != empty {
		t.Error("the instance cache does not return Box[func()] for its own key")
	}

	src, err = os.ReadFile(filepath.Join("testdata", "classf", "row-gap-witness.ergo"))
	if err != nil {
		t.Fatal(err)
	}
	c, diags := checkFor(string(src))
	if len(diags) > 0 {
		t.Fatalf("row-gap witness: %v", diags)
	}
	tn = func(name string) Type { return c.pkg.Scope.names[name].(*TypeName).Type }
	held := c.pkg.Scope.names["old"].(*Var).Type.(*Signature)
	fault, bang := tn("Fault").(*Named), tn("Bang")
	solved := c.instantiate(fault, []Type{&Signature{Errors: c.types.row(bang), Uses: newRow()}})
	want := &Signature{Errors: c.types.row(solved), Uses: newRow()}
	if !c.identical(held, want) || !structural(held, want) {
		t.Errorf("old row %s: not identical to %s", held, want)
	}
	if !c.types.row(solved).equal(c.types, held.Errors) {
		t.Errorf("old row %s: not equal to error[%s]", held.Errors, solved)
	}
}

// funcName is Recv.Name for a method, Name for a function.
func funcName(fd *ast.FuncDecl) string {
	if fd.Recv == nil || len(fd.Recv.List) == 0 {
		return fd.Name.Name
	}
	recv := fd.Recv.List[0].Type
	if star, ok := recv.(*ast.StarExpr); ok {
		recv = star.X
	}
	if id, ok := recv.(*ast.Ident); ok {
		return id.Name + "." + fd.Name.Name
	}
	return fd.Name.Name
}
