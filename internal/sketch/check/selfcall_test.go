package check

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/syntax"
)

// 196m (round-6 ruling B1): there are no receivers, so x.f(a) resolves to a
// field f of x first and otherwise to a package function f on x's type,
// whose first parameter x fills (rule 1). Such functions make a type
// satisfy an operation type (rule 2). Functions of one name are allowed
// when their first parameters are distinct local types, and are called
// only as x.f(…) (rule 3). x.f without a call is only a field (rule 4).

// selfWant is one expected diagnostic: its code, a part of its message and
// its line.
type selfWant struct {
	code, text string
	line       int
}

type selfCase struct {
	name string
	src  string
	want []selfWant
}

// checkSelfSource checks src as package main over the universe alone. The
// cases name nothing from the prelude, so they do not depend on its
// spelling.
func checkSelfSource(t *testing.T, src string) []Diagnostic {
	t.Helper()
	_, diags := selfChecker(t, src)
	return diags
}

// selfChecker is checkSelfSource that also returns the checker.
func selfChecker(t *testing.T, src string) (*checker, []Diagnostic) {
	t.Helper()
	f, err := syntax.Parse(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	c := newChecker("main", Std, universe())
	c.run([]*syntax.File{f})
	return c, c.sorted()
}

// checkSelfCases checks each source and requires exactly the expected
// diagnostics, no more.
func checkSelfCases(t *testing.T, cases []selfCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			diags := checkSelfSource(t, tc.src)
			t.Logf("diagnostics: %v", diags)
			left := append([]Diagnostic(nil), diags...)
			for _, w := range tc.want {
				found := -1
				for i, d := range left {
					if d.Code == w.code && d.Line == w.line && strings.Contains(d.Message, w.text) {
						found = i
						break
					}
				}
				if found < 0 {
					t.Errorf("missing %s at line %d containing %q; got %v", w.code, w.line, w.text, diags)
					continue
				}
				left = append(left[:found], left[found+1:]...)
			}
			for _, d := range left {
				t.Errorf("unexpected diagnostic %v", d)
			}
		})
	}
}

func TestSelfCallResolvesToPackageFunction(t *testing.T) {
	checkSelfCases(t, []selfCase{
		{name: "call", src: `

type Counter { n int }

func bump(c Counter, by int) int { return c.n + by }

func F() int {
	let c = Counter{n: 1}
	return c.bump(2) + bump(c, 3)
}
`},
		{name: "remaining arguments are checked", src: `

type Counter { n int }

func bump(c Counter, by int) int { return c.n + by }

func F() int {
	let c = Counter{n: 1}
	return c.bump("two")
}

func G() int {
	let c = Counter{n: 1}
	return c.bump(1, 2)
}
`, want: []selfWant{
			{"S108", "a string literal takes only a string type, not int", 9},
			{"S108", "too many arguments: want 1, have 2", 14},
		}},
		{name: "result type", src: `

type Counter { n int }

func label(c Counter) string { return "c" }

func F() int {
	let c = Counter{n: 1}
	return c.label()
}
`, want: []selfWant{{"S108", "cannot use string as int in return", 9}}},
		{name: "field first", src: `

type Job { run func() int }

func run(j Job) string { return "fn" }

func F(j Job) string {
	let a string = run(j)
	let b string = j.run()
	return a + b
}
`, want: []selfWant{{"S108", "cannot use int as string", 9}}},
		{name: "pointer first parameter", src: `

type Counter { n int }

func inc(c *Counter) { c.n = c.n + 1 }

func make() Counter { return Counter{n: 0} }

func F() {
	let mut c = Counter{n: 0}
	c.inc()
	let p = &c
	p.inc()
	make().inc()
}
`, want: []selfWant{{"S108", "is not addressable", 14}}},
		{name: "generic owner binds the type parameter", src: `

type Box[T] { v T }

func get[T](b Box[T]) T { return b.v }

func F() int {
	let b = Box[int]{v: 1}
	return b.get()
}

func G() string {
	let b = Box[int]{v: 1}
	return b.get()
}
`, want: []selfWant{{"S108", "cannot use int as string in return", 14}}},
		{name: "generic function keeps its own type parameters", src: `

type Box[T] { v T }

func pair[T, U](b Box[T], u U) U { return u }

func F() string {
	let b = Box[int]{v: 1}
	return b.pair("x")
}
`},
		{name: "a type from no declaration has no such function", src: `

func twice(n int) int { return n * 2 }

func F() int {
	let n = 2
	return n.twice()
}
`, want: []selfWant{{"S104", "no field or method twice", 7}}},
		{name: "inferred rows flow through x.f", src: `

type Bang {}

type Door { open bool }

func fail() fails Bang { return Bang }

func knock(d Door) { try fail() }

func F(d Door) fails Bang { try d.knock() }

func G(d Door) { try d.knock() }
`, want: []selfWant{{"S101", "Bang", 13}}},
		// The body graph has an edge for h's call d.knock() before any
		// body is typed, so knock's inferred row is solved before h's,
		// whatever the declaration order.
		{name: "inferred rows through x.f before the callee", src: `

func H(d Door) { try h(d) }

func h(d Door) { try d.knock() }

func K(d Door) fails Bang { try h(d) }

func knock(d Door) { try fail() }

func fail() fails Bang { return Bang }

type Door { open bool }

type Bang {}
`, want: []selfWant{{"S101", "Bang", 3}}},
	})
}

func TestSelfCallSatisfiesOperationType(t *testing.T) {
	checkSelfCases(t, []selfCase{
		{name: "provider", src: `

type Clock { Now() int }

type Fixed { t int }

func Now(f Fixed) int { return f.t }

let W = context { Clock: Fixed{t: 1} }

func F() int { return with W { Clock.Now() } }
`},
		{name: "signature differs", src: `

type Clock { Now() int }

type Fixed { t int }

func Now(f Fixed) string { return "" }

let W = context { Clock: Fixed{t: 1} }

func F() int { return with W { Clock.Now() } }
`, want: []selfWant{{"S106", "Now has signature func() string, want func() int", 9}}},
		{name: "wider error row", src: `

type Bang {}

type Clock { Now() int }

type Fixed { t int }

func Now(f Fixed) int fails Bang { return f.t }

let W = context { Clock: Fixed{t: 1} }

func F() int { return with W { Clock.Now() } }
`, want: []selfWant{{"S106", "Bang", 11}}},
		{name: "extra needs", src: `

type Log { Write(s string) }

type Clock { Now() int }

type Fixed { t int }

func Now(f Fixed) int needs Log { return f.t }

let W = context { Clock: Fixed{t: 1} }

func F() int { return with W { Clock.Now() } }
`, want: []selfWant{{"S106", "Log", 11}}},
		{name: "missing function", src: `

type Clock { Now() int }

type Fixed { t int }

let W = context { Clock: Fixed{t: 1} }

func F() int { return with W { Clock.Now() } }
`, want: []selfWant{{"S106", "no method Now", 7}}},
	})
}

func TestSelfCallSameNamedFunctions(t *testing.T) {
	checkSelfCases(t, []selfCase{
		{name: "distinct local types", src: `

type Cat { name string }

type Dog { name string }

func speak(c Cat) string { return "meow" }

func speak(d Dog) string { return "woof" }

func F() string {
	let c = Cat{name: "a"}
	let d = Dog{name: "b"}
	return c.speak() + d.speak()
}
`},
		{name: "plain call is ambiguous", src: `

type Cat { name string }

type Dog { name string }

func speak(c Cat) string { return "meow" }

func speak(d Dog) string { return "woof" }

func F() string {
	let c = Cat{name: "a"}
	return speak(c)
}
`, want: []selfWant{{"S108", "several functions are named speak; call it as x.speak(…)", 13}}},
		{name: "same type twice", src: `

type Cat { name string }

func speak(c Cat) string { return "meow" }

func speak(c Cat) string { return "purr" }
`, want: []selfWant{{"S108", "speak is already declared in this package", 7}}},
		{name: "first parameter not a local type", src: `

type Cat { name string }

func speak(c Cat) string { return "meow" }

func speak(n int) string { return "n" }
`, want: []selfWant{{"S108", "speak is already declared in this package", 7}}},
		{name: "a type and a function", src: `

type Cat { name string }

type speak { n int }

func speak(c Cat) string { return "meow" }
`, want: []selfWant{{"S108", "speak is already declared as a type in this package", 7}}},
	})
}

func TestSelfCallSelectorWithoutCallIsAField(t *testing.T) {
	checkSelfCases(t, []selfCase{
		{name: "function is not a value through a selector", src: `

type Counter { n int }

func bump(c Counter, by int) int { return c.n + by }

func F() int {
	let c = Counter{n: 1}
	let f = c.bump
	return c.n + f(1)
}
`, want: []selfWant{{"S104", "has no field bump", 9}}},
		{name: "parenthesised callee is a call", src: `

type Counter { n int }

func bump(c Counter, by int) int { return c.n + by }

func F() int {
	let c = Counter{n: 1}
	return (c.bump)(1)
}
`},
		{name: "plain name is still a value", src: `

type Counter { n int }

func bump(c Counter, by int) int { return c.n + by }

func F() int {
	let f = bump
	return f(Counter{n: 1}, 2)
}
`},
	})
}

func TestSelfCallErrorTypeDeclaresNoError(t *testing.T) {
	checkSelfCases(t, []selfCase{
		{name: "Error on an error type", src: `

type Bang { code int }

func Error(b Bang) string { return "bang" }

func F() fails Bang { return Bang{code: 1} }
`, want: []selfWant{{"S108", "Bang is an error type, so it declares no Error method", 5}}},
	})
}

// TestSelfCallOrderFree permutes the declarations of a package with two
// providers whose functions share a name, each holding an inline context: the
// diagnostics (codes and messages) do not depend on the order, and the context
// pass keys the two functions named Now as distinct declarations.
func TestSelfCallOrderFree(t *testing.T) {
	decls := []string{
		"type Clock { Now() int }",
		"type Fixed { t int }",
		"type Late { t int }",
		"func Now(f Fixed) int { return with context { Clock: Late{t: 2} } { Clock.Now() } + f.t }",
		"func Now(l Late) int { return with context { Clock: Fixed{t: 1} } { Clock.Now() } + l.t }",
		"func Bad(l Late) string { return Now(l) }",
		"let W = context { Clock: Fixed{t: 1} }",
		"func F() int { return with W { Clock.Now() } }",
	}
	var want []string
	order := make([]int, len(decls))
	for i := range order {
		order[i] = i
	}
	for round := 0; round < len(decls); round++ {
		var b strings.Builder
		for _, i := range order {
			b.WriteString(decls[i] + "\n\n")
		}
		c, diags := selfChecker(t, b.String())
		var got []string
		for _, d := range diags {
			got = append(got, d.Code+" "+d.Message)
		}
		slices.Sort(got)
		_, groups, _ := c.contextSource()
		var keys []string
		for _, g := range groups {
			keys = append(keys, g.key)
		}
		slices.Sort(keys)
		if want := []string{"func Fixed.Now", "func Late.Now"}; !slices.Equal(keys, want) {
			t.Fatalf("order %v: inline context groups %v, want %v", order, keys, want)
		}
		if want == nil {
			want = got
			if len(want) != 1 || !strings.Contains(want[0], "several functions are named Now") {
				t.Fatalf("diagnostics %v, want the one plain call of Now refused", want)
			}
		} else if !slices.Equal(got, want) {
			t.Fatalf("order %v: diagnostics %v, want %v", order, got, want)
		}
		// Rotate, then swap a pair, so the two functions named Now trade
		// places across the rounds.
		order = append(order[1:], order[0])
		order[0], order[len(order)-1] = order[len(order)-1], order[0]
	}
}

// TestSelfCallBindsOwnerKinds: a 196m method's first parameter binds its
// owner's bracket parameters by position, as a receiver did, so its
// signature gives them their kinds (D6 "body and methods"; round-3 M3); a
// conflict is reported once, at the declaration. A function with its own
// parameter, or one on an operation type, binds nothing.
func TestSelfCallBindsOwnerKinds(t *testing.T) {
	checkSelfCases(t, []selfCase{
		{"error row", `type Job[E] {}
func Run[E](j Job[E]) fails E {}
`, nil},
		{"capability row", `type Job[R] {}
func Run[R](j Job[R]) needs R {}
`, nil},
		{"pointer first parameter", `type Job[E] {}
func Run[E](j *Job[E]) fails E {}
`, nil},
		// q47: an error row in a field names its value type.
		{"field of an error row", `type Job[E] { v E }
func Run[E](j Job[E]) fails E {}
`, nil},
		{"conflict reported once", `type Job[R] { v R }
func Run[R](j Job[R]) needs R {}
`, []selfWant{{"S108", "used both as a type and as a row", 1}}},
		{"not the first parameter", `type Job[E] {}
func Run[E](n int, j Job[E]) fails E {}
`, nil},
	})
}

// TestSelfCallEdgesByStaticType: x.f(…) on a receiver whose type is fixed
// before bodies (a composite literal, a typed local) reaches only the
// function on that type, so n same-named methods give n body-graph edges,
// not n², and n separate initialization cycles, not one.
func TestSelfCallEdgesByStaticType(t *testing.T) {
	var b strings.Builder
	n := 50
	for i := range n {
		fmt.Fprintf(&b, "type R%d {}\nlet v%d = func() { R%d.M() }\nfunc M(_ R%d) { _ = v%d }\n", i, i, i, i, i)
	}
	c, diags := selfChecker(t, b.String())
	cycles := 0
	for _, d := range diags {
		if d.Code == "S108" && strings.Contains(d.Message, "initialization cycle") {
			cycles++
		} else {
			t.Fatalf("unexpected %v", d)
		}
	}
	if cycles != n || c.bodyEdges > 2*n {
		t.Fatalf("%d cycles and %d body edges, want %d and at most %d", cycles, c.bodyEdges, n, 2*n)
	}
}

// TestSelfCallInstantiationCycle: a 196m method's parameters are its
// owner's for the instantiation-cycle check, as a receiver's are in Go
// (go/types mono.go), so a signature that grows the owner is S108. A
// function with a type parameter of its own lowers to a Go function, which
// Go does not canonicalize, so it is not.
func TestSelfCallInstantiationCycle(t *testing.T) {
	checkSelfCases(t, []selfCase{
		{"method grows its owner", `type R[P] {}
func M[P](r R[P], x R[[]P]) {}
`, []selfWant{{"S108", "instantiation cycle", 2}}},
		{"across two owners", `type R[P] {}
type S[Q] {}
func M[P](r R[P], x S[[]P]) {}
func N[Q](s S[Q], x R[Q]) {}
`, []selfWant{{"S108", "instantiation cycle", 3}}},
		{"own parameter", `type R[P] {}
func F[P, Q](r R[P], x R[[]P], q Q) {}
`, nil},
		{"no growth", `type R[P] {}
func M[P](r R[P], x R[P]) {}
`, nil},
	})
}
