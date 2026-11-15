package sketchfront

import (
	"path/filepath"
	"strings"

	"ergo.local/prototype/internal/sketch/eval"
	"ergo.local/prototype/internal/sketch/lower"
	"ergo.local/prototype/internal/stage"
)

// EvaluatedMark ends each verdict line that the evaluator wrote (TESTSPEED
// FT1, "the evidence is in the output"), where the law cache's mark
// " (cached)" ends a stored one.
const EvaluatedMark = " (evaluated)"

// TestAnswer is a package's `ergo test` answered by the check-time
// evaluator (tier 1, TESTSPEED FT1): what the native test binary would
// print, line for line, with each verdict marked EvaluatedMark.
type TestAnswer struct {
	// Stdout is the verdict lines, in the native runner's order, and its
	// summary line.
	Stdout string
	// Stderr is the native runner's error line of a failing run.
	Stderr string
	// Exit is the native runner's exit status: 1 when an obligation
	// failed, else 0.
	Exit int
	// Obligations and Steps are the evidence: the obligations evaluated
	// and the evaluator's steps.
	Obligations int
	Steps       int64
}

// EvaluateTests answers the root package's full `ergo test` (no --law,
// --skip-law or --run) from the evaluator, at seed and with a budget of
// states for a reachability law (0 is the default). admitted is the
// admitted path of the source (BuildRequest.Source), and source the path
// ergo test names the program by, for the replay lines.
//
// ok is false when the evaluator does not decide the whole package: a
// line of the native run the evaluator does not write (lower.TestLines
// not Plain), an obligation it skipped, left incomplete or reported in
// words the native runner does not use (eval.VerdictLine), a defect (the
// native runner stops there), or a set of obligations that is not the
// native runner's. The native binary then answers the package (tier 2).
func (a Analysis) EvaluateTests(seed uint64, states int, admitted, source string) (TestAnswer, bool) {
	unit, err := analysisUnit(a, admitted)
	if err != nil {
		return TestAnswer{}, false
	}
	plan := lower.TestLinesOf(unit)
	if !plan.Plain || len(plan.Names) == 0 {
		return TestAnswer{}, false
	}
	// A source of one file is parsed with no name; the native runner
	// names its sites by the file's base name, so the evaluator does too
	// while it runs.
	if a.program == nil && a.file != nil && a.file.Name == "" {
		a.file.Name = filepath.Base(admitted)
		defer func() { a.file.Name = "" }()
	}
	b := eval.DefaultBudget()
	b.Seed, b.States = seed, states
	rep, err := a.Evaluate(b)
	if err != nil {
		return TestAnswer{}, false
	}
	stage.Add(stage.EvalSteps, rep.Steps)
	// The native runner's obligations, in its order: the evaluator's
	// results must be the same list (eval.Summary relies on its order).
	if len(rep.Results) != len(plan.Names) {
		return TestAnswer{}, false
	}
	for i, r := range rep.Results {
		if r.Name != plan.Names[i] {
			return TestAnswer{}, false
		}
	}
	lines, stderr, exit, ok := eval.Summary(rep.Results, source)
	if !ok {
		return TestAnswer{}, false
	}
	var out strings.Builder
	for i, line := range lines {
		if i == len(lines)-1 {
			out.WriteString(line + "\n") // the summary
			break
		}
		first, rest, more := strings.Cut(line, "\n")
		out.WriteString(first + EvaluatedMark + "\n")
		if more {
			out.WriteString(rest + "\n")
		}
	}
	answer := TestAnswer{Stdout: out.String(), Exit: exit, Obligations: len(rep.Results), Steps: rep.Steps}
	if stderr != "" {
		answer.Stderr = stderr + "\n"
	}
	stage.Add(stage.ObligationsEvaluated, int64(answer.Obligations))
	return answer, true
}
