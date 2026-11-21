package lint_test

import (
	"fmt"
	"math/rand/v2"
	"testing"
)

// The defensive family of the strict preset (LH2c, catalog §4): for each
// rule a generator and a reference that decides from what the generator
// wrote, never from the program text.

// planKind is a declaration a generator writes: a format with one %s (its
// name), whether the rule fires on it, and the line it fires on, counted
// from the declaration's first line (the text starts with a newline).
type planKind struct {
	fires bool
	at    int
	text  string
}

var defaultArms = []planKind{
	{true, 3, "func %s(n int) int {\n\tget(n) catch {\n\tcase _:\n\t\t0\n\t}\n}"},
	{true, 3, "func %s(n int) int {\n\tget(n) catch {\n\tcase _:\n\t\treturn -1\n\t}\n}"},
	{true, 3, "func %s(n int) string {\n\tlabel(n) catch {\n\tcase _:\n\t\t\"\"\n\t}\n}"},
	{true, 3, "func %s(n int) []int {\n\tlist(n) catch {\n\tcase _:\n\t\t[]int{}\n\t}\n}"},
	{false, 0, "func %s(n int) int {\n\tget(n) catch {\n\tcase Bad:\n\t\t0\n\tcase Gone:\n\t\t1\n\t}\n}"},
	{false, 0, "func %s(n int) bool {\n\tflag(n) catch {\n\tcase _:\n\t\tfalse\n\t}\n}"},
	{false, 0, "func %s(n int) int {\n\tget(n) catch {\n\tcase _:\n\t\tn\n\t}\n}"},
	{false, 0, "law %s(n int) {\n\tlet v = get(n) catch {\n\tcase _:\n\t\t0\n\t}\n\tv >= 0 || v < 0\n}"},
}

// TestDefaultOnFailureAgreesWithReference writes functions whose catch
// takes every failure and gives a number, a string or an empty slice, and
// controls that name each failure, give a bool, give a computed value or
// sit in a law, and checks that default-on-failure fires on exactly the
// first. Mutants: lh2c-default-named-arm, lh2c-default-bool,
// lh2c-default-law, lh2c-default-empty-composite.
func TestDefaultOnFailureAgreesWithReference(t *testing.T) {
	for _, seed := range propertySeeds(40) {
		r := rand.New(rand.NewPCG(seed, 0xd1))
		p := newPlanted()
		p.write("type Bad { N int }\n\ntype Gone { N int }\n\nfunc get(n int) int fails Bad | Gone {\n\tif n > 9 {\n\t\treturn Gone{N: n}\n\t}\n\tif n < 0 {\n\t\treturn Bad{N: n}\n\t}\n\tn\n}\n\nfunc label(n int) string fails Bad | Gone { \"\\(try get(n))\" }\n\nfunc list(n int) []int fails Bad | Gone { []int{try get(n)} }\n\nfunc flag(n int) bool fails Bad | Gone { try get(n) > 3 }", false, 0)
		for i := range 1 + r.IntN(5) {
			k := defaultArms[r.IntN(len(defaultArms))]
			p.write("\n"+fmt.Sprintf(k.text, fmt.Sprintf("W%d", i)), k.fires, k.at)
		}
		p.write("\nfunc main() {\n}", false, 0)
		p.check(t, "default-on-failure", "TestDefaultOnFailureAgreesWithReference", seed)
	}
}

var pureBodies = []planKind{
	{true, 3, "func %s(a int) int {\n\tlet x = sq(a)\n\tlet y = sq(a)\n\tx + y\n}"},
	{true, 2, "func %s(a int) int {\n\tsq(a) + sq(a)\n}"},
	{true, 4, "func %s(a int) int {\n\tlet x = sq(a)\n\tif a > 0 {\n\t\treturn sq(a)\n\t}\n\tx\n}"},
	{true, 3, "func %s(a int) int {\n\tlet x = mul(a, 2)\n\tx + mul(a, 2)\n}"},
	{false, 0, "func %s(a int) int {\n\tsq(a) + sq(1)\n}"},
	{false, 0, "func %s(a int) int {\n\tlet x = mul(a, 2)\n\tx + mul(a, 3)\n}"},
	{false, 0, "func %s(a int) int {\n\tif a > 0 {\n\t\treturn sq(a)\n\t}\n\tsq(a)\n}"},
	{false, 0, "func %s(a int) int {\n\tlet mut b = a\n\tlet x = sq(b)\n\tb = b + 1\n\tx + sq(b)\n}"},
	{false, 0, "func %s(a int) int needs Scheduler {\n\troll(a) + roll(a)\n}"},
	{false, 0, "func %s(a int) int {\n\tlen(fresh(a)) + len(fresh(a))\n}"},
	{false, 0, "func %s(a int) int {\n\tlet xs = []int{a}\n\ttotal(xs) + total(xs)\n}"},
}

// TestRepeatedPureCallAgreesWithReference writes functions that call a
// pure function twice with the same arguments, where the first call runs
// whenever the second does, and controls with other arguments, the first
// call in a branch, a `let mut` argument changed between the calls, a
// function that sleeps, one that gives a fresh slice, and a slice
// argument (another name may change it); it checks that
// repeated-pure-call fires on exactly the second call of the first kind.
// Mutants: lh2c-pure-mut-ignored, lh2c-pure-branch-dominates,
// lh2c-pure-footprint-ignored, lh2c-pure-args-ignored,
// lh2c-pure-slice-arg.
func TestRepeatedPureCallAgreesWithReference(t *testing.T) {
	for _, seed := range propertySeeds(40) {
		r := rand.New(rand.NewPCG(seed, 0xd2))
		p := newPlanted()
		p.write("func sq(n int) int { n * n }\n\nfunc mul(n int, m int) int { n * m }\n\nfunc roll(n int) int needs Scheduler {\n\tScheduler.Sleep(1ms)\n\tn\n}\n\nfunc fresh(n int) []int { []int{n} }\n\nfunc total(xs []int) int { len(xs) }", false, 0)
		for i := range 1 + r.IntN(5) {
			k := pureBodies[r.IntN(len(pureBodies))]
			p.write("\n"+fmt.Sprintf(k.text, fmt.Sprintf("w%d", i)), k.fires, k.at)
		}
		p.write("\nfunc main() {\n}", false, 0)
		p.check(t, "repeated-pure-call", "TestRepeatedPureCallAgreesWithReference", seed)
	}
}

var refinedGuards = []planKind{
	{true, 2, "func %s(d Digit) int {\n\tif d < 0 {\n\t\treturn 1\n\t}\n\t0\n}"},
	{true, 2, "func %s(d Digit) int {\n\tif d > 9 {\n\t\treturn 1\n\t}\n\t0\n}"},
	{true, 2, "func %s(d Digit) int {\n\tif 0 > d {\n\t\treturn 1\n\t}\n\t0\n}"},
	{true, 2, "func %s(d Digit) int {\n\tif d >= 0 {\n\t\treturn 1\n\t}\n\t0\n}"},
	{true, 2, "func %s(s Name) int {\n\tif len(s) == 0 {\n\t\treturn 1\n\t}\n\t0\n}"},
	{false, 0, "func %s(d Digit) int {\n\tif d < 5 {\n\t\treturn 1\n\t}\n\t0\n}"},
	{false, 0, "func %s(d Digit) int {\n\tif d == 3 {\n\t\treturn 1\n\t}\n\t0\n}"},
	{false, 0, "func %s(d Digit) int {\n\tif 5 > d {\n\t\treturn 1\n\t}\n\t0\n}"},
	{false, 0, "func %s(s Name) int {\n\tif len(s) > 5 {\n\t\treturn 1\n\t}\n\t0\n}"},
	{false, 0, "func %s(n int) int {\n\tif n < 0 {\n\t\treturn 1\n\t}\n\t0\n}"},
}

// TestGuardTheTypeProvesAgreesWithReference writes guards on a refined
// Digit (0 to 9) and a Name of 1 to 20 bytes that the refinement decides,
// with the constant on either side, and guards it does not decide (a value
// inside the range, a plain int), and checks that guard-the-type-proves
// fires on exactly the first. Mutants: lh2c-guard-mirror-dropped,
// lh2c-guard-eq-inside, lh2c-guard-len-ignored.
func TestGuardTheTypeProvesAgreesWithReference(t *testing.T) {
	for _, seed := range propertySeeds(40) {
		r := rand.New(rand.NewPCG(seed, 0xd3))
		p := newPlanted()
		p.write("type Digit int where 0 <= it && it <= 9\n\ntype Name string where 1 <= len(it) && len(it) <= 20", false, 0)
		for i := range 1 + r.IntN(5) {
			k := refinedGuards[r.IntN(len(refinedGuards))]
			p.write("\n"+fmt.Sprintf(k.text, fmt.Sprintf("w%d", i)), k.fires, k.at)
		}
		p.write("\nfunc main() {\n}", false, 0)
		p.check(t, "guard-the-type-proves", "TestGuardTheTypeProvesAgreesWithReference", seed)
	}
}

// A guard of the repeated-guard generator: its key (the same key is the
// same guard), and the function it opens.
type guardKind struct {
	key  int // -1: not a guard
	text string
}

var guardKinds = []guardKind{
	{0, "func %s(x int) int fails Bad {\n\tif x < 0 {\n\t\treturn Bad{N: x}\n\t}\n\tx\n}"},
	{0, "func %s(y int) int fails Bad {\n\tif y < 0 {\n\t\treturn Bad{N: 0}\n\t}\n\ty + 1\n}"},
	{1, "func %s(x int) int fails Bad {\n\tif x < 1 {\n\t\treturn Bad{N: x}\n\t}\n\tx\n}"},
	{2, "func %s(x int64) int64 fails Bad {\n\tif x < 0 {\n\t\treturn Bad{N: 1}\n\t}\n\tx\n}"},
	{3, "func %s(s string) int fails Bad {\n\tif len(s) == 0 || len(s) > 8 {\n\t\treturn Bad{N: len(s)}\n\t}\n\tlen(s)\n}"},
	{-1, "func %s(x int) int fails Bad {\n\tif x < 0 {\n\t\treturn Bad{N: x}\n\t} else {\n\t\treturn 1\n\t}\n}"},
	{-1, "func %s(x int, y int) int fails Bad {\n\tif x < y {\n\t\treturn Bad{N: x}\n\t}\n\tx\n}"},
	{-1, "func %s(x int) int fails Bad {\n\tif x < 0 {\n\t\treturn 0\n\t}\n\tlet b = Bad{N: x}\n\tif b.N > 99 {\n\t\treturn b\n\t}\n\tx\n}"},
}

// TestRepeatedGuardAgreesWithReference writes functions that open with a
// guard of a few kinds (the same condition on parameters of the same type
// under other names, another constant, another type, a length range) and
// controls (an if with an else, two parameters, an early return of an
// ordinary value, which handles a case and refuses nothing), and checks that
// repeated-guard fires on exactly the guards whose kind opens three or
// more functions. Mutants: lh2c-rguard-type-ignored,
// lh2c-rguard-constant-ignored, lh2c-rguard-else-counted,
// lh2c-rguard-two-enough, lh2c-rguard-ordinary-return.
func TestRepeatedGuardAgreesWithReference(t *testing.T) {
	for _, seed := range propertySeeds(40) {
		r := rand.New(rand.NewPCG(seed, 0xd4))
		var picks []int
		count := map[int]int{}
		for range 2 + r.IntN(7) {
			k := r.IntN(len(guardKinds))
			picks = append(picks, k)
			count[guardKinds[k].key]++
		}
		p := newPlanted()
		p.write("type Bad { N int }\n\nfunc main() {\n}", false, 0)
		for i, k := range picks {
			g := guardKinds[k]
			p.write("\n"+fmt.Sprintf(g.text, fmt.Sprintf("g%d", i)), g.key >= 0 && count[g.key] >= 3, 2)
		}
		p.check(t, "repeated-guard", "TestRepeatedGuardAgreesWithReference", seed)
	}
}
