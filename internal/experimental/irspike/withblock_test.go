package irspike

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// TestIRWithBlocksMatchSource is WITHREL's seeded property over fallible
// calls of generic functions that take a trailing block (GenerateWithBlock):
// a user guard[T, E] and std's term.Raw, caught or tried, with a block
// that has a value or none (T bound to no value: the Go call returns
// struct{} beside its error), after a `let s = try open(n)`, with catch
// arms that return a failure of the enclosing function. Each program
// builds on both paths and prints the same, and each function has an IR
// body. Mutants: wr-unit-result-single (the IR binds such a call's Go pair
// to the error alone: the decoder refuses the body, "(_ struct{}, err
// error) is not error", examples/gent's build failure) and
// wr-unit-return-plain (a frame with no value whose tail is such a call returns
// its pair as one value). A failing seed
// prints the Ergo source, both outcomes and its replay command.
//
// It builds Go because the question is whether the derivative compiler
// takes the IR body and what the program prints; no in-process check
// observes the decoder's typing of a def.
func TestIRWithBlocksMatchSource(t *testing.T) {
	src, ir := needsBuild(t)
	first, count := envInt("ERGO_IRSPIKE_SEED", 1), envInt("ERGO_IRSPIKE_SEEDS", 3)
	for seed := first; seed < first+count; seed++ {
		text, funcs := GenerateWithBlock(uint64(seed))
		replay := "replay: ERGO_IRSPIKE_SEED=" + itoa(seed) + " ERGO_IRSPIKE_SEEDS=1 go test -run TestIRWithBlocksMatchSource ./internal/experimental/irspike"
		dir := t.TempDir()
		file := filepath.Join(dir, "p.ergo")
		if err := os.WriteFile(file, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
		u, err := Load(file)
		if err != nil {
			t.Fatalf("seed %d: the program does not check: %v\n%s", seed, err, text)
		}
		sb, ib, rep := both(t, u, src, ir, dir, BuildOptions{})
		a, b := run(t, sb), run(t, ib)
		if a != b {
			t.Fatalf("seed %d: the paths differ\nsource: %+v\nir:     %+v\n%s\n%s", seed, a, b, replay, text)
		}
		for _, f := range funcs {
			if !slices.Contains(rep.IR, f) {
				t.Errorf("seed %d: %s has no IR body (IR bodies: %v)\n%s\n%s", seed, f, rep.IR, replay, text)
			}
		}
	}
}
