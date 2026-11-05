package check_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// A record update `T{..base, F: v}` is a composite literal of T (grammar.md,
// Expressions 11; types.md, Structs 3): its base is a value of exactly T,
// typed once and first; it names at least one field, by the keyed-literal
// rules; a field it omits is the base's. The cases hold the decision record's
// refusals with their positions, the generic and union risks, the routes
// that refuse the literal (each must keep the base's own diagnostics and add
// none), and the reference walk, which must read the base as a value.

const updateDecls = `type Walk { At int, Turn int }

type Other { At int, Turn int }

type Box[T] { V T, N int }

type Pair[T] { V T, N int }

type Playing { W Walk }

type Won { W Walk }

type Game = Playing | Won

type Ints []int

type Bad { Why string }

func mk() Walk { Walk{At: 1, Turn: 2} }

func risky(n int) int fails Bad {
	if n < 0 {
		return Bad{Why: "neg"}
	}
	n
}

func walkOrBad(n int) Walk fails Bad {
	if n < 0 {
		return Bad{Why: "neg"}
	}
	Walk{At: n, Turn: n}
}
`

type updateCase struct {
	name string
	src  string
	want []string // "line:col code", in order; lines count from the case's first line
}

var updateCases = []updateCase{
	{"one field", `func f(w Walk) Walk { Walk{..w, Turn: 1} }`, nil},
	{"every field", `func f(w Walk) Walk { Walk{..w, At: 2, Turn: 1} }`, nil},
	{"a call as the base", `func f() Walk { Walk{..mk(), Turn: 1} }`, nil},
	{"a nested update as the base", `func f(w Walk) Walk { Walk{..Walk{..w, At: 1}, Turn: 2} }`, nil},
	{"an update as a field value", `func f(p Playing) Playing { Playing{..p, W: Walk{..p.W, Turn: 1}} }`, nil},
	{"a variant in its match arm", `func f(g Game) Game {
	match g {
	case Playing as p:
		Playing{..p, W: Walk{At: 0, Turn: 0}}
	case Won:
		g
	}
}`, nil},
	{"a generic record with written type arguments", `func f(b Box[string]) Box[string] { Box[string]{..b, V: "z"} }`, nil},
	{"a generic record takes its type arguments from the expected type", `func f(b Box[string]) Box[string] { Box{..b, V: "z"} }`, nil},
	{"a generic record takes its type arguments from the base", `func f(b Box[int]) int {
	let c = Box{..b, V: 2}
	c.V
}`, nil},
	{"a tried field value", `func f(w Walk) Walk fails Bad { Walk{..w, Turn: try risky(1)} }`, nil},
	{"a tried base", `func f() Walk fails Bad { try Walk{..walkOrBad(1), Turn: 1} }`, nil},
	{"no field", `func f(w Walk) Walk { Walk{..w} }`, []string{"1:23 S108"}},
	{"no field after a comma", `func f(w Walk) Walk { Walk{..w,} }`, []string{"1:23 S108"}},
	{"a base of another record type", `func f(o Other) Walk { Walk{..o, Turn: 1} }`, []string{"1:31 S108"}},
	{"a base of the union type", `func f(g Game) Playing { Playing{..g, W: Walk{At: 0, Turn: 0}} }`, []string{"1:36 S108"}},
	{"a field set twice", `func f(w Walk) Walk { Walk{..w, Turn: 1, Turn: 2} }`, []string{"1:42 S108"}},
	{"an unknown field", `func f(w Walk) Walk { Walk{..w, Nope: 1} }`, []string{"1:33 S104"}},
	{"a field value of another type", `func f(w Walk) Walk { Walk{..w, Turn: "x"} }`, []string{"1:39 S108"}},
	{"a base of another instance", `func f(b Box[int]) Box[string] { Box[string]{..b, V: "z"} }`, []string{"1:48 S108"}},
	{"a base of another instance, by the expected type", `func f(b Box[int]) Box[string] { Box{..b, V: "z"} }`, []string{"1:40 S108"}},
	{"a base that gives no type arguments", `func f(w Walk) int {
	let c = Box{..w, N: 2}
	c.N
}`, []string{"2:16 S108"}},
	{"a base of another generic record that gives no type arguments", `func f(p Pair[int]) int {
	let c = Box{..p, N: 2}
	c.N
}`, []string{"2:16 S108"}},
	{"a value the inferred instance refuses", `func f(b Box[int]) int {
	let c = Box{..b, V: "z"}
	c.N
}`, []string{"2:22 S108"}},
	{"an unknown base", `func f() Walk { Walk{..nope, Turn: 1} }`, []string{"1:24 S104"}},
	{"an unknown base of a generic record", `func f() int {
	let c = Box{..nope, N: 1}
	c.N
}`, []string{"2:16 S104"}},
	{"a type as the base", `func f() Walk { Walk{..Walk, Turn: 1} }`, []string{"1:24 S108"}},
	{"an unknown type", `func f(w Walk) Walk { Nope{..w, Turn: 1} }`, []string{"1:23 S104"}},
	{"a slice type", `func f(xs Ints) Ints { Ints{..xs, Turn: 1} }`, []string{"1:24 S108"}},
	{"an unhandled failure in a field value", `func f(w Walk) Walk { Walk{..w, Turn: risky(1)} }`, []string{"1:39 S105"}},
	{"an unhandled failure in the base", `func f() Walk { Walk{..walkOrBad(1), Turn: 1} }`, []string{"1:24 S105"}},
}

// updateTexts holds, by case name, the text a case's first diagnostic must
// contain, where the code and position alone do not show the rule.
var updateTexts = map[string]string{
	// A Box literal must not give a Pair: the inferred base is an instance
	// of the literal's own generic record, or it is refused.
	"a base of another generic record that gives no type arguments": "the base of a record update of Box has type Pair[int], not Box",
}

func updateDiags(src string) []string {
	_, diags := check.CheckSource(src)
	var got []string
	for _, d := range diags {
		got = append(got, fmt.Sprintf("%d:%d %s", d.Line, d.Column, d.Code))
	}
	return got
}

// TestRecordUpdateRules checks each case's diagnostics at their positions,
// and that the reference walk agrees with typing on it.
func TestRecordUpdateRules(t *testing.T) {
	offset := strings.Count(updateDecls, "\n") + 1
	for name := range updateTexts {
		if !slices.ContainsFunc(updateCases, func(c updateCase) bool { return c.name == name }) {
			t.Errorf("updateTexts names no case %q", name)
		}
	}
	for _, c := range updateCases {
		src := updateDecls + "\n" + c.src + "\n"
		var want []string
		for _, w := range c.want {
			var line, col int
			var code string
			if _, err := fmt.Sscanf(w, "%d:%d %s", &line, &col, &code); err != nil {
				t.Fatalf("%s: bad want %q", c.name, w)
			}
			want = append(want, fmt.Sprintf("%d:%d %s", line+offset, col, code))
		}
		got := updateDiags(src)
		if strings.Join(got, ", ") != strings.Join(want, ", ") {
			_, diags := check.CheckSource(src)
			t.Errorf("%s: got %v, want %v\n%v", c.name, got, want, diags)
		}
		if text, ok := updateTexts[c.name]; ok {
			_, diags := check.CheckSource(src)
			if len(diags) == 0 || !strings.Contains(diags[0].Message, text) {
				t.Errorf("%s: want the first diagnostic to contain %q, got %v", c.name, text, diags)
			}
		}
		if p := check.RefWalkProblems(src); len(p) != 0 {
			t.Errorf("%s: the walk and typing disagree: %v", c.name, p)
		}
	}
}

// TestRecordUpdateBaseRefs: a package function or value named in a record
// update's base is a reference edge for typing and for the walk alike, on
// every route the checker takes: an accepted update, and each refusal of
// the literal or of the base (each types the base, as a value or as an
// orphan, and the walk reads it as a value). The bases are a name, a call
// and a nested update; the CompositeLit.Base row of the literal guards
// (TestLiteralRefsAgree, litNoShape) points here.
func TestRecordUpdateBaseRefs(t *testing.T) {
	const decls = "type Rec { V int }\ntype Other { V int }\ntype Box[T] { V T }\ntype Ints []int\nfunc mkRec(f func()) Rec { Rec{V: 1} }\nfunc fx() {}\nlet pv = Rec{V: 5}\n\n"
	bases := []string{"$N", "mkRec($N)", "Rec{..$N, V: 2}"}
	names := []string{"fx", "pv"}
	routes := []struct{ name, lit string }{
		{"accepted", "Rec{..BASE, V: 1}"},
		{"no field", "Rec{..BASE}"},
		{"unknown type", "Nope{..BASE, V: 1}"},
		{"a slice type", "Ints{..BASE, V: 1}"},
		{"another record type", "Other{..BASE, V: 1}"},
		{"an unknown field", "Rec{..BASE, W: 1}"},
		{"a generic record from its base", "Box{..BASE, V: 1}"},
	}
	n := 0
	for _, r := range routes {
		for _, b := range bases {
			for _, nm := range names {
				base := b
				if nm == "fx" && b != "mkRec($N)" {
					continue // fx is a function value, never a Rec
				}
				if nm == "pv" && b == "mkRec($N)" {
					continue
				}
				body := "func host() {\n\tlet _ = " + strings.Replace(r.lit, "BASE", base, 1) + "\n}\n"
				at := len(decls) + strings.Index(body, "$N")
				src := decls + strings.Replace(body, "$N", nm, 1)
				info, walk, found := check.RefEdge(src, at)
				if !found {
					t.Errorf("%s, %s: no identifier at the name\n%s", r.name, base, src)
					continue
				}
				n++
				if info != "edge" || walk != "edge" {
					t.Errorf("%s, %s, %s: want Info edge and the walk edge, got Info %s and the walk %s\n%s", r.name, base, nm, info, walk, src)
				}
				if p := check.RefWalkProblems(src); len(p) != 0 {
					t.Errorf("%s, %s, %s: the walk and typing disagree: %v\n%s", r.name, base, nm, p, src)
				}
			}
		}
	}
	if n != len(routes)*3 {
		t.Errorf("%d programs, want %d", n, len(routes)*3)
	}
}

// updateProgram is the acceptance program of record-update (R1): records,
// a nested update, a generic record, a variant in its match arm, evaluation
// order, a tried field value and catch arms that update.
const updateProgram = `type Walk { At int, Tide int, Turn int, Shells int, Left string }

type Inner { X int, Y int }

type Outer { Name string, In Inner }

type Box[T] { V T, N int }

type Playing { W Walk }

type Won { W Walk }

type Game = Playing | Won

type Bad { Why string }

func turn(w Walk) Walk {
	Walk{..w, Turn: w.Turn + 1}
}

func step(g Game) Game {
	match g {
	case Playing as p:
		Playing{..p, W: turn(p.W)}
	case Won:
		g
	}
}

func mk() Walk needs Console {
	Console.Println("base")
	Walk{At: 1, Tide: 2, Turn: 3, Shells: 4, Left: "abc"}
}

func val(n int) int needs Console {
	Console.Println("value \(n)")
	n
}

func parse(s string) int fails Bad {
	if s == "" {
		return Bad{Why: "empty"}
	}
	len(s)
}

func tried(w Walk, s string) Walk fails Bad {
	Walk{..w, Tide: try parse(s)}
}

func main() needs Console {
	let w = Walk{At: 1, Tide: 2, Turn: 3, Shells: 4, Left: "abc"}
	let o = Outer{Name: "o", In: Inner{X: 1, Y: 2}}
	let o2 = Outer{..o, In: Inner{..o.In, Y: 9}}
	let b = Box[string]{V: "a", N: 1}
	let b2 = Box[string]{..b, V: "z"}
	let e = Walk{..mk(), Turn: val(7), Shells: val(8)}
	let ok = tried(w, "xyz") catch {
	case Bad as x:
		Walk{..w, Left: x.Why}
	}
	Console.Println("\(step(Playing{W: turn(w)}) == step(Playing{W: w})) \(o2.In.Y) \(b2.V) \(e.Turn) \(ok.Tide)")
}
`

// TestRecordUpdateWalk: on the acceptance program the walk visits every
// base, as typing types it; and Info records each base with exactly its
// literal's type, so lowering reads the copy's type and never derives it.
func TestRecordUpdateWalk(t *testing.T) {
	if got := updateDiags(updateProgram); len(got) != 0 {
		_, diags := check.CheckSource(updateProgram)
		t.Fatalf("the program checks with %v", diags)
	}
	if p := check.RefWalkProblems(updateProgram); len(p) != 0 {
		t.Errorf("the walk and typing disagree: %v", p)
	}
	pkg, _ := check.CheckSource(updateProgram)
	updates := 0
	for x, typ := range pkg.Info.Types {
		lit, ok := x.(*syntax.CompositeLit)
		if !ok || lit.Base == nil {
			continue
		}
		updates++
		bt, ok := pkg.Info.Types[lit.Base]
		if !ok {
			t.Errorf("the base at %d has no Types entry", lit.Base.Pos())
			continue
		}
		if bt.String() != typ.String() {
			t.Errorf("the base at %d has type %s, its update %s", lit.Base.Pos(), bt, typ)
		}
	}
	if updates != 8 {
		t.Errorf("Info records %d updates, want 8", updates)
	}
}

// TestRecordUpdateProviderFreshness: a with owns a resource provider only
// when it is fresh (resources.md, Refusals). A record update copies every
// field it does not name from its base, so it is fresh only when its base
// is: an update of a package value is a second owner of that value, S109
// at the provider; an update of a literal or of a fresh call is fresh.
func TestRecordUpdateProviderFreshness(t *testing.T) {
	const decls = `type A { Use() }

type Res { n int }

func Use(_ Res) {}

func Close(_ Res) {}

let shared = Res{n: 1}

func open() Res { Res{n: 1} }

`
	for _, tc := range []struct {
		name, src string
		want      []string // "line:col code"; lines count from the case's first line
	}{
		{"an update of a package value", `let V = context { A: Res{..shared, n: 3} }
func g() { with V { A.Use() } }`, []string{"1:22 S109"}},
		{"a constructor that returns an update of a package value", `func make() Res { Res{..shared, n: 2} }
let V = context { A: make() }
func g() { with V { A.Use() } }`, []string{"2:22 S109"}},
		{"an inline update of a package value", `func g() { with context { A: Res{..shared, n: 3} } { A.Use() } }`, []string{"1:30 S109"}},
		{"an update of a literal", `let V = context { A: Res{..Res{n: 1}, n: 3} }
func g() { with V { A.Use() } }`, nil},
		{"an update of a fresh call", `let V = context { A: Res{..open(), n: 3} }
func g() { with V { A.Use() } }`, nil},
		{"a constructor that returns an update of a fresh call", `func make() Res { Res{..open(), n: 2} }
let V = context { A: make() }
func g() { with V { A.Use() } }`, nil},
	} {
		offset := strings.Count(decls, "\n")
		var want []string
		for _, w := range tc.want {
			var line, col int
			var code string
			if _, err := fmt.Sscanf(w, "%d:%d %s", &line, &col, &code); err != nil {
				t.Fatalf("%s: bad want %q", tc.name, w)
			}
			want = append(want, fmt.Sprintf("%d:%d %s", line+offset, col, code))
		}
		src := decls + tc.src + "\n"
		if got := updateDiags(src); strings.Join(got, ", ") != strings.Join(want, ", ") {
			_, diags := check.CheckSource(src)
			t.Errorf("%s: got %v, want %v\n%v", tc.name, got, want, diags)
		}
	}
}
