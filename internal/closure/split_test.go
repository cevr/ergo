package closure

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The sweep splits a package's test binary into runs by the recorded time
// of its tests, so that each run holds at most sweepRunBudget of it and the
// number of runs follows the package's total. Until batch 11 it split
// internal/sketch/lower into a fixed 4 runs that ran at once: each run then
// held a quarter of the package, so a run's time grew with every test the
// package gained, and under the batch-10 gate's load the four runs took
// 13m23s to 13m59s, at the deadline the gate's 15 minute step timeout left
// them (fu-sweep-deadline, measured in batch-10 try9).

// costFile records each test's serial seconds per package, by import path,
// as `go test -json` measured them (scripts/gate -record-durations). The
// gate shards its own test steps by the same file. A stale file costs only
// balance: a test it does not name weighs the median of the package's
// recorded tests, and the last run of a package skips the names the others
// run, so every test runs once whatever the file says.
const costFile = "scripts/gate/durations.json"

// sweepRunBudget is the recorded test time one run of the sweep holds at
// most: a run holds more only when it is one test that alone exceeds it.
// Swept, a test takes about four times its recorded time in CPU (the
// observer and, until gl-sweep-cache shared one GOCACHE across a package's
// runs, a cold GOCACHE per run: internal/sketch/lower recorded 368 s and
// its sweep took 1472 CPU-s in batch-10 try9; at 409 s recorded, 2014
// CPU-s with a cache per run and 1521 with the shared one, gl-sweep-cache,
// measured under load), so a run of 45 s takes
// about 3 CPU-minutes, and at the batch-10 gate's share (0.45 CPU per run)
// about 7 minutes; under the batch-16b merge tier's load two runs took
// over 19 minutes, so a run's deadline follows its recorded cost
// (runDeadline, at least 30 minutes).
const sweepRunBudget = 45.0

// readCosts reads costFile under root: each package's recorded seconds per
// top-level test, by package directory relative to the module root. No
// file is no table, and every package then runs whole.
func readCosts(root, module string) (map[string]map[string]float64, error) {
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(costFile)))
	if errors.Is(err, os.ErrNotExist) {
		return map[string]map[string]float64{}, nil
	}
	if err != nil {
		return nil, err
	}
	var byPath map[string]map[string]float64
	if err := json.Unmarshal(data, &byPath); err != nil {
		return nil, fmt.Errorf("%s: %w", costFile, err)
	}
	costs := map[string]map[string]float64{}
	for importPath, tests := range byPath {
		if dir, ok := strings.CutPrefix(importPath, module+"/"); ok {
			costs[dir] = tests
		}
	}
	return costs, nil
}

// testFunction finds the top-level tests of a test file.
var testFunction = regexp.MustCompile(`(?m)^func (Test[A-Za-z0-9_]*)\(`)

// scanTests returns the names of the top-level tests in the test files of
// pkgDir, sorted, TestMain left out.
func scanTests(pkgDir string) ([]string, error) {
	files, err := filepath.Glob(filepath.Join(pkgDir, "*_test.go"))
	if err != nil {
		return nil, err
	}
	var names []string
	for _, name := range files {
		content, err := os.ReadFile(name)
		if err != nil {
			return nil, err
		}
		for _, match := range testFunction.FindAllSubmatch(content, -1) {
			if test := string(match[1]); test != "TestMain" {
				names = append(names, test)
			}
		}
	}
	slices.Sort(names)
	return slices.Compact(names), nil
}

// sweepRun is one run of a package's test binary: the flags that select its
// tests, the tests it was dealt and their recorded time. The last run of a
// split package also runs every test the name scan missed.
type sweepRun struct {
	args       []string
	tests      []string
	cost       float64
	unmeasured []string // tests of the run with no recorded time
	weights    map[string]float64
	// deadlineCost is the run's recorded cost that sets its deadline
	// (runDeadline): withDeadlineCosts.
	deadlineCost float64
}

// cpuCostFile records each test's CPU seconds for the packages the gate
// shards (scripts/gate testCPUFile, `-record-durations -cpu`). Wall time
// (costFile) plans the runs; a run's deadline reads CPU where the package
// has rows, since a test that builds Go spends several CPUs per second of
// wall: the runs of internal/sketch/lower took far more than their
// recorded wall under load (gate16b-fix3).
const cpuCostFile = "scripts/gate/test-cpu.json"

// readCPUCosts reads cpuCostFile under root by package directory, as
// readCosts reads costFile; no file is no table.
func readCPUCosts(root, module string) (map[string]map[string]float64, error) {
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(cpuCostFile)))
	if errors.Is(err, os.ErrNotExist) {
		return map[string]map[string]float64{}, nil
	}
	if err != nil {
		return nil, err
	}
	var byPath map[string]map[string]float64
	if err := json.Unmarshal(data, &byPath); err != nil {
		return nil, fmt.Errorf("%s: %w", cpuCostFile, err)
	}
	costs := map[string]map[string]float64{}
	for importPath, tests := range byPath {
		if dir, ok := strings.CutPrefix(importPath, module+"/"); ok {
			costs[dir] = tests
		}
	}
	return costs, nil
}

// withDeadlineCosts sets each run's deadlineCost: the CPU seconds cpu
// records for its tests, or its recorded wall (cost) when that is more,
// as when the package has no CPU rows.
func withDeadlineCosts(plan []sweepRun, cpu map[string]float64) []sweepRun {
	for i := range plan {
		sum := 0.0
		for _, name := range plan[i].tests {
			sum += cpu[name]
		}
		plan[i].deadlineCost = max(sum, plan[i].cost)
	}
	return plan
}

// planRuns splits the tests names of one package into runs over costs, the
// package's recorded seconds per test, with at most budget each. The count
// of runs starts at the total over the budget, rounded up; the tests are
// dealt longest first, each to the run with the least time so far (LPT,
// fewest tests on a tie), and the count grows until every run holds at
// most the budget or a single test. A test with no recorded time weighs
// the median of those recorded. A package with no recorded test runs
// whole: nothing says how to split it.
func planRuns(names []string, costs map[string]float64, budget float64) []sweepRun {
	var known []float64
	for _, name := range names {
		if c, ok := costs[name]; ok {
			known = append(known, c)
		}
	}
	if len(known) == 0 || len(names) < 2 {
		return []sweepRun{{tests: slices.Clone(names), unmeasured: slices.Clone(names)}}
	}
	slices.Sort(known)
	median := known[len(known)/2]
	weights := map[string]float64{}
	total := 0.0
	for _, name := range names {
		w, ok := costs[name]
		if !ok {
			w = median
		}
		weights[name] = w
		total += w
	}
	n := min(len(names), max(1, int(math.Ceil(total/budget))))
	groups, loads := deal(names, weights, n)
	for n < len(names) && !fits(groups, loads, budget) {
		n++
		groups, loads = deal(names, weights, n)
	}
	runs := make([]sweepRun, n)
	var assigned []string
	for i, group := range groups {
		runs[i] = sweepRun{tests: group, cost: loads[i], weights: weights}
		for _, name := range group {
			if _, ok := costs[name]; !ok {
				runs[i].unmeasured = append(runs[i].unmeasured, name)
			}
		}
		switch {
		case n == 1:
		case i < n-1:
			runs[i].args = []string{"-test.run=" + exactNames(group)}
			assigned = append(assigned, group...)
		default:
			runs[i].args = []string{"-test.skip=" + exactNames(assigned)}
		}
	}
	return runs
}

// runsAlone are the tests, by package directory, that run their package's
// tests again in a child process, each with the reason. Its recorded time
// is a warm pass of the package, but swept with the package's empty
// GOCACHE (runPlan) and the observer on every child it is a cold pass
// again, so the sweep gives each its own run and plans the other tests of the package as usual (planPackage). A name that
// is not a test of its package fails the sweep (runPackage).
//
// Measured on 9c8f862dd at load 25-27, swept alone (gate16b-fix2):
// cmd/ergo in one run took 10m29s; with this test in a run of its own,
// 6m47s and 7m43s at once. Under the batch-16 merge tier's load the one
// run reached its 19m30s test timeout.
var runsAlone = map[string]map[string]string{
	"cmd/ergo": {"TestPackageNeverWritesUnderHome": "it runs every other test of the package again with an empty HOME"},
}

// sweepPlan is the sweep's plan of the package in dir, whose tests are
// names: planPackage with the package's runsAlone tests, at
// sweepRunBudget. It fails when runsAlone names a test that is not in
// names.
func sweepPlan(dir string, names []string, costs map[string]float64) ([]sweepRun, error) {
	for name := range runsAlone[dir] {
		if !slices.Contains(names, name) {
			return nil, fmt.Errorf("runsAlone names %s, which is not a test of %s", name, dir)
		}
	}
	return planPackage(names, costs, sweepRunBudget, runsAlone[dir]), nil
}

// planPackage is planRuns with each test of alone that names holds in a
// run of its own, ahead of the runs of the other tests; the last of those
// runs skips the tests run alone too. With none of alone in names it is
// planRuns.
func planPackage(names []string, costs map[string]float64, budget float64, alone map[string]string) []sweepRun {
	var solo, rest []string
	for _, name := range names {
		if _, ok := alone[name]; ok {
			solo = append(solo, name)
		} else {
			rest = append(rest, name)
		}
	}
	if len(solo) == 0 || len(rest) == 0 {
		return planRuns(names, costs, budget)
	}
	others := planRuns(rest, costs, budget)
	var selected []string
	for _, run := range others[:len(others)-1] {
		selected = append(selected, run.tests...)
	}
	last := &others[len(others)-1]
	last.args = []string{"-test.skip=" + exactNames(append(selected, solo...))}
	plan := make([]sweepRun, 0, len(solo)+len(others))
	for _, name := range solo {
		run := sweepRun{args: []string{"-test.run=" + exactNames([]string{name})}, tests: []string{name}, cost: costs[name], weights: map[string]float64{name: costs[name]}}
		if _, ok := costs[name]; !ok {
			run.unmeasured = []string{name}
		}
		plan = append(plan, run)
	}
	return append(plan, others...)
}

// deal deals names, longest first, each to the group with the least load
// so far, the one with fewer tests on a tie and the first on a tie of both,
// into n groups; each group is sorted.
func deal(names []string, weights map[string]float64, n int) ([][]string, []float64) {
	ordered := slices.Clone(names)
	slices.SortStableFunc(ordered, func(a, b string) int {
		if weights[a] != weights[b] {
			if weights[a] > weights[b] {
				return -1
			}
			return 1
		}
		return strings.Compare(a, b)
	})
	groups := make([][]string, n)
	loads := make([]float64, n)
	for _, name := range ordered {
		least := 0
		for i := 1; i < n; i++ {
			if loads[i] < loads[least] || loads[i] == loads[least] && len(groups[i]) < len(groups[least]) {
				least = i
			}
		}
		groups[least] = append(groups[least], name)
		loads[least] += weights[name]
	}
	for _, group := range groups {
		slices.Sort(group)
	}
	return groups, loads
}

// fits reports whether every group holds at most budget or a single test.
func fits(groups [][]string, loads []float64, budget float64) bool {
	for i, group := range groups {
		if loads[i] > budget && len(group) > 1 {
			return false
		}
	}
	return true
}

// exactNames is a pattern that matches exactly the names.
func exactNames(names []string) string {
	quoted := make([]string, len(names))
	for i, name := range names {
		quoted[i] = regexp.QuoteMeta(name)
	}
	return "^(" + strings.Join(quoted, "|") + ")$"
}

// heaviestFirst is the order in which the runs take their slots: the most
// recorded time first.
func heaviestFirst(plan []sweepRun) []int {
	order := make([]int, len(plan))
	for i := range order {
		order[i] = i
	}
	slices.SortStableFunc(order, func(a, b int) int {
		switch {
		case plan[a].cost > plan[b].cost:
			return -1
		case plan[a].cost < plan[b].cost:
			return 1
		}
		return 0
	})
	return order
}

// planCost is the recorded time of every run of the plan.
func planCost(plan []sweepRun) float64 {
	total := 0.0
	for _, run := range plan {
		total += run.cost
	}
	return total
}

// slowestShown is how many tests a deadline failure names.
const slowestShown = 5

// slowest names the run's tests with the most recorded time, at most
// slowestShown, each with its time, and says how many have none.
func (r sweepRun) slowest() string {
	tests := slices.Clone(r.tests)
	slices.SortStableFunc(tests, func(a, b string) int {
		switch {
		case r.weights[a] > r.weights[b]:
			return -1
		case r.weights[a] < r.weights[b]:
			return 1
		}
		return 0
	})
	var shown []string
	for _, name := range tests[:min(len(tests), slowestShown)] {
		if slices.Contains(r.unmeasured, name) {
			shown = append(shown, name+" (not recorded)")
			continue
		}
		shown = append(shown, name+" "+strconv.FormatFloat(r.weights[name], 'f', -1, 64)+"s")
	}
	text := strings.Join(shown, ", ")
	if len(r.unmeasured) > 0 {
		text += fmt.Sprintf("; %d of its %d tests have no recorded time", len(r.unmeasured), len(r.tests))
	}
	return text
}

// runFailure names the run i of n in its error: a run that passed its
// deadline also names its slowest tests by recorded time and its recorded
// total against the budget, so the failure says which tests to split or
// fix; any other failure of a split package names the run's selection.
func runFailure(i, n int, run sweepRun, err error) error {
	if err == nil {
		return nil
	}
	selection := "every test"
	if len(run.args) > 0 {
		selection = strings.Join(run.args, " ")
	}
	if timeout := (runTimeout{}); errors.As(err, &timeout) {
		return fmt.Errorf("run %d of %d: %d tests of %.0fs recorded time (budget %.0fs); its slowest by recorded time: %s; selection %s: %w", i+1, n, len(run.tests), run.cost, sweepRunBudget, run.slowest(), selection, err)
	}
	if n == 1 {
		return err
	}
	return fmt.Errorf("run %d of %d (%s): %w", i+1, n, selection, err)
}

// splitSeed is the seed of TestPlanRunsKeepsEveryRunUnderBudget, or a new
// one when it is unset; a failure prints the seed to replay it.
const splitSeed = "ERGO_CLOSURE_SPLIT_SEED"

// The sweep's plan, over generated cost tables (seeded, replayable): every
// test runs in exactly one run, a test the name scan missed in the last;
// each run holds at most the budget or one test; the count of runs is the
// least, from the total over the budget up, at which the dealing fits; a
// package with no recorded test runs whole; and planned with runsAlone
// tests (planPackage), each of them holds a run of its own while every test
// still runs once.
func TestPlanRunsKeepsEveryRunUnderBudget(t *testing.T) {
	seed := uint64(time.Now().UnixNano())
	if v := os.Getenv(splitSeed); v != "" {
		parsed, err := strconv.ParseUint(v, 10, 64)
		if err != nil {
			t.Fatalf("%s=%q: %v", splitSeed, v, err)
		}
		seed = parsed
	}
	t.Logf("seed %d (replay with %s=%d)", seed, splitSeed, seed)
	rng := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
	for iteration := range 400 {
		budget := 5 + rng.Float64()*115
		names := make([]string, 1+rng.IntN(400))
		costs := map[string]float64{}
		known := rng.Float64()
		for i := range names {
			names[i] = fmt.Sprintf("Test%03d", i)
			if rng.Float64() >= known {
				continue // no recorded time
			}
			switch r := rng.Float64(); {
			case r < 0.05:
				costs[names[i]] = 0
			case r < 0.10:
				costs[names[i]] = budget * (0.5 + 2.5*rng.Float64()) // alone near or over the budget
			case r < 0.30:
				costs[names[i]] = rng.Float64() * 10
			default:
				costs[names[i]] = math.Round(rng.ExpFloat64()*100) / 100
			}
		}
		plan := planRuns(names, costs, budget)
		fail := func(format string, args ...any) {
			t.Helper()
			t.Fatalf("seed %d, case %d (%d tests, %d recorded, budget %.1fs): %s", seed, iteration, len(names), len(costs), budget, fmt.Sprintf(format, args...))
		}
		checkRunsOnce(t, plan, names, fail)
		// A test of runsAlone holds a run of its own, and still every test
		// runs once (planPackage).
		// When every test is one of them, the package runs as planRuns
		// plans it.
		alone := map[string]string{"TestNotInThePackage": "a stale name changes nothing"}
		others := 0
		for _, name := range names {
			if rng.IntN(20) == 0 {
				alone[name] = "runs the package again"
			} else {
				others++
			}
		}
		apart := planPackage(names, costs, budget, alone)
		checkRunsOnce(t, apart, names, fail)
		for _, run := range apart {
			for _, name := range run.tests {
				if _, ok := alone[name]; ok && len(run.tests) != 1 && others > 0 {
					fail("%s of runsAlone shares a run with %v", name, run.tests)
				}
			}
		}
		// The sweep's own plan (sweepPlan) of each runsAlone package: each
		// of its tests is the whole test set of exactly one run and is in
		// no other run; a package missing one of them is refused.
		for dir, tests := range runsAlone {
			withAlone := slices.Clone(names)
			swept := map[string]float64{}
			for name, c := range costs {
				swept[name] = c
			}
			for name := range tests {
				withAlone = append(withAlone, name)
				if rng.IntN(2) == 0 {
					swept[name] = rng.Float64() * 3 * budget
				}
			}
			if _, err := sweepPlan(dir, names, swept); err == nil {
				fail("sweepPlan of %s without its runsAlone tests %v gives no error", dir, slices.Sorted(maps.Keys(tests)))
			}
			plan, err := sweepPlan(dir, withAlone, swept)
			if err != nil {
				fail("sweepPlan of %s: %v", dir, err)
			}
			checkRunsOnce(t, plan, withAlone, fail)
			for name := range tests {
				own, shared := 0, 0
				for _, run := range plan {
					switch {
					case len(run.tests) == 1 && run.tests[0] == name:
						own++
					case slices.Contains(run.tests, name):
						shared++
					}
				}
				if own != 1 || shared != 0 {
					fail("sweepPlan of %s: %s is alone in %d runs and shares %d; want 1 and 0", dir, name, own, shared)
				}
			}
		}
		if len(costs) == 0 || len(names) < 2 {
			if len(plan) != 1 || plan[0].args != nil {
				fail("an unmeasured package runs in %d runs, want one run whole", len(plan))
			}
			continue
		}
		weights := plan[0].weights
		total := 0.0
		for _, name := range names {
			total += weights[name]
			if c, ok := costs[name]; ok && weights[name] != c {
				fail("%s weighs %v, recorded %v", name, weights[name], c)
			}
		}
		var groups [][]string
		var loads []float64
		for _, run := range plan {
			sum := 0.0
			for _, name := range run.tests {
				sum += weights[name]
			}
			if math.Abs(sum-run.cost) > 1e-6 {
				fail("a run's cost %v is not the sum %v of its tests", run.cost, sum)
			}
			if run.cost > budget+1e-9 && len(run.tests) > 1 {
				fail("a run of %d tests holds %.2fs, over the budget", len(run.tests), run.cost)
			}
			groups, loads = append(groups, run.tests), append(loads, run.cost)
		}
		least := min(len(names), max(1, int(math.Ceil(total/budget))))
		if len(plan) < least {
			fail("%d runs for %.1fs of recorded time; want at least %d", len(plan), total, least)
		}
		if len(plan) > least {
			if g, l := deal(names, weights, len(plan)-1); fits(g, l, budget) {
				fail("%d runs where %d fit the budget", len(plan), len(plan)-1)
			}
		}
		// Dealt longest first, the tests fit in n runs once n is at
		// least twice the total over the budget plus the tests over half
		// the budget: those go first, each to a run of its own, and each
		// later test, at most half the budget, goes to the least loaded
		// run, which holds at most the average, at most half the budget.
		big := 0
		for _, name := range names {
			if weights[name] > budget/2 {
				big++
			}
		}
		if bound := max(least, int(math.Ceil(2*total/budget))+big); len(plan) > bound {
			fail("%d runs; dealt longest first the tests fit in %d", len(plan), bound)
		}
	}
}

// checkRunsOnce checks that the plan's flags run every name, and a name the
// scan missed, in exactly one run, as the test binary reads them.
func checkRunsOnce(t *testing.T, plan []sweepRun, names []string, fail func(string, ...any)) {
	t.Helper()
	dealt := map[string]int{}
	for _, run := range plan {
		for _, name := range run.tests {
			dealt[name]++
		}
	}
	type selection struct {
		all, run bool
		pattern  *regexp.Regexp
	}
	selections := make([]selection, len(plan))
	for i, run := range plan {
		switch {
		case len(run.args) == 0 && len(plan) != 1:
			fail("a run of a split package has no selection")
		case len(run.args) == 0:
			selections[i].all = true
		case len(run.args) != 1:
			fail("run flags %q; want one -test.run or -test.skip", run.args)
		default:
			flag, pattern, _ := strings.Cut(run.args[0], "=")
			selections[i] = selection{run: flag == "-test.run", pattern: regexp.MustCompile(pattern)}
		}
	}
	for _, name := range append(slices.Clone(names), "TestMissedByTheScan") {
		if name != "TestMissedByTheScan" && dealt[name] != 1 {
			fail("%s is dealt to %d runs", name, dealt[name])
		}
		runs := 0
		for _, s := range selections {
			if s.all || s.run == s.pattern.MatchString(name) {
				runs++
			}
		}
		if runs != 1 {
			fail("%s runs in %d runs, want 1", name, runs)
		}
	}
}

// A run stopped at its deadline names its slowest tests by recorded time,
// the run's recorded total and the budget, and keeps the deadline error.
func TestRunFailureNamesTheSlowestTests(t *testing.T) {
	names := []string{"TestA", "TestB", "TestC", "TestD", "TestE", "TestF", "TestG", "TestNew"}
	costs := map[string]float64{"TestA": 1, "TestB": 30, "TestC": 2, "TestD": 25, "TestE": 3, "TestF": 20, "TestG": 4}
	plan := planRuns(names, costs, 1000)
	if len(plan) != 1 {
		t.Fatalf("%d runs; want one under a budget above the total", len(plan))
	}
	stopped := runTimeout{pkg: "internal/x", output: "/tmp/x.out", deadline: 20 * time.Minute, left: []string{"x.test (pid 1)"}}
	err := runFailure(0, 1, plan[0], stopped)
	if !errors.As(err, new(runTimeout)) {
		t.Fatalf("the deadline error is not kept: %v", err)
	}
	text := err.Error()
	for _, want := range []string{"its slowest by recorded time: TestB 30s, TestD 25s, TestF 20s, TestG 4s, TestNew (not recorded)", "1 of its 8 tests have no recorded time", "budget 45s", "internal/x: the run did not end within 20m0s"} {
		if !strings.Contains(text, want) {
			t.Errorf("the failure does not say %q:\n%s", want, text)
		}
	}
	other := errors.New("the tests failed")
	if got := runFailure(0, 1, plan[0], other); got != other {
		t.Errorf("a failure of a package run whole is rewritten: %v", got)
	}
	split := planRuns(names, costs, 30)
	if got := runFailure(1, len(split), split[1], other); !errors.Is(got, other) || !strings.Contains(got.Error(), "run 2 of ") || !strings.Contains(got.Error(), split[1].args[0]) {
		t.Errorf("a failure of a split run does not name the run and its selection: %v", got)
	}
}

// heaviestFirst starts the runs with the most recorded time first.
func TestHeaviestRunStartsFirst(t *testing.T) {
	plan := []sweepRun{{cost: 3}, {cost: 9}, {cost: 1}, {cost: 9}}
	if got := heaviestFirst(plan); !slices.Equal(got, []int{1, 3, 0, 2}) {
		t.Errorf("order %v; want [1 3 0 2]", got)
	}
}
