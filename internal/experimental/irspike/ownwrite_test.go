package irspike

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/eval"
)

// TestOwnWriteAgrees is XML-ATTR's agreement property: for generated
// programs that encode records holding a type with its own write
// (WireWrite, GenerateOwnWrite's attr) on a cell of its provider and on
// json's Text, the source path, the IR path and the evaluator print the
// same, and the own write ran (a `key@value` on the tape), so attr is the
// attribute of its record and not a field of its own. A failing seed
// prints the Ergo source, the outputs and its replay command.
//
// It builds Go because the other sides of the agreement are the built
// programs. Mutants: eval-own-write-ignored (the evaluator writes attr by
// its structure), lower-own-write-ignored (the codec function on the
// provider's cell writes attr by its structure).
func TestOwnWriteAgrees(t *testing.T) {
	src, ir := needsBuild(t)
	first, count := envInt("ERGO_IRSPIKE_SEED", 1), envInt("ERGO_IRSPIKE_SEEDS", 4)
	for seed := first; seed < first+count; seed++ {
		text := GenerateOwnWrite(seed)
		replay := "replay: ERGO_IRSPIKE_SEED=" + itoa(seed) + " ERGO_IRSPIKE_SEEDS=1 go test -run TestOwnWriteAgrees ./internal/experimental/irspike"
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
		if a != b {
			t.Fatalf("seed %d: the paths differ\nsource: %+v\nir:     %+v\n%s\n%s", seed, a, b, replay, text)
		}
		ev, err := eval.RunMain(u.Package, eval.DefaultBudget(), eval.Process{})
		if err != nil {
			t.Fatalf("seed %d: the evaluator: %v\n%s\n%s", seed, err, replay, text)
		}
		if ev.Skipped != "" || ev.Incomplete != "" || ev.Stdout != a.stdout || ev.Status != a.exit {
			t.Fatalf("seed %d: the evaluator and the built program differ\nbuilt: %+v\neval:  %q (status %d, skipped %q, incomplete %q)\n%s\n%s", seed, a, ev.Stdout, ev.Status, ev.Skipped, ev.Incomplete, replay, text)
		}
		if !strings.Contains(a.stdout, "id@") || !strings.Contains(a.stdout, "lang@") {
			t.Fatalf("seed %d: no attr was written by its own write (no id@ or lang@ on a tape)\n%s\n%s\n%s", seed, a.stdout, replay, text)
		}
	}
}
