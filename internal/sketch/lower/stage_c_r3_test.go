package lower

import (
	"strings"
	"testing"
)

// Stage C round 2 review witnesses, run through the public CLI.

// M1, M2: a program that checks clean never panics with "comparing
// uncomparable type". Each route to equality over a union whose variant
// does not compare is refused before Go, and so is a comparable route
// (a constraint) over a row value of such a variant (q47); equality over a
// union whose variants all compare runs.
func TestErgoRunComparability(t *testing.T) {
	const decls = `type E { N []int }
type F {}
type V = E | F
func g() int fails E { E{N: []int{1}} }
func eq[T comparable](a T, b T) bool { a == b }
`
	for _, tc := range []struct{ name, src string }{
		{"row-generic", `func f() int { g() catch { case err: if eq(err, err) { 1 } else { 0 } } }`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := decls + tc.src + "\nfunc main() needs Console { if f() == 1 { Console.Println(\"equal\") } }\n"
			out, err := ergoRun(t, src)
			if err == nil || !strings.Contains(out, "S108") || strings.Contains(out, "uncomparable") {
				t.Fatalf("want an S108 refusal; got %v\n%s", err, out)
			}
		})
	}
	// eq-structural: a union with a slice variant is equatable, so == on a
	// value of V, assigned from a row value or declared, compares by
	// structure and never panics as Go's interface == would.
	wantRun(t, decls+`func f() int {
	g() catch {
	case err:
		let v V = err
		if v == v { 1 } else { 0 }
	}
}
func same(a V, b V) bool { a == b }
func main() needs Console {
	Console.Println("\(f()) \(same(E{N: []int{1}}, F)) \(same(E{N: []int{1}}, E{N: []int{1}})) \(same(F, F))")
}
`, "1 false true true\n")
	wantRun(t, `type C { N int }
type F {}
type U = C | F
func eq[T comparable](a T, b T) bool { a == b }
func main() needs Console {
	let a U = C{N: 1}
	let b U = C{N: 1}
	let c U = F
	let m = Map[U, int]{a: 1}
	if a == b && eq(a, b) && (a, 1) == (b, 1) && a != c && len(m) == 1 {
		Console.Println("comparable union")
	}
}
`, "comparable union\n")
}

// m4: a with, try or catch whose value is discarded discards its call
// tails (one statement-position rule, rows.md Totality 3), and the lowered
// program runs each of them: call tails of two types, a fallible tail
// under a statement catch, and a with in a void tail.
func TestErgoRunDiscardedWith(t *testing.T) {
	needsBuild(t)
	wantRun(t, `let W = context {}
type E {}
func g(b bool) int fails E { if b { return E }; 7 }
func each(f func()) { f() }
func i() int needs Console { Console.Println("i"); 1 }
func s() string needs Console { Console.Println("s"); "s" }
func pi() int { 1 }
func ps() string { "s" }
func f(b bool) needs Console {
	with W { if b { i() } else { s() } }
	with W { Console.Println("in with") }
	with W { g(b) } catch { case E: s() }
	with W { if b { g(b) } else { s() } } catch { case E: Console.Println("caught E") }
	each() { with W { if b { pi() } else { ps() } } }
}
func main() needs Console {
	f(true)
	f(false)
	Console.Println("ok")
}
`, "i\nin with\ns\ncaught E\ns\nin with\ns\nok\n")
}
