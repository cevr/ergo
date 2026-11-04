package main

import (
	"path/filepath"
	"slices"
	"strconv"
	"testing"
)

// Each host package has one step that never replays (-count=1) and bounds
// its parallel tests explicitly (-parallel hostTestParallel): Go's default,
// GOMAXPROCS, would start as many layout gates and systemd scopes at once as
// the host has CPUs.
func TestHostPackagesRunOnEveryGateWithBoundedParallelism(t *testing.T) {
	t.Parallel()
	if n, err := strconv.Atoi(hostTestParallel); err != nil || n < 1 {
		t.Fatalf("hostTestParallel %q is not a positive count", hostTestParallel)
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
		if i := slices.Index(argv, "-parallel"); i < 0 || i+1 >= len(argv) || argv[i+1] != hostTestParallel {
			t.Errorf("step %q does not bound %s's parallel tests to %s: %q", found[0].Name, pkg, hostTestParallel, argv)
		}
	}
}
