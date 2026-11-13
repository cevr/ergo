package main

import (
	"math"
	"path/filepath"
	"regexp"
	"testing"
)

// TestFastShardsFollowRecordedCPU: the fast tier splits a slow package by
// the CPU its fast tests recorded (testCPUFile), not by their wall time.
// cmd/ergo's fast tests recorded about 13 s of wall and 65 CPU-s, and as
// one step it was the fast tier's longest after a std edit (GS16). The
// model: the shard count is the fast tests' CPU over shardTargetSeconds,
// at most the CPU budget. Mutant gs16-fast-shards-by-wall (this package's
// ledger).
func TestFastShardsFollowRecordedCPU(t *testing.T) {
	root := checkoutRoot(t)
	t.Setenv(cpuVariable, "4")
	cpu, err := readTestCPU(root)
	if err != nil {
		t.Fatal(err)
	}
	durations, err := readDurations(root)
	if err != nil {
		t.Fatal(err)
	}
	pkg := modulePath(root) + "/cmd/ergo"
	if len(cpu[pkg]) == 0 {
		t.Fatalf("%s has no rows in %s", pkg, testCPUFile)
	}
	names, err := testNames(filepath.Join(root, "cmd", "ergo"))
	if err != nil {
		t.Fatal(err)
	}
	fast, _ := fastSelection(names, durations[pkg])
	total := 0.0
	for _, name := range fast {
		total += cpu[pkg][name]
	}
	want := max(1, min(int(math.Ceil(total/shardTargetSeconds)), stepCPUs(), len(fast)))
	steps, err := fastSteps(root)
	if err != nil {
		t.Fatal(err)
	}
	shard := regexp.MustCompile(`^go test \./cmd/ergo (\[\d+/\d+\] )?\(fast\)$`)
	got := 0
	for _, step := range steps {
		if shard.MatchString(step.Name) {
			got++
		}
	}
	if got != want {
		t.Fatalf("cmd/ergo runs in %d fast steps; its fast tests recorded %.1f CPU-s, so want %d", got, total, want)
	}
}
