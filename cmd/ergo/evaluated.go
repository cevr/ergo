package main

import (
	"io"
	"strconv"
)

// evaluatedTestRun answers a package's `ergo test` from the check-time
// evaluator (tier 1, TESTSPEED FT1): no lowering, no Go build and no
// process. It prints what the native test binary would, each verdict line
// marked " (evaluated)", and returns the native runner's exit: 1 with its
// error line when an obligation failed. A passing run writes the law
// ledger as a passing native run does.
//
// answered is false, and nothing is printed, when tier 2 must answer: with
// --native, --receipt, --explore (a schedule search or replay included) or
// a selection (--law, --skip-law, --run), or when the evaluator does not
// decide every obligation of the package (sketchfront.EvaluateTests).
func evaluatedTestRun(opts fileOptions, l loaded, stdout, stderr io.Writer) (answered bool, err error) {
	if opts.native || opts.receipt != "" || opts.explore || opts.search.mode != "" ||
		opts.law != "" || opts.skipLaw != "" || opts.run != "" {
		return false, nil
	}
	seed, states := uint64(1), 0
	if opts.seed != "" {
		seed, _ = strconv.ParseUint(opts.seed, 10, 64)
	}
	if opts.states != "" {
		states, _ = strconv.Atoi(opts.states)
	}
	answer, ok := l.analysis.EvaluateTests(seed, states, l.source.Admitted(), opts.file)
	if !ok {
		return false, nil
	}
	if _, err := io.WriteString(stdout, answer.Stdout); err != nil {
		return true, err
	}
	if answer.Failed > 0 {
		_, _ = io.WriteString(stderr, answer.Stderr)
		return true, exitStatusError{code: 1, handled: true}
	}
	return true, writeTestLedger(opts, l)
}
