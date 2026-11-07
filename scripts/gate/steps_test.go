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

// In both tiers every step that runs a CLI package's tests
// (cliTestPackages) needs the step that builds bin/ergo in this run and
// names that file to the tests, so cmd/ergo builds once per run and no step
// can run a stale or missing CLI. The build step writes the file the
// variable names.
func TestCLIPackagesRunTheCLIThisRunBuilt(t *testing.T) {
	root := checkoutRoot(t)
	module := modulePath(root)
	variable, value, _ := strings.Cut(cliTestEnvironment[0], "=")
	if len(cliTestEnvironment) != 1 || variable != "ERGO_TEST_CLI" || value != "bin/ergo" {
		t.Fatalf("cliTestEnvironment %q; want ERGO_TEST_CLI=bin/ergo, the file the build step writes", cliTestEnvironment)
	}
	full, err := gateSteps(root)
	if err != nil {
		t.Fatal(err)
	}
	fast, err := fastSteps(root)
	if err != nil {
		t.Fatal(err)
	}
	for tier, steps := range map[string][]*Step{fullTier: full, fastTier: fast} {
		build := slices.IndexFunc(steps, func(s *Step) bool { return s.Name == buildCLIStep })
		if build < 0 {
			t.Fatalf("%s tier: no %q step", tier, buildCLIStep)
		}
		if argv := steps[build].Argv; !slices.Contains(argv, value) || !slices.Contains(argv, "./cmd/ergo") {
			t.Errorf("%s tier: %q does not build ./cmd/ergo into %s: %q", tier, buildCLIStep, value, argv)
		}
		for _, relative := range cliTestPackages {
			found := 0
			for _, step := range steps {
				if !runsGoTest(step) || !slices.Contains(step.Argv, module+"/"+relative) && !slices.Contains(step.Argv, "./"+relative) {
					continue
				}
				found++
				if !slices.Contains(step.Needs, buildCLIStep) {
					t.Errorf("%s tier: %s does not need %q: it could run a stale or missing CLI (needs %q)", tier, step.Name, buildCLIStep, step.Needs)
				}
				if !slices.Contains(step.Env, cliTestEnvironment[0]) {
					t.Errorf("%s tier: %s does not name the built CLI to its tests (environment %q), so each test process builds its own", tier, step.Name, step.Env)
				}
				// The fast step replays under its package key, and bin/ergo
				// is not a file of the package: the key must cover what
				// the binary is built from, so an edit of the CLI or of
				// the compiler under it runs the step again.
				if tier == fastTier {
					for _, source := range []string{"cmd/ergo/main.go", "cmd/ergo/build.go", "internal/sketch/check/check.go"} {
						if step.Package == nil || !step.Package.Covers(source) {
							t.Errorf("fast tier: the key of %s does not cover %s, a source of the bin/ergo its tests run: an edit of it would replay a stale pass", step.Name, source)
						}
					}
				}
			}
			if found == 0 {
				t.Errorf("%s tier: no step runs the tests of %s", tier, relative)
			}
		}
	}
}

// The CLI a cliTestPackages step names (cliTestEnvironment) is a part of
// its key in both tiers: the full tier's step key (gate.key) and the fast
// tier's package identity (packageIdentity). A step that names another CLI
// cannot replay a pass recorded with this one.
func TestCLIPathIsInTheStepKey(t *testing.T) {
	root := fixture(t)
	g := &gate{root: root, cacheDir: t.TempDir()}
	if err := g.loadFiles(); err != nil {
		t.Fatal(err)
	}
	step := func(env []string) *Step {
		return &Step{Name: "go test ./internal/sketch/lower [1/8]", Argv: []string{"go", "test", "./internal/sketch/lower"}, Env: env, Inputs: &Inputs{Paths: []string{"src/"}}}
	}
	this, other := step(cliTestEnvironment), step([]string{"ERGO_TEST_CLI=bin/other"})
	thisKey, err := g.key(this)
	if err != nil {
		t.Fatal(err)
	}
	otherKey, err := g.key(other)
	if err != nil {
		t.Fatal(err)
	}
	if thisKey == otherKey {
		t.Errorf("full tier: the step key does not change with the CLI the step names (%q, %q)", this.Env, other.Env)
	}
	if slices.Equal(packageIdentity(this), packageIdentity(other)) {
		t.Errorf("fast tier: the package identity does not change with the CLI the step names: %q", packageIdentity(this))
	}
}

// Each step of a host package never replays (-count=1) and bounds its
// parallel tests explicitly (-parallel hostTestParallel()): at most
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
		// One step, and one per test of sandboxHostTests
		// (TestSandboxHostTestsRunInStepsOfTheirOwn).
		if want := 1 + len(sandboxHostTests[relative]); len(found) != want {
			t.Fatalf("host package %s is in %d go test steps, want %d", pkg, len(found), want)
		}
		for _, step := range found {
			argv := step.Argv
			if !slices.Contains(argv, "-count=1") {
				t.Errorf("step %q may replay %s: %q has no -count=1", step.Name, pkg, argv)
			}
			if i := slices.Index(argv, "-parallel"); i < 0 || i+1 >= len(argv) || argv[i+1] != parallel {
				t.Errorf("step %q does not bound %s's parallel tests to %s: %q", step.Name, pkg, parallel, argv)
			}
		}
	}
}
