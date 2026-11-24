package lower

import (
	"os"
	"slices"
	"testing"

	"ergo.local/prototype/internal/sketch/syntax"
)

// TestValueCapturesTakeTheIR is the in-process half of the IR lowering
// of escaping function literals that snapshot captured let mut locals by
// value (ir_closure.go snapshotCaptures). The fixture runs on both paths
// in TestIRReprosMatchSource/valuecapture (irspike).
//
// Mutant (testdata/mutants.json): ir-value-capture-refused.
func TestValueCapturesTakeTheIR(t *testing.T) {
	src, err := os.ReadFile("../../experimental/irspike/testdata/valuecapture.ergo")
	if err != nil {
		t.Fatal(err)
	}
	o := mustLower(t, "valuecapture", string(src))
	u := Unit{Package: o.pkg, Files: []*syntax.File{o.file}, Sources: []Source{{Path: testSourcePath, Text: string(src)}}}
	_, rep, err := LowerIR(u)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Fallback) != 0 {
		t.Errorf("bodies keep their source body: %v", rep.Fallback)
	}
	for _, name := range []string{"makeAdder", "makeLookup", "nestedCapture", "trailingCapture"} {
		if !slices.Contains(rep.IR, name) {
			t.Errorf("%s has no IR body (IR %v)", name, rep.IR)
		}
	}
}
