package check_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"ergo.local/prototype/internal/sketch/check"
)

// reviewCases reproduce the round-1 review probes (round-1-findings.md) and
// the repairs' own cases. Each source is a decided file body (#196); want
// is the exact sequence of codes, "" for none.
var reviewCases = []struct {
	finding, name, want, src string
}{
	{"M1", "inference-chain-through-values", "S108", ""}, // built by largeSCC; class G (round 14)
	{"M2", "machine-initial-inferred", "S102", `type C { Use() }
type S = Idle
type Idle {}
type E = Ping
type Ping {}
type M machine[S, E] { initial: initial(), step: step }
func initial() S {
	C.Use()
	return Idle
}
func step(s S, e E) Decision[S, int] { return Ignore }`},
	{"M2", "package-var-inferred", "S102", `type C { Use() string }
let v = helper()
func helper() string { return C.Use() }`},
	{"M3", "error-row-package-var", "S101", `type Fault {}
func Identity(v Fault) Fault { return v }
let _ = func() fails Fault { return Fault }`},
	{"M3", "generic-row-body-order", "S101", `type Fault[T] { Value T }
func Identity(v Fault[int]) Fault[int] { return v }
func F()                               { _ = func() fails Fault[int] { return Fault[int]{1} } }`},
	{"M3", "local-error-row", "S101", `type Fault {}
func Identity(v Fault) Fault { return v }
func F()                     { _ = func() fails Fault { return Fault } }`},
	{"M4", "error-union-rethrow", "S108", `type A {}
type B {}
func Fail() fails A | B { return A }
func F() fails A | B {
	Fail() catch {
	case A as e, B as e:
		return e
	}
}`},
	{"M4", "catch-union-switch", "S108", `type A {}
type B {}
func Fail() fails A | B { return A }
func F() {
	Fail() catch {
	case A as e, B as e:
		match e {
		case A:
			return
		case B:
			return
		}
	}
}`},
	{"M4", "catch-union-switch-not-total", "S108", `type A {}
type B {}
func Fail() fails A | B { return A }
func F() {
	Fail() catch {
	case A as e, B as e:
		match e {
		case A:
			return
		}
	}
}`},
	{"M4", "error-union-as-value", "S108 S101", `type A {}
type B {}
type V = A | B
func Fail() fails A | B { return A }
func F() V {
	Fail() catch {
	case A as e, B as e:
		return e
	}
	return A
}`},
	{"M5", "generic-named-constraint-inferred", "S108", `type I { M() }
type S {}
func M(_ S) {}
type Box[T I] { X T }
func F() { _ = Box{1} }`},
	{"M5", "generic-constraint-method-row", "S108", `type A {}
type B {}
type I { M() fails A }
type S {}
func M(_ S) fails B { return B }
type Box[T I] { X T }
func F() { _ = Box{S} }`},
	{"M5", "generic-constraint-inferred-ok", "", `type I { M() }
type S {}
func M(_ S) {}
type Box[T I] { X T }
func F() { _ = Box{S} }`},
	{"M6", "implicit-row-in-generic-argument", "", `func Await[T, E](t Task[T, E]) T fails E { return t.Wait() }`},
	// D3a: a record field holds no Task, so the field is a function type.
	{"M6", "bracket-row-in-generic-field", "", `type Box[T, E] { run func() T fails E }
func Open[T, E](b Box[T, E]) T fails E { return b.run() }`},
	{"M6", "row-kind-conflict-still-reported", "S108", `func Bad[T, E](t Task[T, E], e E) T { return t.Wait() }`},
	{"M7", "generic-recursive-value", "S108", `type Wrap[T] { Value T }
type U { Field Wrap[U] }
func F() U { return U{} }`},
	{"M7", "generic-recursive-constraint", "S108", `type Wrap[T] { Value T }
type U { Field Wrap[U] }`},
	{"M7", "generic-recursive-through-slice", "", `type Wrap[T] { Values []T }
type U { Field Wrap[U] }`},
	{"M7", "generic-self-recursive", "S108", `type L[T] { next L[T] }`},
	{"M7", "generic-growing-instance", "S108", `type L[T] { next L[[]T] }`},
	{"M7", "generic-pair-cycle", "S108", `type A[T] { b B[T] }
type B[T] { a A[T] }`},
	{"M9", "duplicate-local-unused", "S108", `func F() {
	let x = 1
	let x = 2
	_ = x
}`},
	{"M9", "inner-block-shadows", "", `func F() int {
	let x = 1
	if true {
		let x2 = "s"
		_ = x2
	}
	return x
}`},
	{"M10", "function-value-explicit", "", `func Id[T](x T) T { return x }
func F() func(int) int { return Id[int] }`},
	{"M10", "function-value-explicit-constraint", "S108", `type I { M() }
func Id[T I](x T) T    { return x }
func F() func(int) int { return Id[int] }`},
	{"M10", "function-value-wrong-type", "S108", `func Id[T](x T) T { return x }
func F() func(string) string { return Id[int] }`},
	{"M10", "function-value-called", "", `func Id[T](x T) T { return x }
func F() int {
	let f = Id[int]
	return f(3) + Id[int](4)
}`},
	{"M10", "function-value-discarded-tail", "S110", `func Id[T](x T) T { return x }
func F() { Id[int] }`},
	{"M10", "function-value-before-tail", "S110", `func Id[T](x T) T { return x }
func F() { Id[int]; _ = 0 }`},
	{"M10", "function-value-implicit", "S108", `func Await[T, E](x T, t Task[T, E]) T fails E { return t.Wait() }
func F() { _ = Await[int] }`},
	{"M11", "index-negative", "S108", `func F(xs []int) int { return xs[-1] }`},
	{"M11", "slice-bounds", "S108", `func F(xs []int) []int { return xs[3:1] }`},
	{"M11", "string-index-out-of-bounds", "S108", `func F() byte { return "ab"[2] }`},
	{"M11", "string-slice-out-of-bounds", "S108", `func F() string { return "ab"[1:3] }`},
	{"M11", "bounds-ok", "", `func F(xs []int) []int { return xs[1:3] }`},
	{"M12", "float-rounding", "S108", `func F() int { return 1 / int(float32(16777217) - float32(16777216)) }`},
	{"M12", "float-rounding-arithmetic", "S108", `func F() int { return 1 / int(float32(16777216) + 1 - float32(16777216)) }`},
	{"M12", "float64-exact-enough", "", `func F() int { return 1 / int(float64(16777217) - float64(16777216)) }`},
	{"M13", "embedded-collision-replaced", "", `type I { M() }
type S {}
func M(_ S) {}
let A = context { I: S }
let B = context { I: S }
let W = context { replace I: S, A, B }
func F() { with W { I.M() } }`},
	{"M13", "embedded-collision-inline-replaced", "", `type I { M() }
type S {}
func M(_ S) {}
let A = context { I: S }
let B = context { I: S }
func F() { with context { replace I: S, A, B } { I.M() } }`},
	{"M13", "embedded-collision-unresolved", "S111", `type I { M() }
type S {}
func M(_ S) {}
let A = context { I: S }
let B = context { I: S }
let W = context { A, B }
func F() { with W { I.M() } }`},
	// Two replaces of one capability in one context are S111 since check-v1
	// (round-3 review M2): a with binds each capability to one provider.
	{"M13", "context-double-replace", "S111", `type I { M() }
type S {}
func M(_ S) {}
let Base = context { I: S }
let W = context { replace I: S, replace I: S, Base }
func F() { with W { I.M() } }`},
	{"M14", "named-context-internal-capability", "", `type A { M() }
type B { N() }
type S {}
func M(_ S) {}
func N(_ S) {}
func Make() B needs A {
	A.M()
	return S
}
let W = context { A: S, B: Make() }
func F() { with W { B.N() } }`},
	{"M14", "named-context-internal-cycle", "S108", `type A { M() }
type B { N() }
type S {}
func M(_ S) {}
func N(_ S) {}
func MakeA() A needs B {
	B.N()
	return S
}
func MakeB() B needs A {
	A.M()
	return S
}
let W = context { A: MakeA(), B: MakeB() }
func F() { with W { B.N() } }`},
	{"M14", "named-context-caller-capability", "S102", `type A { M() }
type B { N() }
type S {}
func N(_ S) {}
func Make() B needs A {
	A.M()
	return S
}
let W = context { B: Make() }
func F() needs A { with W { B.N() } }`},
	{"M14", "named-context-self-cycle", "S108", `type A { M() }
type S {}
func M(_ S) {}
func MakeA() A needs A {
	A.M()
	return S
}
let W = context { A: MakeA() }
func F() { with W { A.M() } }`},
	{"M14", "named-context-three-cycle", "S108", `type A { M() }
type B { N() }
type C { O() }
type S {}
func M(_ S) {}
func N(_ S) {}
func O(_ S) {}
func MakeA() A needs C {
	C.O()
	return S
}
func MakeB() B needs A {
	A.M()
	return S
}
func MakeC() C needs B {
	B.N()
	return S
}
let W = context { A: MakeA(), B: MakeB(), C: MakeC() }
func F() { with W { B.N() } }`},
	{"M14", "named-context-needs-embedded", "", `type A { M() }
type B { N() }
type S {}
func M(_ S) {}
func N(_ S) {}
func Make() B needs A {
	A.M()
	return S
}
let Base = context { A: S }
let W = context { B: Make(), Base }
func F() { with W { B.N() } }`},
	{"M14", "inline-context-stays-open", "S102", `type A { M() }
type B { N() }
type S {}
func M(_ S) {}
func N(_ S) {}
func Make() B needs A {
	A.M()
	return S
}
func F() { with context { A: S, B: Make() } { B.N() } }`},
	{"M15", "main-through-alias", "S113", `func main() {}
func F() {
	let run = main
	run()
}`},
	{"M15", "main-as-argument", "S113", `func main() {}
func call(f func()) { f() }
func F() { call(main) }`},
	{"M15", "main-returned", "S113", `func main() {}
func F() func() { return main }`},
	{"M15", "main-called-in-parens", "S113", `func main() {}
func F() { (main)() }`},
	{"M15", "local-main-is-ordinary", "", `func main() {}
func F() {
	let _ as main = func() {}
	main()
}`},
	{"M16", "with-default-hint", "", `type Box[T] { X T }
func F() Box[int] { return with context {} { Box{} } }`},
	{"M17", "map-index-field-assignment", "S108", `type S { X int }
func F(m Map[int, S]) { m[0].X = 1 }`},
	{"M17", "map-index-assignment", "", `type S { X int }
func F(m Map[int, S], n Map[int, int]) {
	let mut m2 = m
	let mut n2 = n
	m2[0] = S{}
	m2[1] = S{X: 2}
	n2[3] = S{}.X
}`},
	{"M17", "map-index-field-increment", "S108", `type S { X int }
func F(m Map[int, S]) { m[2].X += 1 }`},
	// An op-assignment to a Map entry reads it first: a Map read is
	// refused (stage D2 design ruling), once per site.
	{"M17", "map-value-increment", "S108 S108", `func F(m Map[int, int]) {
	let mut m2 = m
	m2[0] += 1
	m2[1] += 2
}`},
	{"M17", "slice-element-field", "", `type S { X int }
func F(xs []S) {
	let mut ys = xs
	ys[0].X = 1
}`},
	{"M17", "map-index-address", "S108", `type S { X int }
func G(s *S)          {}
func F(m Map[int, S]) { G(&m[0]) }`},
	{"M18", "unknown-context-cascade", "S104", `func F() { with(Missing) { Console.Println("x") } }`},
	{"M18", "unknown-context-cascade-in-context", "S104", `func F() { with(context{ Missing }) { Console.Println("x") } }`},
	{"M18", "unknown-context-cascade-named", "S104", `let W = context { Missing }
func F() { with W { Console.Println("x") } }`},
	{"N1", "pointer-receiver-parameter", "", `type I { M() }
type S {}
func M(s *S) {}
func Take(x I) {}
func F(s *S) { Take(s) }`},
	{"N1", "pointer-receiver-value", "S108", `type I { M() }
type S {}
func M(s *S) {}
func Take(x I) {}
func F(s S) { Take(s) }`},
	{"N2", "complex-types", "", `func F() complex128 { return complex128(1) }
func G(a, b complex64) complex64 { return a*b + a/b - b }
func H(a complex128) bool { return a == a }`},
	{"N2", "complex-ordering", "S108", `func F(a, b complex128) bool { return a < b }`},
	{"N2", "complex-conversions", "", `func F(a complex128, b complex64) complex64 { return complex64(a) + b + complex64(complex128(2)) }`},
	{"N2", "float-to-complex-value", "S108", `func F(f float64) complex128 { return complex128(f) }`},
	{"N2", "complex-remainder", "S108", `func F(a, b complex128) complex128 { return a % b }`},
	{"N2", "complex64-overflow", "S108", `func F() complex64 { return 1 << 200 }`},
	{"N3", "map-duplicate-key", "S108", `func F() Map[int, int] { return Map[int, int]{1: 1, 1: 2} }`},
	{"N3", "map-duplicate-string-key", "S108", `func F() Map[string, int] { return Map[string, int]{"a": 1, "a": 2} }`},
	{"N3", "map-converted-key", "S108", `func F() Map[int64, int] { return Map[int64, int]{1: 1, int64(1): 2} }`},
	{"N3", "map-distinct-keys", "", `func F(k int) Map[int, int] { return Map[int, int]{1: 1, 2: 2, k: 3, k: 4} }`},
	{"N4", "paren-lhs", "", `func F() int {
	let mut x = 1
	(x) = 2
	return x
}`},
	// Cascades and accepted forms the review logged without a contract.
	{"D3", "unknown-import-cascade", "S104", `import missing
func F() { missing.Call() }`},
	{"D3", "unknown-named-import-cascade", "S104", `import m missing/deep
func F() { m.Call() }`},
	{"D3", "go-import-cascade", "S109", `import go fmt
func F() { fmt.Println("x") }`},
	{"D3", "unknown-import-type-cascade", "S104", `import missing
func F(x missing.T) {}`},
	{"D3", "unknown-type-unused-cascade", "S104", `func F() { let x Missing }`},
	{"D3", "unknown-callee-return-cascade", "S104", `func F() int { return missing() }`},
	{"D3", "invalid-field-cascade", "S108", `type S { X int }
func F() S { return S{X: "x"} }`},
	{"D3", "switch-no-value-cascade", "S110", `type A {}
type B {}
type U = A | B
func F(x U) int {
	match x {
	case A as v:
		return 1
	case B:
		return 2
	}
}`},
	{"D2", "inferred-no-value-return", "S108", `func f() {}
func F() { return f() }`},
	{"D2", "numeric-constraint-literal", "S108", `func Zero[T comparable](x T) T { return 0 }`},
	{"D6", "generic-two-rows", "", `type A {}
type B {}
func First() fails A { return A }
func Second() fails B { return B }
func Both[E](f func() fails E, g func() fails E) fails E {
	try f()
	return g()
}
func F() fails A | B { return Both(First, Second) }`},
	{"D6", "generic-two-rows-too-wide", "S101", `type A {}
type B {}
func First() fails A { return A }
func Second() fails B { return B }
func Both[E](f func() fails E, g func() fails E) fails E {
	try f()
	return g()
}
func F() fails A { return Both(First, Second) }`},
}

func codesOf(diags []check.Diagnostic) string {
	var codes []string
	for _, d := range diags {
		codes = append(codes, d.Code)
	}
	return strings.Join(codes, " ")
}

func TestReviewCases(t *testing.T) {
	for _, c := range reviewCases {
		t.Run(c.finding+"/"+c.name, func(t *testing.T) {
			src := c.src
			if src == "" {
				src = largeSCC(100, true)
			} else {
				src = src + "\n"
			}
			_, diags := check.CheckSource(src)
			got := codesOf(diags)
			if got != c.want {
				t.Errorf("want %q, got %q\n\t%s", c.want, got, describe(diags))
			}
		})
	}
}

// largeSCC is the M1 probe: f0 calls f1…fn (or names them as values) and
// calls C.Use(); each fi calls f(i-1); exported F calls fn, so the inferred
// row must reach F. Named as values, f1…fn are used as values inside their
// own group (class G, round 14): their rows are pinned empty, and f1, whose
// body calls f0, gets the one S108.
func largeSCC(n int, values bool) string {
	var b strings.Builder
	b.WriteString("type C { Use() }\nfunc f0() {\n")
	for i := 1; i <= n; i++ {
		if values {
			fmt.Fprintf(&b, "\t_ = f%d\n", i)
		} else {
			fmt.Fprintf(&b, "\tf%d()\n", i)
		}
	}
	b.WriteString("\tC.Use()\n}\n")
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, "func f%d() { f%d() }\n", i, i-1)
	}
	fmt.Fprintf(&b, "func F() { f%d() }\n", n)
	return check.Decided(b.String())
}

// TestInferenceConverges: every function of the cycle gets the full row,
// whatever the cycle's length (M1).
func TestInferenceConverges(t *testing.T) {
	for _, n := range []int{10, 100, 400} {
		pkg, diags := check.CheckSource(largeSCC(n, false))
		if codesOf(diags) != "S102" {
			t.Fatalf("n=%d: want one S102, got %s", n, describe(diags))
		}
		for _, f := range pkg.Funcs {
			if f.Name == "F" || f.Name == "main" {
				continue
			}
			if !strings.Contains(f.Sig.String(), "main.C") {
				t.Fatalf("n=%d: %s has row %s, want main.C", n, f.Name, f.Sig)
			}
		}
	}
}

// oddCycleSwitch is the M8 probe: n subjects of union A | B, with cases that
// cover equal adjacent subjects around an odd cycle. It is total, and no case
// is redundant.
func oddCycleSwitch(n int) string {
	var b strings.Builder
	b.WriteString("type A {}\ntype B {}\ntype U = A | B\nfunc F(")
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, "x%d U", i)
	}
	b.WriteString(") int {\nmatch (")
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, "x%d", i)
	}
	b.WriteString(") {\n")
	for i := 0; i < n; i++ {
		for _, variant := range []string{"A", "B"} {
			b.WriteString("case (")
			for j := 0; j < n; j++ {
				if j > 0 {
					b.WriteString(",")
				}
				if j == i || j == (i+1)%n {
					b.WriteString(variant)
				} else {
					b.WriteString("_")
				}
			}
			b.WriteString("): return 1\n")
		}
	}
	b.WriteString("}\n}\n")
	return check.Decided(b.String())
}

// TestTotalityCost: the odd-cycle family stays clean and cheap (M8, D16).
// The bound is generous for a loaded gate machine; before the repair the
// 21-subject case took about 4.9 s.
func TestTotalityCost(t *testing.T) {
	for _, n := range []int{11, 15, 19, 21, 41} {
		src := oddCycleSwitch(n)
		start := time.Now()
		ts, us, diags := check.SolverSteps(src)
		elapsed := time.Since(start)
		t.Logf("subjects=%d bytes=%d elapsed=%s total=%v useful=%v budgets=%v", n, len(src), elapsed, ts, us, check.TotalityBudgets(src))
		if len(diags) != 0 {
			t.Fatalf("n=%d: %s", n, describe(diags))
		}
		if elapsed > 500*time.Millisecond {
			t.Fatalf("n=%d: %s, want under 500ms", n, elapsed)
		}
	}
	// An even cycle is two-colourable with unequal neighbours: not total, and
	// an alternating vector is the witness. Its closing edge's two cases are
	// unreachable (the path before them already forces x0 != x19). No S107
	// follows the S103 (D3; round-2 N1).
	src := oddCycleSwitch(20)
	_, diags := check.CheckSource(src)
	if got := codesOf(diags); got != "S103 S108 S108" {
		t.Fatalf("even cycle: want S103 S108 S108, got %s", describe(diags))
	}
	if !strings.Contains(diags[0].Message, "(main.A, main.B, main.A, main.B") && !strings.Contains(diags[0].Message, "(main.B, main.A, main.B, main.A") {
		t.Fatalf("even cycle: want an alternating witness, got %s", diags[0].Message)
	}
}
