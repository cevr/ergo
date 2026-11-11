package check_test

import (
	"testing"

	"ergo.local/prototype/internal/sketch/check"
)

// TestMachineLawDiagnostics holds the text and place of the diagnostics of
// machine laws (MU6; types.md, Laws 13): a target that is not a variant of
// the state, a law declared twice in one machine, and an entry after a law.
// Mutant caught: a reachability law that admits any type as its target.
func TestMachineLawDiagnostics(t *testing.T) {
	const head = "type S = Idle{} | Busy{}\ntype E = Ping{}\ntype Other {}\nfunc step(s S, e E) Decision[S, int] { Ignore }\n"
	rows := []struct {
		name, src, code, msg string
		line                 int
	}{
		{"not a variant", head + "type M machine[S, E] {\n\tinitial: Idle,\n\tstep: step,\n\tlaw L reaches Other,\n}\nfunc main() {}\n",
			"S108", "Other is not a variant of S, the state of machine M", 8},
		{"declared twice", head + "type M machine[S, E] {\n\tinitial: Idle,\n\tstep: step,\n\tlaw L reaches Busy,\n\tlaw L never reaches Busy,\n}\nfunc main() {}\n",
			"S108", "law L is declared twice", 9},
		{"entry after a law", head + "type M machine[S, E] {\n\tinitial: Idle,\n\tlaw L reaches Busy,\n\tstep: step,\n}\nfunc main() {}\n",
			"S001", "a machine's entries come before its laws", 8},
		// Invariant laws (X2). Mutants caught: the footprint check of a
		// predicate skipped; a predicate with an error row admitted; a
		// predicate of another parameter type admitted.
		{"impure predicate", head + "func q(s S) bool { true }\nfunc p(s S) bool {\n\tlet f = q\n\tf(s)\n}\ntype M machine[S, E] {\n\tinitial: Idle,\n\tstep: step,\n\tlaw L always p,\n}\nfunc main() {}\n",
			"S108", "the predicate of law L must be pure, since the explorer calls it at every state: predicate p calls a function value at 8:2 (types.md, Laws 13)", 13},
		{"fallible predicate", head + "type Fault {}\nfunc p(s S) bool fails Fault { true }\ntype M machine[S, E] {\n\tinitial: Idle,\n\tstep: step,\n\tlaw L sometimes p,\n}\nfunc main() {}\n",
			"S108", "the predicate p of law L must have the type func(S) bool, with no error or capability row: the explorer calls it at every state (types.md, Laws 13)", 10},
		{"predicate of another type", head + "func p(n int) bool { n > 0 }\ntype M machine[S, E] {\n\tinitial: Idle,\n\tstep: step,\n\tlaw L always p,\n}\nfunc main() {}\n",
			"S108", "the predicate p of law L must have the type func(S) bool, with no error or capability row: the explorer calls it at every state (types.md, Laws 13)", 9},
	}
	for _, r := range rows {
		_, diags := check.CheckSource(r.src)
		found := false
		for _, d := range diags {
			if d.Code == r.code && d.Message == r.msg && d.Line == r.line {
				found = true
			}
		}
		if !found {
			t.Errorf("%s: want %s %q at line %d, got %v", r.name, r.code, r.msg, r.line, diags)
		}
	}
}
