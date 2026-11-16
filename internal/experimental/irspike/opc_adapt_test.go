package irspike

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// TestIRAdaptersMatchSource is the seeded property over std calls that
// adapt a function value to a Go parameter (FLIP OPC-adapt): each program
// of GenerateAdapt (comparators, a Ref's Update) and of GenerateAdaptTask
// (tasks.Go of a literal and of a trailing block, a block on a task of its
// own) prints the same under both lowerings, and the functions that make
// the calls have IR bodies. Mutants: opc-adapt-params-reversed (the
// adapter passes its parameters to the value in the other order, so a
// comparator sorts the other way), opc-adapt-task-error-dropped (a task
// block's failure is lost, so the Set's Wait does not fail). A failing
// seed prints the Ergo source, both outcomes and its replay command.
//
// It builds Go because the adapter is a Go function literal the runtime/v1
// body calls, on a task the runtime starts; only the compiled program runs
// it.
func TestIRAdaptersMatchSource(t *testing.T) {
	src, ir := needsBuild(t)
	first, count := envInt("ERGO_IRSPIKE_SEED", 1), envInt("ERGO_IRSPIKE_SEEDS", 4)
	gens := []struct {
		gen   func(uint64) string
		funcs []string
	}{
		{GenerateAdapt, []string{"order", "orderBy", "bump"}},
		{GenerateAdaptTask, []string{"quiet", "loud", "block"}},
	}
	for seed := first; seed < first+count; seed++ {
		for _, g := range gens {
			text := g.gen(uint64(seed))
			replay := "replay: ERGO_IRSPIKE_SEED=" + itoa(seed) + " ERGO_IRSPIKE_SEEDS=1 go test -run TestIRAdaptersMatchSource ./internal/experimental/irspike"
			dir := t.TempDir()
			file := filepath.Join(dir, "p.ergo")
			if err := os.WriteFile(file, []byte(text), 0o644); err != nil {
				t.Fatal(err)
			}
			u, err := Load(file)
			if err != nil {
				t.Fatalf("the program does not check: %v\n%s", err, text)
			}
			sb, ib, rep := both(t, u, src, ir, dir, BuildOptions{})
			a, b := run(t, sb), run(t, ib)
			if a != b {
				t.Fatalf("seed %d: the paths differ\nsource: %+v\nir:     %+v\n%s\n%s", seed, a, b, replay, text)
			}
			for _, f := range g.funcs {
				if !slices.Contains(rep.IR, f) {
					t.Errorf("seed %d: %s has no IR body (IR bodies: %v)\n%s\n%s", seed, f, rep.IR, replay, text)
				}
			}
		}
	}
}
