package lint_test

import (
	"fmt"
	"math/rand/v2"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/lint"
)

// The speed preset (LH3): for each rule a generator of functions, each
// kind with whether the rule fires on it and where (at counts from the
// blank line the generator writes before the declaration), and a run of
// the rule alone on what it wrote.

// speedHelpers are the declarations the speed generators call.
const speedHelpers = `type Counter { N int }

func read(c Counter, k int) int { c.N + k }

func readAfter(k int, c Counter) int { c.N + k }

func bump(s mut Counter) int {
	s.N = s.N + 1
	s.N
}

func double(n int) int { n * 2 }

func count(ys []int, v int) int {
	let mut n = 0
	for y in ys {
		if y == v {
			n = n + 1
		}
	}
	n
}

func size(ys []int) int { len(ys) }

// has is whether v is in ys.
//
// Scans: ys, until it finds v.
func has(ys []int, v int) bool { v >= 0 && len(ys) > 0 }

// pick is the first of ys.
//
// Scans: zs, which it does not take.
func pick(ys []int) int { len(ys) }

func countIn(ys []int, zs []int) int {
	let mut n = 0
	for z in zs {
		n = n + z
	}
	n + len(ys)
}`

func speedProperty(t *testing.T, rule, name string, salt uint64, kinds []planKind) {
	t.Helper()
	for _, seed := range propertySeeds(30) {
		r := rand.New(rand.NewPCG(seed, salt))
		p := newPlanted()
		p.write(speedHelpers, false, 0)
		for i := range 1 + r.IntN(5) {
			k := kinds[r.IntN(len(kinds))]
			p.write("\n"+fmt.Sprintf(k.text, fmt.Sprintf("f%d", i)), k.fires, k.at)
		}
		p.write("\nfunc main() {\n}", false, 0)
		p.check(t, rule, name, seed)
	}
}

var concatKinds = []planKind{
	{true, 4, "func %s(xs []string) string {\n\tlet mut out = \"\"\n\tfor x in xs {\n\t\tout = out + x + \",\"\n\t}\n\tout\n}"},
	{true, 4, "func %s(xs []string) string {\n\tlet mut out = \"\"\n\tfor x in xs {\n\t\tout = x + out\n\t}\n\tout\n}"},
	{true, 5, "func %s(n int) string {\n\tlet mut i = 0\n\tlet mut out = \"\"\n\tfor i < n {\n\t\tout = out + \"a\"\n\t\ti = i + 1\n\t}\n\tout\n}"},
	{false, 0, "func %s(xs []int) int {\n\tlet mut n = 0\n\tfor x in xs {\n\t\tn = n + x\n\t}\n\tn\n}"},
	{false, 0, "func %s(xs []string) int {\n\tlet mut n = 0\n\tfor x in xs {\n\t\tlet mut s = \"<\"\n\t\ts = s + x\n\t\tn = n + len(s)\n\t}\n\tn\n}"},
	{false, 0, "func %s(xs []string) string {\n\tlet mut out = \"\"\n\tfor x in xs {\n\t\tout = x\n\t}\n\tout = out + \"!\"\n\tout\n}"},
}

// TestConcatInLoopAgreesWithReference writes loops that grow a string by
// + (appended, prepended, in a for-in and a for with a condition) and
// ones that do not (an int sum, a string declared in the loop, a + after
// the loop), and checks that concat-in-loop fires on exactly the growing
// assignments in a loop. Mutants: lh3-concat-outside-loop,
// lh3-concat-declared-inside, lh3-concat-any-type, lh3-concat-left-only.
func TestConcatInLoopAgreesWithReference(t *testing.T) {
	speedProperty(t, "concat-in-loop", "TestConcatInLoopAgreesWithReference", 0x51, concatKinds)
}

var traversalKinds = []planKind{
	{true, 7, "func %s(xs []int) int {\n\tlet mut a = 0\n\tfor x in xs {\n\t\ta = a + x\n\t}\n\tlet mut b = 0\n\tfor x in xs {\n\t\tb = b + x * x\n\t}\n\ta + b\n}"},
	{false, 0, "func %s(xs []int) int {\n\tlet mut a = 0\n\tfor x in xs {\n\t\ta = a + x\n\t}\n\tlet mut b = 0\n\tfor x in xs {\n\t\tb = b + x * a\n\t}\n\tb\n}"},
	{false, 0, "func %s(xs []int) int {\n\tlet a = 0\n\tfor x in xs {\n\t\tif x < 0 {\n\t\t\treturn 0\n\t\t}\n\t}\n\tlet mut b = 0\n\tfor x in xs {\n\t\tb = b + x\n\t}\n\tb + a\n}"},
	{false, 0, "func %s(xs []int) int {\n\tlet mut a = 0\n\tfor x in xs {\n\t\ta = a + x\n\t}\n\tlet k = double(3)\n\tlet mut b = 0\n\tfor x in xs {\n\t\tb = b + x\n\t}\n\ta + b + k\n}"},
	{false, 0, "func %s(xs []int) int {\n\tlet mut ys = xs\n\tlet mut a = 0\n\tfor y in ys {\n\t\ta = a + y\n\t}\n\tys = xs\n\tlet mut b = 0\n\tfor y in ys {\n\t\tb = b + y\n\t}\n\ta + b\n}"},
	{false, 0, "func %s(xs []int, zs []int) int {\n\tlet mut a = 0\n\tfor x in xs {\n\t\ta = a + x\n\t}\n\tlet mut b = 0\n\tfor z in zs {\n\t\tb = b + z\n\t}\n\ta + b\n}"},
}

// TestRepeatedTraversalAgreesWithReference writes two loops in a row over
// one collection, and the same two with what makes the second needed (it
// reads the first's result, the first can return early, a call or a
// write of the collection between them) or over another collection, and
// checks that repeated-traversal fires on exactly the free second loops.
// Mutants: lh3-trav-early, lh3-trav-reads-first, lh3-trav-call-between,
// lh3-trav-write-between.
func TestRepeatedTraversalAgreesWithReference(t *testing.T) {
	speedProperty(t, "repeated-traversal", "TestRepeatedTraversalAgreesWithReference", 0x52, traversalKinds)
}

var quadraticKinds = []planKind{
	{true, 4, "func %s(xs []int) int {\n\tlet mut n = 0\n\tfor x in xs {\n\t\tfor y in xs {\n\t\t\tif x == y {\n\t\t\t\tn = n + 1\n\t\t\t}\n\t\t}\n\t}\n\tn\n}"},
	{true, 4, "func %s(xs []int) int {\n\tlet mut n = 0\n\tfor x in xs {\n\t\tn = n + count(xs, x)\n\t}\n\tn\n}"},
	{false, 0, "func %s(xs []int, zs []int) int {\n\tlet mut n = 0\n\tfor x in xs {\n\t\tfor z in zs {\n\t\t\tn = n + x * z\n\t\t}\n\t}\n\tn\n}"},
	{false, 0, "func %s(xs []int) int {\n\tlet mut n = 0\n\tfor x in xs {\n\t\tn = n + x * size(xs)\n\t}\n\tn\n}"},
	{false, 0, "func %s(xs []int) int {\n\tfor x in xs {\n\t\tif x < 0 {\n\t\t\treturn count(xs, x)\n\t\t}\n\t}\n\t0\n}"},
	{true, 4, "func %s(xs []int) int {\n\tlet mut n = 0\n\tfor x in xs {\n\t\tif has(xs, x) {\n\t\t\tn = n + 1\n\t\t}\n\t}\n\tn\n}"},
	{false, 0, "func %s(xs []int) int {\n\tlet mut n = 0\n\tfor x in xs {\n\t\tn = n + x * pick(xs)\n\t}\n\tn\n}"},
	{false, 0, "func %s(xs []int, zs []int) int {\n\tlet mut n = 0\n\tfor x in xs {\n\t\tn = n + x * countIn(xs, zs)\n\t}\n\tn\n}"},
	{true, 4, "func %s(xs []int, zs []int) int {\n\tlet mut n = 0\n\tfor x in xs {\n\t\tn = n + x * countIn(zs, xs)\n\t}\n\tn\n}"},
}

// TestQuadraticScanAgreesWithReference writes loops over a collection
// that walk it again (a nested loop, a call of a function that loops over
// the parameter it is passed to) and ones that do not (a nested loop over
// another collection, a call that only measures it, a call that loops
// over another parameter), and calls of functions whose doc says Scans:
// of the parameter passed (has) or of another name (pick), and checks
// that quadratic-scan fires on exactly the walks inside. Mutants:
// lh3-quad-nested, lh3-quad-any-callee, lh3-quad-param-first,
// lh3-quad-scans-ignored, lh3-quad-scans-any-name, lh3-quad-return.
func TestQuadraticScanAgreesWithReference(t *testing.T) {
	speedProperty(t, "quadratic-scan", "TestQuadraticScanAgreesWithReference", 0x53, quadraticKinds)
}

var snapshotKinds = []planKind{
	{true, 2, "func %s(s mut Counter) int {\n\tread(*s, bump(s))\n}"},
	{false, 0, "func %s(s mut Counter) int {\n\tread(*s, 1)\n}"},
	{false, 0, "func %s(s mut Counter) int {\n\treadAfter(bump(s), *s)\n}"},
	{false, 0, "func %s(s mut Counter) int {\n\tread(*s, double(s.N))\n}"},
}

// TestSnapshotArgumentAgreesWithReference writes calls with an argument
// *s and checks that snapshot-argument fires on exactly those where a
// later argument passes s to a call; not when s goes to a call before
// *s, nor when a later call reads a field of s. Mutants:
// lh3-snap-any-order, lh3-snap-field-read.
func TestSnapshotArgumentAgreesWithReference(t *testing.T) {
	speedProperty(t, "snapshot-argument", "TestSnapshotArgumentAgreesWithReference", 0x54, snapshotKinds)
}

var largeKinds = []planKind{
	{true, 5, "type Big%[1]s { A [20]int, B [20]int }\n\nfunc %[1]s(xs []Big%[1]s) int {\n\tlet mut n = 0\n\tfor b in xs {\n\t\tn = n + b.A[0] + b.B[0]\n\t}\n\tn\n}"},
	{true, 5, "type Big%[1]s { A [33]int }\n\nfunc %[1]s(xs []Big%[1]s) int {\n\tlet mut n = 0\n\tfor b in xs {\n\t\tn = n + b.A[0]\n\t}\n\tn\n}"},
	{false, 0, "type Edge%[1]s { A [32]int }\n\nfunc %[1]s(xs []Edge%[1]s) int {\n\tlet mut n = 0\n\tfor b in xs {\n\t\tn = n + b.A[0]\n\t}\n\tn\n}"},
	{false, 0, "type Small%[1]s { A [10]int, S string }\n\nfunc %[1]s(xs []Small%[1]s) int {\n\tlet mut n = 0\n\tfor b in xs {\n\t\tn = n + b.A[0] + len(b.S)\n\t}\n\tn\n}"},
}

// TestLargeValueCopyAgreesWithReference writes loops over records of 320,
// 264, 256 and 96 bytes, and checks that large-value-copy, at its default
// max of 256, fires on exactly the loops whose element is bigger. Mutants:
// lh3-large-boundary.
func TestLargeValueCopyAgreesWithReference(t *testing.T) {
	speedProperty(t, "large-value-copy", "TestLargeValueCopyAgreesWithReference", 0x55, largeKinds)
}

// TestSpeedPresetOnExamples is LH3's acceptance check: the speed preset
// over examples/jobs and examples/confparse names the two strings built
// by + in a loop that the catalog found (PF13). Mutant:
// lh3-concat-assign-form (only s += e counted).
func TestSpeedPresetOnExamples(t *testing.T) {
	conf, err := lint.Builtin().Configure(lint.Config{Version: lint.ConfigVersion}, []string{lint.Speed})
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join("..", "..", "..", "examples")
	got := append(layoutFindings(t, filepath.Join(root, "jobs"), conf), layoutFindings(t, filepath.Join(root, "confparse"), conf)...)
	for _, want := range []string{"api/routes.ergo:268 concat-in-loop", "config/encode.ergo:26 concat-in-loop"} {
		if !slices.ContainsFunc(got, func(g string) bool { return strings.HasSuffix(g, want) }) {
			t.Errorf("the speed preset does not name %s; it found %v", want, got)
		}
	}
}
