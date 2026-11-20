package irspike

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/eval"
	"ergo.local/prototype/internal/sketch/lower"
)

// failLocalUnit writes GenerateFailLocal(seed) into dir and loads it.
func failLocalUnit(t *testing.T, seed uint64, dir string) (lower.Unit, string) {
	t.Helper()
	text := GenerateFailLocal(seed)
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

// lowerFailLocal is u's IR lowering with INLINE-FAIL-LOCAL set to mode
// ("" or "off").
func lowerFailLocal(t *testing.T, u lower.Unit, mode string) lower.IRReport {
	t.Helper()
	prev := lower.SetFailLocalLoweringForTest(mode)
	defer lower.SetFailLocalLoweringForTest(prev)
	_, rep, err := lower.LowerIR(u)
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

// TestIRFailLocalCounts is INLINE-FAIL-LOCAL's count guard, in process: in
// every generated program the IR inliner copies fallible bodies whose
// failure is a local of type error (a try's error passed on: tryLet,
// tryRet, tryMatch) and ones whose failure is a local or a parameter of a
// failure type (held, failWith, orFail), and copies more calls than with
// the case off, which copies none of them. A count, not a time: the
// output is the same either way, so only the counters see the refusal
// put back (mutant fail-local-refused).
func TestIRFailLocalCounts(t *testing.T) {
	if !lower.IRInlineOn() || !lower.IRFailLocalOn() {
		t.Skip("the inliner or INLINE-FAIL-LOCAL is off")
	}
	first, count := envInt("ERGO_IRSPIKE_SEED", 1), envInt("ERGO_IRSPIKE_SEEDS", 8)
	for seed := first; seed < first+count; seed++ {
		u, text := failLocalUnit(t, seed, t.TempDir())
		on, off := lowerFailLocal(t, u, ""), lowerFailLocal(t, u, "off")
		replay := "replay: ERGO_IRSPIKE_SEED=" + itoa(seed) + " ERGO_IRSPIKE_SEEDS=1 go test -run '^TestIRFailLocalCounts$' ./internal/experimental/irspike"
		if on.FailLocalErrors == 0 || on.FailLocalValues == 0 || on.Inlined <= off.Inlined {
			t.Errorf("seed %d: %d try errors and %d failure values set at a site, %d calls inlined (%d with the case off); want both > 0 and more\n%s\n%s", seed, on.FailLocalErrors, on.FailLocalValues, on.Inlined, off.Inlined, replay, text)
		}
		if off.FailLocalErrors != 0 || off.FailLocalValues != 0 {
			t.Errorf("seed %d: the reference set %d try errors and %d failure values\n%s", seed, off.FailLocalErrors, off.FailLocalValues, replay)
		}
	}
}

// TestIRFailLocalMatchesSource is INLINE-FAIL-LOCAL's agreement property:
// each generated program prints the same and exits the same (0 when main
// catches run's failure, 1 when it fails with it) under the source
// lowering, the IR, the IR with the case off (the reference: those
// bodies stay calls), and the evaluator. A failing seed prints the Ergo
// source, the outcomes and its replay command.
//
// It builds Go because a copy that sets the wrong failure, or one of the
// wrong dynamic type, shows only in the compiled program, and a failure
// set with no conversion only in the patched compiler's type check.
func TestIRFailLocalMatchesSource(t *testing.T) {
	src, ir := needsBuild(t)
	first, count := envInt("ERGO_IRSPIKE_SEED", 1), envInt("ERGO_IRSPIKE_SEEDS", 4)
	for seed := first; seed < first+count; seed++ {
		dir := t.TempDir()
		u, text := failLocalUnit(t, seed, dir)
		replay := "replay: ERGO_IRSPIKE_SEED=" + itoa(seed) + " ERGO_IRSPIKE_SEEDS=1 go test -run '^TestIRFailLocalMatchesSource$' ./internal/experimental/irspike"
		sb, ib, _ := both(t, u, src, ir, dir, BuildOptions{})
		prev := lower.SetFailLocalLoweringForTest("off")
		_, err := Emit(u, IR, filepath.Join(dir, "iroff"))
		lower.SetFailLocalLoweringForTest(prev)
		if err != nil {
			t.Fatal(err)
		}
		offBin := filepath.Join(dir, "iroff.bin")
		if _, err := Build(ir, IR, filepath.Join(dir, "iroff"), offBin, BuildOptions{}); err != nil {
			t.Fatal(err)
		}
		a, b, c := run(t, sb), run(t, ib), run(t, offBin)
		if a != b || a != c {
			t.Fatalf("seed %d: the paths differ\nsource: %+v\nir:     %+v\nir off: %+v\n%s\n%s", seed, a, b, c, replay, text)
		}
		if want := int(seed % 2); a.exit != want {
			t.Errorf("seed %d: exit %d, want %d\n%s", seed, a.exit, want, replay)
		}
		ev, err := eval.RunMain(u.Package, eval.DefaultBudget(), eval.Process{})
		if err != nil {
			t.Fatal(err)
		}
		// The evaluator stops before it writes the text of a failure
		// that leaves main (its exit status is then the run's 1).
		if strings.HasPrefix(ev.Skipped, "the text of a failure that leaves main") {
			ev.Skipped, ev.Status = "", 1
		}
		if ev.Skipped != "" || ev.Incomplete != "" || ev.Stdout != a.stdout || ev.Status != a.exit {
			t.Fatalf("seed %d: the evaluator differs\nevaluator: %q exit %d (skipped %q, incomplete %q)\nsource:    %q exit %d\n%s\n%s", seed, ev.Stdout, ev.Status, ev.Skipped, ev.Incomplete, a.stdout, a.exit, replay, text)
		}
	}
}
