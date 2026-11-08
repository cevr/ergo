package check_test

import (
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
)

// The rule of types.md, Generics 7 (std-strengths SS4a): a generic type's
// trailing row parameters may be left out of its written type arguments,
// and each one left out is the empty row. A type parameter, a row
// parameter that a written argument follows, and an extra argument stay
// S108; a generic type with no written argument stays S108 as well.

// elidedRowsPrelude is the generic type of the fixtures: a command whose
// Build makes a C and may fail with E, the shape of flag.Command.
const elidedRowsPrelude = `type Bad {}

type Command[C, E] { Name string, Build func() C fails E }

func one() int { 1 }

func bad() int fails Bad { Bad }
`

// TestElidedRowArgFixtures: each program checks with no diagnostic, or
// gets exactly its want marker.
func TestElidedRowArgFixtures(t *testing.T) {
	cases := []struct{ name, src string }{
		// The row left out is empty: Build of a Command[int] cannot fail,
		// so a function with no fails calls it with no try.
		{"left-out-row-is-empty", elidedRowsPrelude + `
func run(c Command[int]) int { c.Build() }

func main() needs Console {
	let cs = []Command[int]{Command[int]{Name: "one", Build: one}}
	Console.Println("\(run(cs[0]))")
}
`},
		// It is the type inference gives when no argument binds E.
		{"same-as-inferred", elidedRowsPrelude + `
func make[C, E](name string, f escaping func() C fails E) Command[C, E] { Command[C, E]{Name: name, Build: f} }

func run() int {
	let c Command[int] = make("one", one)
	let back = make(c.Name, c.Build)
	let again Command[int] = back
	again.Build()
}
`},
		// A written row argument still works beside it.
		{"written-row", elidedRowsPrelude + `
func run(c Command[int, Bad]) int fails Bad { try c.Build() }
`},
		// A capability row may be left out too.
		{"left-out-needs", `type Job[T, R] { Run func() T needs R }

func run(j Job[int]) int { j.Run() }
`},
		// The empty row admits no failure: a Build that fails Bad is not a
		// Command[int].
		{"empty-row-refuses-a-failure", elidedRowsPrelude + `
func main() {
	let c = Command[int]{Name: "bad", Build: bad} // want S108
	_ = c
}
`},
		// A type parameter is never left out.
		{"left-out-type", `type Pair[A, B] { a A, b B }

func f(p Pair[int]) {} // want S108
`},
		// A row parameter that a written argument follows is not trailing.
		{"left-out-row-before-written", `type Mid[A, E, B] { f func(a A) B fails E }

func f(m Mid[int, string]) {} // want S108
`},
		// Too many arguments.
		{"too-many", elidedRowsPrelude + `
func f(c Command[int, Bad, Bad]) {} // want S108
`},
		// No written argument: the bare name stays a generic type that
		// needs its arguments.
		{"bare-name", `type Rows[E] { f func() fails E }

func f(r Rows) {} // want S108
`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, diags := check.CheckSource(tc.src)
			want, have := expected(tc.src), got(diags)
			if strings.Join(want, " ") != strings.Join(have, " ") {
				t.Errorf("want %v, got %v\n\t%s\n%s", want, have, describe(diags), tc.src)
			}
		})
	}
}

// paramKind is the kind of one generated type parameter.
type paramKind int

const (
	kindType paramKind = iota
	kindFails
	kindNeeds
)

// TestElidedRowArgProperty: for generated generic types with 1 to 5
// parameters of mixed kinds, each written arity a from 0 to n+1 checks
// exactly when 1 <= a <= n and every parameter after the a-th is a row;
// and when it checks, each row left out is empty: a function whose own
// rows hold only the written rows calls the field with no try. Seeded and
// replayable: a failure prints the seed and the program.
func TestElidedRowArgProperty(t *testing.T) {
	const seeds = 400
	checked, refused, elided := 0, 0, 0
	for seed := uint64(1); seed <= seeds; seed++ {
		r := rand.New(rand.NewPCG(seed, 0x55a4))
		kinds := make([]paramKind, 1+r.IntN(5))
		for i := range kinds {
			kinds[i] = paramKind(r.IntN(3))
		}
		for a := 0; a <= len(kinds)+1; a++ {
			src := elisionProgram(kinds, a)
			_, diags := check.CheckSource(src)
			wantOK := a >= 1 && a <= len(kinds) && allRows(kinds[a:])
			switch {
			case wantOK && len(diags) != 0:
				t.Fatalf("seed %d, arity %d: want no diagnostic, got %s\n%s", seed, a, describe(diags), src)
			case !wantOK && (len(diags) != 1 || diags[0].Code != "S108"):
				t.Fatalf("seed %d, arity %d: want one S108, got %s\n%s", seed, a, describe(diags), src)
			case wantOK:
				checked++
				if a < len(kinds) {
					elided++
				}
			default:
				refused++
			}
		}
	}
	// The generator reaches every branch: programs that check with rows
	// left out, programs that check in full, and programs refused.
	if elided < 100 || checked-elided < 100 || refused < 100 {
		t.Errorf("weak generator: %d checked (%d with rows left out), %d refused", checked, elided, refused)
	}
}

func allRows(kinds []paramKind) bool {
	for _, k := range kinds {
		if k == kindType {
			return false
		}
	}
	return true
}

// elisionProgram declares T with the parameters kinds and a function Use
// that takes T with its first a parameters written: int for a type
// parameter, Bad for a failure row, Cap for a capability row. Use's own
// rows hold only the rows written, and it calls T's field with no try, so
// it checks only when every row left out is empty.
func elisionProgram(kinds []paramKind, a int) string {
	var params, fieldParams, args, fails, needs, written []string
	wroteFail, wroteNeed := false, false
	for i, k := range kinds {
		name := fmt.Sprintf("P%d", i)
		params = append(params, name)
		switch k {
		case kindType:
			fieldParams = append(fieldParams, fmt.Sprintf("p%d %s", i, name))
			args = append(args, "1")
			if i < a {
				written = append(written, "int")
			}
		case kindFails:
			fails = append(fails, name)
			if i < a {
				written = append(written, "Bad")
				wroteFail = true
			}
		case kindNeeds:
			needs = append(needs, name)
			if i < a {
				written = append(written, "Cap")
				wroteNeed = true
			}
		}
	}
	if a > len(kinds) {
		written = append(written, "int")
	}
	field := "func(" + strings.Join(fieldParams, ", ") + ") int"
	if len(fails) > 0 {
		field += " fails " + strings.Join(fails, " | ")
	}
	if len(needs) > 0 {
		field += " needs " + strings.Join(needs, " | ")
	}
	typ := "T"
	if len(written) > 0 {
		typ += "[" + strings.Join(written, ", ") + "]"
	}
	use := "func Use(t " + typ + ") int"
	call := "t.f(" + strings.Join(args, ", ") + ")"
	if wroteFail {
		use += " fails Bad"
		call = "try " + call
	}
	if wroteNeed {
		use += " needs Cap"
	}
	return fmt.Sprintf("type Bad {}\n\ntype Cap {\n\tUse(),\n}\n\ntype T[%s] { f %s }\n\n%s { %s }\n",
		strings.Join(params, ", "), field, use, call)
}
