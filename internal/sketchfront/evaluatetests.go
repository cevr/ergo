package sketchfront

import (
	"path/filepath"
	"strings"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/eval"
	"ergo.local/prototype/internal/sketch/lawcache"
	"ergo.local/prototype/internal/sketch/lower"
	"ergo.local/prototype/internal/stage"
)

// EvaluatedMark ends each verdict line that the evaluator wrote (TESTSPEED
// FT1, "the evidence is in the output"), where the law cache's mark
// " (cached)" ends a stored one.
const EvaluatedMark = " (evaluated)"

// StoredMark ends each verdict line read from the verdict store (lawcache,
// TESTSPEED FT2), as the test binary marks a law verdict it did not run.
const StoredMark = " (cached)"

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
	// Stored is the count of verdicts read from the store, not run.
	Stored int
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
//
// With a store (lawcache), a test or law run whose verdict it holds under
// an unchanged reach digest is not run and prints StoredMark; a pass is
// stored. native is EvalVerdicts': the toolchain whose test binary's law
// verdicts it reads too. An obligation the evaluator leaves incomplete
// gets a route note under its key, and while any obligation of the
// package has one the package goes to the test binary with no evaluation
// (TESTSPEED FT2).
func (a Analysis) EvaluateTests(seed uint64, states int, admitted, source string, store *lawcache.Store, native string) (TestAnswer, bool) {
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
	pkg := a.rootPackage()
	foreign := foreignKeys(a, nil)
	var notes map[string]string
	if store != nil && pkg != nil {
		// A route note of an obligation whose inputs did not change
		// sends the package to the test binary with no evaluation.
		notes = routes(pkg, seed, states, b.Steps, foreign)
		for _, name := range plan.Names {
			if text, ok := store.Get(notes[name]); ok && text == routeNote {
				stage.Add(stage.PackagesRouted, 1)
				return TestAnswer{}, false
			}
		}
		b.Verdicts = a.EvalVerdicts(store, seed, states, native)
		b.Tests = &testVerdicts{store: store, pkg: pkg, seed: seed, keys: map[*check.Test]string{}, foreign: foreign}
	}
	rep, err := a.Evaluate(b)
	if err != nil {
		return TestAnswer{}, false
	}
	// The obligations the evaluator gave up on get a route note.
	for _, r := range rep.Results {
		if r.Status == eval.Incomplete {
			if k, ok := notes[r.Name]; ok {
				store.Put(k, routeNote)
			}
		}
	}
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
	stored := 0
	for i, line := range lines {
		if i == len(lines)-1 {
			out.WriteString(line + "\n") // the summary
			break
		}
		mark := EvaluatedMark
		if rep.Results[i].Cached {
			mark, stored = StoredMark, stored+1
		}
		first, rest, more := strings.Cut(line, "\n")
		out.WriteString(first + mark + "\n")
		if more {
			out.WriteString(rest + "\n")
		}
	}
	answer := TestAnswer{Stdout: out.String(), Exit: exit, Obligations: len(rep.Results) - stored, Stored: stored, Steps: rep.Steps}
	if stderr != "" {
		answer.Stderr = stderr + "\n"
	}
	stage.Add(stage.ObligationsStored, int64(stored))
	return answer, true
}
