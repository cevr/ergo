package eval

import (
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
// range-live-domain, int8-conversion-dropped, match-arm-last,
// variant-key-ignored, literal-frame-copies, generic-zero-unbound,
// init-order-ignored, string-range-bytes, slice-expr-shares-array.
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
