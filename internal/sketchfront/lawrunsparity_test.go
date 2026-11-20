package sketchfront

import (
	"path/filepath"
	"testing"

	"ergo.local/prototype/internal/sketch/lower"
)

// TestCheckBuildsAgreeOnEveryPath (LAW-RUNS-PARITY): the rewrite check's
// build of each example binary whose sites rest on laws (its site
// instances, post laws and the imported packages' runs of them) runs the
// same law runs on every test entry point, the text path and each IR one
// (lower.CheckLawRunParity); at least one example's check build runs an
// imported package's laws.
//
// Mutant (lower mutants.json): lrp-imports-dropped.
func TestCheckBuildsAgreeOnEveryPath(t *testing.T) {
	imported := false
	for _, name := range []string{"catalog", "cborpack"} {
		dir, err := filepath.Abs(filepath.Join("..", "..", "examples", name, "cmd", name))
		if err != nil {
			t.Fatal(err)
		}
		a, err := CheckDir(dir)
		if err != nil || len(a.Diagnostics) > 0 {
			t.Fatalf("%s does not check: %v %v", name, err, a.Diagnostics)
		}
		unit, err := analysisUnit(a, filepath.Join(dir, "main.ergo"))
		if err != nil {
			t.Fatal(err)
		}
		cu, ok, err := lower.RewriteCheckUnit(unit)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !ok {
			continue
		}
		imported = imported || len(cu.ImportLaws) > 0
		if err := lower.CheckLawRunParity(cu); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	if !imported {
		t.Fatal("no example's check build runs an imported package's laws; the property needs one")
	}
}
