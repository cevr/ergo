package main

import (
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/eval"
)

// TestStdRedGuard: what fails on its own source, and a std law with no Go
// runner that did not run there (a skip, one provider's skip, no
// verdict), needs a line in the red list in the class it fails in, and
// fails by name without one; a line of another class is a new failure;
// a listed key that passes, or names nothing, fails too, so the list only
// shrinks. Mutants (testdata/mutants.json): nl-skip-counts-as-run,
// nl-failure-unlisted-passes, nl-listed-pass-kept, nl-class-change-hidden.
func TestStdRedGuard(t *testing.T) {
	bare := []string{"a Passes", "a Skips", "a Unseen", "a ListedPasses", "a OneProviderSkips"}
	run := stdRun{
		verdicts: map[string][]eval.Status{
			"a Passes":           {eval.Passed},
			"a Skips":            {eval.Skipped},
			"a ListedPasses":     {eval.Passed},
			"a OneProviderSkips": {eval.Passed, eval.Skipped},
			"b Refuted":          {eval.Refuted},
		},
		failed: map[string]string{"b Refuted": "refuted", "c *": "internal error", "d Listed": "refuted", "f Moved": "internal error"},
	}
	listed := map[string]redLine{
		"a Unseen":       {"not run", "a host call", "R1"},
		"a ListedPasses": {"not run", "was skipped", "R2"},
		"e Gone":         {"refuted", "renamed", "R3"},
		"d Listed":       {"refuted", "known", "R4"},
		"f Moved":        {"refuted", "was refuted", "R5"},
	}
	got := strings.Join(redProblems(bare, run, listed), "\n")
	for _, want := range []string{"a Skips is not run", "a OneProviderSkips is not run", "b Refuted is refuted", "c * is internal error", "lists a ListedPasses", "lists e Gone", "f Moved is internal error on its own source, and"} {
		if !strings.Contains(got, want) {
			t.Errorf("the problems do not name %q:\n%s", want, got)
		}
	}
	for _, quiet := range []string{"a Passes", "a Unseen", "d Listed"} {
		if strings.Contains(got, quiet) {
			t.Errorf("the problems name %q, which ran or is listed in its class:\n%s", quiet, got)
		}
	}
}
