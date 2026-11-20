package selfhostcheck

import "testing"

// TestCompilerIncLawsRun runs the law of compiler/check/inc (capstone S3e,
// S3E-MODEL slice 2) through `ergo test`: over seeded edit sequences of a
// three-package program, the incremental check's rows equal the check
// from scratch, and its plan checks again exactly the rows the rule names
// (models/checkinc is the design, proved there over every sequence).
// Mutants of compiler/check/inc/testdata/mutants.json it alone catches:
// listed there once they are run (LAND FIRST).
//
// It builds Go because `ergo test` lowers the law into a test program and
// runs it: the check-time evaluator skips the law (task.Check calls
// tasks.Transform, a Go function).
func TestCompilerIncLawsRun(t *testing.T) {
	needsBuild(t)
	runCompilerLaws(t, "./check/inc")
}
