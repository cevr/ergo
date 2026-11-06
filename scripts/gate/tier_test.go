package main

import (
	"fmt"
	"math"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"ergo.local/prototype/scripts/gate/replay"
)

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
// (it relinks its test binaries on every run to find Go's cached result).
func TestFastStepsKeyEveryTestPackage(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	steps, err := fastSteps(root)
	if err != nil {
		t.Fatal(err)
	}
	keyed := 0
	for _, step := range steps {
		switch {
		case step.Package != nil:
			keyed++
			if !runsGoTest(step) || !slices.Contains(step.Argv, fastFlag) {
				t.Errorf("%s: a package step that does not run go test -short: %q", step.Name, step.Argv)
			}
			if step.Package.Dir == "scripts/gate" && (step.Declarations == "" || !slices.Equal(step.Package.Declared, hostPackageInputs["scripts/gate"])) {
				t.Errorf("%s: the host package is not keyed on its declared inputs and the declarations", step.Name)
			}
			hermetic := strings.HasPrefix(step.Package.Dir, hermeticTestRoot) && !slices.Contains(hostTestPackages, step.Package.Dir)
			if hermetic != slices.Equal(step.Env, hermeticEnvironment) {
				t.Errorf("%s: environment %q; a hermetic package's step builds with %q, no other step does", step.Name, step.Env, hermeticEnvironment)
			}
		case step.Hermetic:
			t.Errorf("%s: the full tier's hermetic step is in the fast tier", step.Name)
		case step.Name == "go test build", !runsGoTest(step):
		default:
			t.Errorf("%s: a go test step with no package key in the fast tier: %q", step.Name, step.Argv)
		}
	}
	if keyed < 30 {
		t.Errorf("%d keyed package steps; want one per test package", keyed)
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
// on it and those two. The fast set of the checker edit stays under a
// pinned CPU-second budget, the recorded serial seconds of its selected
// tests under -short (shortDurationsFile; a test with no -short row counts
// its full row, and an unrecorded test the size cutoff), so a growing suite
// fails here before the wall time misses 60 s.
func TestFastTierReachOfAnEdit(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	packages, problem, err := testPackages(root)
	if err != nil || problem != "" {
		t.Fatalf("%v %s", err, problem)
	}
	durations, err := readDurations(root)
	if err != nil {
		t.Fatal(err)
	}
	short, err := readDurationsFile(root, shortDurationsFile)
	if err != nil {
		t.Fatal(err)
	}
	// What a selected test costs in the fast tier: its time under -short,
	// else its full time (an upper bound), else the size cutoff.
	cost := func(pkg, name string) float64 {
		if d, ok := short[pkg][name]; ok {
			return d
		}
		if d, ok := durations[pkg][name]; ok {
			return d
		}
		return sizeCutoff
	}
	reach := func(file string) (dirs []string, cpu float64) {
		for _, p := range packages {
			if !p.closure.Covers(file) {
				continue
			}
			dirs = append(dirs, p.dir)
			names, err := testNames(filepath.Join(root, p.dir))
			if err != nil {
				t.Fatal(err)
			}
			fast, _ := fastSelection(names, durations[p.importPath])
			for _, name := range fast {
				cpu += cost(p.importPath, name)
			}
		}
		return dirs, cpu
	}
	docs, _ := reach("docs/tooling.md")
	if want := []string{"internal/residue", "internal/testinputs/guard"}; !slices.Equal(docs, want) {
		t.Errorf("a docs edit reaches %v; want %v", docs, want)
	}
	check, cpu := reach("internal/sketch/check/check.go")
	want := []string{"cmd/ergo", "internal/bindc", "internal/migrate196", "internal/residue", "internal/sketch/check", "internal/sketch/lower", "internal/sketch/std", "internal/sketch/syntax", "internal/sketchfront", "internal/testinputs/guard"}
	if !slices.Equal(check, want) {
		t.Errorf("a checker edit reaches %v; want %v", check, want)
	}
	const budget = 200.0
	t.Logf("the checker edit's fast set: %.1f CPU-s of recorded serial time", cpu)
	if cpu > budget {
		t.Errorf("the checker edit's fast set is %.1f CPU-s; the budget is %.0f (gate-fast plan 3.4)", cpu, budget)
	}
}

// Over the module's tests as go test lists them: each fast-tier package's
// shards run every test that is not large exactly once and no large one,
// so the fast selection and the full-only set are every test (gate-fast
// acceptance leg 4). It builds every test binary, so the full tier runs
// it.
func TestFastTierCoversEveryListedTest(t *testing.T) {
	if testing.Short() {
		t.Skip("full tier: lists every test package's tests (go test -list)")
	}
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
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
			t.Errorf("%s: go test -list lists no test", dir)
		}
	}
}
