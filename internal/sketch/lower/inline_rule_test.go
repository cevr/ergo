package lower

import (
	"regexp"
	"testing"
)

// TestInlineRule holds the cost rule of inline.go, one row per refusal: a
// call of each function below in main's Go body stays a Go call exactly
// when the rule refuses its body or its site. The rule's decision is the
// spec here (the stated cost rule), so the row reads that one fact: whether
// main's Go names the function. Each row kills the mutant of its guard
// (testdata/mutants.json, inline-*); TestInlineAgrees holds that an
// inlined call behaves as the call.
func TestInlineRule(t *testing.T) {
	const src = `type Box { items []int }

func small(b *Box, i int) int {
	if i < 0 {
		return 0
	}
	b.items[i]
}

func looped(b *Box) int {
	let mut s = 0
	for x in b.items {
		s = s + x
	}
	s
}

func rec(n int) int {
	if n <= 0 {
		return 0
	}
	rec(n - 1) + 1
}

func asValue(f func(x int) int) int { apply(f, 1) }

func apply(f func(x int) int, x int) int { f(x) }

func say(s string) needs Console { Console.Println(s) }

type Ops { h func(x int) int }

func mixed(o Ops, f func(x int) int, x int) int { o.h(f(x)) }

func big(b *Box) int {
	let a = b.items[0] + b.items[1] + b.items[2] + b.items[0] * b.items[1] * b.items[2]
	let c = a + b.items[0] - b.items[1] + b.items[2] - a * a + b.items[0] * a - b.items[1] * a
	let d = c + a * b.items[0] - c * b.items[1] + a * c * b.items[2] - a - c + b.items[0]
	let e = d * d - c * c + a * a - b.items[0] * d + b.items[1] * c - b.items[2] * a
	a + c + d + e + a * c * d * e - b.items[0] * b.items[1] * b.items[2] + a - c + d - e
}

func main() needs Console {
	let mut b = Box{items: []int{1, 2, 3}}
	b.items[0] = 4
	let f = func(x int) int { x + 1 }
	Console.Println("\(small(&b, 1)) \(looped(&b)) \(rec(3)) \(asValue(func(x int) int { x * 2 }))")
	Console.Println("\(apply(func(x int) int { x * 3 }, 2)) \(apply(f, 2)) \(big(&b))")
	say("x")
	Console.Println("\(mixed(Ops{h: func(x int) int { x - 1 }}, func(x int) int { x + 5 }, 1))")
}
`
	o := mustLower(t, "inlinerule", src)
	body := goFunc(t, o.mainGo(), "main_1") // Go's main is the entry; Ergo's main is main_1
	// A call of a known variant (known.go, nameKnown) is a call too.
	calls := func(name string) bool {
		return regexp.MustCompile(`\b` + name + `(Known)?(_[0-9]+)?\(`).MatchString(body)
	}
	for _, row := range []struct {
		name, why string
		kept      bool
	}{
		{"small", "a small body with early returns and a defect site is inlined", false},
		{"looped", "a body with a loop stays a call", true},
		{"rec", "a recursive function stays a call", true},
		{"asValue", "a callback parameter used other than as a callee stays a call", true},
		{"say", "a body with a capability (a needs row) stays a call", true},
		{"big", "a body over inlineBudget nodes stays a call", true},
		{"mixed", "a body that calls a function value other than its callbacks keeps its entry poll: it stays a call", true},
	} {
		if calls(row.name) != row.kept {
			t.Errorf("%s: main's Go names it %v, want %v\n%s", row.why, calls(row.name), row.kept, body)
		}
	}
	// The depth bound leaves a call of rec in main even when rec is
	// inlined, so the recursion row reads the count: no site of a
	// recursive function is inlined.
	const recSrc = "func rec(n int) int {\n\tif n <= 0 {\n\t\treturn 0\n\t}\n\trec(n - 1) + 1\n}\n\nfunc main() needs Console {\n\tConsole.Println(\"\\(rec(3))\")\n}\n"
	if _, s := lowerInlineMode(t, "inlinerec", recSrc, false); s.Inlined != 0 {
		t.Errorf("a recursive function is inlined at %d sites, want 0", s.Inlined)
	}
	// apply is inlined with its literal and stays a call with a function
	// value bound to a let: exactly one call remains.
	if n := len(regexp.MustCompile(`\bapply(_[0-9]+)?\(`).FindAllString(body, -1)); n != 1 {
		t.Errorf("apply is called %d times in main's Go, want 1 (the site that passes a let-bound function value)\n%s", n, body)
	}
}
