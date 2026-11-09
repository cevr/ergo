package check_test

import (
	"fmt"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// genShape is a type built from one of lib's records by a word of
// constructors (196b Q7, the generated shape algebra). Each text is a
// function of the qualifier of the package that declares the word's types
// ("" where they are declared, "mid." from an importer of it).
type genShape struct {
	name  string
	decls string // the declarations, written in their own package
	typ   func(q string) string
	value func(q string) string // a value made from lib's functions only
	lit   func(q string) string // a literal's fields that main may name
	upd   func(q string) string // a field an update may replace
	// own: every constructor of the word is a defined type, so a literal
	// of the shape builds lib's record itself; else the outermost record is
	// this package's and a literal or update copies lib's record into it.
	own bool
}

// genShapes is every word of at most depth constructors over lib.Q and
// lib.G[int]: a defined type over the shape (D), a record holding it with
// a field of its own (H) and an instance of a generic record (I).
func genShapes(depth int) []genShape {
	bases := []genShape{
		{name: "Q", typ: str("lib.Q"), value: str("lib.ParseQ(1)"), lit: str("N: 1"), upd: str("N: 2"), own: true},
		{name: "G", typ: str("lib.G[int]"), value: str("lib.NewG(1)"), lit: str("N: 1"), upd: str("N: 2"), own: true},
	}
	var out []genShape
	var grow func(s genShape, n int)
	grow = func(s genShape, n int) {
		out = append(out, s)
		if n == depth {
			return
		}
		w := fmt.Sprintf("W%d", n)
		in := s
		grow(genShape{
			name:  "D" + in.name,
			decls: in.decls + "type " + w + " " + in.typ("") + "\n",
			typ:   func(q string) string { return q + w },
			value: func(q string) string { return q + w + "(" + in.value(q) + ")" },
			lit:   in.lit,
			upd:   in.upd,
			own:   in.own,
		}, n+1)
		grow(genShape{
			name:  "H" + in.name,
			decls: in.decls + "type " + w + " { X " + in.typ("") + ", N int }\n",
			typ:   func(q string) string { return q + w },
			value: func(q string) string { return q + w + "{X: " + in.value(q) + ", N: 1}" },
			lit:   func(q string) string { return "X: " + in.value(q) + ", N: 1" },
			upd:   str("N: 2"),
		}, n+1)
		grow(genShape{
			name:  "I" + in.name,
			decls: in.decls + "type " + w + "[T] { X T }\n",
			typ:   func(q string) string { return q + w + "[" + in.typ(q) + "]" },
			value: func(q string) string { return q + w + "[" + in.typ(q) + "]{X: " + in.value(q) + "}" },
			lit:   func(q string) string { return "X: " + in.value(q) },
			upd:   func(q string) string { return "X: " + in.value(q) },
		}, n+1)
	}
	for _, b := range bases {
		grow(b, 0)
	}
	return out
}

func str(s string) func(string) string { return func(string) string { return s } }

// genSite makes a value of a shape at one kind of site. implicit: the site
// makes the shape's zero value, or the zero of lib's record inside it.
type genSite struct {
	name     string
	implicit bool
	explicit bool // a literal or an update of the shape's own record
	src      func(s genShape, q string) string
}

var genSites = []genSite{
	{name: "let-zero", implicit: true, src: func(s genShape, q string) string {
		return "func use() { let v " + s.typ(q) + "\n let _ = v }\n"
	}},
	{name: "package-zero", implicit: true, src: func(s genShape, q string) string {
		return "let Z " + s.typ(q) + "\n"
	}},
	{name: "tuple-zero", implicit: true, src: func(s genShape, q string) string {
		return "func use() { let v (" + s.typ(q) + ", int)\n let _ = v }\n"
	}},
	{name: "field-zero", implicit: true, src: func(s genShape, q string) string {
		return "type E { X " + s.typ(q) + ", Y int }\nfunc use() { let e = E{Y: 1}\n let _ = e }\n"
	}},
	{name: "instance-zero", implicit: true, src: func(s genShape, q string) string {
		return "type Wz[T] { X T }\nfunc use() { let w Wz[" + s.typ(q) + "]\n let _ = w }\n"
	}},
	{name: "empty-literal", implicit: true, src: func(s genShape, q string) string {
		return "func use() { let v = " + s.typ(q) + "{}\n let _ = v }\n"
	}},
	{name: "literal", explicit: true, src: func(s genShape, q string) string {
		return "func use() { let v = " + s.typ(q) + "{" + s.lit(q) + "}\n let _ = v }\n"
	}},
	{name: "update", explicit: true, src: func(s genShape, q string) string {
		return "func use() { let b = " + s.value(q) + "\n let v = " + s.typ(q) + "{..b, " + s.upd(q) + "}\n let _ = v }\n"
	}},
	{name: "value", src: func(s genShape, q string) string {
		return "func use() { let v " + s.typ(q) + " = " + s.value(q) + "\n let _ = v }\n"
	}},
}

// midImporter serves lib, mid and the standard library.
type midImporter struct{ lib, mid *check.Package }

func (i midImporter) Import(path string) (*check.Package, error) {
	switch path {
	case "lib":
		return i.lib, nil
	case "mid":
		return i.mid, nil
	}
	return check.Std.Import(path)
}

// split checks a shape's declarations as package mid, which imports lib,
// and site, which names them through mid, as package main.
func split(t *testing.T, lib *check.Package, s genShape, site string) []check.Diagnostic {
	t.Helper()
	decls := s.decls
	if strings.Contains(decls, "lib.") {
		decls = "import lib\n" + decls // a word of instances only names no lib type
	}
	f, err := syntax.Parse(decls)
	if err != nil {
		t.Fatalf("parse mid: %v\n%s", err, s.decls)
	}
	mid, ds := check.Check("mid", []*syntax.File{f}, libImporter{lib})
	if len(ds) != 0 {
		t.Fatalf("mid: %v\n%s", ds, s.decls)
	}
	head := "import mid\n"
	if strings.Contains(site, "lib.") {
		head += "import lib\n"
	}
	f, err = syntax.Parse(head + site)
	if err != nil {
		t.Fatalf("parse main: %v\n%s", err, site)
	}
	_, ds = check.Check("main", []*syntax.File{f}, midImporter{lib, mid})
	return ds
}

// TestConstructionAuthorityShapes is the generated property of 196b Q7:
// for every shape of the algebra, every site and every package position,
// the verdict comes from whether the site constructs lib's record or copies
// one lib built. Outside lib (the shape declared in main, or in mid and
// used from main) an implicit zero constructs it, so it is S114; a literal
// or an update constructs it exactly when the shape's record is lib's own
// (own); a value made by lib's functions copies it. Inside lib nothing is
// refused.
func TestConstructionAuthorityShapes(t *testing.T) {
	lib := checkedLib(t)
	shapes := genShapes(3)
	if len(shapes) != 80 {
		t.Fatalf("generated %d shapes, want 80", len(shapes))
	}
	checks := 0
	for _, s := range shapes {
		for _, site := range genSites {
			want := ""
			if site.implicit || site.explicit && s.own {
				want = "S114"
			}
			t.Run(s.name+"/"+site.name, func(t *testing.T) {
				src := s.decls + site.src(s, "")
				if got := outside(t, lib, src); sortedCodes(got) != want {
					t.Errorf("outside lib: got %v, want %q\n%s", got, want, src)
				}
				if got := inside(t, src); len(got) != 0 {
					t.Errorf("inside lib: got %v, want none\n%s", got, src)
				}
				if s.decls != "" {
					main := site.src(s, "mid.")
					if got := split(t, lib, s, main); sortedCodes(got) != want {
						t.Errorf("through mid: got %v, want %q\nmid:\n%smain:\n%s", got, want, s.decls, main)
					}
				}
			})
			checks += 2
			if s.decls != "" {
				checks++
			}
		}
	}
	t.Logf("%d shapes x %d sites x 3 positions (2 for a bare lib record) = %d verdicts", len(shapes), len(genSites), checks)
}

// sealSource is a type whose values are given where lib.Ops (or main's
// Local) is wanted: a concrete type, an operation type or a type parameter
// constrained by one. origin is the package whose inner it brings.
type sealSource struct {
	name, origin string
	tparams      string // the type parameter list of f, for a type parameter
	typ          string
}

var sealSources = []sealSource{
	{name: "concrete-lib", origin: "lib", typ: "lib.Impl"},
	{name: "concrete-main", origin: "main", typ: "Mine"},
	{name: "interface-lib", origin: "lib", typ: "lib.Ops"},
	{name: "interface-main", origin: "main", typ: "Local"},
	{name: "param-lib", origin: "lib", tparams: "[T lib.Ops]", typ: "T"},
	{name: "param-main", origin: "main", tparams: "[T Local]", typ: "T"},
}

// sealTargets are the operation types with an unexported operation inner:
// lib's and main's own.
var sealTargets = []struct{ name, owner string }{
	{"lib.Ops", "lib"},
	{"Local", "main"},
}

// sealSites give a value v of a source where the target is wanted.
var sealSites = []struct {
	name string
	src  func(target, tparams, typ string) string
}{
	{"argument", func(target, tparams, typ string) string {
		return "func take(o " + target + ") int { o.Outer() }\nfunc f" + tparams + "(v " + typ + ") int { take(v) }\n"
	}},
	{"result", func(target, tparams, typ string) string {
		return "func f" + tparams + "(v " + typ + ") " + target + " { v }\n"
	}},
	{"constraint", func(target, tparams, typ string) string {
		return "func need[U " + target + "](u U) int { u.Outer() }\nfunc f" + tparams + "(v " + typ + ") int { need(v) }\n"
	}},
	{"let", func(target, tparams, typ string) string {
		return "func f" + tparams + "(v " + typ + ") int { let o " + target + " = v\n o.Outer() }\n"
	}},
}

// sealDecls declares main's own operation type and a concrete type of main
// with both operations; made uses lib in every program. (A defined type over an operation type is refused,
// S108 types.md Defined types, so it is no source.)
const sealDecls = "type Local { inner() int, Outer() int }\ntype Mine {}\nfunc inner(_ Mine) int { 1 }\nfunc Outer(_ Mine) int { 2 }\nfunc made() lib.Ops { lib.MakeOps() }\n"

// TestSealedOperationIdentity (grammar.md, Visibility 2; 196b Q6): an
// unexported operation's identity is its name and its package, as in Go.
// For every source (a concrete type, an operation type, a type parameter),
// every target (lib's operation type or main's) and every site (argument,
// result, constraint, let), the source implements the target exactly when
// its inner comes from the target's package; else S108 names the package
// that alone implements it. Inside lib every package is lib: no refusal.
func TestSealedOperationIdentity(t *testing.T) {
	lib := checkedLib(t)
	for _, target := range sealTargets {
		for _, s := range sealSources {
			for _, site := range sealSites {
				src := sealDecls + site.src(target.name, s.tparams, s.typ)
				name := strings.ReplaceAll(target.name, ".", "") + "/" + s.name + "/" + site.name
				t.Run(name, func(t *testing.T) {
					ds := outside(t, lib, src)
					if s.origin == target.owner {
						if len(ds) != 0 {
							t.Errorf("same origin: got %v, want none\n%s", ds, src)
						}
					} else {
						text := "so only a type of package " + target.owner + " implements it"
						if sortedCodes(ds) != "S108" || !strings.Contains(ds[0].Message, text) {
							t.Errorf("foreign origin: got %v, want one S108 saying %q\n%s", ds, text, src)
						}
					}
					if ds := inside(t, src); len(ds) != 0 {
						t.Errorf("inside lib: got %v, want none\n%s", ds, src)
					}
				})
			}
		}
	}
}

// TestAuthorityVerifierAttacks are the round-1 verifier's attacks
// (wf10/authority round-1-receipts/generated_probe_test.go.txt), kept as
// regressions; sealed-generic-launder was accepted at 2f1285da. The
// generic-sealed cases keep an instance's operations package-qualified
// (subst copies Method pkg). The
// conversion probes check that a record of main with lib's field names is
// not a way in: conversion is nominal (convertible).
func TestAuthorityVerifierAttacks(t *testing.T) {
	lib := checkedLib(t)
	cases := []struct{ name, src, want string }{
		{"concrete-hidden-function", "func f(v lib.Impl) int { v.inner() }", "S104"},
		{"defined-concrete-hidden-function", "type M lib.Impl\nfunc f(v M) int { v.inner() }", "S104"},
		{"sealed-forward", "func f(v lib.Ops) lib.Ops { v }", ""},
		{"sealed-generic-forward", "func f[T lib.Ops](v T) lib.Ops { v }", ""},
		{"sealed-local-launder", "type Local { inner() int, Outer() int }\nfunc f(v lib.Ops) int { let x Local = v\n x.inner() }", "S108"},
		{"defined-interface-hidden-call", "type M lib.Ops\nfunc f(v M) int { v.inner() }", "S108"},
		{"sealed-generic-launder", "type Local { inner() int, Outer() int }\ntype Mine {}\nfunc inner(_ Mine) int { 1 }\nfunc Outer(_ Mine) int { 2 }\nfunc lift[T Local](v T) lib.Ops { v }\nfunc main() { let x = lift(Mine)\n let _ = x }", "S108"},
		{"empty-slice", "func f() { let x []lib.Q\n let _ = x }", ""},
		{"generic-defined-zero", "type D[T] lib.G[T]\nfunc f() { let x D[int]\n let _ = x }", "S114"},
		{"generic-defined-value", "type D[T] lib.G[T]\nfunc f() { let x = D[int](lib.NewG(1))\n let _ = x }", ""},
		{"nested-tuple-zero", "type Pair (int, lib.Q)\ntype H { X Pair }\nfunc f() { let x = H{}\n let _ = x }", "S114"},
		{"nested-property", "type H { X []lib.Q }\ntest P(x H) { assert true }", "S108"},
		{"generic-sealed-foreign", "type Mine {}\nfunc peek(_ Mine) int { 1 }\nfunc f() lib.Peek[int] { Mine }", "S108"},
		{"generic-sealed-same", "func f(v lib.Impl) lib.Peek[int] { v }", ""},
		{"generic-sealed-param", "func f[T lib.Peek[int]](v T) lib.Peek[int] { v }", ""},
		{"generic-sealed-param-foreign", "type Local[T] { peek() T }\nfunc f[T Local[int]](v T) lib.Peek[int] { v }", "S108"},
		{"generic-hidden-call", "func f(v lib.Peek[int]) int { v.peek() }", "S104"},
		{"convert-into", "type MyQ { N int, units int }\nfunc f(m MyQ) lib.Q { lib.Q(m) }", "S108"},
		{"convert-out", "type MyQ { N int, units int }\nfunc f(q lib.Q) MyQ { MyQ(q) }", "S108"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ds := outside(t, lib, tc.src)
			if got := sortedCodes(ds); got != tc.want {
				t.Errorf("got %q %v, want %q", got, ds, tc.want)
			}
		})
	}
}
