package irspike

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/lower"
)

// admitAll admits every law's rewrite.
type admitAll struct{}

func (admitAll) Admit(*check.Law, *check.Named, lower.RewriteInstance) (bool, string) {
	return true, ""
}

// TestIRCellWritersMatchSource is the seeded property over the codec
// writers the IR builds (CW-IR, lower's ir_wire.go; GenerateCellWire):
// each program prints the same on json's Text and on Tape under the IR
// path and the source path, whose codec functions are the text emitter's
// (the reference), every law admitted, and the IR file holds the cell
// codec functions' bodies (ergoWrite…), so the IR path is the one under
// test. A failing seed prints the Ergo source, both outcomes and its
// replay command.
//
// Mutants (ir_wire.go; the ledger names the ones this kills):
// cw-ir-tail-past-loop, cw-ir-arm-tail,
// cw-ir-variant-any-state, cw-ir-passthrough-first,
// cw-ir-tail-after-call, cw-ir-map-unordered.
//
// It builds Go because the codec functions' events run only in the
// compiled program, on the providers' functions.
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
		// Every law admitted, as lower's TestCellWriterAgrees admits
		// them: Tape's are true by construction, json's proved.
		u.Rewrite = admitAll{}
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
