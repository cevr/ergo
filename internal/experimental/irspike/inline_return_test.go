package irspike

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/eval"
	"ergo.local/prototype/internal/sketch/lower"
)

// inlineReturnUnit writes GenerateInlineReturn(seed) into dir and loads it.
func inlineReturnUnit(t *testing.T, seed uint64, dir string) (lower.Unit, string) {
	t.Helper()
	text := GenerateInlineReturn(seed)
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

// lowerInlineReturn is u's IR lowering with INLINE-RETURN set to mode
// ("" or "off").
func lowerInlineReturn(t *testing.T, u lower.Unit, mode string) lower.IRReport {
	t.Helper()
	prev := lower.SetInlineReturnLoweringForTest(mode)
	defer lower.SetInlineReturnLoweringForTest(prev)
	_, rep, err := lower.LowerIR(u)
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

// TestIRInlineReturnCounts is INLINE-RETURN's count guard, in process: in
// every generated program the IR inliner copies calls in return position
// (the chain c1 … cL, n2's two arms, at2) and binds a returned pair at a
// catch's site (viaSpin's call of spin, which loops and stays a call).
// The chain's failures land in main's catch site as variants built in
// place; they do not jump to the arm, since failJumps moves an arm only
// for one failure (ir_forms_failjump.go) and the chain has several. The
// reference (off) copies no call in return position and binds no
// pair. A count, not a time: the program's output is the same either
// way, so only the counters see a refusal put back (mutants
// inline-return-ret-refused, inline-return-pair-refused).
func TestIRInlineReturnCounts(t *testing.T) {
	if !lower.IRInlineOn() || !lower.IRInlineReturnOn() {
		t.Skip("the inliner or INLINE-RETURN is off")
	}
	first, count := envInt("ERGO_IRSPIKE_SEED", 1), envInt("ERGO_IRSPIKE_SEEDS", 8)
	for seed := first; seed < first+count; seed++ {
		u, text := inlineReturnUnit(t, seed, t.TempDir())
		on, off := lowerInlineReturn(t, u, ""), lowerInlineReturn(t, u, "off")
		replay := "replay: ERGO_IRSPIKE_SEED=" + itoa(seed) + " ERGO_IRSPIKE_SEEDS=1 go test -run '^TestIRInlineReturnCounts$' ./internal/experimental/irspike"
		if on.InlinedReturns == 0 || on.TailPairs == 0 {
			t.Errorf("seed %d: %d calls in return position inlined, %d returned pairs bound; want both > 0\n%s\n%s", seed, on.InlinedReturns, on.TailPairs, replay, text)
		}
		if off.InlinedReturns != 0 || off.TailPairs != 0 {
			t.Errorf("seed %d: the reference inlined %d calls in return position and bound %d pairs\n%s", seed, off.InlinedReturns, off.TailPairs, replay)
		}
	}
}

// TestIRInlineReturnMatchesSource is INLINE-RETURN's agreement property:
// each generated program prints the same and exits the same (0 when main
// catches run's failure, 1 when it fails with it) under the source
// lowering, the IR with calls in return position inlined, the IR with
// them left calls (the reference), and the evaluator. A failing seed
// prints the Ergo source, the outcomes and its replay command.
//
// It builds Go because a copy whose returns, failures or bound pair are
// wrong shows only in the compiled program: the counters say a copy was
// made, not that it computes what the call did.
func TestIRInlineReturnMatchesSource(t *testing.T) {
	src, ir := needsBuild(t)
	first, count := envInt("ERGO_IRSPIKE_SEED", 1), envInt("ERGO_IRSPIKE_SEEDS", 4)
	for seed := first; seed < first+count; seed++ {
		dir := t.TempDir()
		u, text := inlineReturnUnit(t, seed, dir)
		replay := "replay: ERGO_IRSPIKE_SEED=" + itoa(seed) + " ERGO_IRSPIKE_SEEDS=1 go test -run '^TestIRInlineReturnMatchesSource$' ./internal/experimental/irspike"
		sb, ib, _ := both(t, u, src, ir, dir, BuildOptions{})
		prev := lower.SetInlineReturnLoweringForTest("off")
		_, err := Emit(u, IR, filepath.Join(dir, "iroff"))
		lower.SetInlineReturnLoweringForTest(prev)
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
		want := int(seed % 2) // odd seeds: main fails with run's failure
		if a.exit != want {
			t.Errorf("seed %d: exit %d, want %d\n%s", seed, a.exit, want, replay)
		}
		ev, err := eval.RunMain(u.Package, eval.DefaultBudget(), eval.Process{})
		if err != nil {
			t.Fatal(err)
		}
		// The evaluator stops before it writes the text of a failure
		// that leaves main (its exit status is then the run's 1).
		left := strings.HasPrefix(ev.Skipped, "the text of a failure that leaves main")
		if left {
			ev.Skipped, ev.Status = "", 1
		}
		if ev.Skipped != "" || ev.Incomplete != "" || ev.Stdout != a.stdout || ev.Status != a.exit {
			t.Fatalf("seed %d: the evaluator differs\nevaluator: %q exit %d (skipped %q, incomplete %q)\nsource:    %q exit %d\n%s\n%s", seed, ev.Stdout, ev.Status, ev.Skipped, ev.Incomplete, a.stdout, a.exit, replay, text)
		}
	}
}
