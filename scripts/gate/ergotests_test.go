package main

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// TestCompilerErgoTestsStep: a checkout with compiler/ runs its Ergo tests
// in the gate through scripts/ergotests over compiler/... (ST-0), so no Go
// bridge test is needed to bring them in; one without compiler/ has no
// such step.
//
// Mutant (testdata/mutants.json): st0-gate-step-dropped.
func TestCompilerErgoTestsStep(t *testing.T) {
	root := t.TempDir()
	if step := compilerErgoTests(root); step != nil {
		t.Errorf("no compiler/: step %+v; want none", step)
	}
	if err := os.MkdirAll(filepath.Join(root, "compiler", "syntax"), 0o755); err != nil {
		t.Fatal(err)
	}
	step := compilerErgoTests(root)
	if step == nil || !slices.Equal(step.Argv, []string{"go", "run", "./scripts/ergotests", "./compiler/..."}) {
		t.Errorf("compiler/: step %+v; want go run ./scripts/ergotests ./compiler/...", step)
	}
}
