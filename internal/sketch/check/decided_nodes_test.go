package check_test

import (
	"fmt"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
)

// decidedFamilies is every node and field family the decided parser builds
// (spec D3; merge round step 3): each has at least one row in
// TestDecidedNodes, with its verdict, so a family without one fails the
// test. A family the 196a checker reads is checked; any other is S109
// naming its stage, with no S108 or S104 beside it (round 6 review M2).
var decidedFamilies = []string{
	"import path", "import go", "import std",
	"let", "let mut", "let without value", "let pattern", "let tuple pattern",
	"package let pattern", "package let tuple pattern",
	"context declaration", "context header", "context literal", "inline context header", "context entries",
	"with", "with parentheses", "with several contexts",
	"record type", "operation type", "law", "union", "variant in place", "machine",
	"defined type", "type parameters", "ordered constraint",
	"fails and needs", "escaping", "tuple type", "tuple value", "Map type",
	"first-parameter function", "extern",
	"test", "assert",
	"if value", "match value", "value match", "type match", "tuple subject",
	"record pattern", "as pattern", "nested as pattern",
	"catch arm", "catch-all binding",
	"for in", "for pattern", "for tuple pattern", "counting range", "range value", "for condition",
	"assignment", "break and continue",
	"function literal", "untyped parameter",
	"interpolation", "float literal",
	"core forms", "doc comments",
}

// A decidedCase is one row of TestDecidedNodes: its family, its name, its
// source and its verdict, each "code:line", sorted as the checker reports
// them.
type decidedCase struct {
	family string
	name   string
	src    string
	want   []string
}

// decidedCases are the rows of TestDecidedNodes; TestDecidedNodeInventory
// reads the trees they parse to.
var decidedCases = []decidedCase{
	{"import path", "an unquoted import path", `
import timeout
func f() timeout.Expired { return timeout.Expired }
func main() {}
`, nil},
	{"import go", "a Go import is #202's", `
import go strings
func main() {}
`, []string{"S109:2"}},
	{"import std", "an import of a std module", `
import task
func main() { let t = task.Go() { 1 }; t.Stop() }
`, nil},
	{"import go", "a refused import adds no cascade", `
import go task
type Bang {}
func a() { task.Go() { b() }; return Bang }
func b() { return a() }
func F() fails Bang { return b() }
func main() {}
`, []string{"S109:2"}},
	{"let", "let names a value", `
func f() int64 {
	let x int64 = 1
	let y = x + 1
	return y
}
func main() {}
`, nil},
	{"let mut", "let mut is assigned", `
func f() int64 {
	let mut x int64 = 1
	x += 2
	return x
}
func main() {}
`, nil},
	{"let without value", "let with a type and no value takes the zero value", `
func f() int64 {
	let x int64
	return x
}
func main() {}
`, nil},
	{"let pattern", "a record pattern after let destructures irrefutably", `
type P { X int, Y int }
func f(p P) int {
	let P{X: x, Y: y} = p
	return x + y
}
func main() {}
`, nil},
	{"let tuple pattern", "a tuple pattern after let, and its value is checked", `
func f() int {
	let (a, b) = (1, undefinedName)
	return a + b
}
func main() {}
`, []string{"S104:3"}},
	{"package let pattern", "a package-level record pattern checks its value", `
type P { X int, Y int }
let P{X: a, Y: b} = P{X: 1, Y: "no"}
func f() int { return a + b }
func main() {}
`, []string{"S108:3"}},
	{"package let tuple pattern", "a package-level tuple pattern is never accepted unchecked", `
let (a, b) = (1, undefinedName)
func main() {}
`, []string{"S104:2"}},
	{"context declaration", "let W = context", `
type Ticker { Now() int64 }
type Fixed { t int64 }
func Now(f Fixed) int64 { return f.t }
let W = context { Ticker: Fixed{t: 1} }
func f() int64 { return with W { Ticker.Now() } }
func main() {}
`, nil},
	{"context header", "a named context's provides and needs", `
type Ticker { Now() int64 }
type Log { Write(s string) }
type Fixed { t int64 }
func Now(f Fixed) int64 { return f.t }
let W = context provides Ticker needs Log { Ticker: Fixed{t: 1} }
func main() {}
`, []string{"S109:6", "S109:6"}},
	{"context literal", "a local context (E5): checked at its let, built at its with; a literal elsewhere is S108, with no cascade", `
type Ticker { Now() int64 }
type Fixed { t int64 }
func Now(f Fixed) int64 { return f.t }
func f() int64 {
	let w = context { Ticker: Fixed{t: 1} }
	return with w { Ticker.Now() }
}
func g() int64 { return use(context { Ticker: Fixed{t: 2} }) }
func use(n int64) int64 { n }
func main() {}
`, []string{"S108:9"}},
	{"inline context header", "an inline context's provides", `
type Ticker { Now() int64 }
type Fixed { t int64 }
func Now(f Fixed) int64 { return f.t }
func f() int64 { return with context provides Ticker { Ticker: Fixed{t: 1} } { Ticker.Now() } }
func main() {}
`, []string{"S109:5"}},
	{"context entries", "an entry of two keys, an embedding and a replace", `
type Ticker { Now() int64 }
type Timer { Now() int64 }
type Fixed { t int64 }
func Now(f Fixed) int64 { return f.t }
let Base = context { Ticker, Timer: Fixed{t: 1} }
let W = context { replace Ticker: Fixed{t: 2}, Base }
func f() int64 { return with W { Ticker.Now() + Timer.Now() } }
func main() {}
`, nil},
	{"with", "with W { … } discharges W's capabilities", `
type Log { Write(s string) }
type Quiet {}
func Write(q Quiet, s string) {}
let W = context { Log: Quiet }
func f() { with W { Log.Write("x") } }
func main() {}
`, nil},
	{"with parentheses", "with (W)", `
type Log { Write(s string) }
type Quiet {}
func Write(q Quiet, s string) {}
let W = context { Log: Quiet }
func f() { with (W) { Log.Write("x") } }
func main() {}
`, nil},
	{"with several contexts", "with A, B is with A { with B { … } } (E5)", `
type Log { Write(s string) }
type Quiet {}
func Write(q Quiet, s string) {}
let A = context { Log: Quiet }
let B = context { Log: Quiet }
func f() { with A, B { Log.Write("x") } }
func main() {}
`, nil},
	{"record type", "a record and an empty record", `
type P { X int, Y int }
type E {}
func f() P { _ = E; return P{X: 1, Y: 2} }
func main() {}
`, nil},
	{"operation type", "an operation type as a capability", `
type Log { Write(s string) }
func f() needs Log { Log.Write("x") }
func main() {}
`, nil},
	{"law", "a law in an operation type", `
type Monoid {
	Combine(a int, b int) int,
	law Assoc(a int, b int, c int) {
		assert true
		Combine(Combine(a, b), c) == Combine(a, Combine(b, c))
	},
}
func main() {}
`, nil},
	{"union", "a union of declared records", `
type A {}
type B {}
type U = A | B
func f(u U) int {
	match u {
	case A:
		return 1
	case B:
		return 2
	}
}
func main() {}
`, nil},
	{"variant in place", "a variant declared in a union", `
type U = Paid{ ID string } | Pending{}
func main() {}
`, nil},
	{"machine", "a machine type", `
type Closed {}
type Open {}
type State = Closed | Open
type Trip {}
type Signal = Trip
func step(s State, sig Signal) Decision[State, string] {
	match s {
	case Closed:
		return Enter{Open}
	case Open:
		return Done{"open"}
	}
}
type Breaker machine[State, Signal] {
	initial: Closed,
	step:    step,
}
func main() {}
`, nil},
	{"defined type", "a defined type and its conversion", `
type Celsius float64
type Name string
func f(n int) Name { return Name("\(n)") }
func g() Celsius { return Celsius(1.5) }
func main() {}
`, nil},
	{"type parameters", "bracket kinds by position", `
func pass[A, E, R](f func() A fails E needs R) A fails E needs R { return try f() }
func main() {}
`, nil},
	{"type parameters", "a bracket parameter of two kinds", `
func k[E](a E) fails E { return }
func main() {}
`, []string{"S108:2"}},
	{"ordered constraint", "ordered is checked (stage E1): > is defined on T", `
func max[T ordered](a T, b T) T {
	if a > b {
		return a
	}
	return b
}
func f() int { return max(1, 2) }
func main() {}
`, nil},
	{"fails and needs", "a written row", `
type Bang {}
type Log { Write(s string) }
func f() int fails Bang needs Log {
	Log.Write("x")
	return Bang
}
func main() {}
`, nil},
	{"escaping", "an escaping function type (196e, check-v1 stage E2)", `
func f(g escaping func(int) int) int { return g(1) }
func main() {}
`, nil},
	{"tuple type", "a tuple result type", `
func pair() (int, string) { return pair() }
func main() {}
`, nil},
	{"tuple value", "a tuple value", `
func f() { _ = (1, "a") }
func main() {}
`, nil},
	{"Map type", "append and delete are library calls", `
func f(m Map[string, int64], xs []int64) []int64 {
	m.Delete("a")
	return xs.Append(1).Append(2)
}
func main() {}
`, nil},
	{"Map type", "append checks its elements", `
func f(xs []int64) []int64 { return xs.Append("a") }
func main() {}
`, []string{"S108:2"}},
	{"Map type", "delete checks its key", `
func f(m Map[string, int64]) { m.Delete(1) }
func main() {}
`, []string{"S108:2"}},
	{"Map type", "append is only called", `
func f(xs []int64) { let g = xs.Append }
func main() {}
`, []string{"S104:2"}},
	{"first-parameter function", "x.f(a) reaches f on x's type", `
type Door { open bool }
func knock(d Door, n int) bool { return d.open }
func f(d Door) bool { return d.knock(1) }
func main() {}
`, nil},
	{"extern", "an extern go binding", `
extern go example.com/door.Fail func fail() int
func f() int { return fail() }
func main() {}
`, []string{"S109:2"}},
	{"test", "test declaration", `
test Adds {
	assert 1 + 1 == 2
}
func main() {}
`, nil},
	{"assert", "assert in a function body (196l: S108 outside a test or law)", `
func f() {
	assert true
}
func main() {}
`, []string{"S108:3"}},
	{"if value", "an if in value position yields its branch (D-C2)", `
func f(c bool) int {
	let x = if c { 1 } else { 2 }
	return x
}
func main() {}
`, nil},
	{"match value", "a match in value position yields its arm (D-C2)", `
func f(n int) int {
	let x = match n {
	case 1: 10
	case _: 20
	}
	return x
}
func main() {}
`, nil},
	{"value match", "a match over literal patterns", `
func f(n int) int {
	match n {
	case 1:
		return 10
	case _:
		return 20
	}
}
func main() {}
`, nil},
	{"type match", "as binds the variant", `
type A { X int }
type B {}
type U = A | B
func f(u U) int {
	match u {
	case A as a:
		return a.X
	case B:
		return 0
	}
}
func main() {}
`, nil},
	{"as pattern", "as under _ binds the subject", `
type A {}
type B {}
type U = A | B
func g(u U) int { return 1 }
func f(u U) int {
	match u {
	case A:
		return 0
	case _ as rest:
		return g(rest)
	}
}
func main() {}
`, nil},
	{"as pattern", "an unread as binding", `
type A {}
type B {}
type U = A | B
func f(u U) int {
	match u {
	case A as a:
		return 1
	case B:
		return 0
	}
}
func main() {}
`, []string{"S110:7"}},
	{"tuple subject", "as in a tuple case", `
type A { X int }
type B {}
type U = A | B
func f(u U, v U) int {
	match (u, v) {
	case (A as a, A):
		return a.X
	case _:
		return 0
	}
}
func main() {}
`, nil},
	{"record pattern", "record pattern", `
type A { X int }
type B {}
type U = A | B
func f(u U) int {
	match u {
	case A{X: x}:
		return x
	case B:
		return 0
	}
}
func main() {}
`, nil},
	{"nested as pattern", "as in a case of several patterns", `
type A {}
type B {}
type U = A | B
func f(u U) int {
	match u {
	case A as a, B as b:
		return 0
	}
}
func main() {}
`, []string{"S108:7", "S108:7"}},
	{"catch arm", "catch arm with as", `
type NotFound { ID string }
type Gone {}
func load() int fails NotFound | Gone { return 1 }
func f() string {
	let n = load() catch {
	case NotFound as e:
		return e.ID
	case Gone:
		return "gone"
	}
	return "ok \(n)"
}
func main() {}
`, nil},
	{"catch-all binding", "case err: binds the row value (D-C3)", `
type Gone {}
func load() int fails Gone { return 1 }
func f() int fails Gone {
	return load() catch {
	case err:
		return err
	}
}
func main() {}
`, nil},
	{"for in", "element and index bindings", `
func f(xs []int64, m Map[string, int64]) int64 {
	let mut s int64 = 0
	for x in xs {
		s += x
	}
	for i, x in xs {
		s += x + int64(i)
	}
	for v in m {
		s += v
	}
	return s
}
func main() {}
`, nil},
	{"for pattern", "a record pattern after for; its body is checked", `
type P { X int, Y int }
func f(ps []P) int {
	let mut s = 0
	for P{X: x, Y: y} in ps {
		s += x + y + undefinedA
	}
	return s
}
func main() {}
`, []string{"S104:6"}},
	{"for tuple pattern", "a tuple pattern after for over int elements", `
func f(xs []int) int {
	let mut s = 0
	for (a, b) in xs {
		s += a + b
	}
	return s
}
func main() {}
`, []string{"S108:4"}},
	{"counting range", "counting range", `
func sum(n int64) int64 {
	let mut s int64 = 0
	for i in 0..n {
		s += i
	}
	return s
}
func main() {}
`, nil},
	{"counting range", "a range's bounds are integers", `
func f() {
	for i in "a".."b" {
		let _ = i
	}
}
func main() {}
`, []string{"S108:3"}},
	{"range value", "range value", `
func f() int {
	let r = 1..2
	r.End
}
func main() {}
`, nil},
	{"for condition", "for with a condition and for alone", `
func f() int {
	let mut n = 0
	for n < 3 {
		n += 1
	}
	for {
		return n
	}
}
func main() {}
`, nil},
	{"assignment", "= and op=", `
func f() int {
	let mut n = 1
	n = 2
	n *= 3
	return n
}
func main() {}
`, nil},
	{"break and continue", "break and continue in a loop", `
func f(xs []int) int {
	let mut s = 0
	for x in xs {
		if x < 0 {
			continue
		}
		if x > 9 {
			break
		}
		s += x
	}
	return s
}
func main() {}
`, nil},
	{"function literal", "a typed function literal", `
func f() int {
	let g = func(a int, b int) int { return a + b }
	return g(1, 2)
}
func main() {}
`, nil},
	{"untyped parameter", "an untyped function literal parameter with no expected type is S108 (D-E1)", `
func f() { _ = func(x) {} }
func main() {}
`, []string{"S108:2"}},
	{"interpolation", "interpolation is a string", `
func label(n int, ok bool) string { return "n = \(n), ok = \(ok), \("s"), \(7)" }
func main() {}
`, nil},
	{"interpolation", "interpolation reads each value", `
type Bang {}
func g() int fails Bang { return 1 }
func label() string { return "g = \(g())" }
func main() {}
`, []string{"S105:4"}},
	{"interpolation", "interpolation through try", `
type Bang {}
func g() int fails Bang { return 1 }
func label() string fails Bang { return "g = \(try g())" }
func main() {}
`, nil},
	{"float literal", "a float literal takes a float type; two literals take their defaults", `
func f() float64 {
	let x float64 = 1.5
	let y = 2.5e-1 + 1.0
	return x * y * 2.0
}
func main() {}
`, nil},
	{"float literal", "a float constant an integer cannot hold", `
func f() int { return 1.5 }
func main() {}
`, []string{"S108:2"}},
	// Round 7 review T2: the node types and fields the inventory
	// (TestDecidedNodeInventory) found no row building.
	{"fails and needs", "try passes a failure on", `
type Bang {}
func g() int fails Bang { return 1 }
func f() int fails Bang {
	let n = try g()
	return n + 1
}
func main() {}
`, nil},
	{"core forms", "the expressions and statements Go shares", `
type P { X int, Y int }
func g(p *P) int { return p.X }
func f(xs []int, m Map[string, int]) int {
	let mut p = P{X: 1, Y: 2}
	p.X = -(g(&p) + 1)
	let ys = xs[1:2:3]
	let zs = xs[:1]
	let n = ys[0] + len(m) + len(zs)
	if n > 0 && !(n == 3) {
		return n
	} else if n < 0 {
		return 0
	}
	for x in xs {
		if x > 1 {
			break
		}
		continue
	}
	g(&p)
	return len(ys)
}
func main() {}
`, nil},
	{"doc comments", "a doc comment on each declaration that keeps one", `
// The timeout module.
import timeout

// Ticker reads the time.
type Ticker {
	// Now is the time.
	Now() int64,
}

// Fixed is a clock.
type Fixed {
	// t is the time.
	t int64,
}

// Now reads f.
func Now(f Fixed) int64 { return f.t }

// fail is one.
func fail() int { return 1 }

// W provides Ticker.
let W = context {
	// The fixed clock.
	Ticker: Fixed{t: 1},
}

// n is one.
let n = 1

// f reads W.
func f() int64 { return with W { Ticker.Now() } + int64(n) + int64(fail()) }
func g() timeout.Expired { return timeout.Expired }
func main() {}
`, nil},
	{"import path", "an import alias for a name clash (D-D7)", `
import t timeout
func timeout() int { return 1 }
func f() t.Expired { return t.Expired }
func main() {}
`, nil},
	{"test", "a documented test with parameters", `
// Adds holds for every x.
test Adds(x int) {
	assert x + 0 == x
}
func main() {}
`, nil},
	{"type parameters", "a generic record type", `
type Box[T] { V T }
func f(b Box[int]) int { return b.V }
func main() {}
`, nil},
	{"variant in place", "a generic variant declared in place", `
type Opt[T] = Has[T]{ V T } | Empty{}
func main() {}
`, nil},
	{"inline context header", "an inline context's provides and needs", `
type Ticker { Now() int64 }
type Log { Write(s string) }
type Fixed { t int64 }
func Now(f Fixed) int64 { return f.t }
func f() int64 needs Log { return with context provides Ticker needs Log { Ticker: Fixed{t: 1} } { Ticker.Now() } }
func main() {}
`, []string{"S109:6", "S109:6"}},
	{"law", "a documented law", `
type Monoid {
	Combine(a int, b int) int,
	// Assoc groups either way.
	law Assoc(a int, b int, c int) { Combine(Combine(a, b), c) == Combine(a, Combine(b, c)) },
}
func main() {}
`, nil},
	{"machine", "a machine type with documented entries", `
type Closed {}
type Open {}
type State = Closed | Open
type Trip {}
type Signal = Trip
func step(s State, sig Signal) Decision[State, string] {
	match s {
	case Closed:
		return Enter{Open}
	case Open:
		return Done{"open"}
	}
}
type Breaker machine[State, Signal] {
	// The breaker starts closed.
	initial: Closed,
	// step decides each signal.
	step:    step,
}
func main() {}
`, nil},
}

// TestDecidedNodes: the checker reads the trees the decided parser builds
// (#196). Each row names its family; every family has a row, and every
// row's verdict holds: never a panic, never silently accepted, never a
// cascade beside an S109.
func TestDecidedNodes(t *testing.T) {
	covered := map[string]bool{}
	known := map[string]bool{}
	for _, f := range decidedFamilies {
		known[f] = true
	}
	for _, tc := range decidedCases {
		if !known[tc.family] {
			t.Errorf("row %q names the family %q, which decidedFamilies does not list", tc.name, tc.family)
		}
		covered[tc.family] = true
		t.Run(tc.family+"/"+tc.name, func(t *testing.T) {
			_, diags := check.CheckSource(tc.src)
			var got []string
			for _, d := range diags {
				got = append(got, fmt.Sprintf("%s:%d", d.Code, d.Line))
			}
			if strings.Join(got, " ") != strings.Join(tc.want, " ") {
				t.Fatalf("got %v, want %v\n%v", got, tc.want, diags)
			}
			// The class H walk visits every node of the decided forms too.
			if p := check.RefWalkProblems(tc.src); len(p) > 0 {
				t.Fatalf("reference walk: %s", strings.Join(p, "; "))
			}
		})
	}
	for _, f := range decidedFamilies {
		if !covered[f] {
			t.Errorf("the decided family %q has no row", f)
		}
	}
}
