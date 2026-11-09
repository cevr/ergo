package main

import (
	"maps"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"ergo.local/prototype/internal/testwait"
)

// TestSlotPoolServesWritersFirst: with every slot taken, a freed slot goes
// to the writers' queue first, though its step came last, and each queue
// is served in the order its steps came.
func TestSlotPoolServesWritersFirst(t *testing.T) {
	pool := newSlotPool(1)
	pool.acquire(false)
	order := make(chan string, 8)
	wait := func(first, rest int) {
		t.Helper()
		if !testwait.Until(t, func() bool { f, r := pool.waiting(); return f == first && r == rest }) {
			f, r := pool.waiting()
			t.Fatalf("waiting %d, %d; want %d, %d", f, r, first, rest)
		}
	}
	start := func(name string, first bool) {
		go func() {
			pool.acquire(first)
			order <- name
			pool.release()
		}()
	}
	start("a", false)
	wait(0, 1)
	start("b", false)
	wait(0, 2)
	start("writer", true)
	wait(1, 2)
	start("build", true)
	wait(2, 2)
	pool.release()
	var got []string
	for range 4 {
		got = append(got, <-order)
	}
	if want := []string{"writer", "build", "a", "b"}; !slices.Equal(got, want) {
		t.Fatalf("slots went to %v, want %v", got, want)
	}
	// Every slot is free again: two acquires of a pool of one would block.
	pool.acquire(false)
	if f, r := pool.waiting(); f != 0 || r != 0 || pool.free != 0 {
		t.Fatalf("after the run: waiting %d, %d, free %d", f, r, pool.free)
	}
}

// TestSlotPoolServesTheHeaviestFirst: with every slot taken, a freed slot
// goes to the writers' queue first, then to the heaviest waiting step; a
// step with no weight weighs unrecordedWeight (after a heavier step, before
// a lighter one, not first), and steps of equal weight go in the order they
// came.
func TestSlotPoolServesTheHeaviestFirst(t *testing.T) {
	pool := newSlotPool(1)
	pool.acquire(false)
	order := make(chan string, 8)
	queued := 0
	start := func(name string, first bool, weight float64) {
		go func() {
			pool.acquireWeighted(first, weight)
			order <- name
			pool.release()
		}()
		queued++
		if !testwait.Until(t, func() bool { f, r := pool.waiting(); return f+r == queued }) {
			t.Fatalf("%s did not queue", name)
		}
	}
	start("light", false, 5)
	start("unknown", false, 0)
	start("heavy", false, 2*unrecordedWeight)
	start("mid", false, unrecordedWeight/2)
	start("mid2", false, unrecordedWeight/2)
	start("writer", true, 1)
	pool.release()
	var got []string
	for range queued {
		got = append(got, <-order)
	}
	if want := []string{"writer", "heavy", "unknown", "mid", "mid2", "light"}; !slices.Equal(got, want) {
		t.Fatalf("slots went to %v, want %v", got, want)
	}
}

// TestLongestStepTakesTheFirstFreedSlot: in a gate of one slot, seven steps
// queue at once, the heaviest declared last. One of them takes the free
// slot; the others then run heaviest first, where a queue in arrival order
// runs them in the order the scheduler started their goroutines.
func TestLongestStepTakesTheFirstFreedSlot(t *testing.T) {
	root := fixture(t)
	t.Setenv(cpuVariable, "1")
	order := filepath.Join(t.TempDir(), "order")
	var steps []*Step
	for i, name := range []string{"s1", "s2", "s3", "s4", "s5", "s6", "heavy"} {
		weight := float64(i + 1)
		if name == "heavy" {
			weight = 100
		}
		steps = append(steps, &Step{Name: name, Weight: weight, Argv: []string{"sh", "-c", `sleep 0.2; echo ` + name + ` >> '` + order + `'`}})
	}
	code, out := runGate(t, root, t.TempDir(), steps)
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	content, err := os.ReadFile(order)
	if err != nil {
		t.Fatal(err)
	}
	ran := strings.Fields(string(content))
	if len(ran) != 7 {
		t.Fatalf("the steps ran %v; want seven", ran)
	}
	// After the step that found the slot free, the queue is served
	// heaviest first.
	var want []string
	for _, name := range []string{"heavy", "s6", "s5", "s4", "s3", "s2", "s1"} {
		if name != ran[0] {
			want = append(want, name)
		}
	}
	if !slices.Equal(ran[1:], want) {
		t.Fatalf("the steps ran in the order %v; want %s, then %v", ran, ran[0], want)
	}
}

// TestStepsWeighTheirRecordedTime: in the full tier every swept package
// has rows in durations.json, every step that runs a package's tests has a
// weight, each sharded test step weighs the recorded time of its tests, the
// other packages' step theirs, a closure sweep sweepWeightFactor times its
// package's (a host package's the stated default), and the closure
// sweep of internal/sketch/lower is the heaviest step after the writers,
// so it takes the first slot the writers free (batch-10: it started at
// 381 s of the merge tier and was its longest step). Every sweep step has
// the sweep's test timeout.
func TestStepsWeighTheirRecordedTime(t *testing.T) {
	host := sweepOS
	t.Cleanup(func() { sweepOS = host })
	sweepOS = "linux"
	root := checkoutRoot(t)
	steps, err := gateSteps(root)
	if err != nil {
		t.Fatal(err)
	}
	durations, err := readDurations(root)
	if err != nil {
		t.Fatal(err)
	}
	module := modulePath(root)
	lower := module + "/internal/sketch/lower"
	total := func(pkg string) float64 {
		return recordedSeconds(slices.Collect(maps.Keys(durations[pkg])), durations[pkg])
	}
	index := map[string]int{}
	for i, step := range steps {
		index[step.Name] = i
	}
	writers := writerPath(steps, index)
	var heaviest *Step
	shards := map[string]float64{}
	for i, step := range steps {
		if step.Sweep != "" {
			// Each test package has one sweep step: its package needs
			// rows, from which its test steps weigh; only a host
			// package's sweep, which skips its tests, weighs the stated
			// default.
			if _, ok := durations[module+"/"+step.Sweep]; !ok {
				t.Errorf("%s has no row for %s: its steps weigh unrecordedWeight; run ./scripts/gate.sh -record-durations ./%s", durationsFile, step.Sweep, step.Sweep)
			}
			want := sweepWeightFactor * total(module+"/"+step.Sweep)
			if slices.Contains(hostTestPackages, step.Sweep) {
				want = 0
			} else if step.Weight <= 0 {
				t.Errorf("%s weighs nothing, as a step with no record", step.Name)
			}
			if math.Abs(step.Weight-want) > 1e-6 {
				t.Errorf("%s weighs %v, want %v", step.Name, step.Weight, want)
			}
			if !slices.Contains(step.Argv, sweepTimeout) {
				t.Errorf("%s: argv %q lacks %s", step.Name, step.Argv, sweepTimeout)
			}
		}
		if runsGoTest(step) && step.Sweep == "" && step.Name != fullBuildStep && step.Weight <= 0 {
			t.Errorf("%s runs recorded tests and has no weight", step.Name)
		}
		if pkg := step.Argv[len(step.Argv)-1]; strings.HasPrefix(step.Name, "go test ./") && strings.Contains(step.Name, "/") && strings.Contains(step.Name, "[") {
			shards[pkg] += step.Weight
		}
		if !writers[i] && step.Weight > 0 && (heaviest == nil || step.Weight > heaviest.Weight) {
			heaviest = step
		}
	}
	if heaviest == nil || heaviest.Name != "closure sweep ./internal/sketch/lower" {
		t.Errorf("the heaviest step after the writers is %v; want the closure sweep of internal/sketch/lower", heaviest)
	}
	if len(shards) == 0 {
		t.Fatal("no sharded test step")
	}
	for pkg, weight := range shards {
		names, err := testNames(filepath.Join(root, strings.TrimPrefix(pkg, module+"/")))
		if err != nil {
			t.Fatal(err)
		}
		if want := recordedSeconds(names, durations[pkg]); math.Abs(weight-want) > 1e-6 {
			t.Errorf("the shards of %s weigh %v in all; want its tests' recorded %v", pkg, weight, want)
		}
	}
	if shards[lower] == 0 {
		t.Errorf("the shards of %s have no weight", lower)
	}
	others := steps[index["go test (other packages)"]]
	want := 0.0
	for _, pkg := range others.Argv[3:] {
		want += total(pkg)
	}
	if want == 0 || math.Abs(others.Weight-want) > 1e-6 {
		t.Errorf("go test (other packages) weighs %v; want %v", others.Weight, want)
	}
}

// TestWriterPathMarksWritersAndTheirNeeds: in every tier the writers' queue
// holds build bin/ergo and every step it needs (in the full tier, go test
// build), and no closure sweep.
func TestWriterPathMarksWritersAndTheirNeeds(t *testing.T) {
	host := sweepOS
	t.Cleanup(func() { sweepOS = host })
	sweepOS = "linux"
	root := checkoutRoot(t)
	full, err := gateSteps(root)
	if err != nil {
		t.Fatal(err)
	}
	fast, err := fastSteps(root)
	if err != nil {
		t.Fatal(err)
	}
	for tier, steps := range map[string][]*Step{fastTier: fast, fullTier: full} {
		index := map[string]int{}
		for i, step := range steps {
			index[step.Name] = i
		}
		marked := writerPath(steps, index)
		var names []string
		for i, step := range steps {
			if marked[i] {
				names = append(names, step.Name)
			}
			if marked[i] && step.Sweep != "" {
				t.Errorf("%s tier: the sweep %s is in the writers' queue", tier, step.Name)
			}
		}
		if !slices.Contains(names, buildCLIStep) {
			t.Errorf("%s tier: writers' queue %v lacks %s", tier, names, buildCLIStep)
		}
		for i, step := range steps {
			for _, need := range step.Needs {
				if marked[i] && !marked[index[need]] {
					t.Errorf("%s tier: %s is in the writers' queue and %s, which it needs, is not", tier, step.Name, need)
				}
			}
		}
	}
	steps := []*Step{{Name: "a"}, {Name: "b", Needs: []string{"a"}}, {Name: "w", Needs: []string{"b"}, Writes: []string{"bin/"}}, {Name: "c", Needs: []string{"a"}}}
	if got := writerPath(steps, map[string]int{"a": 0, "b": 1, "w": 2, "c": 3}); !slices.Equal(got, []bool{true, true, true, false}) {
		t.Errorf("writerPath = %v, want a, b and w", got)
	}
}

// TestWriterTakesTheNextFreedSlot: in a gate of one slot, six steps queue
// at once, and the writer queues only when the build it needs ends, 0.3 s
// later. The writer still runs before at least three of the six: it takes
// the next freed slot after one step at most, where a queue in arrival
// order runs it after all six.
func TestWriterTakesTheNextFreedSlot(t *testing.T) {
	root := fixture(t)
	t.Setenv(cpuVariable, "1")
	order := filepath.Join(t.TempDir(), "order")
	record := func(name, wait string) []string {
		return []string{"sh", "-c", wait + `echo ` + name + ` >> '` + order + `'`}
	}
	steps := []*Step{{Name: "build", Argv: record("build", "sleep 0.3; ")}}
	for _, name := range []string{"s1", "s2", "s3", "s4", "s5", "s6"} {
		steps = append(steps, &Step{Name: name, Argv: record(name, "sleep 0.05; ")})
	}
	steps = append(steps, &Step{Name: "writer", Needs: []string{"build"}, Writes: []string{"gen/"}, Argv: record("writer", "")})
	code, out := runGate(t, root, t.TempDir(), steps)
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	content, err := os.ReadFile(order)
	if err != nil {
		t.Fatal(err)
	}
	ran := strings.Fields(string(content))
	at := slices.Index(ran, "writer")
	if at < 0 || len(ran) != 8 || len(ran)-1-at < 3 {
		t.Fatalf("the steps ran in the order %v; want the writer before at least three of s1-s6", ran)
	}
}
