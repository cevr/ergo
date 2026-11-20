package selfhostcheck

import "testing"

// TestCompilerTaskLawsRun runs the laws of compiler/check/task (capstone
// S3c2) through `ergo test`: a name resolved into an import names that
// import's exported declaration of its own text, through a selector whose
// X is the import, and the program checks to the same resolutions on one
// task and on eight. Mutants of compiler/check/task/testdata/mutants.json
// it alone catches: waves-ignore-imports, deps-skip-frozen,
// result-by-wave-order, import-index-unshifted, selector-ignores-shadow,
// selector-unexported, selector-unrecorded and join-drops-ext.
//
// It builds Go because `ergo test` lowers the laws into a test program and
// runs it: the check-time evaluator runs an operation type's laws, not a
// package's top-level laws, so no in-process check runs these.
func TestCompilerTaskLawsRun(t *testing.T) {
	needsBuild(t)
	runCompilerLaws(t, "./check/task")
}
