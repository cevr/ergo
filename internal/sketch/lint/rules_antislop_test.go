package lint_test

import (
	"fmt"
	"math/rand/v2"
	"testing"
)

// The anti-slop family of the strict preset (AS4, anti-slop.md §4): for
// each rule a generator and a reference that decides from what the
// generator wrote. In each planKind, at counts from the blank line the
// generator writes before the declaration.

var causeArms = []planKind{
	{true, 4, "func %s(n int) int fails Lost {\n\tget(n) catch {\n\tcase _:\n\t\treturn Lost{Why: \"failed\"}\n\t}\n}"},
	{true, 6, "func %s(n int) int fails Lost {\n\tget(n) catch {\n\tcase Bad as b:\n\t\treturn Lost{Why: \"\\(b.N)\"}\n\tcase _:\n\t\treturn Lost{Why: \"other\"}\n\t}\n}"},
	{false, 0, "func %s(n int) int fails Lost {\n\tget(n) catch {\n\tcase e:\n\t\treturn Lost{Why: describe(e)}\n\t}\n}"},
	{false, 0, "func %s(n int) int fails Lost {\n\tget(n) catch {\n\tcase Bad:\n\t\treturn Lost{Why: \"bad\"}\n\tcase Gone:\n\t\treturn Lost{Why: \"gone\"}\n\t}\n}"},
	{false, 0, "func %s(n int) int fails Lost {\n\tget(n) catch {\n\tcase _:\n\t\treturn 0\n\t}\n}"},
}

// TestCauseDroppedAgreesWithReference writes functions whose catch turns
// the caught failure into another, and checks that cause-dropped fires on
// exactly the arms that take every failure (`case _`, alone or after a
// named arm) and return a failure; not on an arm that binds the failure
// and reads it, arms that name what they catch, or a catch-all that
// returns a value. Mutants: as4-cause-reads-ignored,
// as4-cause-named-arm, as4-cause-value-return.
func TestCauseDroppedAgreesWithReference(t *testing.T) {
	for _, seed := range propertySeeds(30) {
		r := rand.New(rand.NewPCG(seed, 0xa1))
		p := newPlanted()
		p.write("type Bad { N int }\n\ntype Gone {}\n\ntype Lost { Why string }\n\ntype Failure = Bad | Gone\n\nfunc get(n int) int fails Bad | Gone {\n\tif n > 9 {\n\t\treturn Gone\n\t}\n\tif n < 0 {\n\t\treturn Bad{N: n}\n\t}\n\tn\n}\n\nfunc describe(e Failure) string {\n\tmatch e {\n\tcase Bad as b:\n\t\t\"bad \\(b.N)\"\n\tcase Gone:\n\t\t\"gone\"\n\t}\n}", false, 0)
		for i := range 1 + r.IntN(5) {
			k := causeArms[r.IntN(len(causeArms))]
			p.write("\n"+fmt.Sprintf(k.text, fmt.Sprintf("w%d", i)), k.fires, k.at)
		}
		p.write("\nfunc main() {\n}", false, 0)
		p.check(t, "cause-dropped", "TestCauseDroppedAgreesWithReference", seed)
	}
}

var variantResults = []planKind{
	{true, 1, "func %s(k string) Found {\n\tif k == \"\" {\n\t\treturn NotFound{Key: k}\n\t}\n\tHit{V: 1}\n}"},
	{false, 0, "func %s(f Found) Found {\n\tf\n}"},
	{false, 0, "func %s(k string) Found fails NotFound {\n\tif k == \"\" {\n\t\treturn NotFound{Key: k}\n\t}\n\tHit{V: 1}\n}"},
	{false, 0, "func %s(k string) Other {\n\tHit{V: 2}\n}"},
	{false, 0, "func %s(k string) Hit {\n\tHit{V: 3}\n}"},
}

// TestFailureAsVariantAgreesWithReference writes functions that return a
// union holding NotFound, which look fails with, with and without a fails
// row, one that passes on a Found it is given, and a union of plain
// values, and checks that failure-as-variant fires on exactly the
// function that builds the NotFound and has no fails row. Mutants:
// as4-variant-row-ignored, as4-variant-any-type, as4-variant-passes-on.
func TestFailureAsVariantAgreesWithReference(t *testing.T) {
	for _, seed := range propertySeeds(30) {
		r := rand.New(rand.NewPCG(seed, 0xa2))
		p := newPlanted()
		p.write("type NotFound { Key string }\n\ntype Hit { V int }\n\ntype Miss {}\n\ntype Found = Hit | NotFound\n\ntype Other = Hit | Miss\n\nfunc look(k string) int fails NotFound {\n\tif k == \"\" {\n\t\treturn NotFound{Key: k}\n\t}\n\t1\n}", false, 0)
		for i := range 1 + r.IntN(5) {
			k := variantResults[r.IntN(len(variantResults))]
			p.write("\n"+fmt.Sprintf(k.text, fmt.Sprintf("w%d", i)), k.fires, k.at)
		}
		p.write("\nfunc main() {\n}", false, 0)
		p.check(t, "failure-as-variant", "TestFailureAsVariantAgreesWithReference", seed)
	}
}

var vacuousBodies = []planKind{
	{true, 1, "test %s {\n\tassert 2 == 2\n}"},
	{true, 1, "test %s {\n\tlet x = 4\n\tassert x == 4\n}"},
	{true, 1, "test %s {\n\tassert int64(3) == 3\n}"},
	{true, 1, "test %s {\n\tlet _ = double(2)\n}"},
	{false, 0, "test %s {\n\tassert double(2) == 4\n}"},
	{false, 0, "test %s {\n\tlet x = double(2)\n\tlet y = x + 1\n\tassert y == 5\n}"},
	{false, 0, "test %s {\n\tlet _ = try parse(1)\n}"},
	{false, 0, "test %s {\n\tlet _ = parse(1) catch {\n\tcase Bad:\n\t\tassert false\n\t\treturn\n\t}\n}"},
}

// TestVacuousTestAgreesWithReference writes tests that assert on values
// they built (a literal, a local of a literal, a conversion), or on
// nothing, and tests that assert on a call's result directly or through
// locals, try a call, or assert in the arm of a catch on a call, and
// checks that vacuous-test fires on exactly the first. Mutants:
// as4-vacuous-conversion, as4-vacuous-direct-only, as4-vacuous-try,
// as4-vacuous-control.
func TestVacuousTestAgreesWithReference(t *testing.T) {
	for _, seed := range propertySeeds(30) {
		r := rand.New(rand.NewPCG(seed, 0xa3))
		p := newPlanted()
		p.write("type Bad { N int }\n\nfunc double(n int) int { n * 2 }\n\nfunc parse(n int) int fails Bad {\n\tif n < 0 {\n\t\treturn Bad{N: n}\n\t}\n\tn\n}", false, 0)
		for i := range 1 + r.IntN(5) {
			k := vacuousBodies[r.IntN(len(vacuousBodies))]
			p.write("\n"+fmt.Sprintf(k.text, fmt.Sprintf("T%d", i)), k.fires, k.at)
		}
		p.write("\nfunc main() {\n}", false, 0)
		p.check(t, "vacuous-test", "TestVacuousTestAgreesWithReference", seed)
	}
}

// A declaration of the parameter-clump generator: its text, its (name,
// type) pairs, and whether it constructs Spot.
type clumpKind struct {
	text      string
	pairs     []string
	construct bool
}

var clumpKinds = []clumpKind{
	{"func move%s(x int, y int, z int) int { x + y + z }", []string{"x int", "y int", "z int"}, false},
	{"func far%s(x int, y int, z int, w int) int { x + y + z + w }", []string{"x int", "y int", "z int", "w int"}, false},
	{"func near%s(x int, y int, z string) int { x + y + len(z) }", []string{"x int", "y int", "z string"}, false},
	{"func spot%s(x int, y int, z int) Spot { Spot{x: x, y: y, z: z} }", []string{"x int", "y int", "z int"}, true},
	{"type Box%s { x int, y int, w int }", []string{"x int", "y int", "w int"}, false},
	{"type Fail%[1]s { x int, y int, z int }\n\nfunc risky%[1]s() int fails Fail%[1]s { 1 }", nil, false},
}

// TestParameterClumpAgreesWithReference writes functions and records over
// a few groups of named, typed parameters (the same three, a superset,
// one of another type, a constructor of the record Spot, another record,
// a failure record) beside Spot, and checks that parameter-clump fires on
// exactly the declarations that share three or more pairs with each of
// two others,
// a constructor and its record aside, and a failure never. Mutants:
// as4-clump-constructor, as4-clump-type-ignored, as4-clump-two-enough,
// as4-clump-failure, as4-clump-one-partner.
func TestParameterClumpAgreesWithReference(t *testing.T) {
	spot := clumpKind{pairs: []string{"x int", "y int", "z int"}}
	shared := func(a, b clumpKind) int {
		n := 0
		for _, x := range a.pairs {
			for _, y := range b.pairs {
				if x == y {
					n++
				}
			}
		}
		return n
	}
	for _, seed := range propertySeeds(40) {
		r := rand.New(rand.NewPCG(seed, 0xa5))
		var picks []clumpKind
		for range 1 + r.IntN(5) {
			picks = append(picks, clumpKinds[r.IntN(len(clumpKinds))])
		}
		fires := func(d clumpKind, self int, isSpot bool) bool {
			partners := 0
			for j, e := range picks {
				if j == self || isSpot && e.construct {
					continue
				}
				if shared(d, e) >= 3 {
					partners++
				}
			}
			if !isSpot && !d.construct && shared(d, spot) >= 3 {
				partners++
			}
			return partners >= 2
		}
		p := newPlanted()
		p.write("type Spot { x int, y int, z int }", fires(spot, -1, true), 0)
		for i, k := range picks {
			p.write("\n"+fmt.Sprintf(k.text, fmt.Sprint(i)), fires(k, i, false), 1)
		}
		p.write("\nfunc main() {\n}", false, 0)
		p.check(t, "parameter-clump", "TestParameterClumpAgreesWithReference", seed)
	}
}

// TestConstantArgumentAgreesWithReference writes functions f0..fn of two
// int parameters and calls from main, each passing a variable first and
// a constant second, and controls: a function called once, one whose
// calls pass different constants, one used as a value, and a program
// without main (an open world); a test may call one of them twice with
// a constant, which does not count. constant-argument fires on exactly the
// second parameter of a function called twice or more, always with the
// same constant, never used as a value, in a program with main. Mutants:
// as4-carg-one-call, as4-carg-different, as4-carg-value-use,
// as4-carg-open, as4-carg-test-calls.
func TestConstantArgumentAgreesWithReference(t *testing.T) {
	for _, seed := range propertySeeds(40) {
		r := rand.New(rand.NewPCG(seed, 0xa6))
		closed := r.IntN(4) != 0
		n := 1 + r.IntN(4)
		calls := make([][]int, n)
		value := make([]bool, n)
		for i := range n {
			for range r.IntN(4) {
				calls[i] = append(calls[i], 1+r.IntN(2))
			}
			value[i] = r.IntN(5) == 0
			if value[i] {
				// two same constants, so only the value use keeps it quiet
				calls[i] = []int{1, 1}
			}
		}
		p := newPlanted()
		for i := range n {
			same := len(calls[i]) >= 2
			for _, c := range calls[i] {
				same = same && c == calls[i][0]
			}
			p.write(fmt.Sprintf("\nfunc f%d(a int, b int) {\n}", i), closed && same && !value[i], 1)
		}
		body := "\tlet v = 7"
		for i := range n {
			for _, c := range calls[i] {
				body += fmt.Sprintf("\n\tf%d(v, %d)", i, c)
			}
			if value[i] {
				body += fmt.Sprintf("\n\tlet g%d = f%d\n\tg%d(v, 9)", i, i, i)
			}
		}
		if closed {
			p.write("\nfunc main() {\n"+body+"\n}", false, 0)
		} else {
			p.write("\nfunc Drive() {\n"+body+"\n}", false, 0)
		}
		if r.IntN(2) == 0 {
			i := r.IntN(n)
			p.write(fmt.Sprintf("\ntest Calls {\n\tf%d(1, 3)\n\tf%d(1, 3)\n}", i, i), false, 0)
		}
		p.check(t, "constant-argument", "TestConstantArgumentAgreesWithReference", seed)
	}
}

// TestLongParameterListAgreesWithReference writes functions of three to
// eight parameters and a test of seven, and checks that
// long-parameter-list fires on exactly the functions of more than five.
// Mutant: as4-long-at-max.
func TestLongParameterListAgreesWithReference(t *testing.T) {
	for _, seed := range propertySeeds(30) {
		r := rand.New(rand.NewPCG(seed, 0xa7))
		p := newPlanted()
		p.write("test Wide(a int, b int, c int, d int, e int, f int, g int) {\n\tassert a + b + c + d + e + f + g == g + f + e + d + c + b + a\n}", false, 0)
		for i := range 1 + r.IntN(5) {
			k := 3 + r.IntN(6)
			params, sum := "", "0"
			for j := range k {
				if j > 0 {
					params += ", "
				}
				params += fmt.Sprintf("p%d int", j)
				sum += fmt.Sprintf(" + p%d", j)
			}
			p.write(fmt.Sprintf("\nfunc w%d(%s) int { %s }", i, params, sum), k > 5, 1)
		}
		p.write("\nfunc main() {\n}", false, 0)
		p.check(t, "long-parameter-list", "TestLongParameterListAgreesWithReference", seed)
	}
}

var verbNames = []planKind{
	{true, 1, "func handle%s() int { 1 }"},
	{true, 1, "func process%s() int { 2 }"},
	{false, 0, "func Process%s() int { 2 }"},
	{false, 0, "func handleKey%s() int { 3 }"},
	{false, 0, "func parse%s() int { 4 }"},
	{false, 0, "let run%s = 5"},
}

// TestBannedVerbsAgreeWithReference writes top-level functions named by a
// generic verb alone, unexported and exported, by a verb and a noun, by a
// specific verb, and a let named by a generic verb, in a package with or
// without main, and checks that banned-words fires on exactly the
// unexported functions named by the verb alone, and only without main.
// The %s is a digit, which nameWords splits off. Mutants:
// as4-verbs-any-word, as4-verbs-exported, as4-verbs-entry.
func TestBannedVerbsAgreeWithReference(t *testing.T) {
	for _, seed := range propertySeeds(30) {
		r := rand.New(rand.NewPCG(seed, 0xa8))
		entry := r.IntN(3) == 0
		p := newPlanted()
		if entry {
			p.write("func main() {\n}", false, 0)
		} else {
			p.write("func Begin() {\n}", false, 0)
		}
		for i := range 1 + r.IntN(5) {
			k := verbNames[r.IntN(len(verbNames))]
			p.write("\n"+fmt.Sprintf(k.text, fmt.Sprint(i)), k.fires && !entry, k.at)
		}
		p.check(t, "banned-words", "TestBannedVerbsAgreeWithReference", seed)
	}
}
