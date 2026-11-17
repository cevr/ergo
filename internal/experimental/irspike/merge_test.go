package irspike

import (
	"os"
	"path/filepath"
	"testing"

	"ergo.local/prototype/internal/sketch/lower"
)

// mergeProgram writes GenerateMerge(seed) into dir and loads it.
func mergeProgram(t *testing.T, seed uint64, dir string) (lower.Unit, string) {
	t.Helper()
	text := GenerateMerge(seed)
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

// emitMerge lowers u by the IR path with the merge pass on or off
// (lower.SetPureMerge) into dir.
func emitMerge(t *testing.T, u lower.Unit, on bool, dir string) lower.IRReport {
	t.Helper()
	restore := lower.SetPureMerge(on)
	defer restore()
	rep, err := Emit(u, IR, dir)
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

// TestPureMergeCounts: the IR of the merge programs reads some pure calls
// from an equal call before them, and with the pass off none (LAWFFI
// LF3b). In process: the IR report counts them.
//
// Mutants it alone catches: ffi-merge-never (no call is merged; the
// agreement property still holds), ffi-merge-off-ignored (the switch off
// still merges).
func TestPureMergeCounts(t *testing.T) {
	on, off := 0, 0
	for seed := uint64(1); seed <= 8; seed++ {
		dir := t.TempDir()
		u, _ := mergeProgram(t, seed, dir)
		on += emitMerge(t, u, true, filepath.Join(dir, "on")).Merge
		off += emitMerge(t, u, false, filepath.Join(dir, "off")).Merge
	}
	if on == 0 || off != 0 {
		t.Errorf("merged calls: %d with the pass on, %d off; want some, then none", on, off)
	}
}

// TestPureMergeAgrees is LF3b's seeded property: each merge program
// prints the same, stops where it stops and exits the same with the merge
// pass on and off (the reference: the IR without the pass). A failing
// seed prints the Ergo source, both outcomes and its replay command.
//
// Mutants it alone catches: ffi-merge-across-write (a let mut operand
// counts as unwritten), ffi-merge-impure (an impure call gets the facts),
// ffi-merge-other-callee (calls of different callees are equal),
// ffi-merge-operand-ignored (calls on different operands are equal).
//
// It builds Go because the claim is about what the IR bodies do when they
// run, which only the patched compile's machine code shows.
func TestPureMergeAgrees(t *testing.T) {
	_, ir := needsBuild(t)
	first, count := envInt("ERGO_MERGE_SEED", 1), envInt("ERGO_MERGE_SEEDS", 8)
	merged := 0
	for seed := first; seed < first+count; seed++ {
		dir := t.TempDir()
		u, text := mergeProgram(t, seed, dir)
		merged += emitMerge(t, u, true, filepath.Join(dir, "on")).Merge
		emitMerge(t, u, false, filepath.Join(dir, "off"))
		onBin, offBin := filepath.Join(dir, "on.bin"), filepath.Join(dir, "off.bin")
		for _, b := range [][2]string{{"on", onBin}, {"off", offBin}} {
			if _, err := Build(ir, IR, filepath.Join(dir, b[0]), b[1], BuildOptions{}); err != nil {
				t.Fatalf("seed %d: the %s build: %v\n%s", seed, b[0], err, text)
			}
		}
		if a, b := run(t, onBin), run(t, offBin); a != b {
			t.Errorf("seed %d: the merged calls change the program\non:  %+v\noff: %+v\nreplay: ERGO_MERGE_SEED=%d ERGO_MERGE_SEEDS=1 go test -run TestPureMergeAgrees ./internal/experimental/irspike\n%s", seed, a, b, seed, text)
		}
	}
	if merged == 0 {
		t.Errorf("no seed merged a call: the property compares two equal lowerings")
	}
}
