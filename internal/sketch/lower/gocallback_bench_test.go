package lower

import "testing"

// BenchmarkGoCallbackSort is IG6's control: slices.SortFunc over a copy of
// 64 ints with an Ergo comparator literal (the adapter of Go's func type
// around the Ergo value), against hand Go with a Go closure over a
// slices.Clone (Ergo copies the live argument by the value rule), in the
// same binary. Target: equal within noise (one
// closure each; the adapter adds one indirect call per comparison).
func BenchmarkGoCallbackSort(b *testing.B) {
	const src = `import go slices

func Sorted(xs []int) int {
	slices.SortFunc(xs, func(a int, b int) int { a - b })
	len(xs)
}

func main() needs Console {
	Console.Println("\(Sorted([]int{3, 1, 2}))")
}
`
	gen := func(mainGo string) string {
		return `package main

import (
	"slices"
	"testing"
)

var input = func() []int {
	xs := make([]int, 64)
	for i := range xs {
		xs[i] = (i * 7919) % 101
	}
	return xs
}()

var sink int

func sortedGo(xs []int) int {
	ys := slices.Clone(xs)
	slices.SortFunc(ys, func(a, b int) int { return a - b })
	return len(ys)
}

func BenchmarkSortErgo(b *testing.B) {
	for range b.N {
		sink = ` + emittedCall(mainGo, "Sorted", "input") + `
	}
}

func BenchmarkSortGo(b *testing.B) {
	for range b.N {
		sink = sortedGo(input)
	}
}
`
	}
	emittedBench(b, "gocallback_sort", src, gen, "SortErgo", "SortGo")
}
