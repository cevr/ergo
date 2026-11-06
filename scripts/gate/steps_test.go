package main

import (
	"fmt"
	"math"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// The checker and the lowering are sharded packages: each has recorded
// durations, and at each CPU budget it splits into that many shards or
// fewer, by its measured work (shardTargetSeconds). At a budget of 4 or more
// each has at least 4. Neither is left to the step of the other packages.
func TestCheckAndLowerShardByTheCPUBudget(t *testing.T) {
	root := checkoutRoot(t)
	module := modulePath(root)
	durations, err := readDurations(root)
	if err != nil {
		t.Fatal(err)
	}
	sharded := []string{"./internal/sketch/check", "./internal/sketch/lower"}
	var packages []string
	for _, pkg := range sharded {
		if !slices.Contains(slowPackages, pkg) {
			t.Errorf("%s is not among the slow packages that -record-durations measures", pkg)
		}
		full := module + "/" + strings.TrimPrefix(pkg, "./")
		if len(durations[full]) < 100 {
			t.Errorf("%s records %d test durations for %s", durationsFile, len(durations[full]), full)
		}
		packages = append(packages, full)
	}
	for _, budget := range []int{2, 4, 8} {
		t.Setenv(cpuVariable, strconv.Itoa(budget))
		cpus := stepCPUs()
		steps, err := declareSteps(root, packages)
		if err != nil {
			t.Fatal(err)
		}
		for i, pkg := range sharded {
			names, err := testNames(filepath.Join(root, pkg))
			if err != nil {
				t.Fatal(err)
			}
			total := 0.0
			for _, name := range names {
				total += durations[packages[i]][name]
			}
			want := max(1, min(int(math.Ceil(total/shardTargetSeconds)), cpus, len(names)))
			shards := 0
			for _, step := range steps {
				if strings.HasPrefix(step.Name, "go test "+pkg+" [") {
					shards++
					if !strings.HasSuffix(step.Name, fmt.Sprintf("/%d]", want)) || step.Argv[len(step.Argv)-1] != packages[i] {
						t.Errorf("budget %d: step %q %q is not one of %d shards of %s", budget, step.Name, step.Argv, want, packages[i])
					}
				}
				if step.Name == "go test (other packages)" && slices.Contains(step.Argv, packages[i]) {
					t.Errorf("budget %d: %s runs in the step of the other packages too", budget, packages[i])
				}
			}
			if shards != want {
				t.Errorf("budget %d (%d CPUs): %s runs in %d shards, want %d", budget, cpus, pkg, shards, want)
			}
			if cpus >= 4 && shards < 4 {
				t.Errorf("budget %d: %s runs in %d shards, want 4 or more", budget, pkg, shards)
			}
		}
	}
}

// Each host package has one step that never replays (-count=1) and bounds
// its parallel tests explicitly (-parallel hostTestParallel()): at most
// maxHostTestParallel and the CPU budget.
func TestHostPackagesRunOnEveryGateWithBoundedParallelism(t *testing.T) {
	t.Parallel()
	parallel := hostTestParallel()
	if n, err := strconv.Atoi(parallel); err != nil || n < 1 || n > maxHostTestParallel || n > stepCPUs() {
		t.Fatalf("hostTestParallel() %q is not a positive count within %d and the CPU budget %d", parallel, maxHostTestParallel, stepCPUs())
	}
	root := checkoutRoot(t)
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
