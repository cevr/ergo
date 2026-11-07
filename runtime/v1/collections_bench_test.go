package v1

import (
	"cmp"
	"math/rand/v2"
	"slices"
	"testing"

	"ergo.local/prototype/runtime/rt"
)

// Benchmarks of std slices against hand-written Go (std-align S2a; plan
// section 4, Benchmark). Each pair does the same work. The ergo side of
// SortFunc and of Filter(Transform(xs, f), p) is the Go the lowering
// emits for those calls, copied from `ergo build -work` at this unit's
// head (benchEnv stands for the program's Env; Transform and Filter are
// the lowered Ergo bodies of std slices): the Ergo function value has the
// uniform shape func(*Env, a, b) (int, error), and the comparator adapter
// (lower adaptPlainFunc) calls it.

func benchInts(n int) []int {
	r := rand.New(rand.NewPCG(7, 7))
	xs := make([]int, n)
	for i := range xs {
		xs[i] = r.Int()
	}
	return xs
}

// BenchmarkSlicesSort: slices.Sort of 100,000 ints. Ergo's Sort returns a
// new slice, so the Go control clones and sorts: the same work.
func BenchmarkSlicesSort(b *testing.B) {
	xs := benchInts(100_000)
	b.Run("ergo", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			_ = SlicesSort(xs)
		}
	})
	b.Run("go", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			ys := slices.Clone(xs)
			slices.Sort(ys)
		}
	})
}

type benchEnv struct{ task *rt.Task }

// BenchmarkSlicesSortFunc: slices.SortFunc of 100,000 ints by a
// descending comparator. The ergo side passes the comparator through the
// adapter the lowering emits around an Ergo function literal.
func BenchmarkSlicesSortFunc(b *testing.B) {
	xs := benchInts(100_000)
	env := &benchEnv{task: rt.Root("bench")}
	b.Run("ergo", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			_ = SlicesSortFunc[int](xs, func(p int, p_1 int) int {
				r, _ := func(env_1 *benchEnv, a int, b int) (int, error) {
					v := CmpCompare[int](b, a)
					return v, nil
				}(env, p, p_1)
				return r
			})
		}
	})
	b.Run("go", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			ys := slices.Clone(xs)
			slices.SortFunc(ys, func(a, b int) int { return cmp.Compare(b, a) })
		}
	})
}

// benchTransform and benchFilter are the lowered Ergo bodies of
// slices.Transform and slices.Filter, as emitted.
func benchTransform[T any, U any](env *benchEnv, xs []T, f func(*benchEnv, T) (U, error)) ([]U, error) {
	env.task.Check()
	v := WithCapacity[U](len(xs))
	var out []U = v
	for _, v_1 := range xs {
		var x T = v_1
		t := out
		v_2, err := f(env, x)
		if err != nil {
			return *new([]U), err
		}
		out = Append[U](t, v_2)
		env.task.Check()
	}
	return out, nil
}

func benchFilter[T any](env *benchEnv, xs []T, keep func(*benchEnv, T) (bool, error)) ([]T, error) {
	env.task.Check()
	var out []T = []T{}
	for _, v := range xs {
		var x T = v
		v_1, err := keep(env, x)
		if err != nil {
			return *new([]T), err
		}
		if v_1 {
			out = Append[T](out, x)
		}
		env.task.Check()
	}
	return out, nil
}

// BenchmarkSlicesFilterTransform: Filter(Transform(xs, x*3), even) of
// 100,000 ints, against one hand loop with a preallocated result (the
// target of the fusion lever X3) and against Go's two-pass form.
func BenchmarkSlicesFilterTransform(b *testing.B) {
	xs := benchInts(100_000)
	env := &benchEnv{task: rt.Root("bench")}
	b.Run("ergo", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			v, _ := benchTransform[int, int](env, xs, func(env_1 *benchEnv, x int) (int, error) {
				return x * 3, nil
			})
			_, _ = benchFilter[int](env, v, func(env_2 *benchEnv, x_1 int) (bool, error) {
				return x_1%2 == 0, nil
			})
		}
	})
	b.Run("go-two-pass", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			ys := make([]int, 0, len(xs))
			for _, x := range xs {
				ys = append(ys, x*3)
			}
			var zs []int
			for _, y := range ys {
				if y%2 == 0 {
					zs = append(zs, y)
				}
			}
			_ = zs
		}
	})
	b.Run("go-one-loop", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			zs := make([]int, 0, len(xs))
			for _, x := range xs {
				if y := x * 3; y%2 == 0 {
					zs = append(zs, y)
				}
			}
			_ = zs
		}
	})
}
