package irspike

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/eval"
)

// TestIRTakeAtMatchesSource is CONV-RESULT's seeded property
// (GenerateTakeAt): a std call whose Go result is a tuple of the runtime
// body's generic types, xs.TakeAt(i)'s (T, []T), takes the IR at every
// element type; the source path, the IR path and the evaluator print the
// same, and every take<k> has an IR body. A failing seed prints the Ergo
// source, the outputs and its replay command. Mutant:
// cr-tuple-result-spelled-only (the tuple's results compared by spelling
// alone again).
//
// It builds Go because the other sides of the agreement are the built
// programs, and the patched compile checks the tuple's binding.
func TestIRTakeAtMatchesSource(t *testing.T) {
	src, ir := needsBuild(t)
	first, count := envInt("ERGO_IRSPIKE_SEED", 1), envInt("ERGO_IRSPIKE_SEEDS", 4)
	for seed := first; seed < first+count; seed++ {
		text := GenerateTakeAt(seed)
		replay := "replay: ERGO_IRSPIKE_SEED=" + itoa(seed) + " ERGO_IRSPIKE_SEEDS=1 go test -run '^TestIRTakeAtMatchesSource$' ./internal/experimental/irspike"
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
		ev, err := eval.RunMain(u.Package, eval.DefaultBudget(), eval.Process{})
		if err != nil {
			t.Fatalf("seed %d: the evaluator: %v\n%s\n%s", seed, err, replay, text)
		}
		if ev.Skipped != "" || ev.Incomplete != "" || ev.Stdout != a.stdout || ev.Status != a.exit {
			t.Fatalf("seed %d: the evaluator and the built program differ\nbuilt: %+v\neval:  %q (status %d, skipped %q, incomplete %q)\n%s\n%s", seed, a, ev.Stdout, ev.Status, ev.Skipped, ev.Incomplete, replay, text)
		}
		has := map[string]bool{}
		for _, f := range rep.IR {
			has[f] = true
		}
		for _, line := range strings.Split(text, "\n") {
			name, ok := strings.CutPrefix(line, "func take")
			if !ok {
				continue
			}
			name, _, _ = strings.Cut(name, "(")
			if !has["take"+name] {
				t.Fatalf("seed %d: take%s has no IR body (IR: %v; fallback reasons: %v)\n%s\n%s", seed, name, rep.IR, rep.Fallback, replay, text)
			}
		}
	}
}
