package main

import (
	"slices"
	"strings"
	"testing"
)

// heavyWork says why a step's command is heavy, or "" when nothing in it
// is: a closure sweep, a go test without -short (the full tier's test
// steps, the build of every test binary, the provenance analyzer's
// controls, the sketch9 kernel tests), a build of another module (-C),
// the tests of a host package, or gc over the lowered corpus. It reads the
// command as the gate runs it, scripts included, so a new step is judged
// by what it runs and not by its name.
func heavyWork(module string, step *Step) string {
	command := strings.Join(append(slices.Clone(step.Argv), step.Env...), " ")
	switch {
	case step.Sweep != "" || strings.Contains(command, sweepVariable+"="):
		return "it sweeps a package's closure"
	case strings.Contains(command, "ERGO_TEST_CORPUS_GC=1"):
		return "it compiles the lowered corpus with gc"
	case strings.Contains(command, " -C ") || slices.Contains(step.Argv, "-C"):
		return "it builds another module"
	case strings.Contains(command, "go test") && !strings.Contains(command, fastFlag):
		return "it runs go test without " + fastFlag
	}
	for _, host := range hostTestPackages {
		if runsGoTest(step) && (slices.Contains(step.Argv, "./"+host) || slices.Contains(step.Argv, module+"/"+host)) {
			return "it runs the tests of the host package " + host
		}
	}
	return ""
}

// The fast tier stays fast by its declaration, with no time limit
// (test-speed gate-minute; decision Q1: in-process tests and the examples
// step). Every step the gate declares has a class; a step whose command
// does heavy work (heavyWork) is declared classHeavy; the fast tier holds
// every classFast step of the full tier, each -short package step, and no
// step that does heavy work.
func TestFastTierHoldsNoHeavyStep(t *testing.T) {
	root := checkoutRoot(t)
	module := modulePath(root)
	full, err := gateSteps(root)
	if err != nil {
		t.Fatal(err)
	}
	var light []string
	for _, step := range full {
		if strings.HasPrefix(step.Name, "ergo test models/") && !step.MemoryHeavy {
			t.Errorf("%s reserves no memory weight: model searches must run alone", step.Name)
		}
		switch why := heavyWork(module, step); {
		case step.Class == classUndeclared:
			t.Errorf("%s declares no class (stepClass)", step.Name)
		case why != "" && step.Class != classHeavy:
			t.Errorf("%s is not declared classHeavy, and %s: the fast tier would run it", step.Name, why)
		case step.Class == classFast:
			light = append(light, step.Name)
		}
	}
	fast, err := fastSteps(root)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, step := range fast {
		names = append(names, step.Name)
		if step.Class != classFast {
			t.Errorf("fast tier: %s has class %d; want classFast", step.Name, step.Class)
		}
		if why := heavyWork(module, step); why != "" {
			t.Errorf("fast tier: %s is heavy: %s", step.Name, why)
		}
	}
	for _, name := range light {
		if !slices.Contains(names, name) {
			t.Errorf("fast tier: the classFast step %s is missing", name)
		}
	}
	if len(light) < 5 {
		t.Errorf("%d classFast steps in the full tier (%v); want the checks and the examples step", len(light), light)
	}
}

// A step that declares no class fails the fast tier's declaration, so a
// new step cannot reach the fast tier by default.
func TestAStepWithNoClassIsRefused(t *testing.T) {
	steps, err := classFastSteps([]*Step{{Name: "light", Class: classFast}, {Name: "heavy", Class: classHeavy}})
	if err != nil || len(steps) != 1 || steps[0].Name != "light" {
		t.Errorf("classFastSteps kept %v (%v); want only the classFast step", steps, err)
	}
	if _, err := classFastSteps([]*Step{{Name: "light", Class: classFast}, {Name: "new"}}); err == nil || !strings.Contains(err.Error(), `"new" declares no class`) {
		t.Errorf("a step with no class: err %v; want it refused by name", err)
	}
}
