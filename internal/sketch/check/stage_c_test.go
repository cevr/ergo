package check_test

import (
	"fmt"
	"sort"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
)

// A protected name in a binder names the prelude rule, in a let and in a
// match arm alike (D-D3 for every binder kind; stage D1 round 1 review
// m2).
func TestPreludeLetNamesUseBinderDiagnostic(t *testing.T) {
	// No prelude function is a name in scope (every one is an operation,
	// prelude-scope S1), so the built-in len is the case.
	for _, name := range []string{"len"} {
		t.Run(name, func(t *testing.T) {
			_, d := check.CheckSource(fmt.Sprintf("func F() { let %s = 3; _ = %s }\n", name, name))
			want := name + " is a prelude name, which cannot be redeclared; choose another name (#196, eighth round, item 3)"
			if len(d) != 1 || d[0].Code != check.CodeType || d[0].Message != want {
				t.Fatalf("want one prelude-binder diagnostic %q, got %s", want, describe(d))
			}
			_, arm := check.CheckSource(fmt.Sprintf("func F(n int) int { match n { case %s: return %s\ncase _: return 0 } }\n", name, name))
			if len(arm) != 1 || arm[0].Code != check.CodeType || arm[0].Message != want {
				t.Fatalf("want one prelude-binder diagnostic %q in a match arm, got %s", want, describe(arm))
			}
		})
	}
}

func TestMatchBranchesTargetEnclosingLoop(t *testing.T) {
	for _, tc := range []struct {
		name, src, code string
		count           int
	}{
		{"no-loop", `func F(n int) { match n { case 0: break
case _: continue } }`, check.CodeType, 2},
		{"loop", `func F(n int) int { let mut out = 0
for { match n { case 0: out = 1; break
case _: continue } }
return out }`, "", 0},
		{"loop-can-exit", `func F(n int) int { for { match n { case 0: break
case _: continue } } }`, check.CodeMissingRet, 1},
		{"nested-for", `func F(n int) int { match n { case 0: for { break }; return 1
case _: return 2 } }`, "", 0},
		{"barrier", `func F(n int) { for { with context {} { match n { case 0: break
case _: continue } } } }`, check.CodeType, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, d := check.CheckSource(tc.src + "\n")
			if len(d) != tc.count {
				t.Fatalf("want %d diagnostics, got %s", tc.count, describe(d))
			}
			for _, x := range d {
				if x.Code != tc.code {
					t.Fatalf("want %s, got %s", tc.code, describe(d))
				}
			}
		})
	}
}

func TestFunctionBodiesYieldTailExpressions(t *testing.T) {
	for _, tc := range []struct{ name, src string }{
		{"value", `func Value() int { 42 }`},
		{"context", `func Small() int8 { -128 }`},
		{"early-return", `func F(n int) int { if n < 0 { return 0 }; n }`},
		{"discard", `func N() int { 123 }
func F() { N() }`},
		{"literal", `func F() int { let f = func() int { 7 }; return f() }`},
		{"failure", `type Failed { Code int }
func F() int fails Failed { Failed{Code: 3} }`},
		{"fallible", `type Failed { Code int }
func G() int fails Failed { return Failed{Code: 3} }
func F() int fails Failed { G() }`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, d := check.CheckSource(tc.src + "\n")
			if len(d) != 0 {
				t.Fatalf("want a typed tail body, got %s", describe(d))
			}
			if gaps := check.InfoGaps(tc.src + "\n"); len(gaps) != 0 {
				t.Fatalf("Info gaps: %v", gaps)
			}
		})
	}
}

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

// A value match records its result type, and each yielding branch end is a
// Tails entry with the sink's type, so lowering assigns, never re-derives.
func TestBranchTailsInInfo(t *testing.T) {
	src := `type A {}
type B { N int }
type U = A | B
func F(u U) int64 {
	let x int64 = match u {
	case A:
		1
	case B:
		return 2
	}
	if x > 0 { x } else { 0 }
}
`
	pkg, d := check.CheckSource(src)
	if len(d) != 0 {
		t.Fatal(describe(d))
	}
	var results, tails []string
	for _, m := range pkg.Info.Matches {
		if m.Result != nil {
			results = append(results, m.Result.String())
		}
	}
	for s, ty := range pkg.Info.Tails {
		tails = append(tails, fmt.Sprintf("%T %s", s, ty))
	}
	sort.Strings(tails)
	if strings.Join(results, ",") != "int64" {
		t.Errorf("match results: %v", results)
	}
	if want := "*syntax.ExprStmt int64,*syntax.ExprStmt int64,*syntax.ExprStmt int64,*syntax.IfStmt int64"; strings.Join(tails, ",") != want {
		t.Errorf("tails:\n got %s\nwant %s", strings.Join(tails, ","), want)
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

// D-C5: a function literal spells its own rows as a declaration does,
// func() T fails E needs A | B { … }, and a written row is declared: a
// failure or a service outside it is S101 or S102, never a wider row.
func TestLiteralRowsAreDeclared(t *testing.T) {
	const decls = `type E {}
type X {}
type Svc { Do() int }
type Other { Go() int }
func G() int fails E { 1 }
func H() int fails X { 1 }
`
	for _, tc := range []struct{ name, src, want string }{
		{"written", `func F() int fails E needs Svc {
	let f = func() int fails E needs Svc { try G() + Svc.Do() }
	try f()
}`, ""},
		{"undeclared-failure", `func F() int fails E | X {
	let f = func() int fails E { H() }
	try f()
}`, "S101:8"},
		{"undeclared-service", `func F() int needs Svc | Other {
	let f = func() int needs Svc { Other.Go() }
	f()
}`, "S102:8"},
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
