package syntax

import (
	"math/rand/v2"
	"strings"
	"testing"
)

// TestTupleTypesParseInEveryPosition: a generated tuple type, whose
// elements are any types (a slice, a borrow, a function type, a Map, an
// instance, a nested tuple, in any position), parses wherever a type is
// written: a parameter, a result, a field, a let, a type argument in a
// type, and a type argument in an expression's index (lane MB F9). The
// mutant it kills: an expression's index does not read `(` as a type
// (parseIndexValue), so `Some[([]int, string)]{t}` is "a type is not a
// value". Seeded and replayable: a failure prints the seed and the source.
func TestTupleTypesParseInEveryPosition(t *testing.T) {
	leaves := []string{"int", "string", "[]int", "*int", "func() int", "func(x int) string", "Map[string, int]", "Option[int]", "[][]string"}
	var gen func(r *rand.Rand, depth int) string
	gen = func(r *rand.Rand, depth int) string {
		if depth > 0 && r.IntN(4) == 0 {
			n := 2 + r.IntN(2)
			elts := make([]string, n)
			for i := range elts {
				elts[i] = gen(r, depth-1)
			}
			return "(" + strings.Join(elts, ", ") + ")"
		}
		return leaves[r.IntN(len(leaves))]
	}
	positions := []func(ty string) string{
		func(ty string) string { return "func f(p " + ty + ") {}\n" },
		func(ty string) string { return "func f() " + ty + " { g() }\n" },
		func(ty string) string { return "type R { P " + ty + " }\n" },
		func(ty string) string { return "func f() {\n\tlet x " + ty + " = g()\n\t_ = x\n}\n" },
		func(ty string) string { return "func f(o Option[" + ty + "]) {}\n" },
		func(ty string) string { return "func f() {\n\tlet o = None[" + ty + "]{}\n\t_ = o\n}\n" },
		func(ty string) string { return "func f() {\n\tlet o = g[" + ty + ", int](1)\n\t_ = o\n}\n" },
	}
	for seed := uint64(1); seed <= 300; seed++ {
		r := rand.New(rand.NewPCG(seed, 0xf9))
		n := 2 + r.IntN(3)
		elts := make([]string, n)
		for i := range elts {
			elts[i] = gen(r, 2)
		}
		ty := "(" + strings.Join(elts, ", ") + ")"
		for i, pos := range positions {
			src := pos(ty)
			if _, err := Parse(src); err != nil {
				t.Fatalf("seed %d, position %d: %v\n%s", seed, i, err, src)
			}
		}
	}
}
