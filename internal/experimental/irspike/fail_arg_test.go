package irspike

import (
	"os"
	"path/filepath"
	"testing"

	"ergo.local/prototype/internal/sketch/eval"
	"ergo.local/prototype/internal/sketch/lower"
)

// failArgUnit writes GenerateFailArg(seed) into dir and loads it.
func failArgUnit(t *testing.T, seed uint64, dir string) (lower.Unit, string) {
	t.Helper()
	text := GenerateFailArg(seed)
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

// lowerFailArg is u's IR lowering with INLINE-FAILARG set to mode ("" or
// "off").
func lowerFailArg(t *testing.T, u lower.Unit, mode string) lower.IRReport {
	t.Helper()
	prev := lower.SetFailArgLoweringForTest(mode)
	defer lower.SetFailArgLoweringForTest(prev)
	_, rep, err := lower.LowerIR(u)
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

// TestIRFailArgCounts is INLINE-FAILARG's count guard, in process: in every
// generated program the literal argument stands for its parameter at
// exactly four copies (main's orFail and orFailN of the loop's locals,
// look's orFail in look and in look's copy in main), the two copies of
// look pass that failure on as built (failThrough), and three failures
// jump to main's arms (a, a2, through); the three sites that must not
// take their literal (a later argument, a body or the literal itself
// that writes the counter) bind it. With the case off all three counts
// are 0. A count, not a time: only the counters see the substitution
// dropped (mutant fail-arg-refused) or the pass-through dropped
// (fail-through-refused).
func TestIRFailArgCounts(t *testing.T) {
	if !lower.IRInlineOn() || !lower.IRFailArgOn() {
		t.Skip("the inliner or INLINE-FAILARG is off")
	}
	first, count := envInt("ERGO_IRSPIKE_SEED", 1), envInt("ERGO_IRSPIKE_SEEDS", 8)
	for seed := first; seed < first+count; seed++ {
		u, text := failArgUnit(t, seed, t.TempDir())
		on, off := lowerFailArg(t, u, ""), lowerFailArg(t, u, "off")
		replay := "replay: ERGO_IRSPIKE_SEED=" + itoa(seed) + " ERGO_IRSPIKE_SEEDS=1 go test -run '^TestIRFailArgCounts$' ./internal/experimental/irspike"
		if on.FailArgs != 4 || on.FailThrough != 2 || on.FailJumps != 3 || off.FailArgs+off.FailThrough+off.FailJumps != 0 {
			t.Errorf("seed %d: %d failure arguments in place, %d passed on as built, %d failures jump (off: %d, %d, %d); want 4, 2, 3 (off 0)\n%s\n%s", seed, on.FailArgs, on.FailThrough, on.FailJumps, off.FailArgs, off.FailThrough, off.FailJumps, replay, text)
		}
	}
}

// TestIRFailArgMatchesSource is INLINE-FAILARG's agreement property: each
// generated program prints the same under the source lowering, the IR,
// the IR with the case off (the reference: every failure argument bound
// at the copy's head), and the evaluator. A failing seed prints the Ergo
// source, the outcomes and its replay command.
//
// It builds Go because a literal evaluated after a write it must precede,
// or not evaluated at all, shows only in the compiled program's output.
func TestIRFailArgMatchesSource(t *testing.T) {
	src, ir := needsBuild(t)
	first, count := envInt("ERGO_IRSPIKE_SEED", 1), envInt("ERGO_IRSPIKE_SEEDS", 4)
	for seed := first; seed < first+count; seed++ {
		dir := t.TempDir()
		u, text := failArgUnit(t, seed, dir)
		replay := "replay: ERGO_IRSPIKE_SEED=" + itoa(seed) + " ERGO_IRSPIKE_SEEDS=1 go test -run '^TestIRFailArgMatchesSource$' ./internal/experimental/irspike"
		sb, ib, _ := both(t, u, src, ir, dir, BuildOptions{})
		prev := lower.SetFailArgLoweringForTest("off")
		_, err := Emit(u, IR, filepath.Join(dir, "iroff"))
		lower.SetFailArgLoweringForTest(prev)
		if err != nil {
			t.Fatal(err)
		}
		offBin := filepath.Join(dir, "iroff.bin")
		if _, err := Build(ir, IR, filepath.Join(dir, "iroff"), offBin, BuildOptions{}); err != nil {
			t.Fatal(err)
		}
		a, b, c := run(t, sb), run(t, ib), run(t, offBin)
		if a != b || a != c || a.exit != 0 {
			t.Fatalf("seed %d: the paths differ\nsource: %+v\nir:     %+v\nir off: %+v\n%s\n%s", seed, a, b, c, replay, text)
		}
		ev, err := eval.RunMain(u.Package, eval.DefaultBudget(), eval.Process{})
		if err != nil {
			t.Fatal(err)
		}
		if ev.Skipped != "" || ev.Incomplete != "" || ev.Stdout != a.stdout || ev.Status != 0 {
			t.Fatalf("seed %d: the evaluator differs\nevaluator: %q exit %d (skipped %q, incomplete %q)\nsource:    %q\n%s\n%s", seed, ev.Stdout, ev.Status, ev.Skipped, ev.Incomplete, a.stdout, replay, text)
		}
	}
}
