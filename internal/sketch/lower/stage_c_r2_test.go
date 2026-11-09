package lower

import (
	"strings"
	"testing"
)

// Stage C round 1 review witnesses, run through the public CLI.

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
