package irspike

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/lower"
)

// TestIRPatternsMatchSource is the seeded property over matches and
// catches with patterns (FLIP F7, GeneratePatterns): each program prints
// the same under both lowerings, which build one decision tree
// (matchTree) and write it each in its own form, and every function with
// patterns has an IR body. A failing seed prints the Ergo source, both
// outcomes and its replay command.
//
// It builds Go because the IR's switches, assertions and shared-arm
// dispatch are typed by the patched compile, and the arm a value reaches
// shows only in the compiled program.
func TestIRPatternsMatchSource(t *testing.T) {
	src, ir := needsBuild(t)
	first, count := envInt("ERGO_IRSPIKE_SEED", 1), envInt("ERGO_IRSPIKE_SEEDS", 6)
	for seed := first; seed < first+count; seed++ {
		text := GeneratePatterns(seed)
		replay := "replay: ERGO_IRSPIKE_SEED=" + itoa(seed) + " ERGO_IRSPIKE_SEEDS=1 go test -run TestIRPatternsMatchSource ./internal/experimental/irspike"
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
		if a != b || a.exit != 0 {
			t.Fatalf("seed %d: the paths differ or fail\nsource: %+v\nir:     %+v\n%s\n%s", seed, a, b, replay, text)
		}
		got := map[string]bool{}
		for _, f := range rep.IR {
			got[f[strings.LastIndex(f, ".")+1:]] = true
		}
		for _, f := range PatternFuncs {
			if !got[f] {
				t.Errorf("seed %d: %s has no IR body (fallbacks: %v)\n%s", seed, f, rep.Fallback, text)
			}
		}
		if _, tag := unionForms(t, filepath.Join(dir, "ir", lower.IRFileName), map[string]bool{}); tag == 0 {
			t.Errorf("seed %d: no tree switch on a flat union's tag (opv's Op is flat)\n%s", seed, text)
		}
	}
}
