package irspike

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"ergo.local/prototype/internal/sketch/lower"
)

// TestIRFramesMatchSource is the seeded property over bodies that open a
// frame scope (FLIP F7 GP-FRAME, GenerateFrame): exit actions run when the
// body returns, on every path and last registered first, the same under
// both lowerings, and their functions have IR bodies. A failing seed
// prints the Ergo source, both outcomes and its replay command.
//
// It builds Go because the frame's End is a deferred call that runs the
// actions; only the compiled program runs it.
//
// Mutant f7g-frame-no-end: the frame's deferred End is dropped, so no
// action runs.
func TestIRFramesMatchSource(t *testing.T) {
	f7Property(t, "TestIRFramesMatchSource", GenerateFrame, frameFuncs...)
}

// TestIRFramesHaveBodies checks in process, on LowerIR's report, that the
// bodies of GenerateFrame that open a frame scope have IR bodies.
//
// Mutant f7g-frame-gated: irFrame admits only the static form, so a frame
// scope's body keeps its source (the code before GP-FRAME).
func TestIRFramesHaveBodies(t *testing.T) {
	first, count := envInt("ERGO_IRSPIKE_SEED", 1), envInt("ERGO_IRSPIKE_SEEDS", 4)
	for seed := first; seed < first+count; seed++ {
		text := GenerateFrame(seed)
		file := filepath.Join(t.TempDir(), "p.ergo")
		if err := os.WriteFile(file, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
		u, err := Load(file)
		if err != nil {
			t.Fatalf("seed %d: the program does not check: %v\n%s", seed, err, text)
		}
		_, rep, err := lower.LowerIR(u)
		if err != nil {
			t.Fatalf("seed %d: %v\n%s", seed, err, text)
		}
		for _, f := range frameFuncs {
			if !slices.Contains(rep.IR, f) {
				t.Errorf("seed %d: %s has no IR body (IR bodies: %v)\n%s", seed, f, rep.IR, text)
			}
		}
	}
}
