package irspike

// A property's program must run the bodies it compares (IRSPIKE-VACUOUS,
// 2026-11-19). CF1 (internal/sketch/lower/ir_constfold.go) computes a
// rowless call whose arguments are constants at build time, so a main that
// prints f(3) holds the evaluator's value and never calls f: the IR body
// of f is compiled and never run, and a property comparing the two paths'
// output compares the text path with the evaluator. So the harness lowers
// the IR path with the fold off (Emit), which keeps every call as written,
// and builds the folded program beside it (both, in the tests) to hold the
// fold to the same output. Runs is the guard both() applies.

import (
	"fmt"

	"ergo.local/prototype/internal/sketch/lower"
)

// Runs is nil when rep, an IR lowering's report, shows no call or
// template the build replaced by its value (CF1's Folded and Templates
// counts): then the program calls every body it built where the source
// calls it. Else it says how many calls the program skips.
func Runs(rep lower.IRReport) error {
	if n := rep.Folds.Folded + rep.Folds.Templates; n > 0 {
		return fmt.Errorf("the build-time fold (CF1) computed %d calls or templates, so the program never runs those bodies and the property compares the evaluator, not the IR (vacuous.go)", n)
	}
	return nil
}
