package eval

import (
	"sync"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
)

// TestFirstReaderSeesBuiltNatives: a law whose first native is a Go
// function std reaches through import go (std math.Abs calls Go's
// math.Abs) decides on natives no run has built yet. The tables were once
// package maps that loadNatives filled; goNativeOf read its map without
// loading it, so in a fresh process the first law's preflight found it
// empty and skipped std math.Abs as "a Go function (Abs), even a pure one"
// (EVAL-NATIVE-LOAD; std bench's MatchesEnumeration, the first law of its
// run). loadedNatives is set to a fresh, unbuilt one for the test.
//
// Mutant (testdata/mutants.json): enl-go-read-unbuilt.
func TestFirstReaderSeesBuiltNatives(t *testing.T) {
	was := loadedNatives
	t.Cleanup(func() { loadedNatives = was })
	loadedNatives = sync.OnceValue(buildNatives)
	pkg, diags := check.CheckSource("import math\n\nlaw AbsIsNotNegative(x float64) { math.Abs(x) >= 0.0 || x != x }\n")
	if pkg == nil || len(diags) > 0 {
		t.Fatalf("check: %v", diags)
	}
	rep, err := Run(pkg, DefaultBudget())
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Results) != 1 || rep.Results[0].Status != Passed {
		t.Fatalf("law AbsIsNotNegative on unbuilt natives: %+v, want one passed result", rep.Results)
	}
}
