package irspike

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"ergo.local/prototype/internal/sketch/lower"
)

// TestIRCellWritersMatchSource is the seeded property over the codec
// writers the IR builds (WIRE-IR, lower's ir_wire.go; GenerateCellWire):
// each program prints the same JSON under the IR path and the source
// path, whose codec functions are the text emitter's (the reference), and
// the IR file holds the cell codec functions' bodies (ergoWrite…), so
// the IR path is the one under test. A failing seed prints the Ergo
// source, both outcomes and its replay command.
//
// Mutants (ir_wire.go): wire-ir-lead-kept (a run that starts in the
// carried state writes the carried call again), wire-ir-tail-past-loop
// (the state a loop's body ends in carried past the loop),
// wire-ir-arm-tail (a switch's arm starts in the state the previous arm
// ended in, not the switch's), wire-ir-variant-any-state (a call takes
// the callee's entry variant in a state the variant was not made for),
// wire-ir-passthrough-first (a law's pass-through argument read from the
// run's first call).
//
// It builds Go because the codec functions' events run only in the
// compiled program, on std json's provider.
func TestIRCellWritersMatchSource(t *testing.T) {
	src, ir := needsBuild(t)
	first, count := envInt("ERGO_IRSPIKE_SEED", 1), envInt("ERGO_IRSPIKE_SEEDS", 4)
	writers := regexp.MustCompile(`"name":"ergoWrite[A-Za-z0-9_]*"`)
	for seed := first; seed < first+count; seed++ {
		text := GenerateCellWire(uint64(seed))
		replay := "replay: ERGO_IRSPIKE_SEED=" + itoa(seed) + " ERGO_IRSPIKE_SEEDS=1 go test -run TestIRCellWritersMatchSource ./internal/experimental/irspike"
		dir := t.TempDir()
		file := filepath.Join(dir, "p.ergo")
		if err := os.WriteFile(file, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
		u, err := Load(file)
		if err != nil {
			t.Fatalf("seed %d: the program does not check: %v\n%s", seed, err, text)
		}
		sb, ib, _ := both(t, u, src, ir, dir, BuildOptions{})
		a, b := run(t, sb), run(t, ib)
		if a != b || a.exit != 0 {
			t.Fatalf("seed %d: the paths differ (or fail)\nsource: %+v\nir:     %+v\n%s\n%s", seed, a, b, replay, text)
		}
		bodies := 0
		filepath.WalkDir(filepath.Join(dir, "ir"), func(path string, d fs.DirEntry, err error) error {
			if err == nil && !d.IsDir() && d.Name() == lower.IRFileName {
				data, _ := os.ReadFile(path)
				bodies += len(writers.FindAll(data, -1))
			}
			return nil
		})
		if bodies == 0 {
			t.Errorf("seed %d: the IR file holds no codec writer's body\n%s\n%s", seed, replay, text)
		}
	}
}
