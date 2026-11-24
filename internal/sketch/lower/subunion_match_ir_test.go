package lower

import (
	"os"
	"slices"
	"testing"

	"ergo.local/prototype/internal/sketch/syntax"
)

// TestSubUnionMatchTakesTheIR is the in-process half of the IR lowering
// of sub-union pattern matches (ir_tree.go expandSubUnions): matching a
// declared sub-union (flat or interface, with or without `as`, after an
// overlapping variant, inside a tuple pattern, or in a slot variant)
// builds an IR body with no fallback. The fixture runs on both paths in
// TestIRReprosMatchSource/subunionmatch (irspike).
//
// Mutant (testdata/mutants.json): ir-subunion-match-refused.
func TestSubUnionMatchTakesTheIR(t *testing.T) {
	src, err := os.ReadFile("../../experimental/irspike/testdata/subunionmatch.ergo")
	if err != nil {
		t.Fatal(err)
	}
	o := mustLower(t, "subunionmatch", string(src))
	u := Unit{Package: o.pkg, Files: []*syntax.File{o.file}, Sources: []Source{{Path: testSourcePath, Text: string(src)}}}
	_, rep, err := LowerIR(u)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Fallback) != 0 {
		t.Errorf("bodies keep their source body: %v", rep.Fallback)
	}
	for _, name := range []string{"settle", "noBind", "firstWins", "nodeLeaf", "nodeBranch", "ergo__nodeBranchSlot", "describeNode"} {
		if !slices.Contains(rep.IR, name) {
			t.Errorf("%s has no IR body (IR %v)", name, rep.IR)
		}
	}
}
