package irspike

import (
	"os"
	"path/filepath"
	"testing"
)

// TestIRWitnessesMatchSource is the seeded property over calls that pass
// witnesses (F7 lane B, GenerateWitness): each program prints the same
// under both lowerings, and every function that calls through a witness
// has an IR body. A failing seed prints the Ergo source, both outcomes and
// its replay command.
//
// It builds Go because the witness the IR passes is a package variable the
// patched compile types against the callee's witness parameter, and which
// operations it holds shows only in the compiled program.
func TestIRWitnessesMatchSource(t *testing.T) {
	src, ir := needsBuild(t)
	first, count := envInt("ERGO_IRSPIKE_SEED", 1), envInt("ERGO_IRSPIKE_SEEDS", 6)
	for seed := first; seed < first+count; seed++ {
		text, names := GenerateWitness(seed)
		replay := "replay: ERGO_IRSPIKE_SEED=" + itoa(seed) + " ERGO_IRSPIKE_SEEDS=1 go test -run TestIRWitnessesMatchSource ./internal/experimental/irspike"
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
			t.Fatalf("the paths differ\nsource: %+v\nir:     %+v\n%s\n%s", a, b, replay, text)
		}
		got := map[string]bool{}
		for _, f := range rep.IR {
			got[f] = true
		}
		for _, f := range names {
			if !got[f] {
				t.Errorf("seed %d: %s has no IR body (IR bodies: %v)\n%s\n%s", seed, f, rep.IR, replay, text)
			}
		}
	}
}
