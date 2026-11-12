package main

import (
	"math"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
)

// goTestTimeouts are the -timeout values of each go test command a step
// runs: in its argv (go test, or env ... go test), or in a reviewed
// script's text, one per go test there. An empty value is a go test
// command without -timeout.
func goTestTimeouts(step *Step) []string {
	var commands [][]string
	for i := 0; i+1 < len(step.Argv); i++ {
		if step.Argv[i] == "go" && step.Argv[i+1] == "test" {
			commands = append(commands, step.Argv[i+2:])
		}
	}
	if len(step.Argv) >= 3 && step.Argv[0] == "sh" && step.Argv[1] == "-c" {
		for _, line := range regexp.MustCompile(`&&|;|\n`).Split(step.Argv[2], -1) {
			if words := strings.Fields(line); len(words) >= 2 {
				if i := slices.Index(words, "go"); i >= 0 && i+1 < len(words) && words[i+1] == "test" {
					commands = append(commands, words[i+2:])
				}
			}
		}
	}
	var timeouts []string
	for _, args := range commands {
		value := ""
		for _, arg := range args {
			if v, ok := strings.CutPrefix(arg, "-timeout="); ok {
				value = v
			}
		}
		timeouts = append(timeouts, value)
	}
	return timeouts
}

// TestGoTestStepsHaveHangDeadlines: no gate step relies on go test's
// default 15 minute timeout as a budget (gate16b-fix3: the heaviest shard
// of internal/sketch/lower sat at it and load decided its verdict). Every
// go test command of every step of both tiers passes -timeout, at least
// ten times the step's recorded cost (Step.Weight) and at least 30
// minutes, so a deadline only stops a hung test.
func TestGoTestStepsHaveHangDeadlines(t *testing.T) {
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
	checked := 0
	for _, step := range append(full, fast...) {
		for _, value := range goTestTimeouts(step) {
			checked++
			if value == "" {
				t.Errorf("%s: a go test without -timeout: %q", step.Name, step.Argv)
				continue
			}
			got, err := time.ParseDuration(value)
			if err != nil {
				t.Errorf("%s: -timeout=%s: %v", step.Name, value, err)
				continue
			}
			want := max(30*time.Minute, time.Duration(10*step.Weight*float64(time.Second)))
			if got < want-time.Minute {
				t.Errorf("%s: -timeout=%s for a recorded cost of %.1fs; want at least %s", step.Name, value, step.Weight, want)
			}
		}
	}
	if checked == 0 {
		t.Fatal("no go test command in the gate's steps")
	}
}

// TestShardsBalanceTheirRecordedCPU: at the batch gate's CPU budget of 4,
// the full-tier shards of each package with rows in testCPUFile are within
// 1.5 times their mean recorded CPU, each shard's CPU summed from the tests
// its selection runs (-run names it, -skip the package's other tests).
// Dealt by wall time, internal/sketch/lower's first shard took 948 CPU-s
// where the others took 293 to 458 (gate16b-fix3). A test that grows until
// no dealing balances the shards fails here: split it.
func TestShardsBalanceTheirRecordedCPU(t *testing.T) {
	root := checkoutRoot(t)
	t.Setenv(cpuVariable, "4")
	steps, err := gateSteps(root)
	if err != nil {
		t.Fatal(err)
	}
	cpu, err := readTestCPU(root)
	if err != nil {
		t.Fatal(err)
	}
	module := modulePath(root)
	shards := map[string][]*Step{}
	shard := regexp.MustCompile(`^go test (\S+) \[\d+/\d+\]$`)
	for _, step := range steps {
		if m := shard.FindStringSubmatch(step.Name); m != nil {
			shards[m[1]] = append(shards[m[1]], step)
		}
	}
	lower := module + "/internal/sketch/lower"
	if len(cpu[lower]) == 0 || len(shards["./internal/sketch/lower"]) < 2 {
		t.Fatalf("internal/sketch/lower has %d rows in %s and %d shards; want both", len(cpu[lower]), testCPUFile, len(shards["./internal/sketch/lower"]))
	}
	for pkg, group := range shards {
		rows := cpu[module+"/"+strings.TrimPrefix(pkg, "./")]
		if len(rows) == 0 {
			continue
		}
		names, err := testNames(filepath.Join(root, filepath.FromSlash(strings.TrimPrefix(pkg, "./"))))
		if err != nil {
			t.Fatal(err)
		}
		var costs []float64
		total := 0.0
		for _, step := range group {
			cost := 0.0
			for _, name := range names {
				if runsTest(step.Argv, name) {
					cost += rows[name]
				}
			}
			costs = append(costs, cost)
			total += cost
		}
		mean := total / float64(len(costs))
		if heaviest := slices.Max(costs); heaviest > 1.5*mean {
			t.Errorf("%s: its heaviest shard costs %.1f CPU-s, over 1.5 times the mean %.1f (%v)", pkg, heaviest, mean, roundAll(costs))
		}
	}
}

// runsTest says whether a go test argv's selection (-run or -skip) runs
// the top-level test name.
func runsTest(argv []string, name string) bool {
	for i := 0; i+1 < len(argv); i++ {
		switch argv[i] {
		case "-run":
			return regexp.MustCompile(argv[i+1]).MatchString(name)
		case "-skip":
			return !regexp.MustCompile(argv[i+1]).MatchString(name)
		}
	}
	return true
}

func roundAll(values []float64) []float64 {
	out := make([]float64, len(values))
	for i, v := range values {
		out[i] = math.Round(v*10) / 10
	}
	return out
}
