package irspike

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// TestIRAdaptersMatchSource is the seeded property over std calls that
// adapt a function value to a Go parameter (FLIP OPC-adapt,
// GenerateAdapt): each program prints the same under both lowerings, and
// order, orderBy and bump, which make the calls, have IR bodies (mutant
// opc-adapt-params-reversed: the adapter passes its parameters to the
// value in the other order, so a comparator sorts the other way). A
// failing seed prints the Ergo source, both outcomes and its replay
// command.
//
// It builds Go because the adapter is a Go function literal the runtime/v1
// body calls; only the compiled program runs it.
func TestIRAdaptersMatchSource(t *testing.T) {
	src, ir := needsBuild(t)
	first, count := envInt("ERGO_IRSPIKE_SEED", 1), envInt("ERGO_IRSPIKE_SEEDS", 4)
	for seed := first; seed < first+count; seed++ {
		text := GenerateAdapt(uint64(seed))
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
		for _, f := range []string{"order", "orderBy", "bump"} {
			if !slices.Contains(rep.IR, f) {
				t.Errorf("seed %d: %s has no IR body (IR bodies: %v)\n%s\n%s", seed, f, rep.IR, replay, text)
			}
		}
	}
}
