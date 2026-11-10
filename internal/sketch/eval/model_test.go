package eval

import (
	"fmt"
	"os"
	"strconv"
	"testing"

	"ergo.local/prototype/internal/sketch/eval/evalgen"
)

// TestEvalAgreesWithModel is the evaluator's seeded property over values
// and control (L7 EV2) against evalgen's reference model, a Go interpreter
// of the generated statements that deep-copies on every assignment and so
// shares nothing (AGENTS.md, Tests: a property against a reference model).
// Each seed is a program whose r() mixes integer widths, strings, slices,
// a slice of slices, Maps, arrays, records holding slices and a union,
// through loops, matches, generic calls, a function literal that writes
// its captures, package lets and conversions, with aliasing between
// locals; its test asserts r() equals the model's value. The evaluator
// must pass it, or report the defect the model raises. The seeds must
// reach every form evalgen.Kinds lists. A failing seed prints the source,
// both verdicts and the replay command. TestEvalAgreesWithGo (lower)
// holds the native Go program to the same model.
//
// Mutants it kills (testdata/mutants.json): slice-write-shared,
// map-write-shared, append-tip-owned, handover-lends-not, record-field-shared,
// range-snapshot-domain, int8-conversion-dropped, literal-frame-copies,
// init-order-ignored, string-range-bytes.
func TestEvalAgreesWithModel(t *testing.T) {
	n := 300
	if testing.Short() {
		n = 120
	}
	first := uint64(1)
	if s := os.Getenv("ERGO_EVAL_MODEL_SEED"); s != "" {
		v, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		first, n = v, 1
	}
	kinds := map[string]int{}
	defects := 0
	for seed := first; seed < first+uint64(n); seed++ {
		p := evalgen.Generate(seed)
		for k, c := range p.Kinds {
			kinds[k] += c
		}
		want := Passed
		if p.Defect {
			want = Defect
			defects++
		}
		pkg := checked(t, p.Source)
		rep, err := Run(pkg, DefaultBudget())
		if err != nil {
			t.Fatalf("seed %d: %v\n%s", seed, err, p.Source)
		}
		if len(rep.Results) != 1 || rep.Results[0].Status != want {
			t.Fatalf("seed %d: the evaluator says %+v, the model %s\n%s\nreplay: ERGO_EVAL_MODEL_SEED=%d go test -run TestEvalAgreesWithModel ./internal/sketch/eval", seed, rep.Results, want, p.Source, seed)
		}
	}
	if n > 1 {
		for _, k := range evalgen.Kinds {
			if kinds[k] == 0 {
				t.Errorf("no seed reached the form %s", k)
			}
		}
		if defects == 0 || defects == n {
			t.Errorf("%d of %d seeds defect: the verdicts are not both reached", defects, n)
		}
	}
}

// TestEvalMetersCollectionsAndCalls checks counted work at small increasing
// sizes through Run: creating an array and copying it twice costs at least
// three element visits per element, independent of AST dispatch. Removing
// ownSlice's charge (copy-fuel-ignored) leaves only the default's linear work.
// The same sizes recurse beyond an explicit call-depth bound; removing that
// bound (recursion-depth-ignored) completes instead of reporting incomplete.
// Existing EV1 loop fuel checks reach neither bulk copying nor call depth.
func TestEvalMetersCollectionsAndCalls(t *testing.T) {
	var previous int64
	var last int
	for n := 8; n <= 128; n *= 2 {
		src := fmt.Sprintf(`test Copies {
	let mut a [%d]int
	let b = a
	a[0] = 1
	let c = a
	a[0] = 2
	assert b[0] == 0 && c[0] == 1 && a[0] == 2
}
`, n)
		rep, err := Run(checked(t, src), DefaultBudget())
		if err != nil || len(rep.Results) != 1 || rep.Results[0].Status != Passed {
			t.Fatalf("n=%d: %+v %v\n%s", n, rep, err, src)
		}
		if last != 0 && rep.Steps-previous < int64(3*(n-last)) {
			t.Fatalf("n=%d: only %d extra steps for %d extra elements (two copies and a default)", n, rep.Steps-previous, n-last)
		}
		previous, last = rep.Steps, n
		src = fmt.Sprintf(`func down(n int) int {
	if n == 0 { return 0 }
	down(n - 1)
}
test Calls { assert down(%d) == 0 }
`, n)
		rep, err = Run(checked(t, src), Budget{Steps: 100_000, Depth: n / 2})
		if err != nil || len(rep.Results) != 1 || rep.Results[0].Status != Incomplete {
			t.Fatalf("n=%d: %+v %v\n%s", n, rep, err, src)
		}
	}
}
