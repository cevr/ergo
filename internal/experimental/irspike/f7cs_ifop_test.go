package irspike

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// TestIRIfOperandsMatchSource is the seeded property over ifs and matches
// inside an operand (F7-cs-ifop, GenerateIfOp): each program prints the
// same under both lowerings, and IfOpFuncs have IR bodies. Most arms
// assign a local an operand to their left reads, so an operand read after
// the arm prints another value. A failing seed prints the Ergo source,
// both outcomes and its replay command.
//
// It builds Go because the order the operands run in, against the arms'
// assignments, shows only in the compiled program.
func TestIRIfOperandsMatchSource(t *testing.T) {
	src, ir := needsBuild(t)
	first, count := envInt("ERGO_IRSPIKE_SEED", 1), envInt("ERGO_IRSPIKE_SEEDS", 4)
	for seed := first; seed < first+count; seed++ {
		dir := t.TempDir()
		text := GenerateIfOp(seed)
		file := filepath.Join(dir, "p.ergo")
		if err := os.WriteFile(file, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
		u, err := Load(file)
		if err != nil {
			t.Fatalf("seed %d: the generated program does not check: %v\n%s", seed, err, text)
		}
		sb, ib, rep := both(t, u, src, ir, dir, BuildOptions{})
		a, b := run(t, sb), run(t, ib)
		replay := "replay: ERGO_IRSPIKE_SEED=" + itoa(seed) + " ERGO_IRSPIKE_SEEDS=1 go test -run TestIRIfOperandsMatchSource ./internal/experimental/irspike"
		if a != b || a.exit != 0 {
			t.Fatalf("seed %d: the paths differ or fail\nsource: %+v\nir:     %+v\n%s\n%s", seed, a, b, replay, text)
		}
		var missing []string
		for _, f := range IfOpFuncs {
			if !slices.Contains(rep.IR, f) {
				missing = append(missing, f)
			}
		}
		if len(missing) > 0 {
			t.Fatalf("seed %d: no IR body for %v (fallback %v)\n%s\n%s", seed, missing, rep.Fallback, replay, text)
		}
	}
}
