package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
)

// stepTimesFile, in the gate's cache directory, holds the seconds each
// step took in the gate's last runs on this host (gate-speed GS6): a step
// that ran, or replayed a pass recorded at its cost. The next run orders
// its steps by them (stepPriorities): a declared weight is a test
// package's serial time on a warm Go cache, which put the examples step
// (no record: unrecordedWeight) and internal/gobind (1.1 s recorded,
// 143 s in the gate) behind steps of seconds, at 101 s and 344 s of the
// batch-18 fast tier. It orders the steps only: no key holds it, and a
// missing or unreadable file orders by the declared weights.
const stepTimesFile = "step-times.json"

// stepTimesKept is how many of a step's last times the file keeps; the
// longest of them weighs the step, so one run that replayed most of a
// step (the examples step's verdict cache) does not make the next run,
// which may rebuild every program, start it last.
const stepTimesKept = 5

// stepTimes is the recorded seconds per step name, oldest first.
type stepTimes map[string][]float64

// readStepTimes reads the times of the cache directory, or none.
func readStepTimes(cacheDir string) stepTimes {
	if cacheDir == "" {
		return nil
	}
	data, err := os.ReadFile(filepath.Join(cacheDir, stepTimesFile))
	if err != nil {
		return nil
	}
	var times stepTimes
	if json.Unmarshal(data, &times) != nil {
		return nil
	}
	return times
}

// longest is the step's longest recorded time, or 0.
func (t stepTimes) longest(name string) float64 {
	if len(t[name]) == 0 {
		return 0
	}
	return slices.Max(t[name])
}

// writeStepTimes adds this run's time of each step that passed (ran, or
// replayed a pass at its recorded cost) to the times read at the start,
// keeping each step's last stepTimesKept, and replaces the file at once
// (a temporary file renamed over it). The other tier's steps keep theirs.
func writeStepTimes(cacheDir string, times stepTimes, steps []*Step, results []*result) error {
	if cacheDir == "" {
		return nil
	}
	merged := stepTimes{}
	for name, list := range times {
		merged[name] = slices.Clone(list)
	}
	for i, step := range steps {
		r := results[i]
		if r == nil || (r.outcome != passed && r.outcome != cached) || r.duration <= 0 {
			continue
		}
		list := append(merged[step.Name], r.duration.Seconds())
		merged[step.Name] = list[max(0, len(list)-stepTimesKept):]
	}
	data, err := json.MarshalIndent(merged, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return err
	}
	file, err := os.CreateTemp(cacheDir, stepTimesFile+".*")
	if err != nil {
		return err
	}
	_, err = file.Write(append(data, '\n'))
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(file.Name(), filepath.Join(cacheDir, stepTimesFile))
	}
	if err != nil {
		os.Remove(file.Name())
	}
	return err
}

// stepPriorities is the order in which waiting steps take a freed slot
// (slotPool, heaviest first): a step's own expected seconds (its longest
// recorded time, else its declared Weight, else unrecordedWeight) plus the
// longest chain of the steps that need it. Longest remaining chain first:
// a step that a long step waits on starts before a step of the same
// length that nothing waits on.
func stepPriorities(steps []*Step, index map[string]int, times stepTimes) []float64 {
	own := make([]float64, len(steps))
	dependents := make([][]int, len(steps))
	for i, step := range steps {
		own[i] = step.Weight
		if recorded := times.longest(step.Name); recorded > 0 {
			own[i] = recorded
		}
		if own[i] <= 0 {
			own[i] = unrecordedWeight
		}
		for _, need := range step.Needs {
			if j, ok := index[need]; ok {
				dependents[j] = append(dependents[j], i)
			}
		}
	}
	priority := make([]float64, len(steps))
	visited := make([]bool, len(steps))
	var visit func(i int) float64
	visit = func(i int) float64 {
		if visited[i] {
			return priority[i]
		}
		// Marked first: a cycle of needs, which never runs, ends here.
		visited[i] = true
		chain := 0.0
		for _, j := range dependents[i] {
			chain = max(chain, visit(j))
		}
		priority[i] = own[i] + chain
		return priority[i]
	}
	for i := range steps {
		visit(i)
	}
	return priority
}
