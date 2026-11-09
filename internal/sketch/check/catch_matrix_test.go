package check_test

import (
	"fmt"
	"sort"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
)

// codeLines lists a source's diagnostics as code:line, sorted.
func codeLines(src string) (string, []check.Diagnostic) {
	_, d := check.CheckSource(src)
	var got []string
	for _, x := range d {
		got = append(got, fmt.Sprintf("%s:%d", x.Code, x.Line))
	}
	sort.Strings(got)
	return strings.Join(got, " "), d
}

// D-C3 (stage C round 1 review M1): a catch's arms are judged together.
// Coverage is cumulative: record patterns that together match every value
// of a type cover it, and an arm the arms before it cover is unreachable.
// The oracle enumerates the four values of E{A bool, B bool} and every
// sequence of up to three arms drawn from the nine record patterns and the
// type test.
func TestCatchCoverageIsCumulative(t *testing.T) {
	type pat struct {
		text string
		a, b int // 0 false, 1 true, 2 any
	}
	field := func(name string, v int) string {
		switch v {
		case 0:
			return name + ": false"
		case 1:
			return name + ": true"
		}
		return name + ": _"
	}
	var pats []pat
	for a := 0; a < 3; a++ {
		for b := 0; b < 3; b++ {
			pats = append(pats, pat{"E{" + field("A", a) + ", " + field("B", b) + "}", a, b})
		}
	}
	pats = append(pats, pat{"E", 2, 2})
	matches := func(p pat, a, b int) bool {
		return (p.a == 2 || p.a == a) && (p.b == 2 || p.b == b)
	}
	const head = "type E { A bool, B bool }\nfunc g() int fails E { 1 }\nfunc f() int {\n\tg() catch {\n"
	const firstArm = 5 // the line of the first arm
	check1 := func(t *testing.T, arms []pat) {
		t.Helper()
		var b strings.Builder
		b.WriteString(head)
		for i, p := range arms {
			fmt.Fprintf(&b, "\tcase %s: %d\n", p.text, i)
		}
		b.WriteString("\t}\n}\n")
		// The oracle: an arm is reachable when it matches a value no arm
		// before it matches; the catch is total when every value matches.
		var want []string
		var covered [2][2]bool
		for i, p := range arms {
			useful := false
			for a := 0; a < 2; a++ {
				for v := 0; v < 2; v++ {
					if matches(p, a, v) && !covered[a][v] {
						useful = true
					}
				}
			}
			if !useful {
				want = append(want, fmt.Sprintf("S108:%d", firstArm+i))
				continue
			}
			for a := 0; a < 2; a++ {
				for v := 0; v < 2; v++ {
					covered[a][v] = covered[a][v] || matches(p, a, v)
				}
			}
		}
		if !(covered[0][0] && covered[0][1] && covered[1][0] && covered[1][1]) {
			want = append(want, "S103:4")
		}
		sort.Strings(want)
		got, d := codeLines(b.String())
		if got != strings.Join(want, " ") {
			t.Fatalf("%s\nwant [%s], got %s", b.String(), strings.Join(want, " "), describe(d))
		}
	}
	n := 0
	var walk func(arms []pat)
	walk = func(arms []pat) {
		if len(arms) > 0 {
			check1(t, arms)
			n++
		}
		if len(arms) == 3 {
			return
		}
		for _, p := range pats {
			walk(append(arms[:len(arms):len(arms)], p))
		}
	}
	walk(nil)
	if n != 10+100+1000 {
		t.Fatalf("checked %d arm sequences", n)
	}
}

// The review's two witnesses, by name.
func TestCatchCoverageWitnesses(t *testing.T) {
	const decls = "type E { B bool }\nfunc g() int fails E { 1 }\n"
	for _, tc := range []struct{ name, src, want string }{
		{"collective-total", `func f() int { g() catch { case E{B: true}: 1; case E{B: false}: 2 } }`, ""},
		{"duplicate-partial", "func f() int {\n\tg() catch {\n\tcase E{B: true}: 1\n\tcase E{B: true}: 2\n\tcase E: 3\n\t}\n}", "S108:6"},
		{"type-after-collective", "func f() int {\n\tg() catch {\n\tcase E{B: true}: 1\n\tcase E{B: false}: 2\n\tcase E: 3\n\t}\n}", "S108:7"},
		{"rest-after-collective", "func f() int {\n\tg() catch {\n\tcase E{B: true}: 1\n\tcase E{B: false}: 2\n\tcase _: 3\n\t}\n}", "S108:7"},
		{"err-binds-partial-type", "func f() int fails E {\n\tg() catch {\n\tcase E{B: true}: 1\n\tcase err: return err\n\t}\n}", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := decls + tc.src + "\n"
			got, d := codeLines(src)
			if got != tc.want {
				t.Fatalf("want [%s], got %s", tc.want, describe(d))
			}
			if tc.want == "" {
				if gaps := check.InfoGaps(src); len(gaps) != 0 {
					t.Fatalf("Info gaps: %v", gaps)
				}
			}
		})
	}
}

// D-B3 rule 6 in a catch (review M2): every pattern of an arm binds the
// same names, a type test or record pattern that binds none included, in
// either order (S108); the name never silently reads as zero.
func TestCatchAlternativesBindTheSameNames(t *testing.T) {
	const decls = "type E { N int }\ntype F {}\nfunc g() int fails E | F { 1 }\n"
	for _, tc := range []struct{ name, src, want string }{
		{"record-then-type", "func f() int {\n\tg() catch { case E{N: x}, F: x }\n}", "S108:5"},
		{"type-then-record", "func f() int {\n\tg() catch { case F, E{N: x}: x }\n}", "S108:5"},
		{"as-then-type", "func f() int {\n\tg() catch { case E as e, F: 0 }\n}", "S108:5"},
		// Two failures join to their row value (TestCatchTreeAgrees);
		// two field types that no union lists both have no join.
		{"as-no-join", "type A { N int }\ntype B {}\ntype H { X A, Y B }\nfunc h() int fails H { 1 }\nfunc f() int {\n\th() catch { case H{X: A{N: 1} as z}, H{Y: _ as z}: 0 }\n}", "S108:9"},
		{"same-names", "type G { N int }\nfunc h() int fails E | G { 1 }\nfunc f() int {\n\th() catch { case E{N: x}, G{N: x}: x }\n}", ""},
		{"bound-twice", "func f() int {\n\tg() catch { case E{N: x} as x: 0; case F: 1 }\n}", "S108:5"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := decls + tc.src + "\n"
			got, d := codeLines(src)
			if got != tc.want {
				t.Fatalf("want [%s], got %s", tc.want, describe(d))
			}
		})
	}
}

// D-C3 (review M3): try covers the whole expression, the caught expression
// of a partial catch included: g's failure propagates while the catch
// handles h's row.
func TestTryCoversACaughtExpression(t *testing.T) {
	const decls = "type E {}\ntype X {}\nfunc g() int fails E { 1 }\nfunc h(n int) int fails X { n }\n"
	for _, tc := range []struct{ name, src, want string }{
		{"propagates", `func f() int fails E { try h(g()) catch { case X: 0 } }`, ""},
		{"undeclared", `func F() int { try h(g()) catch { case X: 0 } }`, "S101:5"},
		{"no-try", `func f() int { h(g()) catch { case X: 0 } }`, "S105:5"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := decls + tc.src + "\n"
			got, d := codeLines(src)
			if got != tc.want {
				t.Fatalf("want [%s], got %s", tc.want, describe(d))
			}
		})
	}
}

// Comparability is structural (stage C round 2 review M1, M2): a union
// compares only when every variant does, a row value and an operation
// type's value never do, and every route reads the one rule: a tuple, a
// record field, a comparable constraint and a map key. == reads
// equatability, which adds slices and Maps (eq-structural).
func TestComparabilityIsStructural(t *testing.T) {
	const decls = "type E { N []int }\ntype F {}\ntype V = E | F\ntype C { N int }\ntype U = C | F\ntype Svc { Do() int }\nfunc g() int fails E { 1 }\nfunc eq[T comparable](a T, b T) bool { a == b }\n"
	for _, tc := range []struct{ name, src, want string }{
		{"row-tuple", "func f() int {\n\tg() catch { case err: if (err, 1) == (err, 1) { 1 } else { 0 } }\n}", "S108:10"},
		{"row-generic", "func f() int {\n\tg() catch { case err: if eq(err, err) { 1 } else { 0 } }\n}", "S108:10"},
		// eq-structural: V's variant E holds a slice, so V is equatable
		// (structural ==) but not comparable (no constraint, no Map key).
		{"row-assigned-union", "func f() int {\n\tg() catch {\n\tcase err:\n\t\tlet v V = err\n\t\tif v == v { 1 } else { 0 }\n\t}\n}", ""},
		{"union-variant", "func same(a V, b V) bool { a == b }", ""},
		{"union-record-field", "type H { X V }\nfunc same(a H, b H) bool { a == b }", ""},
		{"union-generic", "func same(a V, b V) bool { eq(a, b) }", "S108:9"},
		{"union-map-key", "func f() int { let m = Map[V, int]{}; len(m) }", "S108:9"},
		{"operation-type", "func same(a Svc, b Svc) bool { a == b }", "S108:9"},
		{"comparable-union", "func same(a U, b U) bool { a == b && eq(a, b) && (a, 1) == (b, 1) }", ""},
		{"comparable-union-key", "func f() int { let m = Map[U, int]{}; len(m) }", ""},
		// A row value never compares, even when every type of its row does.
		{"row-comparable-tuple", "func g2() int fails C { 1 }\nfunc f() int {\n\tg2() catch { case err: if (err, 1) == (err, 1) { 1 } else { 0 } }\n}", "S108:11"},
		{"row-comparable-generic", "func g2() int fails C { 1 }\nfunc f() int {\n\tg2() catch { case err: if eq(err, err) { 1 } else { 0 } }\n}", "S108:11"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := decls + tc.src + "\n"
			got, d := codeLines(src)
			if got != tc.want {
				t.Fatalf("want [%s], got %s", tc.want, describe(d))
			}
		})
	}
}

// D-C3 (review M8): a row value is returned, matched, caught or assigned
// to a union that lists its types, and has no other operation.
func TestRowValueOperations(t *testing.T) {
	const decls = "type E { N int }\ntype F {}\ntype V = E | F\nfunc g() int fails E | F { 1 }\n"
	for _, tc := range []struct{ name, src, want string }{
		{"equality", "func f() int {\n\tg() catch { case err: if err == err { 1 } else { 0 } }\n}", "S108:6"},
		{"inequality", "func f() int {\n\tg() catch { case err: if err != err { 1 } else { 0 } }\n}", "S108:6"},
		{"returned", "func f() int fails E | F {\n\tg() catch { case err: return err }\n}", ""},
		{"matched", "func f() int {\n\tg() catch { case err: match err { case E: 1; case F: 2 } }\n}", ""},
		{"assigned", "func f() V {\n\tg() catch {\n\tcase err:\n\t\tlet v V = err\n\t\treturn v\n\t}\n\tF\n}", ""},
		{"returned-as-union", "func f() V {\n\tg() catch { case err: return err }\n\tF\n}", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := decls + tc.src + "\n"
			got, d := codeLines(src)
			if got != tc.want {
				t.Fatalf("want [%s], got %s", tc.want, describe(d))
			}
		})
	}
}
