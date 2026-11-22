package irspike

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/eval"
	"ergo.local/prototype/internal/sketch/lower"
)

// inlineSlotUnit writes GenerateInlineSlot(seed) into dir and loads it.
func inlineSlotUnit(t *testing.T, seed uint64, dir string) (lower.Unit, string) {
	t.Helper()
	text := GenerateInlineSlot(seed)
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

// lowerInlineSlot is u's IR lowering with INLINE-SLOT set to mode ("" or
// "off").
func lowerInlineSlot(t *testing.T, u lower.Unit, mode string) lower.IRReport {
	t.Helper()
	prev := lower.SetInlineSlotLoweringForTest(mode)
	defer lower.SetInlineSlotLoweringForTest(prev)
	_, rep, err := lower.LowerIR(u)
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

// TestIRInlineSlotCounts is INLINE-SLOT's count guard, in process: in every
// generated program the IR inliner copies the calls of functions with a
// Slot row whose Go type is known at the call (8: get and pass, pass's
// own F now a Go type parameter of its body), 7 of their failures a field
// of a local (get's `return e.Error`); pass keeps no source body; with
// the case off none is copied and pass keeps its source body. A count,
// not a time: the output is the same either way, so only the counters see
// the exclusion put back or the field failure refused (mutants
// inline-slot-excluded, fail-field-refused).
func TestIRInlineSlotCounts(t *testing.T) {
	if !lower.IRInlineOn() || !lower.IRInlineSlotOn() {
		t.Skip("the inliner or INLINE-SLOT is off")
	}
	first, count := envInt("ERGO_IRSPIKE_SEED", 1), envInt("ERGO_IRSPIKE_SEEDS", 8)
	for seed := first; seed < first+count; seed++ {
		u, text := inlineSlotUnit(t, seed, t.TempDir())
		on, off := lowerInlineSlot(t, u, ""), lowerInlineSlot(t, u, "off")
		replay := "replay: ERGO_IRSPIKE_SEED=" + itoa(seed) + " ERGO_IRSPIKE_SEEDS=1 go test -run '^TestIRInlineSlotCounts$' ./internal/experimental/irspike"
		// pass[F] passes its own Slot row on: its Go type parameter on the
		// IR, a source body with the case off.
		const reason = "generic call with a row type argument of the caller"
		if on.InlinedSlots != 8 || off.InlinedSlots != 0 || on.FailFields != 7 || len(on.Fallback) != 0 || off.Fallback[reason] != 1 {
			t.Errorf("seed %d: %d calls with a Slot row inlined (%d off), %d field failures, fallback %v (off %v); want 8, 0, 7, none (off: pass's source body)\n%s\n%s", seed, on.InlinedSlots, off.InlinedSlots, on.FailFields, on.Fallback, off.Fallback, replay, text)
		}
	}
}

// TestIRInlineSlotMatchesSource is INLINE-SLOT's agreement property: each
// generated program prints the same and exits the same (0 when main
// catches viaTwo's failure, 1 when it fails with it) under the source
// (text) lowering, the IR, the IR with the case off (the reference:
// every call of get stays a call), and the evaluator. A failing seed
// prints the Ergo source, the outcomes and its replay command.
//
// It builds Go because a copy whose Box names error where the caller's
// names the member type-checks and takes the wrong catch arm, which shows
// only running.
func TestIRInlineSlotMatchesSource(t *testing.T) {
	src, ir := needsBuild(t)
	first, count := envInt("ERGO_IRSPIKE_SEED", 1), envInt("ERGO_IRSPIKE_SEEDS", 4)
	for seed := first; seed < first+count; seed++ {
		dir := t.TempDir()
		u, text := inlineSlotUnit(t, seed, dir)
		replay := "replay: ERGO_IRSPIKE_SEED=" + itoa(seed) + " ERGO_IRSPIKE_SEEDS=1 go test -run '^TestIRInlineSlotMatchesSource$' ./internal/experimental/irspike"
		sb, ib, _ := both(t, u, src, ir, dir, BuildOptions{})
		prev := lower.SetInlineSlotLoweringForTest("off")
		_, err := Emit(u, IR, filepath.Join(dir, "iroff"))
		lower.SetInlineSlotLoweringForTest(prev)
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
