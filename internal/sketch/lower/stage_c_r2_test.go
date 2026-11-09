package lower

import (
	"strings"
	"testing"
)

// Stage C round 1 review witnesses, run through the public CLI.

// M1, M3, M4: cumulative catch coverage runs each arm; try over a partial
// catch propagates a failure of the caught expression's operand before the
// outer call runs; with blocks, trailing blocks and catch arms yield
// through a final if or match, and a void catch arm discards its tail.
func TestErgoRunReviewCForms(t *testing.T) {
	needsBuild(t)
	wantRun(t, `type E { B bool }
type X {}
let W = context {}

func flag(b bool) int fails E { E{B: b} }
func collective(b bool) int { flag(b) catch { case E{B: true}: 1; case E{B: false}: 2 } }

func h(n int) int fails X needs Console {
	Console.Println("h ran")
	n
}
func whole() int fails E needs Console { try h(flag(true)) catch { case X: 0 } }
func caught(b bool) int fails E needs Console { try h(if b { flag(true) } else { 3 }) catch { case X: 0 } }

func withIf(b bool) int { with W { if b { 1 } else { 2 } } }
func withMatch(b bool) int { with W { match b { case true: 3; case false: 4 } } }
func withFallible(b bool) int fails E { with W { if b { flag(true) } else { 5 } } }
func voidFail() fails E { E{B: false} }
func voidCatch() { voidFail() catch { case E: withIf(true) } }
func use(f func() int) int { f() }
func trailing(b bool) int { use() { if b { 6 } else { 7 } } }
func arm(b bool) int { flag(b) catch { case E{B: true}: if b { 8 } else { 0 }; case E: 9 } }

func main() needs Console {
	if collective(true) == 1 && collective(false) == 2 {
		Console.Println("collective")
	}
	let w = whole() catch { case E: 9 }
	if w == 9 {
		Console.Println("whole")
	}
	let c = caught(false) catch { case E: 0 }
	if c == 3 {
		Console.Println("caught")
	}
	if withIf(true) == 1 && withIf(false) == 2 && withMatch(true) == 3 && withMatch(false) == 4 {
		Console.Println("with")
	}
	let f1 = withFallible(true) catch { case E: 10 }
	let f2 = withFallible(false) catch { case E: 10 }
	if f1 == 10 && f2 == 5 {
		Console.Println("with fails")
	}
	voidCatch()
	if trailing(true) == 6 && trailing(false) == 7 && arm(true) == 8 && arm(false) == 9 {
		Console.Println("bodies")
	}
}
`, "collective\nwhole\nh ran\ncaught\nwith\nwith fails\nbodies\n")
}

// M5: a package-level let destructures once: a tuple pattern, a record
// pattern and several names over a tuple, initialized
// before their readers whatever the declaration order.
func TestErgoRunPackageDestructuring(t *testing.T) {
	needsBuild(t)
	wantRun(t, `let total = a + b + q + r + x + y
let (a, b) = (1, 2)
type Point { X int, Y int }
let Point{X: x, Y: y} = origin()
let q, r = pair()

func origin() Point {
	Point{X: 10, Y: 20}
}
func pair() (int, int) { (3, 4) }

func main() needs Console {
	if a + b == 3 && x == 10 && y == 20 && q + r == 7 && total == 40 {
		Console.Println("ok")
	}
}
`, "ok\n")
}

// M6: a defined type over a record, named in an error row, is an error type
// of its own: thrown, caught by type and by record pattern, its text from
// its base record's fields; a base field named Error keeps a Go name free
// of the Error method; a generic defined error type too (its fields do not
// mention T: a type-parameter field's text is lower-v1 stage 3's, for
// records as well).
func TestErgoRunDefinedErrorTypes(t *testing.T) {
	needsBuild(t)
	wantRun(t, `type E { N int, Error string }
type D E
type Box[T] { V int }
type Wrapped[T] Box[T]

func g(n int) int fails D { D(E{N: n, Error: "boom"}) }
func f(n int) int { g(n) catch { case D{N: 1}: 10; case D{N: k}: k } }
func typed() string {
	let v = g(2) catch { case D as d: return d.Error }
	if v == 0 { "zero" } else { "none" }
}
func w() int fails Wrapped[string] { Wrapped[string](Box[string]{V: 7}) }
func unwrap() int { w() catch { case Wrapped[string]{V: v}: v } }

func main() needs Console {
	if f(1) == 10 && f(5) == 5 && typed() == "boom" && unwrap() == 7 {
		Console.Println("defined errors")
	}
	let r = g(3) catch {
	case _:
		Console.Println("caught")
		0
	}
	if r == 0 {
		Console.Println("zero")
	}
}
`, "defined errors\ncaught\nzero\n")
}

// M7: a defined type over a basic type keeps its nominal type in emitted
// constants: a tail, a conversion, a comparison and a constant pattern.
func TestErgoRunDefinedBasicConstants(t *testing.T) {
	needsBuild(t)
	wantRun(t, `type Flag bool
type Text string
type Code int

func on() Flag { true }
func name() Text { "x" }
func code() Code { 7 }
func label(t Text) string { match t { case "x": "ex"; case _: "other" } }
func flip(f Flag) Flag { if f == true { false } else { true } }

func main() needs Console {
	if on() == Flag(true) && name() == "x" && string(name()) == "x" && code() == 7 && label("x") == "ex" && label(Text("y")) == "other" && flip(on()) == false {
		Console.Println("defined constants")
	}
}
`, "defined constants\n")
}

// M2, M8: a catch alternative that leaves a name unbound and equality on a
// row value are refused before Go; neither program runs.
func TestErgoRunReviewCRefusals(t *testing.T) {
	needsBuild(t)
	for _, tc := range []struct{ name, src string }{
		{"unbound-alternative", `type E { N int }
type F {}
func g() int fails E | F { F }
func f() int { g() catch { case E{N: x}, F: x } }
func main() needs Console { if f() == 0 { Console.Println("unbound-is-zero") } }
`},
		{"unbound-alternative-reversed", `type E { N int }
type F {}
func g() int fails E | F { F }
func f() int { g() catch { case F, E{N: x}: x } }
func main() needs Console { if f() == 0 { Console.Println("unbound-is-zero") } }
`},
		{"row-equality", `type E { N int }
func g() int fails E { E{N: 1} }
func f() int { g() catch { case err: if err == err { 1 } else { 0 } } }
func main() needs Console { if f() == 1 { Console.Println("row equality accepted") } }
`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := ergoRun(t, tc.src)
			if err == nil || !strings.Contains(out, "S108") || strings.Contains(out, "accepted") || strings.Contains(out, "zero") {
				t.Fatalf("want an S108 refusal; got %v\n%s", err, out)
			}
		})
	}
}
