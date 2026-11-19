package irspike

import (
	"os"
	"path/filepath"
	"testing"

	"ergo.local/prototype/internal/sketch/lower"
)

// hoistProgram writes GenerateHoist(seed) into dir and loads it.
func hoistProgram(t *testing.T, seed uint64, dir string) (lower.Unit, string) {
	t.Helper()
	text := GenerateHoist(seed)
	file := filepath.Join(dir, "p.ergo")
	if err := os.WriteFile(file, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	u, err := Load(file)
	if err != nil {
		t.Fatalf("seed %d: the program does not check: %v\n%s", seed, err, text)
	}
	return u, text
}

// emitHoist lowers u by the IR path with the loop-invariant pure call on
// or off (lower.SetPureHoist) into dir.
func emitHoist(t *testing.T, u lower.Unit, on bool, dir string) lower.IRReport {
	t.Helper()
	restore := lower.SetPureHoist(on)
	defer restore()
	rep, err := Emit(u, IR, dir)
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

// TestPureHoistMemoizes: the IR of the hoist programs computes some of
// their loop-invariant pure calls once per loop entry, and with the
// switch off none (LAWFFI LF3a). In process: the IR report counts them.
//
// Mutant it alone catches: ffi-hoist-never (no call is memoized; the
// agreement property still holds).
func TestPureHoistMemoizes(t *testing.T) {
	on, off := 0, 0
	for seed := uint64(1); seed <= 8; seed++ {
		dir := t.TempDir()
		u, _ := hoistProgram(t, seed, dir)
		on += emitHoist(t, u, true, filepath.Join(dir, "on")).Memo
		off += emitHoist(t, u, false, filepath.Join(dir, "off")).Memo
	}
	if on == 0 || off != 0 {
		t.Errorf("memoized calls: %d with the switch on, %d off; want some, then none", on, off)
	}
}

// TestPureHoistAgrees is LF3a's seeded property (plan-r1 §7): each hoist
// program prints the same, stops where it stops and exits the same with
// the loop-invariant pure call on and off (the reference: the IR without
// the change). A failing seed prints the Ergo source, both outcomes and
// its replay command.
//
// Mutants it alone catches: ffi-hoist-foreign (an impure callee's call is
// memoized), ffi-hoist-variant-argument (a let mut operand counts as
// invariant), ffi-hoist-inner-binding (a name bound inside the loop counts
// as invariant), ffi-hoist-no-reset (the flag outlives an entry of the
// loop), jobsice-memo-leak (a fused match that falls back keeps the
// declarations of the calls its callee's body memoized),
// jobsice-range-binds-invariant (a range's index and element count as
// invariant).
//
// It builds Go because the claim is about what the IR bodies do when they
// run, which only the patched compile's machine code shows.
func TestPureHoistAgrees(t *testing.T) {
	_, ir := needsBuild(t)
	first, count := envInt("ERGO_HOIST_SEED", 1), envInt("ERGO_HOIST_SEEDS", 8)
	memo := 0
	for seed := first; seed < first+count; seed++ {
		dir := t.TempDir()
		u, text := hoistProgram(t, seed, dir)
		memo += emitHoist(t, u, true, filepath.Join(dir, "on")).Memo
		emitHoist(t, u, false, filepath.Join(dir, "off"))
		onBin, offBin := filepath.Join(dir, "on.bin"), filepath.Join(dir, "off.bin")
		for _, b := range [][2]string{{"on", onBin}, {"off", offBin}} {
			if _, err := Build(ir, IR, filepath.Join(dir, b[0]), b[1], BuildOptions{}); err != nil {
				t.Fatalf("seed %d: the %s build: %v\n%s", seed, b[0], err, text)
			}
		}
		if a, b := run(t, onBin), run(t, offBin); a != b {
			t.Errorf("seed %d: the memoized calls change the program\non:  %+v\noff: %+v\nreplay: ERGO_HOIST_SEED=%d ERGO_HOIST_SEEDS=1 go test -run TestPureHoistAgrees ./internal/experimental/irspike\n%s", seed, a, b, seed, text)
		}
	}
	if memo == 0 {
		t.Errorf("no seed memoized a call: the property compares two equal lowerings")
	}
}
