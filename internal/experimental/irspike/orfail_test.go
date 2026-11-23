package irspike

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/eval"
	"ergo.local/prototype/internal/sketch/lower"
)

// orFailUnit writes GenerateOrFail(seed) into dir and loads it.
func orFailUnit(t *testing.T, seed uint64, dir string) (lower.Unit, string) {
	t.Helper()
	text := GenerateOrFail(seed)
	file := filepath.Join(dir, "p.ergo")
	if err := os.WriteFile(file, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	u, err := Load(file)
	if err != nil {
		t.Fatalf("seed %d: the generated program does not check: %v\n%s", seed, err, text)
	}
	return u, text
}

// TestIROrFailCounts is PRELUDE-ORFAIL's count guard, in process: every
// body of the generated program takes the IR (no fallback: a value
// parameter of a slot row's type lowers as its Go type parameter), and the
// prelude's OrFail and the user's myOrFail are copied into their sites
// (InlinedSlots > 0). A count, not a time: the output is the same either
// way, so only the counters see a value parameter that is no slot
// (mutant orfail-value-param-unslotted, not-run-crunch).
func TestIROrFailCounts(t *testing.T) {
	if !lower.IRInlineOn() || !lower.IRInlineSlotOn() {
		t.Skip("the inliner or INLINE-SLOT is off")
	}
	first, count := envInt("ERGO_IRSPIKE_SEED", 1), envInt("ERGO_IRSPIKE_SEEDS", 4)
	for seed := first; seed < first+count; seed++ {
		u, text := orFailUnit(t, seed, t.TempDir())
		_, rep, err := lower.LowerIR(u)
		if err != nil {
			t.Fatal(err)
		}
		replay := "replay: ERGO_IRSPIKE_SEED=" + itoa(seed) + " ERGO_IRSPIKE_SEEDS=1 go test -run '^TestIROrFailCounts$' ./internal/experimental/irspike"
		if len(rep.Fallback) != 0 || rep.InlinedSlots == 0 {
			t.Errorf("seed %d: fallback %v, %d calls with a slot row inlined; want none, some\n%s\n%s", seed, rep.Fallback, rep.InlinedSlots, replay, text)
		}
	}
}

// TestIROrFailMatchesSource is PRELUDE-ORFAIL's agreement property over
// both lowerings: each generated program prints the same and exits the
// same (0 when main catches look's miss, 1 when it fails with it) under
// the source (text) lowering, the IR and the evaluator. A failing seed
// prints the Ergo source, the outcomes and its replay command. Mutants
// (not-run-crunch): orfail-text-boxes-as-error (the text path's value
// parameter spelled error), orfail-ir-wrong-arm (the IR's typed jump to
// another arm).
//
// It builds Go because a value parameter of the wrong Go type, or a
// failure taken by the wrong arm, shows only compiling and running.
func TestIROrFailMatchesSource(t *testing.T) {
	src, ir := needsBuild(t)
	first, count := envInt("ERGO_IRSPIKE_SEED", 1), envInt("ERGO_IRSPIKE_SEEDS", 4)
	for seed := first; seed < first+count; seed++ {
		dir := t.TempDir()
		u, text := orFailUnit(t, seed, dir)
		replay := "replay: ERGO_IRSPIKE_SEED=" + itoa(seed) + " ERGO_IRSPIKE_SEEDS=1 go test -run '^TestIROrFailMatchesSource$' ./internal/experimental/irspike"
		sb, ib, _ := both(t, u, src, ir, dir, BuildOptions{})
		a, b := run(t, sb), run(t, ib)
		if a != b {
			t.Fatalf("seed %d: the paths differ\nsource: %+v\nir:     %+v\n%s\n%s", seed, a, b, replay, text)
		}
		if want := int(seed % 2); a.exit != want {
			t.Errorf("seed %d: exit %d, want %d\n%s", seed, a.exit, want, replay)
		}
		ev, err := eval.RunMain(u.Package, eval.DefaultBudget(), eval.Process{})
		if err != nil {
			t.Fatal(err)
		}
		if strings.HasPrefix(ev.Skipped, "the text of a failure that leaves main") {
			ev.Skipped, ev.Status = "", 1
		}
		if ev.Skipped != "" || ev.Incomplete != "" || ev.Stdout != a.stdout || ev.Status != a.exit {
			t.Fatalf("seed %d: the evaluator differs\nevaluator: %q exit %d (skipped %q, incomplete %q)\nsource:    %q exit %d\n%s\n%s", seed, ev.Stdout, ev.Status, ev.Skipped, ev.Incomplete, a.stdout, a.exit, replay, text)
		}
	}
}
