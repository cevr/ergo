package irspike

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"ergo.local/prototype/internal/sketch/lower"
)

// TestIRSeqLoopsMatchSource is PORT-FORIN's property (GenerateSeq): a
// for-in over a Seq lowers on the IR to Go's range-over-func, and each
// program prints the same, exits the same and reports the same defect
// under the source lowering and the IR: loops that break, continue,
// return, fail and poll, nested Seq loops, and in odd seeds a Seq that
// ignores yield's false, where Go panics on both paths. With the lowering
// on, every function with a Seq loop has an IR body. A failing seed prints
// the Ergo source, both outcomes and its replay command.
//
// It builds Go because the loop's meaning comes from Go's own rangefunc
// rewrite in the compiler, which only the compiled program shows.
func TestIRSeqLoopsMatchSource(t *testing.T) {
	src, ir := needsBuild(t)
	first, count := envInt("ERGO_IRSPIKE_SEED", 1), envInt("ERGO_IRSPIKE_SEEDS", 4)
	for seed := first; seed < first+count; seed++ {
		text := GenerateSeq(uint64(seed))
		replay := "replay: ERGO_IRSPIKE_SEED=" + itoa(seed) + " ERGO_IRSPIKE_SEEDS=1 go test -run '^TestIRSeqLoopsMatchSource$' ./internal/experimental/irspike"
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
		a, b := runReport(t, sb), runReport(t, ib)
		if a != b {
			t.Fatalf("seed %d: the paths differ\nsource: %+v\nir:     %+v\n%s\n%s", seed, a, b, replay, text)
		}
		if seed%2 == 1 && a.exit == 0 {
			t.Errorf("seed %d: the Seq that ignores yield's false did not panic\n%s", seed, replay)
		}
		if !lower.IRSeqRangeOn() {
			continue
		}
		var missing []string
		for _, f := range seqFuncs {
			if !slices.Contains(rep.IR, f) {
				missing = append(missing, f)
			}
		}
		if len(missing) > 0 || rep.SeqRanges == 0 {
			t.Errorf("seed %d: no IR body for %v (%d Seq loops on the IR; fallback %v)\n%s", seed, missing, rep.SeqRanges, rep.Fallback, replay)
		}
	}
}
