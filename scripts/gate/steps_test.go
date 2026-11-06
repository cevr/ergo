package main

import (
	"path/filepath"
	"slices"
	"strconv"
	"testing"
)

// Each host package has one step that never replays (-count=1) and bounds
// its parallel tests explicitly (-parallel hostTestParallel()): at most
// maxHostTestParallel and the CPU budget.
func TestHostPackagesRunOnEveryGateWithBoundedParallelism(t *testing.T) {
	t.Parallel()
	parallel := hostTestParallel()
	if n, err := strconv.Atoi(parallel); err != nil || n < 1 || n > maxHostTestParallel || n > stepCPUs() {
		t.Fatalf("hostTestParallel() %q is not a positive count within %d and the CPU budget %d", parallel, maxHostTestParallel, stepCPUs())
	}
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	steps, err := gateSteps(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, relative := range hostTestPackages {
		pkg := modulePath(root) + "/" + relative
		var found []*Step
		for _, step := range steps {
			if runsGoTest(step) && slices.Contains(step.Argv, pkg) {
				found = append(found, step)
			}
		}
		if len(found) != 1 {
			t.Fatalf("host package %s is in %d go test steps, want 1", pkg, len(found))
		}
		argv := found[0].Argv
		if !slices.Contains(argv, "-count=1") {
			t.Errorf("step %q may replay %s: %q has no -count=1", found[0].Name, pkg, argv)
		}
		if i := slices.Index(argv, "-parallel"); i < 0 || i+1 >= len(argv) || argv[i+1] != parallel {
			t.Errorf("step %q does not bound %s's parallel tests to %s: %q", found[0].Name, pkg, parallel, argv)
		}
	}
}
