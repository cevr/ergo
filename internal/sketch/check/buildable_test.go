package check_test

import (
	"fmt"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// buildLib is the base of every buildShape: a record with an unexported
// field (Auth), an open record (Open) and a constructor of Auth.
const buildLib = `type Auth { root string }
type Open { N int }
func NewAuth() Auth { Auth{root: "/"} }
`

// buildShape is a type built from a base of buildLib by a word of
// constructors, with the written model of whether another package builds
// a value of it with no authority (fu-constructible). Each text is a
// function of the qualifier of lib ("" inside lib, "lib." in main).
type buildShape struct {
	name  string
	decls string // lib's declarations, after buildLib
	typ   func(q string) string
	// model: the written rule, by constructor (TestBuildableShapes).
	model bool
	// wit is a value of the shape that main builds with no function of
	// lib, when model holds; witDecls are main's declarations it needs.
	wit      func(q string) string
	witDecls func(q string) string
	union    bool // the outermost constructor is a union: no defined type over it
	fn       bool // the outermost constructor is a function type: no record field holds it
}

// buildShapes is every word of at most depth constructors over int, Open
// and Auth. The model, constructor by constructor:
//   - S []X, M Map[string, X]: built (an empty literal holds no X);
//   - F func() X: built (main declares a function that never returns);
//   - T (X, int), D type W X, H type W { X X }, I W[X] of type W[T] { X T }:
//     built when X is;
//   - P type W { x X, N int }: never (an unexported field);
//   - U W = A | B with A { X X } and B { root string }: built when X is;
//   - V W = A | B with A { X X } and B {}: built (by B).
func buildShapes(depth int) []buildShape {
	bases := []buildShape{
		{name: "int", typ: str("int"), model: true, wit: str("0")},
		{name: "Open", typ: func(q string) string { return q + "Open" }, model: true, wit: func(q string) string { return q + "Open{N: 1}" }},
		{name: "Auth", typ: func(q string) string { return q + "Auth" }},
	}
	var out []buildShape
	var grow func(s buildShape, n int)
	grow = func(s buildShape, n int) {
		if s.witDecls == nil {
			s.witDecls = str("")
		}
		out = append(out, s)
		if n == depth {
			return
		}
		w := fmt.Sprintf("W%d", n)
		in := s
		x := in.typ
		var words []buildShape
		words = append(words,
			buildShape{name: "S" + in.name, decls: in.decls, typ: func(q string) string { return "[]" + x(q) }, model: true,
				wit: func(q string) string { return "[]" + x(q) + "{}" }},
			buildShape{name: "M" + in.name, decls: in.decls, typ: func(q string) string { return "Map[string, " + x(q) + "]" }, model: true,
				wit: func(q string) string { return "Map[string, " + x(q) + "]{}" }},
			buildShape{name: "F" + in.name, decls: in.decls, typ: func(q string) string { return "func() " + x(q) }, model: true, fn: true,
				wit: func(q string) string { return "func() " + x(q) + " { never" + w + "() }" },
				witDecls: func(q string) string {
					return in.witDecls(q) + "func never" + w + "() " + x(q) + " { never" + w + "() }\n"
				}},
			// A tuple is declared: a field written X (A, B) is an operation.
			buildShape{name: "T" + in.name, decls: in.decls + "type " + w + " (" + x("") + ", int)\n", typ: func(q string) string { return q + w }, model: in.model,
				wit: func(q string) string { return "(" + in.wit(q) + ", 0)" }},
			buildShape{name: "I" + in.name, decls: in.decls + "type " + w + "[T] { X T }\n", typ: func(q string) string { return q + w + "[" + x(q) + "]" }, model: in.model,
				wit: func(q string) string { return q + w + "[" + x(q) + "]{X: " + in.wit(q) + "}" }},
		)
		// A record field holds no function type (S108 "a record holds
		// data"), so H, P, U and V take no F.
		if !in.fn {
			words = append(words,
				buildShape{name: "H" + in.name, decls: in.decls + "type " + w + " { X " + x("") + " }\n", typ: func(q string) string { return q + w }, model: in.model,
					wit: func(q string) string { return q + w + "{X: " + in.wit(q) + "}" }},
				buildShape{name: "P" + in.name, decls: in.decls + "type " + w + " { x " + x("") + ", N int }\n", typ: func(q string) string { return q + w }},
				buildShape{name: "U" + in.name, decls: in.decls + "type " + w + " = " + w + "A | " + w + "B\ntype " + w + "A { X " + x("") + " }\ntype " + w + "B { root string }\n",
					typ: func(q string) string { return q + w }, model: in.model, union: true,
					wit: func(q string) string { return q + w + "A{X: " + in.wit(q) + "}" }},
				buildShape{name: "V" + in.name, decls: in.decls + "type " + w + " = " + w + "A | " + w + "B\ntype " + w + "A { X " + x("") + " }\ntype " + w + "B {}\n",
					typ: func(q string) string { return q + w }, model: true, union: true,
					wit: func(q string) string { return q + w + "B" }},
				// A sealed operation type whose one implementer holds X.
				buildShape{name: "O" + in.name, decls: in.decls + "type " + w + " { inner() int }\ntype " + w + "I { X " + x("") + " }\nfunc inner(_ " + w + "I) int { 1 }\n",
					typ: func(q string) string { return q + w }, model: in.model, union: true,
					wit: func(q string) string { return q + w + "I{X: " + in.wit(q) + "}" }},
			)
		}
		// An open operation type: main implements it.
		words = append(words, buildShape{name: "Q" + in.name, decls: in.decls + "type " + w + " { Outer() " + x("") + " }\n",
			typ: func(q string) string { return q + w }, model: true, union: true,
			wit: func(q string) string { return "Mine" + w },
			witDecls: func(q string) string {
				return in.witDecls(q) + "type Mine" + w + " {}\nfunc Outer(_ Mine" + w + ") " + x(q) + " { never" + w + "() }\nfunc never" + w + "() " + x(q) + " { never" + w + "() }\n"
			}})
		if !in.union {
			words = append(words, buildShape{name: "D" + in.name, decls: in.decls + "type " + w + " " + x("") + "\n", typ: func(q string) string { return q + w }, model: in.model,
				wit: func(q string) string { return q + w + "(" + in.wit(q) + ")" }})
		}
		for _, word := range words {
			if word.witDecls == nil {
				word.witDecls = in.witDecls
			}
			grow(word, n+1)
		}
	}
	for _, b := range bases {
		grow(b, 0)
	}
	return out
}

// buildableIn checks decls as package lib and main as package main, which
// imports lib, and returns main's package and diagnostics.
func buildableIn(t *testing.T, decls, main string) (*check.Package, []check.Diagnostic) {
	t.Helper()
	f, err := syntax.Parse(buildLib + decls)
	if err != nil {
		t.Fatalf("parse lib: %v\n%s", err, decls)
	}
	lib, ds := check.Check("lib", []*syntax.File{f}, nil)
	if len(ds) != 0 {
		t.Fatalf("lib: %v\n%s", ds, buildLib+decls)
	}
	head := ""
	if strings.Contains(main, "lib.") {
		head = "import lib\n" // a shape over int alone names no lib type
	}
	f, err = syntax.Parse(head + main)
	if err != nil {
		t.Fatalf("parse main: %v\n%s", err, main)
	}
	return check.Check("main", []*syntax.File{f}, libImporter{lib})
}

// probeType is the type of the parameter of main's function probe.
func probeType(t *testing.T, pkg *check.Package) check.Type {
	t.Helper()
	for _, f := range pkg.Funcs {
		if f.Name == "probe" {
			return f.Sig.Params[0].Type
		}
	}
	t.Fatal("no probe function in main")
	return nil
}

// TestBuildableShapes is fu-constructible's guard: for every generated
// shape, check.Buildable from main equals the written model of
// buildShapes; when the model says built, main builds the witness with no
// function of lib and no diagnostic; when it says not built, main's zero
// value of the shape is refused (a zero value is a way to build).
func TestBuildableShapes(t *testing.T) {
	shapes := buildShapes(2)
	// Per base: itself, 12 words of one constructor, and their children:
	// 12 each, but 11 under a union or an operation type (U, V, O, Q: no
	// D) and 7 under F (no H, P, U, V, O).
	if want := 3 * (1 + 12 + 7*12 + 4*11 + 7); len(shapes) != want {
		t.Fatalf("generated %d shapes, want %d", len(shapes), want)
	}
	built := 0
	for _, s := range shapes {
		t.Run(s.name, func(t *testing.T) {
			src := s.witDecls("lib.") + "func probe(_ " + s.typ("lib.") + ") {}\n"
			pkg, ds := buildableIn(t, s.decls, src)
			if len(ds) != 0 {
				t.Fatalf("probe: %v\nlib:\n%smain:\n%s", ds, s.decls, src)
			}
			if got := check.Buildable(probeType(t, pkg), pkg); got != s.model {
				t.Errorf("Buildable = %v, the model says %v\nlib:\n%s", got, s.model, s.decls)
			}
			if s.model {
				src := s.witDecls("lib.") + "func use() { let v " + s.typ("lib.") + " = " + s.wit("lib.") + "\n let _ = v }\n"
				if _, ds := buildableIn(t, s.decls, src); len(ds) != 0 {
					t.Errorf("the witness does not check: %v\nlib:\n%smain:\n%s", ds, s.decls, src)
				}
			} else {
				src := "func use() { let v " + s.typ("lib.") + "\n let _ = v }\n"
				if _, ds := buildableIn(t, s.decls, src); len(ds) == 0 {
					t.Errorf("the model says not built, but main makes its zero value\nlib:\n%smain:\n%s", s.decls, src)
				}
			}
		})
		if s.model {
			built++
		}
	}
	t.Logf("%d shapes, %d built from main", len(shapes), built)
	if built == 0 || built == len(shapes) {
		t.Fatalf("built %d of %d: the table does not reach both answers", built, len(shapes))
	}
}

// TestBuildableRecursive: the fact is the least fixed point, so a cycle
// with no way out is not built, and a cycle with one is.
func TestBuildableRecursive(t *testing.T) {
	for _, c := range []struct {
		name, decls string
		want        bool
	}{
		{"cycle-through-authority", "type R = RA | RB\ntype RA { X R }\ntype RB { root string }\n", false},
		{"cycle-with-open-variant", "type R = RA | RB\ntype RA { X R }\ntype RB {}\n", true},
		{"mutual-no-exit", "type R = RA | RB\ntype RA { X S }\ntype RB { root string }\ntype S = SA | SB\ntype SA { X R }\ntype SB { X Auth }\n", false},
		{"mutual-exit-far", "type R = RA | RB\ntype RA { X S }\ntype RB { root string }\ntype S = SA | SB\ntype SA { X R }\ntype SB { X []Auth }\n", true},
		{"list-of-authority", "type L = Nil | Cons\ntype Nil {}\ntype Cons { Head Auth, Tail L }\n", true},
		{"record-over-authority-list", "type L = Nil | Cons\ntype Nil { root string }\ntype Cons { Head Open, Tail L }\ntype R { X L }\n", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			typ := "lib.R"
			if strings.Contains(c.name, "list-of") {
				typ = "lib.L"
			}
			pkg, ds := buildableIn(t, c.decls, "func probe(_ "+typ+") {}\n")
			if len(ds) != 0 {
				t.Fatalf("probe: %v", ds)
			}
			if got := check.Buildable(probeType(t, pkg), pkg); got != c.want {
				t.Errorf("Buildable = %v, want %v\n%s", got, c.want, c.decls)
			}
		})
	}
}

// TestBuildableSealed (fu-hostguard2): a sealed operation type of lib is
// built from main when one of lib's types that implement it is; an open
// one always (main implements it).
func TestBuildableSealed(t *testing.T) {
	for _, c := range []struct {
		name, decls string
		want        bool
	}{
		{"no-implementer", "type R { inner() int }\n", false},
		{"implementer-with-authority", "type R { inner() int }\ntype A { root string }\nfunc inner(_ A) int { 1 }\n", false},
		{"open-implementer", "type R { inner() int }\ntype A { N int }\nfunc inner(_ A) int { 1 }\n", true},
		{"borrow-implementer", "type R { inner() int }\ntype A {}\nfunc inner(_ *A) int { 1 }\n", true},
		{"one-of-two-open", "type R { inner() int, Outer() int }\ntype A { root string }\ntype B { N int }\nfunc inner(_ A) int { 1 }\nfunc Outer(_ A) int { 2 }\nfunc inner(_ B) int { 1 }\nfunc Outer(_ B) int { 2 }\n", true},
		{"open-type-lacks-an-operation", "type R { inner() int, Outer() int }\ntype A { root string }\ntype B { N int }\nfunc inner(_ A) int { 1 }\nfunc Outer(_ A) int { 2 }\nfunc inner(_ B) int { 1 }\n", false},
		{"generic-implementer", "type R { inner() int }\ntype G[T] { X T }\nfunc inner[T](_ G[T]) int { 1 }\n", true},
		{"union-implementer-with-authority", "type R { inner() int }\ntype U = A | B\ntype A { root string }\ntype B { x int }\nfunc inner(_ U) int { 1 }\n", false},
		{"open", "type R { Outer() int }\n", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			pkg, ds := buildableIn(t, c.decls, "func probe(_ lib.R) {}\n")
			if len(ds) != 0 {
				t.Fatalf("probe: %v", ds)
			}
			if got := check.Buildable(probeType(t, pkg), pkg); got != c.want {
				t.Errorf("Buildable = %v, want %v\n%s", got, c.want, c.decls)
			}
		})
	}
}

// TestBuildableBorrow: a borrow *T (a parameter type) is built when T is.
func TestBuildableBorrow(t *testing.T) {
	for typ, want := range map[string]bool{"lib.Auth": false, "lib.Open": true, "[]lib.Auth": true} {
		pkg, ds := buildableIn(t, "", "func probe(_ *"+typ+") {}\n")
		if len(ds) != 0 {
			t.Fatalf("%s: %v", typ, ds)
		}
		if got := check.Buildable(probeType(t, pkg), pkg); got != want {
			t.Errorf("Buildable(*%s) = %v, want %v", typ, got, want)
		}
	}
}

// TestBuildableInOwnPackage: authority binds only other packages.
func TestBuildableInOwnPackage(t *testing.T) {
	f, err := syntax.Parse(buildLib + "type H { x Auth }\nfunc probe(_ H) {}\n")
	if err != nil {
		t.Fatal(err)
	}
	lib, ds := check.Check("lib", []*syntax.File{f}, nil)
	if len(ds) != 0 {
		t.Fatal(ds)
	}
	if !check.Buildable(probeType(t, lib), lib) {
		t.Error("lib's own record with an unexported field is not Buildable in lib")
	}
}
