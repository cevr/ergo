package check

import (
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

// TestSelfCallResolvesToPackageFunction holds the G-ex-e-2 rows (L9): a
// function whose first parameter is an operation type, reached by e.f().
func TestSelfCallResolvesToPackageFunction(t *testing.T) {
	checkSelfCases(t, []selfCase{
		// G-ex-e-2: a function on an operation type, reached after its
		// operations; an operation of the name wins, and the function is
		// then reached by a plain call.
		{name: "operation type", src: `

type Engine { Compile(p string) int }

type Store[T] { Get(k string) T }

func twice(e Engine, p string) int { return e.Compile(p) * 2 }

func Compile(e Engine, p string) string { return "fn" }

func both[T](s Store[T], k string) T { return s.Get(k) }

func F(e Engine, s Store[int]) int {
	let _ string = e.Compile("x")
	return e.twice("ab") + s.both("k")
}
`, want: []selfWant{{"S108", "cannot use int as string", 14}}},
		{name: "operation type's own operation is no group member", src: `

type Engine { Compile(p string) int }

type Fast {}

func Compile(_ Fast, p string) int { return 1 }

func Compile(e Engine, p string) string { return "fn" }
`, want: []selfWant{{"S108", "Compile is already declared in this package", 9}}},
		{name: "operation type in a group", src: `

type Engine { Run() int }

type Fast {}

func Compile(_ Fast, p string) int { return 1 }

func Compile(e Engine, p string) int { return e.Run() }

func F(e Engine, f Fast) int { return e.Compile("a") + f.Compile("b") }
`},
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
