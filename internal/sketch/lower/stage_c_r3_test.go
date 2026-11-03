package lower

import (
	"strings"
	"testing"
)

// Stage C round 2 review witnesses, run through the public CLI.

// M1, M2: a program that checks clean never panics with "comparing
// uncomparable type". Each route to equality over a union whose variant
// does not compare, or over a row value, is refused before Go; equality
// over a union whose variants all compare runs.
func TestErgoRunComparability(t *testing.T) {
	const decls = `type E { N []int }
type F {}
type V = E | F
func g() int fails E { E{N: []int{1}} }
func eq[T comparable](a T, b T) bool { a == b }
`
	for _, tc := range []struct{ name, src string }{
		{"row-tuple", `func f() int { g() catch { case err: if (err, 1) == (err, 1) { 1 } else { 0 } } }`},
		{"row-generic", `func f() int { g() catch { case err: if eq(err, err) { 1 } else { 0 } } }`},
		{"row-assigned-union", "func f() int {\n\tg() catch {\n\tcase err:\n\t\tlet v V = err\n\t\tif v == v { 1 } else { 0 }\n\t}\n}"},
		{"declared-union", "func same(a V, b V) bool { a == b }\nfunc f() int { if same(E{N: []int{1}}, F) { 1 } else { 0 } }"},
		// A row value never compares, even over a comparable error type.
		{"row-comparable", "type K { N int }\nfunc gk() int fails K { K{N: 1} }\nfunc f() int { gk() catch { case err: if (err, 1) == (err, 1) { 1 } else { 0 } } }"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := decls + tc.src + "\nfunc main() needs Console { if f() == 1 { Console.Println(\"equal\") } }\n"
			out, err := ergoRun(t, src)
			if err == nil || !strings.Contains(out, "S108") || strings.Contains(out, "uncomparable") {
				t.Fatalf("want an S108 refusal; got %v\n%s", err, out)
			}
		})
	}
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

// O2: an if or match branch's fallible tail passes its failure to the
// enclosing catch or try, as a with block's does, and the shapes agree at
// run time: through a catch, a try operand, a returned tail and a block's
// tail.
func TestErgoRunBranchTailsPassFailures(t *testing.T) {
	wantRun(t, `let W = context {}
type E { K int }
type F {}
func g(b bool) int fails E { if b { return E{K: 4} }; 7 }
func h(n int) int fails F { if n == 0 { return F }; n }
func viaIf(b bool, c bool) int { (if c { g(b) } else { 2 }) catch { case E{K: k}: k * 10 } }
func viaMatch(b bool, n int) int { (match n { case 0: g(b); case _: h(n) }) catch { case E: 40; case F: 50 } }
func viaWith(b bool, c bool) int { with W { if c { g(b) } else { 2 } } catch { case E{K: k}: k * 10 } }
func viaTry(b bool, c bool) int fails E { try 1 + (if c { g(b) } else { 2 }) }
func viaReturn(b bool, c bool) int fails E { (if c { g(b) } else { 2 }) }
func run(f func() int fails E) int fails E { try f() }
func viaBlock(b bool) int fails E { run() { (if b { g(b) } else { 5 }) } }
func main() needs Console {
	let r = viaTry(true, true) catch { case E{K: k}: k }
	let s = viaTry(false, true) catch { case E{K: k}: k }
	let u = viaReturn(true, true) catch { case E{K: k}: k + 100 }
	let v = viaReturn(false, false) catch { case E: 0 }
	let w = viaBlock(true) catch { case E{K: k}: k + 200 }
	let x = viaBlock(false) catch { case E: 0 }
	if viaIf(true, true) == 40 && viaIf(false, true) == 7 && viaIf(true, false) == 2 && viaWith(true, true) == 40 && viaWith(false, true) == 7 && viaMatch(true, 0) == 40 && viaMatch(false, 0) == 7 && viaMatch(true, 3) == 3 && r == 4 && s == 8 && u == 104 && v == 2 && w == 204 && x == 5 {
		Console.Println("agree")
	}
}
`, "agree\n")
}
