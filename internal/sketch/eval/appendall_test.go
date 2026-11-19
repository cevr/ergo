package eval

import (
	"math/rand/v2"
	"os"
	"slices"
	"strconv"
	"testing"
)

// TestAppendAllKeepsValues is the property of appendAll, which runs an
// extern of Go's append at its spread shape, append(xs, ys...): std's
// slices.AppendAll (SL2's dense.Pool PutList; EVAL-IFP). A slice is a value:
// AppendAll gives xs's elements then ys's, and no slice made before it
// changes, though appendAll grows a store in place when no view sees past
// xs's end. Each seed makes views over shared stores, some ending at their
// store's end, some before it, some with room to spare, and appends views
// of the same or another store (xs to itself too); the reference model
// keeps every view as a Go copy. A failing seed prints the step and both
// element lists, and the replay command. Mutants only it kills
// (testdata/mutants.json): oracle-o36-append-all-grows-shared,
// oracle-o36-append-all-drops-xs, oracle-o36-append-all-drops-ys.
func TestAppendAllKeepsValues(t *testing.T) {
	n := 300
	if testing.Short() {
		n = 100
	}
	first := uint64(0)
	if s := os.Getenv("ERGO_EVAL_APPENDALL_SEED"); s != "" {
		v, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		first, n = v, 1
	}
	pkg := checked(t, "test T {\n\tassert true\n}\n")
	in := &interp{budget: DefaultBudget()}
	fr := in.newFrame(pkg, pkg.Tests[0].Decl)
	ints := func(v sliceV) []int64 {
		var out []int64
		for _, e := range v.elems() {
			out = append(out, e.(int64))
		}
		return out
	}
	for seed := first; seed < first+uint64(n); seed++ {
		r := rand.New(rand.NewPCG(seed, 0x0a11))
		var views []sliceV
		var model [][]int64
		next := int64(0)
		fresh := func() {
			// A store of k elements with room for k+extra, and views of it.
			k, extra := r.IntN(5), r.IntN(4)
			data := make([]Value, k, k+extra)
			for i := range data {
				next++
				data[i] = next
			}
			b := &backing{data: data}
			for range 1 + r.IntN(3) {
				off := r.IntN(k + 1)
				m := r.IntN(k - off + 1)
				v := sliceV{b: b, off: off, n: m}
				views, model = append(views, v), append(model, ints(v))
			}
		}
		fresh()
		for step := 0; step < 12; step++ {
			if r.IntN(4) == 0 {
				fresh()
			}
			i, j := r.IntN(len(views)), r.IntN(len(views))
			in.steps, in.bytes = 0, 0
			got, ab := in.appendAll(fr, fr.decl, views[i], views[j])
			if ab != nil {
				t.Fatalf("seed %d step %d: %s", seed, step, ab.msg)
			}
			want := append(slices.Clone(model[i]), model[j]...)
			out := got.(sliceV)
			views, model = append(views, out), append(model, want)
			for k, v := range views {
				if g := ints(v); !slices.Equal(g, model[k]) {
					t.Fatalf("seed %d step %d: AppendAll(view %d, view %d): view %d is %v, want %v\nreplay: ERGO_EVAL_APPENDALL_SEED=%d go test -run TestAppendAllKeepsValues ./internal/sketch/eval",
						seed, step, i, j, k, g, model[k], seed)
				}
			}
		}
	}
}
