package lower

import (
	"os"
	"slices"
	"testing"

	"ergo.local/prototype/internal/sketch/syntax"
)

// TestOpsConvWitnessesTakeTheIR is the in-process half of the IR lowering
// of operations conversions whose table functions take witnesses
// (ir_with.go opsConv / opsWitnesses). The fixture runs on both paths in
// TestIRReprosMatchSource/opswitness (irspike).
//
// Mutant (testdata/mutants.json): ir-ops-conv-witness-refused.
func TestOpsConvWitnessesTakeTheIR(t *testing.T) {
	src, err := os.ReadFile("../../experimental/irspike/testdata/opswitness.ergo")
	if err != nil {
		t.Fatal(err)
	}
	o := mustLower(t, "opswitness", string(src))
	u := Unit{Package: o.pkg, Files: []*syntax.File{o.file}, Sources: []Source{{Path: testSourcePath, Text: string(src)}}}
	_, rep, err := LowerIR(u)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Fallback) != 0 {
		t.Errorf("bodies keep their source body: %v", rep.Fallback)
	}
	for _, name := range []string{"makeScaled", "makeDynScaled", "makeNested", "withScaled"} {
		if !slices.Contains(rep.IR, name) {
			t.Errorf("%s has no IR body (IR %v)", name, rep.IR)
		}
	}
}
