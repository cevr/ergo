package main

import (
	"fmt"
	"go/build"
	"math"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"ergo.local/prototype/scripts/gate/replay"
)

// checkoutRoot is the real checkout's root, for a test that reads what the
// gate declares of it: its packages and their closures (go list, which reads
// every Go file of the module), the test names of the sharded packages, the
// declared steps. Under -short it skips, so a lane's focused -short run
// of this package stays short. The gate runs this host package only in
// the full tier (classHeavy), uncached, on every batch head.
func checkoutRoot(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("full tier: it reads the gate's declarations of the whole checkout, which the fast tier's key does not hold")
	}
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	return root
}

// runsIn reports whether go test with these selection flags runs the
// top-level test name: -run must match it and -skip must not.
func runsIn(selection []string, name string) bool {
	for i := 0; i+1 < len(selection); i += 2 {
		matched := regexp.MustCompile(selection[i+1]).MatchString(name)
		if selection[i] == "-run" && !matched || selection[i] == "-skip" && matched {
			return false
		}
	}
	return true
}

// testSelection is a go test argv's -run and -skip flags with their values.
func testSelection(argv []string) []string {
	var selection []string
	for i := 0; i+1 < len(argv); i++ {
		if argv[i] == "-run" || argv[i] == "-skip" {
			selection = append(selection, argv[i], argv[i+1])
		}
	}
	return selection
}

// The fast tier's selection and the full-only set are every test, each
// once: over generated test lists (the source scan may miss a test), sizes
// and shard counts, each test that is not large runs in exactly one fast
// shard, and each large one in none (gate-fast acceptance leg 4, the rule;
// TestFastTierCoversEveryListedTest checks it over the module's tests).
func TestFastSelectionAndTheFullOnlySetAreEveryTest(t *testing.T) {
	seed := uint64(time.Now().UnixNano())
	random := rand.New(rand.NewPCG(seed, seed^0x51ce))
	for trial := range 500 {
		var defined []string
		for i := range random.IntN(40) {
			defined = append(defined, fmt.Sprintf("Test%d", i))
		}
		durations := map[string]float64{}
		var listed []string
		for _, name := range defined {
			if random.IntN(5) > 0 {
				listed = append(listed, name)
			}
			switch random.IntN(3) {
			case 0:
				durations[name] = math.Round(random.ExpFloat64()*300) / 100
			case 1:
				durations[name] = sizeCutoff + float64(random.IntN(3))
			}
		}
		fast, large := fastSelection(listed, durations)
		if len(fast)+len(large) != len(listed) {
			t.Fatalf("seed %d trial %d: %d fast and %d large of %d listed", seed, trial, len(fast), len(large), len(listed))
		}
		n := 1 + random.IntN(8)
		if len(fast) > 0 {
			n = min(n, len(fast))
		}
		selections := fastShardArgs(shard(fast, durations, n), large)
		for _, name := range defined {
			runs := 0
			for _, selection := range selections {
				if runsIn(selection, name) {
					runs++
				}
			}
			want := 1
			if slices.Contains(large, name) {
				want = 0
			}
			if runs != want {
				t.Fatalf("seed %d trial %d: %s (%.2fs recorded: %t, listed: %t) runs in %d fast shards, want %d: %q", seed, trial, name, durations[name], hasDuration(durations, name), slices.Contains(listed, name), runs, want, selections)
			}
		}
	}
}

func hasDuration(durations map[string]float64, name string) bool {
	_, ok := durations[name]
	return ok
}

// fastPackageSteps groups the fast tier's package steps by package.
func fastPackageSteps(t *testing.T, root string) map[string][]*Step {
	t.Helper()
	steps, err := fastSteps(root)
	if err != nil {
		t.Fatal(err)
	}
	byPackage := map[string][]*Step{}
	for _, step := range steps {
		if step.Package != nil && step.Sweep == "" {
			byPackage[step.Package.Dir] = append(byPackage[step.Package.Dir], step)
		}
	}
	return byPackage
}

// Every fast-tier step that runs a package's go test is keyed by its
// package and runs -short, the hermetic packages with CGO_ENABLED=0 as
// their full-tier step builds them; the nested modules' steps are the full
// tier's own; no full-tier test step is left, the hermetic one included
// (it relinks its test binaries on every run to find Go's cached result),
// and neither is the build of every test binary, nor a need on it, nor a
// closure sweep. Each package is keyed on its closure table entry, and no
// host package has a step (classHeavy).
func TestFastStepsKeyEveryTestPackage(t *testing.T) {
	root := checkoutRoot(t)
	steps, err := fastSteps(root)
	if err != nil {
		t.Fatal(err)
	}
	source, err := os.ReadFile(filepath.Join(root, replay.DeclaredFile))
	if err != nil {
		t.Fatal(err)
	}
	table, err := replay.ParseDeclared(source)
	if err != nil {
		t.Fatal(err)
	}
	keyed := 0
	for _, step := range steps {
		if slices.Contains(step.Needs, fullBuildStep) {
			t.Errorf("%s needs %q, which the fast tier does not run", step.Name, fullBuildStep)
		}
		switch {
		case step.Package != nil:
			keyed++
			if !runsGoTest(step) || !slices.Contains(step.Argv, fastFlag) {
				t.Errorf("%s: a package step that does not run go test -short: %q", step.Name, step.Argv)
			}
			if slices.Contains(hostTestPackages, step.Package.Dir) {
				t.Errorf("%s: a host package's tests are heavy (classHeavy); the fast tier runs none", step.Name)
			}
			if !slices.Equal(step.Package.Declared, table[step.Package.Dir]) {
				t.Errorf("%s: declared %v; want its closure table entry %v", step.Name, step.Package.Declared, table[step.Package.Dir])
			}
			hermetic := strings.HasPrefix(step.Package.Dir, hermeticTestRoot)
			if hermetic != slices.Equal(step.Env, hermeticEnvironment) {
				t.Errorf("%s: environment %q; a hermetic package's step builds with %q, no other step does", step.Name, step.Env, hermeticEnvironment)
			}
		case step.Hermetic, step.Name == fullBuildStep:
			t.Errorf("%s: a full-tier test step is in the fast tier", step.Name)
		case !runsGoTest(step):
		default:
			t.Errorf("%s: a go test step with no package key in the fast tier: %q", step.Name, step.Argv)
		}
	}
	if keyed < 30 {
		t.Errorf("%d keyed package steps; want one per test package", keyed)
	}
}

// The variables systemd sets per invocation (a service's or a scope's
// INVOCATION_ID, JOURNAL_STREAM, SYSTEMD_EXEC_PID, MANAGERPID) differ on
// every run: a step never receives them and no key holds them, or no
// package key would ever match a run before it.
func TestPerInvocationVariablesAreNotKeyed(t *testing.T) {
	steps, declared := stepEnvironment(), declaredEnvironment()
	names := []string{"INVOCATION_ID", "JOURNAL_STREAM", "SYSTEMD_EXEC_PID", "MANAGERPID", "NOTIFY_SOCKET", "LISTEN_PID"}
	for _, name := range names {
		t.Setenv(name, "per-invocation-"+name)
	}
	// The messages name variables, never values: the environment can
	// hold credentials.
	if got := stepEnvironment(); !slices.Equal(got, steps) {
		var passed []string
		for _, entry := range got {
			if name, _, _ := strings.Cut(entry, "="); slices.Contains(names, name) {
				passed = append(passed, name)
			}
		}
		t.Errorf("the step environment changed with the per-invocation variables; it passes %q", passed)
	}
	if got := declaredEnvironment(); !slices.Equal(got, declared) {
		t.Errorf("the keyed environment changed with the per-invocation variables")
	}
}

// The closure table parses (replay.ParseDeclared): a form the parser does
// not know would key every package on the whole checkout.
func TestTheClosureTableParses(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("../..", replay.DeclaredFile))
	if err != nil {
		t.Fatal(err)
	}
	table, err := replay.ParseDeclared(source)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(table["internal/residue"], replay.Allowance{Path: ".", Grant: replay.GrantSubtree}) {
		t.Errorf("internal/residue: %v; want the whole checkout", table["internal/residue"])
	}
	if !slices.Contains(table["internal/sketch/lower"], replay.Allowance{Path: "cmd/ergo", Grant: replay.GrantProgram}) {
		t.Errorf("internal/sketch/lower: %v; want the cmd/ergo program", table["internal/sketch/lower"])
	}
}

// What a one-line edit reaches in the fast tier (gate-fast acceptance legs
// 2 and 3, the deterministic part): a docs edit only the packages that read
// every file; an edit of the checker's source the eleven packages that build
// on it and those two. The fast set of the checker edit is measured
// against a pinned CPU budget: the recorded CPU of the reached packages'
// fast steps (fastCPUFile), which bounds the wall from below at a CPU
// budget, so a growing suite fails here before the wall time grows.
func TestFastTierReachOfAnEdit(t *testing.T) {
	root := checkoutRoot(t)
	packages, problem, err := testPackages(root)
	if err != nil || problem != "" {
		t.Fatalf("%v %s", err, problem)
	}
	fastCPU, err := readFastCPU(root)
	if err != nil {
		t.Fatal(err)
	}
	// What a reached package costs in the fast tier: the recorded CPU of
	// its fast steps. A package with no row cannot be budgeted. A host
	// package has no fast step (classHeavy), so no edit reaches it here.
	reach := func(file string) (dirs []string, cpu float64) {
		for _, p := range packages {
			if !p.closure.Covers(file) || slices.Contains(hostTestPackages, p.dir) {
				continue
			}
			dirs = append(dirs, p.dir)
			recorded, ok := fastCPU[p.importPath]
			if !ok {
				t.Errorf("%s has no row in %s: run go run ./scripts/gate -record-durations -fast ./%s", p.dir, fastCPUFile, p.dir)
			}
			cpu += recorded
		}
		return dirs, cpu
	}
	docs, _ := reach("docs/tooling.md")
	if want := []string{"internal/residue", "internal/testinputs/guard"}; !slices.Equal(docs, want) {
		t.Errorf("a docs edit reaches %v; want %v", docs, want)
	}
	check, cpu := reach("internal/sketch/check/check.go")
	// scripts/examples builds on the checker since d3-lawcheck-model
	// (batch 9): its examples runner imports the checker. internal/gobind
	// (IG1-IG3), internal/sketch/machinegraph (batch 13) and the check-time
	// evaluator internal/sketch/eval (L7 EV1) import it too.
	want := []string{"cmd/ergo", "internal/bindc", "internal/gobind", "internal/migrate196", "internal/residue", "internal/sketch/check", "internal/sketch/eval", "internal/sketch/lower", "internal/sketch/machinegraph", "internal/sketch/std", "internal/sketch/syntax", "internal/sketchfront", "internal/testinputs/guard", "scripts/examples"}
	if !slices.Equal(check, want) {
		t.Errorf("a checker edit reaches %v; want %v", check, want)
	}
	t.Logf("the checker edit's fast set: %.1f CPU-s recorded, target %.0f, gap %.1f", cpu, fastCPUTarget, cpu-fastCPUTarget)
	if fastCPUKnownGap != 0 {
		t.Errorf("fastCPUKnownGap is %.0f: fast-cpu (batch 9) closed the gap, and a new excess is a defect to fix, not a gap to allow", fastCPUKnownGap)
	}
	if cpu > fastCPUTarget+fastCPUKnownGap {
		t.Errorf("the checker edit's fast set is %.1f CPU-s; the target is %.0f and the known gap %.0f (gate-fast plan 3.4)", cpu, fastCPUTarget, fastCPUKnownGap)
	}
}

// fastCPUTarget is the CPU the checker edit's fast set must stay under to
// finish in 60 s at a CPU budget of 4 (gate-fast plan 3.4). fastCPUKnownGap
// was the measured excess at gate-tier's head (2026-11-06: 205.8 CPU-s,
// gap 6), which fast-cpu (batch 9) removed: the lower shards run the
// gate's one bin/ergo (cliTestPackages) instead of building cmd/ergo per
// shard, TestContextReachProperty builds a fixed quarter of its programs
// under -short, and the lower typed scans share one source importer. The
// gap stays 0: the test fails when it is not.
const (
	fastCPUTarget   = 200.0
	fastCPUKnownGap = 0.0
)

// Over the module's tests as go test lists them: each fast-tier package's
// shards run every test that is not large exactly once and no large one,
// so the fast selection and the full-only set are every test (gate-fast
// acceptance leg 4). It builds every test binary, so the full tier runs
// it.
func TestFastTierCoversEveryListedTest(t *testing.T) {
	root := checkoutRoot(t)
	durations, err := readDurations(root)
	if err != nil {
		t.Fatal(err)
	}
	module := modulePath(root)
	for dir, steps := range fastPackageSteps(t, root) {
		command := exec.Command("go", "test", "-list", ".", "./"+dir)
		command.Dir = root
		output, err := command.Output()
		if err != nil {
			t.Fatalf("%s: go test -list: %v", dir, err)
		}
		listed := 0
		for _, name := range strings.Fields(string(output)) {
			if !strings.HasPrefix(name, "Test") && !strings.HasPrefix(name, "Fuzz") && !strings.HasPrefix(name, "Example") {
				continue
			}
			listed++
			runs := 0
			for _, step := range steps {
				if runsIn(testSelection(step.Argv), name) {
					runs++
				}
			}
			want := 1
			if d, ok := durations[module+"/"+dir][name]; ok && d >= sizeCutoff {
				want = 0
			}
			if runs != want {
				t.Errorf("%s: %s runs in %d fast steps, want %d", dir, name, runs, want)
			}
		}
		if listed == 0 {
			// A package whose every test file is build-constrained out
			// here (internal/testport off Linux) lists none, as it must.
			if defined := platformTestNames(t, filepath.Join(root, filepath.FromSlash(dir))); len(defined) > 0 {
				t.Errorf("%s: go test -list lists no test; its test files for %s/%s define %v", dir, runtime.GOOS, runtime.GOARCH, defined)
			}
		}
	}
}

// platformTestNames are the test functions (testFunction, TestMain aside)
// of the package's test files that build for this platform (go/build's
// default context: GOOS, GOARCH, build tags and file name suffixes).
func platformTestNames(t *testing.T, dir string) []string {
	t.Helper()
	p, err := build.ImportDir(dir, 0)
	if err != nil {
		t.Fatalf("%s: %v", dir, err)
	}
	var names []string
	for _, file := range append(slices.Clone(p.TestGoFiles), p.XTestGoFiles...) {
		content, err := os.ReadFile(filepath.Join(dir, file))
		if err != nil {
			t.Fatal(err)
		}
		for _, match := range testFunction.FindAllSubmatch(content, -1) {
			if name := string(match[1]); name != "TestMain" {
				names = append(names, name)
			}
		}
	}
	return names
}
