package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// A step's priority is its own expected seconds (its longest recorded
// time, else its declared weight, else unrecordedWeight) plus the longest
// chain of the steps that need it (gate-speed GS6).
func TestStepPrioritiesFollowTheLongestChain(t *testing.T) {
	steps := []*Step{
		{Name: "a", Weight: 1},
		{Name: "b", Weight: 100, Needs: []string{"a"}},
		{Name: "c", Weight: 50},
		{Name: "d"},
		{Name: "e", Weight: 5, Needs: []string{"a", "d"}},
	}
	index := map[string]int{}
	for i, step := range steps {
		index[step.Name] = i
	}
	// c's longest recorded time, not its last, weighs it.
	times := stepTimes{"c": {70, 1}}
	got := stepPriorities(steps, index, times)
	want := []float64{101, 100, 70, 35, 5}
	if !slices.Equal(got, want) {
		t.Fatalf("priorities %v, want %v (a: own 1 + b's chain 100; d: unrecorded 30 + e's 5)", got, want)
	}
}

// writeStepTimes keeps each step's last stepTimesKept times of the runs it
// passed in (ran, or replayed at its recorded cost), records no failed or
// blocked step, keeps the steps this run did not declare, and reads back.
func TestStepTimesKeepTheLastRuns(t *testing.T) {
	cache := t.TempDir()
	steps := []*Step{{Name: "ran"}, {Name: "replayed"}, {Name: "failed"}, {Name: "blocked"}}
	times := stepTimes{"other tier": {9}}
	for run := 1; run <= stepTimesKept+2; run++ {
		results := []*result{
			{outcome: passed, duration: time.Duration(run) * time.Second},
			{outcome: cached, duration: 3 * time.Second},
			{outcome: failed, duration: time.Second},
			{outcome: blocked},
		}
		if err := writeStepTimes(cache, times, steps, results); err != nil {
			t.Fatal(err)
		}
		times = readStepTimes(cache)
	}
	want := stepTimes{"other tier": {9}, "ran": {3, 4, 5, 6, 7}, "replayed": {3, 3, 3, 3, 3}}
	if len(times) != len(want) {
		t.Fatalf("times %v, want %v", times, want)
	}
	for name, list := range want {
		if !slices.Equal(times[name], list) {
			t.Errorf("%s: times %v, want %v", name, times[name], list)
		}
	}
	entries, err := os.ReadDir(cache)
	if err != nil || len(entries) != 1 {
		t.Errorf("cache holds %v (%v); want only %s", entries, err, stepTimesFile)
	}
}

// TestRecordedTimeOrdersTheNextRun: in a gate of one slot, ten steps queue
// at once; "late" is declared the lightest, but the cache records that it
// took 100 s. It and pre, the step it needs, run before the steps of 50 s
// but the ones already running, and the run records every step's time for
// the next.
func TestRecordedTimeOrdersTheNextRun(t *testing.T) {
	root := fixture(t)
	cache := t.TempDir()
	t.Setenv(cpuVariable, "1")
	order := filepath.Join(t.TempDir(), "order")
	if err := writeStepTimes(cache, stepTimes{"late": {100}}, nil, nil); err != nil {
		t.Fatal(err)
	}
	// late needs pre, so late never finds the slot free: pre, unrecorded,
	// goes before the steps of 50 s only through late's chain, and late,
	// queued once pre ends, takes the slot after the step then running.
	steps := []*Step{{Name: "pre", Argv: []string{"true"}}}
	for _, name := range []string{"late", "s1", "s2", "s3", "s4", "s5", "s6", "s7", "s8"} {
		step := &Step{Name: name, Weight: 50, Argv: []string{"sh", "-c", `sleep 0.2; echo ` + name + ` >> '` + order + `'`}}
		if name == "late" {
			step.Weight, step.Needs = 1, []string{"pre"}
		}
		steps = append(steps, step)
	}
	code, out := runGate(t, root, cache, steps)
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	content, err := os.ReadFile(order)
	if err != nil {
		t.Fatal(err)
	}
	ran := strings.Fields(string(content))
	if at := slices.Index(ran, "late"); at > 2 || len(ran) != 9 {
		t.Fatalf("the steps ran in the order %v; want late among the first three, where the declared weights run it last", ran)
	}
	data, err := os.ReadFile(filepath.Join(cache, stepTimesFile))
	if err != nil {
		t.Fatal(err)
	}
	var times stepTimes
	if err := json.Unmarshal(data, &times); err != nil {
		t.Fatal(err)
	}
	for _, step := range steps {
		want := 1
		if step.Name == "late" {
			want = 2
		}
		if len(times[step.Name]) != want {
			t.Errorf("%s: recorded %v; want %d times", step.Name, times[step.Name], want)
		}
	}
}
