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
// --native, -work or --debug (which name or keep what a build writes),
// --receipt, --explore (a schedule search or replay included) or a
// selection (--law, --skip-law, --run), or when the evaluator does not decide every
// obligation of the package (sketchfront.EvaluateTests).
func evaluatedTestRun(opts fileOptions, l loaded, stdout, stderr io.Writer, keep func([]byte)) (answered bool, err error) {
	if opts.native || opts.work || opts.debug || opts.receipt != "" || opts.explore || opts.search.mode != "" ||
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
	store := lawStore()
	defer boundLaws(store)
	answer, ok := l.analysis.EvaluateTests(seed, states, l.source.Admitted(), opts.file, store, pinnedToolchain(opts.platform))
	if !ok {
		return false, nil
	}
	if _, err := io.WriteString(stdout, answer.Stdout); err != nil {
		return true, err
	}
	if answer.Exit != 0 {
		_, _ = io.WriteString(stderr, answer.Stderr)
		return true, exitStatusError{code: answer.Exit, handled: true}
	}
	if err := writeLedger(opts, l); err != nil {
		return true, err
	}
	// A passing answer is kept as the warm answer, as a native pass is.
	// Its replay counts the obligations it decided as stored, as a native
	// pass's does (FT0's counter).
	var result testResult
	result.stdout.WriteString(answer.Stdout)
	result.obligations = nativeObligations(l.analysis.Root().Package, opts)
	keep(result.encode())
	return true, nil
}
