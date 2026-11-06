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
// declared steps. Under -short (the fast tier) it skips: the fast tier keys
// this package on its compiled closure and hostPackageInputs, and its input
// observer (Step.ObserveInputs) fails the step on any read outside them;
// keyed on every Go file of the module, the package would rerun (about 250 s)
// on every edit. These tests run in the full tier, uncached, on every batch
// head.
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
		if step.Package != nil {
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
// and neither is the build of every test binary, nor a need on it. A host
// package (hostPackageInputs) is keyed on its declared list and runs under
// the input observer only where the observer exists (observable, Linux);
// elsewhere it is keyed on its closure table entry and is not observed.
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
			declared, host := hostPackageInputs[step.Package.Dir]
			observed := host && observable
			if !observed {
				declared = table[step.Package.Dir]
			}
			if step.ObserveInputs != observed || !slices.Equal(step.Package.Declared, declared) {
				t.Errorf("%s: observed %t, declared %v; on %s (observer: %t) want observed %t, declared %v: a package keyed on its declared inputs (hostPackageInputs) runs under the input observer, no other does, and without the observer a host package keeps its closure table entry", step.Name, step.ObserveInputs, step.Package.Declared, runtime.GOOS, observable, observed, declared)
			}
			hermetic := strings.HasPrefix(step.Package.Dir, hermeticTestRoot) && !slices.Contains(hostTestPackages, step.Package.Dir)
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

// A step that runs under the input observer (Step.ObserveInputs) gets the
// test log flag and passes only when the log is a complete observation
// whose checkout reads its package key covers: a read outside the key, an
// observation the observer could not complete, and no log at all fail the
// step, so a declared list (hostPackageInputs) that misses an input goes red
// instead of making a stale pass. The step here stands in for go test: it
// writes the log the observer would.
func TestObservedStepFailsOnAReadOutsideItsKey(t *testing.T) {
	// $0 is the checkout root; the gate appends -args -test.testlogfile=<log>.
	writer := func(log string) string {
		return `for a; do case $a in -test.testlogfile=*) log=${a#*=};; esac; done; [ -n "$log" ] || exit 9; ` + log
	}
	cases := []struct {
		name, script string
		want         []string
	}{
		{"covered reads", writer(`printf '# test log\nopen %s/src/input.txt\nstat %s/src/missing\ngetenv HOME\nopen ../go.mod\nopen /etc/hostname\n' "$0" "$0" > "$log"`),
			[]string{"== observed: ok"}},
		{"a read outside the key", writer(`printf '# test log\nopen %s/src/input.txt\nopen %s/docs/notes.md\nstat ../docs/other.md\n' "$0" "$0" > "$log"`),
			[]string{"== observed: FAILED", "its tests read 2 checkout paths its package key does not cover", "  docs/notes.md\n", "  docs/other.md\n"}},
		{"an incomplete observation", writer(`printf '# test log\nopen %s/src/input.txt\ntestinputs: unobserved: clone' "$0" > "$log"`),
			[]string{"== observed: FAILED", "the input observer could not observe the tests, so its declared key cannot be checked"}},
		{"no log", writer(`:`),
			[]string{"== observed: FAILED", "the tests wrote no input log"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root := fixture(t)
			step := &Step{Name: "observed", Argv: []string{"sh", "-c", c.script, root}, ObserveInputs: true,
				Package: &replay.Closure{Dir: "src", Deps: []string{"src"}}}
			_, out := runGate(t, root, t.TempDir(), []*Step{step})
			for _, want := range c.want {
				if !strings.Contains(out, want) {
					t.Errorf("output lacks %q:\n%s", want, out)
				}
			}
		})
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
// every file; an edit of the checker's source the eight packages that build
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
	// its fast steps. A package with no row cannot be budgeted, where the
	// fast set has a budget (observable, below).
	reach := func(file string) (dirs []string, cpu float64) {
		for _, p := range packages {
			if !p.closure.Covers(file) {
				continue
			}
			dirs = append(dirs, p.dir)
			recorded, ok := fastCPU[p.importPath]
			if !ok && observable {
				t.Errorf("%s has no row in %s: run go run ./scripts/gate -record-durations -fast ./%s", p.dir, fastCPUFile, p.dir)
			}
			cpu += recorded
		}
		return dirs, cpu
	}
	// Without the input observer (observable) the host package keeps its
	// closure table entry, the whole checkout (TestFastStepsKeyEveryTestPackage):
	// every edit reaches it, and it runs on every fast gate there. The CPU
	// budget is the lane host's (Linux), where the observer keys it on its
	// declared inputs; off Linux the fast set is not budgeted.
	var everyEdit []string
	if !observable {
		everyEdit = []string{"scripts/gate"}
	}
	docs, _ := reach("docs/tooling.md")
	if want := append([]string{"internal/residue", "internal/testinputs/guard"}, everyEdit...); !slices.Equal(docs, want) {
		t.Errorf("a docs edit reaches %v; want %v (%s, observer: %t)", docs, want, runtime.GOOS, observable)
	}
	check, cpu := reach("internal/sketch/check/check.go")
	want := append([]string{"cmd/ergo", "internal/bindc", "internal/migrate196", "internal/residue", "internal/sketch/check", "internal/sketch/lower", "internal/sketch/std", "internal/sketch/syntax", "internal/sketchfront", "internal/testinputs/guard"}, everyEdit...)
	if !slices.Equal(check, want) {
		t.Errorf("a checker edit reaches %v; want %v (%s, observer: %t)", check, want, runtime.GOOS, observable)
	}
	if !observable {
		t.Logf("%s has no input observer: the host package scripts/gate runs on every edit, and the fast set has no CPU budget here", runtime.GOOS)
		return
	}
	t.Logf("the checker edit's fast set: %.1f CPU-s recorded, target %.0f, gap %.1f", cpu, fastCPUTarget, cpu-fastCPUTarget)
	if cpu > fastCPUTarget+fastCPUKnownGap {
		t.Errorf("the checker edit's fast set is %.1f CPU-s; the target is %.0f and the known gap %.0f (gate-fast plan 3.4)", cpu, fastCPUTarget, fastCPUKnownGap)
	}
}

// fastCPUTarget is the CPU the checker edit's fast set must stay under to
// finish in 60 s at a CPU budget of 4 (gate-fast plan 3.4). The leg is red
// against it: fastCPUKnownGap is the measured excess at gate-tier's head
// (2026-11-06), which the follow-up unit fast-cpu must remove; it is not a
// looser target. The test fails when the set grows past target plus gap,
// so the gap can only shrink. fast-cpu sets the gap to 0 when it is done.
// Measured: 205.8 CPU-s recorded with a warm build cache
// (record-fastcpu-2.log); the gate's own runs after a checker edit
// accounted 213 to 223 CPU-s to these steps and 235 to 242 to the whole
// run, and took 71.6 s at the fastest (load ≤ 40), not 60.
const (
	fastCPUTarget   = 200.0
	fastCPUKnownGap = 6.0
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
