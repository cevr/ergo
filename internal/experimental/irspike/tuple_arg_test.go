package irspike

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/eval"
)

// TestIRTupleArgsMatchSource is PORT-TUPLE-ARG's seeded property
// (GenerateTupleArg): a tuple stands wherever a type is spelled, as a type
// argument of Option, of a generic record and of a generic function, a
// slice's element, a Map's value, a tuple's element, at any depth; the
// source path, the IR path and the evaluator print the same, and every
// function has an IR body, so the property never passes on the source
// form alone. A failing seed prints the Ergo source, the outputs and its
// replay command. Mutants: pta-struct-unspelled (spelling refuses a
// struct again), pta-struct-fields-swapped (a struct's fields spelled in
// reverse order).
//
// It builds Go because the other sides of the agreement are the built
// programs, and the patched compile decodes the spelled types.
func TestIRTupleArgsMatchSource(t *testing.T) {
	src, ir := needsBuild(t)
	first, count := envInt("ERGO_IRSPIKE_SEED", 1), envInt("ERGO_IRSPIKE_SEEDS", 6)
	for seed := first; seed < first+count; seed++ {
		text := GenerateTupleArg(seed)
		replay := "replay: ERGO_IRSPIKE_SEED=" + itoa(seed) + " ERGO_IRSPIKE_SEEDS=1 go test -run '^TestIRTupleArgsMatchSource$' ./internal/experimental/irspike"
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
			name, ok := strings.CutPrefix(line, "func ")
			if !ok {
				continue
			}
			name, _, _ = strings.Cut(name, "(")
			name, _, _ = strings.Cut(name, "[")
			if name != "main" && !has[name] {
				t.Fatalf("seed %d: %s has no IR body (IR: %v)\n%s\n%s", seed, name, rep.IR, replay, text)
			}
		}
	}
}
