package irspike

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"ergo.local/prototype/internal/sketch/eval"
)

// TestIRMethodValuesMatchSource is CV1's agreement property for method
// values (gen_methodvalue.go): for each seed, a program of method values
// and their eta twins, taken before a reassignment, a write through a
// borrow, a later argument's write or (a method value only) an escape,
// prints the model's line on the source path, on the IR path (every case
// an IR body) and in the evaluator (its test MethodValuesAgree passes). A
// method value binds the receiver at the take; its non-escaping twin sees
// the later write. The cases take their numbers from main's mutable
// local, so the IR's build-time fold computes none of them and each runs
// its IR body. It is the killer of cv-mv-ir-late-binding in lower's
// ledger (the IR's receiver not spilled): with constant arguments the
// fold computed every case and the mutant looked equivalent (CV1
// fork-answer.md).
// A failing seed prints the Ergo source, the outcomes and its replay. It
// builds Go for the reason needsBuild gives.
func TestIRMethodValuesMatchSource(t *testing.T) {
	src, ir := needsBuild(t)
	first, count := envInt("ERGO_IRSPIKE_SEED", 1), envInt("ERGO_IRSPIKE_SEEDS", 4)
	for seed := first; seed < first+count; seed++ {
		dir := t.TempDir()
		text, want := GenerateMethodValue(seed)
		replay := "replay: ERGO_IRSPIKE_SEED=" + itoa(seed) + " ERGO_IRSPIKE_SEEDS=1 go test -run TestIRMethodValuesMatchSource ./internal/experimental/irspike"
		file := filepath.Join(dir, "p.ergo")
		if err := os.WriteFile(file, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
		u, err := Load(file)
		if err != nil {
			t.Fatalf("seed %d: the generated program does not check: %v\n%s", seed, err, text)
		}
		rep, err := eval.Run(u.Package, eval.DefaultBudget())
		if err != nil || len(rep.Results) != 1 || rep.Results[0].Status != eval.Passed {
			t.Errorf("seed %d: the evaluator disagrees with the model: %v %+v\n%s\n%s", seed, err, rep.Results, replay, text)
		}
		sb, ib, irRep := both(t, u, src, ir, dir, BuildOptions{})
		a, b := run(t, sb), run(t, ib)
		if a.stdout != want || a.exit != 0 || b != a {
			t.Fatalf("seed %d: a path differs from the model %q\nsource: %+v\nir:     %+v\n%s\n%s", seed, want, a, b, replay, text)
		}
		for _, f := range []string{"c0", "c1", "c2", "c3", "c4", "c5", "c6"} {
			if !slices.Contains(irRep.IR, f) {
				t.Fatalf("seed %d: no IR body for %s (fallback %v)\n%s\n%s", seed, f, irRep.Fallback, replay, text)
			}
		}
	}
}
