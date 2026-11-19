package check

import (
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/syntax"
)

// TestExplicitClauseText pins the text and position of the clause
// diagnostics by default (q137; diagnostics.md S101, S102):
// each names the whole clause to write, and every site of one clause
// prints the same text. Mutants: write the site's row instead of the
// whole clause (the two-site row); print the clause without the written
// elements (the extend row); report needs as fails; write the clause after a lone result of
// function type without its parentheses (the maker row).
func TestExplicitClauseText(t *testing.T) {
	const src = `type Bad {}
type Busy {}
type NotFound {}

func leaf() int fails Bad { 1 }
func lost() int fails NotFound { 2 }

func helper(n int) int {
	try leaf()
}

func say(s string) {
	Console.Println(s)
}

func load(k string) int fails Busy {
	let a = try leaf()
	let b = try lost()
	a + b
}

func main() needs Console {
	say("x")
}

func maker() func(int) {
	Console.Println("m")
	func(d int) {}
}
`
	want := []string{
		`9:2: S101 {Bad} escapes function helper, whose error row {} does not list it; write: func helper(n int) int fails Bad`,
		`13:2: S102 this call requires Console, which is not in the capability row of function say and no with provides it; write: func say(s string) needs Console`,
		`17:10: S101 {Bad} escapes function load, whose error row {Busy} does not list it; write: func load(k string) int fails Busy | Bad | NotFound`,
		`18:10: S101 {NotFound} escapes function load, whose error row {Busy} does not list it; write: func load(k string) int fails Busy | Bad | NotFound`,
		`27:2: S102 this call requires Console, which is not in the capability row of function maker and no with provides it; write: func maker() (func(int)) needs Console`,
	}
	f, err := syntax.Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	_, ds := Check("main", []*syntax.File{f}, nil)
	var got []string
	for _, d := range ds {
		got = append(got, d.String())
		if d.Fix == nil {
			t.Errorf("%s: no fix", d)
		}
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("got\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if _, ds := CheckOptions("main", []*syntax.File{f}, nil, Options{Infer: true}); len(ds) != 2 {
		t.Errorf("with Infer: %v, want only say's and load's", ds)
	}
}
