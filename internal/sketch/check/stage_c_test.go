package check_test

import (
	"fmt"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
)

// D-C1 and D-C2 at check.CheckSource: if and match yield their branches'
// last values in every position. Each negative case names the one
// diagnostic it expects, by code and line (one mistake, one diagnostic).
func TestIfAndMatchAreExpressions(t *testing.T) {
	const decls = `type E { Code int }
type A {}
type B { N int }
type U = A | B
func G() int fails E { E{Code: 1} }
`
	for _, tc := range []struct {
		name, src string
		want      string // "CODE:line", or "" for a clean program
	}{
		{"tail-if-chain", `func F(n int) int { if n < 0 { -1 } else if n == 0 { 0 } else { 1 } }`, ""},
		{"tail-match", `func F(u U) int {
	match u {
	case A:
		0
	case B{N: n}:
		n
	}
}`, ""},
		{"let-declared", `func F(c bool, t int64) int64 { let r int64 = if c { 0 } else { 3 }; t * r }`, ""},
		{"let-inferred-literals", `func F(n int) string { let w = if n > 1 { "many" } else { "one" }; w }`, ""},
		{"operand", `func F(u U) int {
	1 + match u {
	case A:
		1
	case _:
		0
	}
}`, ""},
		{"argument", `func H(n int8) int8 { n }
func F(c bool) int8 { H(if c { 1 } else { 2 }) }`, ""},
		{"union-expected", `func F(c bool) U { if c { A } else { B{N: 1} } }`, ""},
		{"return-joins", `func F(c bool) int { let x = if c { return 0 } else { 5 }; x }`, ""},
		{"break-joins", `func F(c bool) int {
	let mut n = 0
	for {
		let x = if c { break } else { 1 }
		n = n + x
	}
	n
}`, ""},
		{"void-tail-escapes", `func F(c bool) fails E { if c { G() } }`, ""},
		{"void-discards-arm", `func N() int { 1 }
func S() string { "s" }
func F(c bool) { if c { N() } else { S() } }`, ""},
		{"void-value-arm", `func N() int { 1 }
func F(c bool) { if c { 1 } else { N() } }`, "S110:7"},
		{"catch-arm-if", `func F(c bool) int {
	G() catch {
	case E as e:
		if c { e.Code } else { 0 }
	}
}`, ""},
		{"value-if-no-else", `func F(c bool) int { let r int = if c { 0 }; r }`, "S108:6"},
		{"tail-if-no-else", `func F(c bool) int {
	if c { 1 }
}`, "S107:8"},
		{"mismatch-no-cascade", `func F(c bool) string { let v = if c { 1 } else { "x" }; v }`, "S108:6"},
		// K3: variants of one union join to it; another type does not.
		{"union-join", `func F(c bool) { let s = if c { A } else { B{N: 1} }; _ = s }`, ""},
		{"no-join-outside-union", `func F(c bool) { let s = if c { A } else { E{Code: 1} }; _ = s }`, "S108:6"},
		{"arm-ends-in-let", `func F(u U) int {
	match u {
	case A:
		let x = 1
		_ = x
	case B:
		2
	}
}`, "S107:13"},
		{"untried-branch", `func F(c bool) int fails E { let x = if c { G() } else { 1 }; x }`, "S105:6"},
		{"no-value-branches", `func F(c bool) int { let x = if c { return 1 } else { return 2 }; x }`, "S108:6"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := decls + tc.src + "\n"
			_, d := check.CheckSource(src)
			var got []string
			for _, x := range d {
				got = append(got, fmt.Sprintf("%s:%d", x.Code, x.Line))
			}
			if strings.Join(got, " ") != tc.want {
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

// D-C3 at check.CheckSource: catch arms are match arms over the failure.
// A record pattern that does not match every value of its type leaves the
// type in the rest; case err: binds the rest as a row value, which is
// returned, matched or assigned to a declared union, and nothing else.
func TestOneCatchArms(t *testing.T) {
	const decls = `type NotFound { ID string }
type Declined { Reason string }
type Frozen {}
type Pay = Declined | Frozen
type Small = Declined
func F(id string) int fails NotFound | Declined | Frozen { 1 }
`
	for _, tc := range []struct {
		name, src string
		want      string
	}{
		{"record-whole", `func G() int {
	F("") catch {
	case NotFound{ID: id}:
		len(id)
	case Declined, Frozen:
		0
	}
}`, ""},
		{"record-part-leaves-rest", `func G() int {
	F("") catch {
	case NotFound{ID: "x"}:
		1
	case Declined, Frozen:
		0
	}
}`, "S103:8"},
		{"record-part-then-type", `func G() int {
	F("") catch {
	case NotFound{ID: "x"}:
		1
	case NotFound:
		2
	case _:
		0
	}
}`, ""},
		{"row-value-returned", `func G() int fails Declined | Frozen {
	F("") catch {
	case NotFound:
		0
	case err:
		return err
	}
}`, ""},
		{"row-value-escapes-undeclared", `func G() int fails Declined {
	F("") catch {
	case NotFound:
		0
	case err:
		return err
	}
}`, "S101:12"},
		{"row-value-to-union", `func G() int {
	F("") catch {
	case NotFound:
		0
	case err:
		let p Pay = err
		match p {
		case Declined:
			1
		case Frozen:
			2
		}
	}
}`, ""},
		{"row-value-to-narrow-union", `func G() int {
	F("") catch {
	case NotFound:
		0
	case err:
		let s Small = err
		_ = s
		1
	}
}`, "S108:12"},
		{"row-value-match-total", `func G() int {
	F("") catch {
	case NotFound:
		0
	case err:
		match err {
		case Declined:
			1
		}
	}
}`, "S103:12"},
		{"rest-after-whole", `func G() int {
	F("") catch {
	case NotFound, Declined, Frozen:
		0
	case err:
		return 1
	}
}`, "S108:11"},
		{"err-not-alone", `func G() int {
	F("") catch {
	case NotFound, err:
		0
	}
}`, "S108:9"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := decls + tc.src + "\n"
			_, d := check.CheckSource(src)
			var got []string
			for _, x := range d {
				got = append(got, fmt.Sprintf("%s:%d", x.Code, x.Line))
			}
			if strings.Join(got, " ") != tc.want {
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

// 196r, sixth round: try covers the whole expression to its right, so
// every fallible value in it propagates; a try over nothing fallible is
// S108.
func TestTryCoversTheWholeExpression(t *testing.T) {
	const decls = `type E {}
type X {}
func A() int fails E { 1 }
func B() int fails X { 2 }
func Id(n int) int { n }
`
	for _, tc := range []struct{ name, src, want string }{
		{"operands", `func F() int fails E | X { try A() + B() }`, ""},
		{"argument", `func F() int fails E { try Id(A()) * 2 }`, ""},
		{"let", `func F() int fails E | X { let n = try A() - B(); n }`, ""},
		{"undeclared", `func F() int fails E { try A() + B() }`, "S101:6"},
		{"nothing-fallible", `func F() int { try Id(1) + 2 }`, "S108:6"},
		{"outside-try", `func F() int fails E { Id(A()) }`, "S105:6"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := decls + tc.src + "\n"
			_, d := check.CheckSource(src)
			var got []string
			for _, x := range d {
				got = append(got, fmt.Sprintf("%s:%d", x.Code, x.Line))
			}
			if strings.Join(got, " ") != tc.want {
				t.Fatalf("want [%s], got %s", tc.want, describe(d))
			}
		})
	}
}
