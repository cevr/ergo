package main

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

// The registry of reviewed scripts (reviewed.go) is checked here, apart from
// the cache policy: no entry is stale, and no two entries share a name or a
// text. The policy test reads each entry's text only for its tripwire
// (cachePolicyProblems).

// registryProblems returns what is wrong with a registry of reviewed scripts
// for the given steps: two entries with one name or one text, and an entry
// that no step runs (a stale entry).
func registryProblems(registry []*reviewedScript, steps []*Step) []string {
	var problems []string
	names, texts := map[string]bool{}, map[string]bool{}
	for _, script := range registry {
		if names[script.name] {
			problems = append(problems, fmt.Sprintf("two reviewed scripts are named %q", script.name))
		}
		if texts[script.text] {
			problems = append(problems, fmt.Sprintf("reviewed script %q repeats another entry's text", script.name))
		}
		names[script.name], texts[script.text] = true, true
		if !slices.ContainsFunc(steps, func(step *Step) bool { return runsScript(step.Argv, script.text) }) {
			problems = append(problems, fmt.Sprintf("no step runs reviewed script %q: a stale entry", script.name))
		}
	}
	return problems
}

// runsScript reports whether argv runs sh -c text.
func runsScript(argv []string, text string) bool {
	for i := 0; i+2 < len(argv); i++ {
		if argv[i] == "sh" && argv[i+1] == "-c" && argv[i+2] == text {
			return true
		}
	}
	return false
}

// TestReviewedScripts checks the registry against the gate's steps, then
// shows that each check fails on the registry it must refuse: a stale entry
// and a repeated name or text.
func TestReviewedScripts(t *testing.T) {
	t.Parallel()
	_, steps := repositorySteps(t)
	if problems := registryProblems(reviewedScripts, steps); len(problems) > 0 {
		t.Fatalf("the reviewed scripts do not match the gate's steps:\n%s", strings.Join(problems, "\n"))
	}
	refuses := func(name string, registry []*reviewedScript, want string) {
		t.Helper()
		problems := registryProblems(registry, steps)
		if !slices.ContainsFunc(problems, func(p string) bool { return strings.Contains(p, want) }) {
			t.Errorf("%s: no problem says %q: %q", name, want, problems)
		}
	}
	with := func(entries ...*reviewedScript) []*reviewedScript {
		return append(slices.Clone(reviewedScripts), entries...)
	}
	refuses("an entry no step runs", with(&reviewedScript{name: "stale probe", text: "true"}), "a stale entry")
	refuses("a repeated name", with(&reviewedScript{name: gofmtScript.name, text: "true"}), "two reviewed scripts are named")
	refuses("a repeated text", with(&reviewedScript{name: "copy probe", text: gofmtScript.text}), "repeats another entry's text")
}
