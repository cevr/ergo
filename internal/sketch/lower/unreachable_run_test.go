package lower

import (
	"fmt"
	"strings"
	"testing"
)

// K5 (owner q29, 2026-11-08; synthesis M22, repro G-ex-a-3): the prelude's
// Unreachable(reason) raises a defect that names the reason and the call's
// position. Each case stands Unreachable in one position of reach, a
// function with a typed failure, and runs it twice: run(0) takes the path
// that does not reach it and prints the value; run(1) reaches it, and the
// program ends with the defect (exit 2, the reason and the position on
// standard error). run's catch has a `case _:` arm, which would handle the
// defect if it were lowered as a typed failure: it never prints "caught".

type unreachableCase struct {
	name, decls, body string
	ok                int
}

var unreachableCases = []unreachableCase{
	{name: "statement", body: "if n > 0 {\n\t\tUnreachable(\"statement\")\n\t}\n\tn + 1", ok: 1},
	{name: "typed-let", body: "if n == 0 {\n\t\treturn 3\n\t}\n\tlet x int = Unreachable(\"typed-let\")\n\tx", ok: 3},
	{name: "let-if", body: "let x = if n == 0 { 4 } else { Unreachable(\"let-if\") }\n\tx", ok: 4},
	{name: "match-arm", body: "match n {\n\tcase 0:\n\t\t5\n\tcase _:\n\t\tUnreachable(\"match-arm\")\n\t}", ok: 5},
	{name: "match-arm-first", body: "let s = match n {\n\tcase 1:\n\t\tUnreachable(\"match-arm-first\")\n\tcase _:\n\t\t13 + n\n\t}\n\ts", ok: 13},
	{name: "return", body: "if n == 0 {\n\t\treturn 6\n\t}\n\treturn Unreachable(\"return\")", ok: 6},
	{name: "argument", decls: "func twice(m int) int { m * 2 }\n", body: "if n == 0 {\n\t\treturn twice(n + 3)\n\t}\n\ttwice(Unreachable(\"argument\"))", ok: 6},
	{name: "tail", body: "if n == 0 {\n\t\treturn 8\n\t}\n\tUnreachable(\"tail\")", ok: 8},
	{name: "operand", body: "if n == 0 {\n\t\treturn 9\n\t}\n\tn + Unreachable(\"operand\")", ok: 9},
	{name: "generic", decls: "func first[T](xs []T) T {\n\tif len(xs) > 0 {\n\t\treturn xs[0]\n\t}\n\tUnreachable(\"generic\")\n}\n", body: "first(if n == 0 { []int{10} } else { []int{} })", ok: 10},
	{name: "catch-arm", decls: "func inner(n int) int fails Missing {\n\tif n > 0 {\n\t\treturn Missing\n\t}\n\t11\n}\n", body: "inner(n) catch {\n\tcase Missing:\n\t\tUnreachable(\"catch-arm\")\n\t}", ok: 11},
	{name: "pure", decls: "func half(n int) int {\n\tif n % 2 == 0 {\n\t\treturn n / 2\n\t}\n\tUnreachable(\"pure\")\n}\n", body: "half(n + 24)", ok: 12},
}

func (c unreachableCase) src() string {
	return "type Missing {}\n\n" + c.decls + `
func reach(n int) int fails Missing {
	if n < 0 {
		return Missing
	}
	` + c.body + `
}

func run(n int) int needs Console {
	reach(n) catch {
	case _:
		Console.Println("caught")
		-1
	}
}

func main() needs Console {
	Console.Println("value \(run(0))")
	Console.Println("value \(run(1))")
}
`
}

// at is the position of the case's Unreachable call in its source, as the
// defect names it.
func (c unreachableCase) at(t *testing.T) string {
	src := c.src()
	i := strings.Index(src, "Unreachable(\""+c.name+"\")")
	if i < 0 {
		t.Fatalf("%s: no Unreachable(%q) in the source", c.name, c.name)
	}
	line := strings.Count(src[:i], "\n") + 1
	column := i - strings.LastIndex(src[:i], "\n")
	return fmt.Sprintf("main.ergo:%d:%d", line, column)
}

// TestErgoRunUnreachable builds Go because it checks what the built program
// prints and how it exits; no in-process check observes that, and Ergo has
// no reference interpreter yet (test-speed T8).
func TestErgoRunUnreachable(t *testing.T) {
	needsBuild(t)
	var programs []*lowered
	for _, c := range unreachableCases {
		programs = append(programs, mustLower(t, "unreachable_"+strings.ReplaceAll(c.name, "-", "_"), c.src()))
	}
	exes := buildAll(t, programs, true)
	for i, c := range unreachableCases {
		p := programs[i]
		exe, ok := exes[p.name]
		if !ok {
			t.Fatalf("%s: not built", c.name)
		}
		stdout, stderr, code := runExe(t, exe)
		wantOut := fmt.Sprintf("value %d\n", c.ok)
		wantErr := "panic: unreachable at " + c.at(t) + ": " + c.name + "\n"
		if code != 2 || stdout != wantOut || !strings.HasPrefix(stderr, wantErr) {
			t.Errorf("%s: exit %d, stdout %q, stderr:\n%s\nwant exit 2, stdout %q, stderr starting %q\n%s", c.name, code, stdout, firstLines(stderr, 6), wantOut, wantErr, c.src())
		}
	}
}

// TestUnreachableLowersToPanic: a statement or a tail is Go's panic
// statement, a value position Never[T] at the type the checker gave it;
// neither calls the Go body Unreachable, and neither returns the defect
// as an error.
func TestUnreachableLowersToPanic(t *testing.T) {
	o := mustLower(t, "unreachable_shape", `func tail(n int) int {
	if n == 0 {
		return 1
	}
	Unreachable("tail")
}

func value(n int) int64 {
	if n == 0 {
		return 2
	}
	let x int64 = Unreachable("value")
	x
}

func main() needs Console {
	Console.Println("\(tail(0)) \(value(0))")
}
`)
	g := o.mainGo()
	// The inline directive of the position stands between panic( and the
	// defect.
	for _, want := range []string{
		`panic( /*line /ergo-test/main.ergo:5:2*/ ergo.UnreachableAt("tail", "main.ergo:5:2"))`,
		`/*line /ergo-test/main.ergo:12:16*/ ergo.Never[int64]("value", "main.ergo:12:16")`,
	} {
		if !strings.Contains(g, want) {
			t.Errorf("the Go has no %s:\n%s", want, g)
		}
	}
	if strings.Contains(g, "ergo.Unreachable(") {
		t.Errorf("the Go calls the body Unreachable:\n%s", g)
	}
}
