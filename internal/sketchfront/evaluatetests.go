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

// RefutedTape is the shrunk draw tape of a refuted property from one test run
// (PROPERTY-TESTING P8), to persist in ergo.cases.
type RefutedTape struct {
	Name string
	Tape []uint64
}

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
	// RefutedTapes holds the shrunk draw tape of each refuted drawn property.
	RefutedTapes []RefutedTape
	// PassedStored records the indices of Budget.StoredCases[name] that passed
	// during replay, keyed by obligation name.
	PassedStored map[string][]int
}

// EvaluateTests answers the root package's full `ergo test` (no --law,
// --skip-law or --run) from the evaluator, at seed and with a budget of
// states for a reachability law (0 is the default).
func (a Analysis) EvaluateTests(seed uint64, states int, admitted, source string, store *lawcache.Store, native string) (TestAnswer, bool) {
	return a.EvaluateTestsWithCases(seed, states, admitted, source, store, native, nil, false)
}

// EvaluateTestsWithCases is EvaluateTests with optional stored regression
// tapes from ergo.cases (PROPERTY-TESTING P8) and prune-cases mode.
func (a Analysis) EvaluateTestsWithCases(seed uint64, states int, admitted, source string, store *lawcache.Store, native string, storedCases map[string][]eval.StoredCase, pruneCases bool) (TestAnswer, bool) {
	unit, err := analysisUnit(a, admitted)
	if err != nil {
		return TestAnswer{}, false
	}
	plan := lower.TestLinesOf(unit)
	if !plan.Plain || len(plan.Names) == 0 {
		return TestAnswer{}, false
	}
	if a.program == nil && a.file != nil && a.file.Name == "" {
		a.file.Name = filepath.Base(admitted)
		defer func() { a.file.Name = "" }()
	}
	b := eval.DefaultBudget()
	b.Seed, b.States = seed, states
	b.Source = source
	b.StoredCases = storedCases
	b.PruneCases = pruneCases
	pkg := a.rootPackage()
	foreign := foreignKeys(a, nil)
	var notes map[string]string
	if store != nil && pkg != nil && !pruneCases {
		memo := lower.NewDigests()
		notes = routes(memo, pkg, seed, states, b.Steps, foreign)
		for _, name := range plan.Names {
			if text, ok := store.Get(notes[name]); ok && text == routeNote {
				stage.Add(stage.PackagesRouted, 1)
				return TestAnswer{}, false
			}
		}
		ev := a.evalVerdictsIn(memo, store, seed, states, native, foreign)
		tv := &testVerdicts{store: store, pkg: pkg, seed: seed, keys: map[*check.Test]string{}, foreign: foreign, memo: memo}
		if len(storedCases) > 0 {
			b.Verdicts = storedCasesVerdicts{inner: ev, pkg: pkg, stored: storedCases}
			b.Tests = storedCasesTests{inner: tv, stored: storedCases}
		} else {
			b.Verdicts = ev
			b.Tests = tv
		}
	}
	rep, err := a.Evaluate(b)
	if err != nil {
		return TestAnswer{}, false
	}
	if notes != nil {
		for _, r := range rep.Results {
			if r.Status == eval.Incomplete {
				if k, ok := notes[r.Name]; ok {
					store.Put(k, routeNote)
				}
			}
		}
	}
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
	var refutedTapes []RefutedTape
	passedStored := map[string][]int{}
	for i, line := range lines {
		if i == len(lines)-1 {
			out.WriteString(line + "\n")
			break
		}
		r := rep.Results[i]
		if r.Status == eval.Refuted && r.ShrunkTape != nil {
			refutedTapes = append(refutedTapes, RefutedTape{Name: r.Name, Tape: r.ShrunkTape})
		}
		if len(r.PassedStored) > 0 {
			passedStored[r.Name] = r.PassedStored
		}
		mark := EvaluatedMark
		if r.Cached {
			mark, stored = StoredMark, stored+1
		}
		first, rest, more := strings.Cut(line, "\n")
		out.WriteString(first + mark + "\n")
		if more {
			out.WriteString(rest + "\n")
		}
	}
	answer := TestAnswer{
		Stdout:       out.String(),
		Exit:         exit,
		Obligations:  len(rep.Results) - stored,
		Stored:       stored,
		Steps:        rep.Steps,
		RefutedTapes: refutedTapes,
		PassedStored: passedStored,
	}
	if stderr != "" {
		answer.Stderr = stderr + "\n"
	}
	stage.Add(stage.ObligationsStored, int64(stored))
	return answer, true
}

type storedCasesVerdicts struct {
	inner  eval.Verdicts
	pkg    *check.Package
	stored map[string][]eval.StoredCase
}

func (v storedCasesVerdicts) hasStored(law *check.Law, binding []check.LawProvider) bool {
	if v.pkg == nil || len(v.stored) == 0 {
		return false
	}
	for _, rt := range lower.RunTargets(v.pkg) {
		if rt.Law == law && len(v.stored[rt.Label]) > 0 {
			return true
		}
	}
	return false
}

func (v storedCasesVerdicts) Cached(law *check.Law, binding []check.LawProvider) (eval.Verdict, bool) {
	if v.hasStored(law, binding) {
		return eval.Verdict{}, false
	}
	return v.inner.Cached(law, binding)
}

func (v storedCasesVerdicts) Held(law *check.Law, binding []check.LawProvider, verdict eval.Verdict) {
	if v.hasStored(law, binding) {
		return
	}
	v.inner.Held(law, binding, verdict)
}

type storedCasesTests struct {
	inner  eval.TestVerdicts
	stored map[string][]eval.StoredCase
}

func (t storedCasesTests) Cached(test *check.Test) (eval.Verdict, bool) {
	if len(t.stored[test.Name]) > 0 {
		return eval.Verdict{}, false
	}
	return t.inner.Cached(test)
}

func (t storedCasesTests) Held(test *check.Test, verdict eval.Verdict) {
	if len(t.stored[test.Name]) > 0 {
		return
	}
	t.inner.Held(test, verdict)
}
