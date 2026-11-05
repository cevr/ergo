package check_test

import (
	"sort"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// authorityLib is package lib of the construction-authority tests (196b
// plan, U2): Q and G have an unexported field, so only lib builds them;
// Open has none; Ops has an unexported operation.
const authorityLib = `type Q { N int, units int }
func ParseQ(n int) Q { Q{N: n, units: 1} }
func hidden() int { 1 }
func priv(q Q) int { q.units }
type t { A int }
type Open { A int }
type Ops { inner() int, Outer() int }
type G[T] { v T, N int }
func NewG[T](v T) G[T] { G{v: v, N: 0} }
type Impl {}
func inner(_ Impl) int { 1 }
func Outer(_ Impl) int { 2 }
func MakeOps() Ops { Impl }
`

// libImporter serves package lib, checked once, and the standard library.
type libImporter struct{ lib *check.Package }

func (i libImporter) Import(path string) (*check.Package, error) {
	if path == "lib" {
		return i.lib, nil
	}
	return check.Std.Import(path)
}

func checkedLib(t *testing.T) *check.Package {
	t.Helper()
	f, err := syntax.Parse(authorityLib)
	if err != nil {
		t.Fatal(err)
	}
	lib, ds := check.Check("lib", []*syntax.File{f}, nil)
	if len(ds) != 0 {
		t.Fatalf("lib: %v", ds)
	}
	return lib
}

// outside checks src as package main, which imports lib.
func outside(t *testing.T, lib *check.Package, src string) []check.Diagnostic {
	t.Helper()
	f, err := syntax.Parse("import lib\n" + src)
	if err != nil {
		t.Fatalf("parse: %v\n%s", err, src)
	}
	_, ds := check.Check("main", []*syntax.File{f}, libImporter{lib})
	return ds
}

// inside checks src written in package lib itself: lib's declarations, then
// src with every lib. qualifier removed.
func inside(t *testing.T, src string) []check.Diagnostic {
	t.Helper()
	body := authorityLib + strings.ReplaceAll(src, "lib.", "")
	f, err := syntax.Parse(body)
	if err != nil {
		t.Fatalf("parse: %v\n%s", err, body)
	}
	_, ds := check.Check("lib", []*syntax.File{f}, nil)
	return ds
}

func sortedCodes(ds []check.Diagnostic) string {
	var cs []string
	for _, d := range ds {
		cs = append(cs, d.Code)
	}
	sort.Strings(cs)
	return strings.Join(cs, " ")
}

// The probe cases of the 196b plan (probe_check_test.go.txt): each was
// accepted at main 6a3b4c6a.
var authorityCases = map[string]string{
	"lit":         "func main() { let q = lib.Q{N: 1}\n let _ = q }\n",
	"emptylit":    "func main() { let q = lib.Q{}\n let _ = q }\n",
	"zero":        "func main() { let q lib.Q\n let _ = q }\n",
	"enclosing":   "type H { A lib.Q, B int }\nfunc main() { let h = H{B: 1}\n let _ = h }\n",
	"update":      "func main() { let q = lib.ParseQ(1)\n let r = lib.Q{..q, N: 2}\n let _ = r }\n",
	"defined":     "type M lib.Q\nfunc main() { let m = M{}\n let _ = m }\n",
	"definedzero": "type M lib.Q\nfunc main() { let m M\n let _ = m }\n",
	"generic":     "type Box[T] { V T }\nfunc main() { let b Box[lib.Q]\n let _ = b }\n",
}

var authorityControls = map[string]string{
	"fieldok":   "func f(q lib.Q) int { q.N }\nfunc main() { }\n",
	"openlit":   "func main() { let o = lib.Open{A: 1}\n let _ = o }\n",
	"openzero":  "func main() { let o lib.Open\n let _ = o }\n",
	"match":     "func f(q lib.Q) int { match q { case lib.Q{N: n}: n } }\nfunc main() { }\n",
	"letpat":    "func f(q lib.Q) int { let lib.Q{N: n} = q\n n }\nfunc main() { }\n",
	"parsed":    "func main() { let q = lib.ParseQ(1)\n let _ = q }\n",
	"convert":   "type M lib.Q\nfunc main() { let m = M(lib.ParseQ(1))\n let q = lib.Q(m)\n let _ = q }\n",
	"enclosed":  "type H { A lib.Q, B int }\nfunc main() { let h = H{A: lib.ParseQ(1), B: 1}\n let _ = h }\n",
	"copyhold":  "type H { A lib.Q, B int }\nfunc f(h H) H { H{..h, B: 2} }\nfunc main() { }\n",
	"boxed":     "type Box[T] { V T }\nfunc main() { let b = Box{V: lib.ParseQ(1)}\n let _ = b }\n",
	"generated": "func main() { let g = lib.NewG(1)\n let n = g.N\n let _ = n }\n",
	"ops":       "func f(s lib.Ops) int { s.Outer() }\nfunc main() { let n = f(lib.MakeOps())\n let _ = n }\n",
}

// TestConstructionAuthority (types.md, Zero values 6; 196b Q7): outside
// lib, every way to build a Q or a G but lib's functions is S114, which
// names lib.ParseQ; inside lib every case checks, and the controls check
// in both places.
func TestConstructionAuthority(t *testing.T) {
	lib := checkedLib(t)
	for name, src := range authorityCases {
		t.Run(name, func(t *testing.T) {
			ds := outside(t, lib, src)
			if sortedCodes(ds) != "S114" {
				t.Fatalf("outside lib: got %v, want one S114", ds)
			}
			if !strings.Contains(ds[0].Message, "lib.ParseQ") || !strings.Contains(ds[0].Message, "only package lib builds it") {
				t.Errorf("the S114 does not name the way in: %s", ds[0].Message)
			}
			if ds := inside(t, src); len(ds) != 0 {
				t.Errorf("inside lib: got %v, want none", ds)
			}
		})
	}
	for name, src := range authorityControls {
		t.Run("control/"+name, func(t *testing.T) {
			if ds := outside(t, lib, src); len(ds) != 0 {
				t.Errorf("outside lib: got %v, want none", ds)
			}
			if ds := inside(t, src); len(ds) != 0 {
				t.Errorf("inside lib: got %v, want none", ds)
			}
		})
	}
}

// authorityShape is a type whose values only lib may build, written as a
// type expression of package main, with the declarations it needs and an
// expression of a value of it that lib's functions give.
type authorityShape struct {
	name, decls, typ, value string
	// own: the record's fields are lib's own (its literal itself needs the
	// authority); else the shape holds such a record by value, and only an
	// implicit zero of that part needs it.
	own bool
	// lit is a literal of the type that sets every field main may name.
	lit string
}

var authorityShapes = []authorityShape{
	{name: "record", typ: "lib.Q", value: "lib.ParseQ(1)", own: true, lit: "lib.Q{N: 1}"},
	{name: "defined", decls: "type M lib.Q\n", typ: "M", value: "M(lib.ParseQ(1))", own: true, lit: "M{N: 1}"},
	{name: "defined-twice", decls: "type M lib.Q\ntype M2 M\n", typ: "M2", value: "M2(M(lib.ParseQ(1)))", own: true, lit: "M2{N: 1}"},
	{name: "generic-lib", typ: "lib.G[int]", value: "lib.NewG(1)", own: true, lit: "lib.G[int]{N: 1}"},
	{name: "holding", decls: "type H { A lib.Q, B int }\n", typ: "H", value: "H{A: lib.ParseQ(1), B: 1}", lit: "H{A: lib.ParseQ(1), B: 1}"},
	{name: "holding-deep", decls: "type H { A lib.Q, B int }\ntype H2 { X H }\n", typ: "H2", value: "H2{X: H{A: lib.ParseQ(1), B: 1}}", lit: "H2{X: H{A: lib.ParseQ(1), B: 1}}"},
	{name: "defined-holding", decls: "type H { A lib.Q, B int }\ntype MH H\n", typ: "MH", value: "MH(H{A: lib.ParseQ(1), B: 1})", lit: "MH{A: lib.ParseQ(1), B: 1}"},
	{name: "instance", decls: "type Box[T] { V T }\n", typ: "Box[lib.Q]", value: "Box{V: lib.ParseQ(1)}", lit: "Box[lib.Q]{V: lib.ParseQ(1)}"},
	{name: "instance-lib", decls: "type Box[T] { V T }\n", typ: "Box[lib.G[int]]", value: "Box{V: lib.NewG(1)}", lit: "Box[lib.G[int]]{V: lib.NewG(1)}"},
}

// authoritySite is a way to make a value of a shape; implicit marks a site
// that makes the shape's zero value, which needs the authority for every
// shape; the others need it only for a shape whose fields are lib's.
type authoritySite struct {
	name     string
	implicit bool
	src      func(s authorityShape) string
}

var authoritySites = []authoritySite{
	{"let-zero", true, func(s authorityShape) string {
		return "func use() { let v " + s.typ + "\n let _ = v }\n"
	}},
	{"package-zero", true, func(s authorityShape) string {
		return "let V " + s.typ + "\n"
	}},
	{"tuple-zero", true, func(s authorityShape) string {
		return "func use() { let v (" + s.typ + ", int)\n let _ = v }\n"
	}},
	{"field-zero", true, func(s authorityShape) string {
		return "type E { X " + s.typ + ", Y int }\nfunc use() { let e = E{Y: 1}\n let _ = e }\n"
	}},
	{"instance-zero", true, func(s authorityShape) string {
		return "type W[T] { X T }\nfunc use() { let w W[" + s.typ + "]\n let _ = w }\n"
	}},
	{"empty-literal", true, func(s authorityShape) string {
		return "func use() { let v = " + s.typ + "{}\n let _ = v }\n"
	}},
	{"literal", false, func(s authorityShape) string {
		return "func use() { let v = " + s.lit + "\n let _ = v }\n"
	}},
	{"update", false, func(s authorityShape) string {
		field := "N: 2"
		if !s.own {
			field = updateField(s)
		}
		return "func use() { let b = " + s.value + "\n let v = " + s.typ + "{..b, " + field + "}\n let _ = v }\n"
	}},
	{"value", false, func(s authorityShape) string {
		return "func use() { let v " + s.typ + " = " + s.value + "\n let _ = v }\n"
	}},
}

// updateField is a field a record update of a shape that holds the
// authority record may replace with a value lib's functions give.
func updateField(s authorityShape) string {
	switch s.name {
	case "holding", "defined-holding":
		return "B: 2"
	case "holding-deep":
		return "X: H{A: lib.ParseQ(2), B: 2}"
	case "instance":
		return "V: lib.ParseQ(2)"
	case "instance-lib":
		return "V: lib.NewG(2)"
	}
	panic("no update field for " + s.name)
}

// TestConstructionAuthorityShapes is the generated property: for every
// authority shape and every site, the verdict is S114 outside lib exactly
// when the site makes a new value of lib's record (an implicit zero, or a
// literal or update of a type whose fields are lib's), and none inside.
func TestConstructionAuthorityShapes(t *testing.T) {
	lib := checkedLib(t)
	for _, shape := range authorityShapes {
		for _, site := range authoritySites {
			src := shape.decls + site.src(shape)
			want := ""
			if site.implicit || shape.own && site.name != "value" {
				want = "S114"
			}
			t.Run(shape.name+"/"+site.name, func(t *testing.T) {
				if got := outside(t, lib, src); sortedCodes(got) != want {
					t.Errorf("outside lib: got %v, want %q\n%s", got, want, src)
				}
				if got := inside(t, src); len(got) != 0 {
					t.Errorf("inside lib: got %v, want none\n%s", got, src)
				}
			})
		}
	}
}

// TestFieldVisibility (grammar.md, Visibility 2; 196b Q6): outside lib, an
// unexported field is not read, written, named in a literal or a pattern,
// and an unexported operation is not called or implemented; inside lib each
// is allowed.
func TestFieldVisibility(t *testing.T) {
	lib := checkedLib(t)
	cases := []struct {
		name, src, want, text string
	}{
		{"fieldread", "func f(q lib.Q) int { q.units }\nfunc main() { }\n", "S104", "units is not an exported field of lib.Q"},
		{"fieldwrite", "func main() { let mut q = lib.ParseQ(1)\n q.units = 2\n let _ = q }\n", "S104", "units is not an exported field of lib.Q"},
		{"definedread", "type M lib.Q\nfunc f(m M) int { m.units }\nfunc main() { }\n", "S104", "units is not an exported field of lib.Q"},
		{"genericread", "func f(g lib.G[int]) int { g.v }\nfunc main() { }\n", "S104", "v is not an exported field of lib.G"},
		{"litpriv", "func main() { let q = lib.Q{N: 1, units: 2}\n let _ = q }\n", "S104 S114", "units is not an exported field of lib.Q"},
		{"patpriv", "func f(q lib.Q) int { match q { case lib.Q{units: n}: n } }\nfunc main() { }\n", "S104", "units is not an exported field of lib.Q"},
		{"letpatpriv", "func f(q lib.Q) int { let lib.Q{units: n} = q\n n }\nfunc main() { }\n", "S104", "units is not an exported field of lib.Q"},
		{"oppriv", "func f(s lib.Ops) int { s.inner() }\nfunc main() { }\n", "S104", "inner is not an exported operation of lib.Ops"},
		{"opsealed", "type Mine {}\nfunc inner(_ Mine) int { 1 }\nfunc Outer(_ Mine) int { 2 }\nfunc f() lib.Ops { Mine }\nfunc main() { }\n", "S108", "inner is an unexported operation of lib.Ops"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ds := outside(t, lib, tc.src)
			if sortedCodes(ds) != tc.want {
				t.Fatalf("outside lib: got %v, want %s", ds, tc.want)
			}
			found := false
			for _, d := range ds {
				found = found || strings.Contains(d.Message, tc.text)
			}
			if !found {
				t.Errorf("no diagnostic says %q: %v", tc.text, ds)
			}
			if ds := inside(t, tc.src); len(ds) != 0 {
				t.Errorf("inside lib: got %v, want none", ds)
			}
		})
	}
}

// TestConstructibleFact: check.Constructible is the authority predicate
// refine, generators (#162) and codecs (#49) read (docs/decisions.md
// 2026-11-03 (a)).
func TestConstructibleFact(t *testing.T) {
	lib := checkedLib(t)
	f, err := syntax.Parse("import lib\ntype H { A lib.Q }\ntype L { Xs []lib.Q }\ntype P { N int }\nfunc main() { }\n")
	if err != nil {
		t.Fatal(err)
	}
	main, ds := check.Check("main", []*syntax.File{f}, libImporter{lib})
	if len(ds) != 0 {
		t.Fatal(ds)
	}
	typ := func(p *check.Package, name string) check.Type {
		for _, tn := range p.Types {
			if tn.Name == name {
				return tn.Type
			}
		}
		t.Fatalf("no type %s", name)
		return nil
	}
	for _, tc := range []struct {
		p    *check.Package
		name string
		from *check.Package
		want bool
	}{
		{lib, "Q", main, false},
		{lib, "Q", lib, true},
		{lib, "Open", main, true},
		{main, "H", main, false},
		{main, "L", main, false},
		{main, "P", main, true},
	} {
		if got := check.Constructible(typ(tc.p, tc.name), tc.from); got != tc.want {
			t.Errorf("Constructible(%s.%s, from %s) = %v, want %v", tc.p.Name, tc.name, tc.from.Name, got, tc.want)
		}
	}
}

// TestPropertyParamAuthority: a property parameter is generated field by
// field, so a type lib alone builds is not generatable outside lib.
func TestPropertyParamAuthority(t *testing.T) {
	lib := checkedLib(t)
	src := "test P(q lib.Q) { assert q.N == q.N }\nfunc main() { }\n"
	ds := outside(t, lib, src)
	if sortedCodes(ds) != "S108" || !strings.Contains(ds[0].Message, "only package lib builds it") {
		t.Fatalf("got %v, want one S108 naming the authority", ds)
	}
	if ds := inside(t, src); len(ds) != 0 {
		t.Errorf("inside lib: got %v", ds)
	}
}
