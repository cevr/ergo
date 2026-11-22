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
// package and runs -short with no environment of its own; the nested
// modules' steps are the full tier's own; no full-tier test step is left,
// and neither is the build of every test binary, nor a need on it, nor a
// closure sweep. Each package is keyed on its closure table entry, and no
// host package has a step (classHeavy), nor a package whose every test
// builds Go (fullTierOnly).
func TestFastStepsKeyEveryTestPackage(t *testing.T) {
	t.Run("fixture", fullTierPackageHasNoFastStep)
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
			if only, err := fullTierOnly(filepath.Join(root, filepath.FromSlash(step.Package.Dir))); err != nil || only {
				t.Errorf("%s: every test of the package builds Go (fullTierOnly %v, %v); the fast tier runs none", step.Name, only, err)
			}
			if !slices.Equal(step.Package.Declared, table[step.Package.Dir]) {
				t.Errorf("%s: declared %v; want its closure table entry %v", step.Name, step.Package.Declared, table[step.Package.Dir])
			}
			if len(step.Env) > 0 {
				t.Errorf("%s: environment %q; a package step sets none", step.Name, step.Env)
			}
		case step.Name == fullBuildStep:
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
// every file; an edit of the checker's source the packages that build
// on it and those two. The checker edit's fast set is counted, not timed
// (AGENTS.md Tests: a growth guard counts work): the Test functions the
// reached packages' fast steps run (fastTestCount), held under
// fastReachTestLimit, so a growing suite fails here before the wall grows.
func TestFastTierReachOfAnEdit(t *testing.T) {
	root := checkoutRoot(t)
	packages, problem, err := testPackages(root)
	if err != nil || problem != "" {
		t.Fatalf("%v %s", err, problem)
	}
	durations, err := readDurations(root)
	if err != nil {
		t.Fatal(err)
	}
	stepped := map[string]bool{}
	for dir := range fastPackageSteps(t, root) {
		stepped[dir] = true
	}
	// What a reached package costs in the fast tier: the Test functions
	// its fast steps run. A host package has no fast step (classHeavy),
	// and neither has a package whose every test builds Go (fullTierOnly:
	// internal/selfhostcheck, internal/experimental/irspike) or is large
	// (fastSteps), so no edit reaches them here.
	reach := func(file string) (dirs []string, tests int) {
		for _, p := range packages {
			if !p.closure.Covers(file) || !stepped[p.dir] {
				continue
			}
			if only, err := fullTierOnly(filepath.Join(root, filepath.FromSlash(p.dir))); err != nil {
				t.Fatal(err)
			} else if only {
				continue
			}
			dirs = append(dirs, p.dir)
			n, err := fastTestCount(root, p, durations[p.importPath])
			if err != nil {
				t.Fatal(err)
			}
			tests += n
		}
		return dirs, tests
	}
	// Rules, not lists of packages: a pinned list went red with every new
	// package (DEAD-SCRIPTS found it red on main d090c6ac1). A docs edit
	// reaches exactly the stepped packages granted the whole checkout; a
	// checker edit reaches those and the packages whose test binaries
	// (or the programs they build) compile the checker or walk its
	// directory, and no other.
	grantsAll := func(p testPackage) bool {
		return slices.Contains(p.closure.Declared, replay.Allowance{Path: ".", Grant: replay.GrantSubtree})
	}
	var wantDocs, wantCheck []string
	for _, p := range packages {
		if !stepped[p.dir] {
			continue
		}
		if only, err := fullTierOnly(filepath.Join(root, filepath.FromSlash(p.dir))); err != nil {
			t.Fatal(err)
		} else if only {
			continue
		}
		if grantsAll(p) {
			wantDocs = append(wantDocs, p.dir)
		}
		compilesCheck := slices.Contains(p.closure.Deps, "internal/sketch/check")
		for _, program := range p.closure.Programs {
			compilesCheck = compilesCheck || slices.Contains(program.Deps, "internal/sketch/check")
		}
		// A subtree grant over the checker's directory reads its sources
		// too (scripts/bootstrap walks each compiler package, GS29).
		walksCheck := slices.ContainsFunc(p.closure.Declared, func(a replay.Allowance) bool {
			return a.Grant == replay.GrantSubtree && strings.HasPrefix("internal/sketch/check/", a.Path+"/")
		})
		if grantsAll(p) || compilesCheck || walksCheck {
			wantCheck = append(wantCheck, p.dir)
		}
	}
	if docs, _ := reach("docs/tooling.md"); !slices.Equal(docs, wantDocs) || !slices.Contains(docs, "internal/residue") {
		t.Errorf("a docs edit reaches %v; want the packages granted the checkout %v, internal/residue among them", docs, wantDocs)
	}
	// The steps that are no package's (fastCPUSteps). The examples step
	// reruns after an edit of its own inputs (the runner's closure,
	// examples/) or of the bin/ergo it runs, which builds cmd/ergo's
	// closure; so an edit that cmd/ergo's tests reach, reaches it.
	examples, err := examplesStep(root)
	if err != nil {
		t.Fatal(err)
	}
	cli := slices.IndexFunc(packages, func(p testPackage) bool { return p.dir == "cmd/ergo" })
	if cli < 0 {
		t.Fatal("no cmd/ergo test package")
	}
	if !slices.Equal(fastCPUSteps, []string{examples.Name}) {
		t.Fatalf("fastCPUSteps is %q: say here what reaches each step", fastCPUSteps)
	}
	// A file under a directory the step walks itself (Installed: examples/,
	// and the modules of modules/, MK0) reaches it too.
	installed := func(file string) bool {
		return slices.ContainsFunc(examples.Inputs.Installed, func(dir string) bool { return strings.HasPrefix(file, dir+"/") })
	}
	reachSteps := func(file string) (names []string) {
		if selected(file, examples.Inputs) || installed(file) || packages[cli].closure.Covers(file) {
			names = append(names, examples.Name)
		}
		return names
	}
	if steps := reachSteps("docs/tooling.md"); len(steps) != 0 {
		t.Errorf("a docs edit reaches the steps %q; want none", steps)
	}
	for _, file := range []string{"examples/pipeline/run/run.ergo", "modules/domain/log/log.ergo"} {
		if steps := reachSteps(file); !slices.Equal(steps, []string{examples.Name}) {
			t.Errorf("an edit of %s reaches the steps %q; want %q", file, steps, examples.Name)
		}
	}
	check, tests := reach("internal/sketch/check/check.go")
	if checkSteps := reachSteps("internal/sketch/check/check.go"); !slices.Equal(checkSteps, []string{examples.Name}) {
		t.Errorf("a checker edit reaches the steps %q; want %q", checkSteps, examples.Name)
	}
	// cmd/ergo, the lowering and the examples runner build on the checker;
	// a checker edit that no longer reaches them has lost its closure.
	for _, dir := range []string{"cmd/ergo", "internal/sketch/check", "internal/sketch/lower", "scripts/examples"} {
		if !slices.Contains(wantCheck, dir) {
			t.Errorf("%s does not compile the checker by its closure (Deps): the reach rule lost it", dir)
		}
	}
	if !slices.Equal(check, wantCheck) {
		t.Errorf("a checker edit reaches %v; want the packages that compile the checker or are granted the checkout %v", check, wantCheck)
	}
	t.Logf("the checker edit's fast set: %d Test functions, limit %d", tests, fastReachTestLimit)
	if tests > fastReachTestLimit {
		t.Errorf("the checker edit's fast set runs %d Test functions, over fastReachTestLimit (%d): a new test that every checker edit reaches is a cost to cut (fullTierOnly, the size rule, a narrower closure), not a limit to raise", tests, fastReachTestLimit)
	}
}

// fastTestCount is the number of Test functions the fast tier runs of p
// (rows, its recorded times): every one of a guard package (wholeOf), else
// those the size rule leaves in the fast tier (fastSelection).
func fastTestCount(root string, p testPackage, rows map[string]float64) (int, error) {
	names, err := testNames(filepath.Join(root, filepath.FromSlash(p.dir)))
	if err != nil {
		return 0, err
	}
	if whole, err := wholeOf(root, p); err != nil {
		return 0, err
	} else if whole != "" {
		return len(names), nil
	}
	fast, _ := fastSelection(names, rows)
	return len(fast), nil
}

// fastReachTestLimit bounds the Test functions a checker edit's fast set
// runs (TestFastTierReachOfAnEdit). It replaced a CPU-seconds target
// (fastCPUTarget 200, with a known gap of 153 that only came down) in
// GATE-REACH, 2026-11-22: a timed bound is no guard (AGENTS.md Tests). Its
// value is the count at that head, 2063 at ddd817a1c, with a tenth of headroom.
const fastReachTestLimit = 2270

// Over the module's tests as go test lists them: each fast-tier package's
// shards run every test that is not large exactly once and no large one,
// so the fast selection and the full-only set are every test (gate-fast
// acceptance leg 4); a package that runs whole (whole.go, its step's note
// "whole: ...") runs every test once, the large ones too. It builds every
// test binary, so the full tier runs it.
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
		whole := slices.ContainsFunc(steps, func(s *Step) bool { return strings.HasPrefix(s.Note, wholeNote) })
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
			if d, ok := durations[module+"/"+dir][name]; ok && d >= sizeCutoff && !whole {
				want = 0
			}
			if runs != want {
				t.Errorf("%s: %s runs in %d fast steps, want %d (whole %t)", dir, name, runs, want, whole)
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

// fullTierPackageHasNoFastStep: planOf gives no fast step to a package
// whose every test starts with needsBuild, even with no recorded time
// (which makes every test fast), and gives it one in the round tier; a
// package with a test that builds nothing gets its step. The checkout
// alone cannot hold this: its one such package, internal/selfhostcheck,
// is also all-large by its recorded times, so the fast tier dropped it
// either way and b18-full-tier-package-stepped survived (batch 23).
func fullTierPackageHasNoFastStep(t *testing.T) {
	root := t.TempDir()
	for dir, body := range map[string]string{
		"builds": "func TestA(t *testing.T) {\n\tneedsBuild(t)\n}\n",
		"plain":  "func TestB(t *testing.T) {}\n",
	} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, dir, "x_test.go"), []byte("package x\n\nimport \"testing\"\n\n"+body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range []struct {
		dir   string
		round bool
		step  bool
	}{{"builds", false, false}, {"builds", true, true}, {"plain", false, true}} {
		pl, err := planOf(root, testPackage{importPath: "m/" + c.dir, dir: c.dir}, c.dir, nil, c.round)
		if err != nil {
			t.Fatal(err)
		}
		if (pl != nil) != c.step {
			t.Errorf("%s (round %v): a step %v, want %v", c.dir, c.round, pl != nil, c.step)
		}
	}
}
