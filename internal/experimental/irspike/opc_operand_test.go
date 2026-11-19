package irspike

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// TestIROperandsMatchSource is the seeded property over std calls whose
// operand is a variant passed as its union (FLIP OPC-conv,
// GenerateOperand): each program prints the same under both lowerings, and
// cell, which makes the calls, has an IR body (mutant
// opc-conv-variant-ignored: the operand's type read before its implicit
// conversion, so the call keeps the source body). A failing seed prints
// the Ergo source, both outcomes and its replay command.
//
// It builds Go because the union value reaches the runtime/v1 body only in
// the compiled program; the decoder checks the operand's type there.
func TestIROperandsMatchSource(t *testing.T) {
	src, ir := needsBuild(t)
	first, count := envInt("ERGO_IRSPIKE_SEED", 1), envInt("ERGO_IRSPIKE_SEEDS", 4)
	for seed := first; seed < first+count; seed++ {
		text := GenerateOperand(uint64(seed))
		replay := "replay: ERGO_IRSPIKE_SEED=" + itoa(seed) + " ERGO_IRSPIKE_SEEDS=1 go test -run TestIROperandsMatchSource ./internal/experimental/irspike"
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
		if !slices.Contains(rep.IR, "cell") {
			t.Errorf("seed %d: cell has no IR body (IR bodies: %v)\n%s\n%s", seed, rep.IR, replay, text)
		}
	}
}
