package main

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"testing"
)

// overShardTarget are the tests of a sharded package that durationsFile
// records over shardTargetSeconds and that no unit has split yet, each
// with the follow-up that owns it. TestDurationsBoundEachTest fails while
// one is not over the bound any more, so the list only shrinks.
var overShardTarget = map[string]string{
	// cmd/ergo is outside lane gate-speed's files (check-split-2, batch
	// 9); recorded 18.04 s.
	"cmd/ergo TestPackageNeverWritesUnderHome": "follow-up: split TestPackageNeverWritesUnderHome (cmd/ergo)",
}

// No test of a sharded package (slowPackages) is recorded over a shard's
// target (shardTargetSeconds; check-split-2, batch 9): shard deals whole
// tests, so one test over the target makes a shard over it whatever the
// count of shards, and the package's wall time cannot fall under its
// longest test. Such a test is cut into parts (the Split of
// internal/sketch/check, the split of internal/sketch/lower). The bound
// reads the serial times that -record-durations measures.
func TestDurationsBoundEachTest(t *testing.T) {
	durations, err := readDurations("../..")
	if err != nil {
		t.Fatal(err)
	}
	if problems := durationsOverBound(durations, slowPackages, shardTargetSeconds, overShardTarget); len(problems) > 0 {
		t.Errorf("%s records %d tests of a sharded package over the shard target %.0f s (split each, as internal/sketch/check/split_test.go does):\n%s", durationsFile, len(problems), shardTargetSeconds, strings.Join(problems, "\n"))
	}
}

// durationsOverBound lists each test of the packages ("./dir") recorded
// over bound seconds and not in known ("<dir> <test>"), and each entry of
// known that is not over the bound any more, sorted.
func durationsOverBound(durations map[string]map[string]float64, packages []string, bound float64, known map[string]string) []string {
	var problems []string
	seen := map[string]bool{}
	for pkg, tests := range durations {
		i := slices.IndexFunc(packages, func(p string) bool { return strings.HasSuffix(pkg, "/"+strings.TrimPrefix(p, "./")) })
		if i < 0 {
			continue
		}
		dir := strings.TrimPrefix(packages[i], "./")
		for name, seconds := range tests {
			if seconds <= bound {
				continue
			}
			id := dir + " " + name
			if _, ok := known[id]; ok {
				seen[id] = true
				continue
			}
			problems = append(problems, fmt.Sprintf("  %s %s: %.2f s > %.0f s", dir, name, seconds, bound))
		}
	}
	for id, reason := range known {
		if !seen[id] {
			problems = append(problems, fmt.Sprintf("  %s is not recorded over %.0f s any more: remove it from overShardTarget (%s)", id, bound, reason))
		}
	}
	sort.Strings(problems)
	return problems
}

// The bound is strict: a test one hundredth over it is named, one at it
// is not; a known test passes only while it is over the bound; a test of
// a package that is not sharded is not read.
func TestDurationsOverBound(t *testing.T) {
	durations := map[string]map[string]float64{
		"example.test/m/a":     {"TestAt": 12, "TestOver": 12.01, "TestKnown": 13, "TestFast": 1},
		"example.test/m/other": {"TestSlow": 40},
	}
	got := durationsOverBound(durations, []string{"./a"}, 12, map[string]string{"a TestKnown": "x"})
	want := []string{"  a TestOver: 12.01 s > 12 s"}
	if !slices.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
	durations["example.test/m/a"]["TestKnown"] = 2
	got = durationsOverBound(durations, []string{"./a"}, 12, map[string]string{"a TestKnown": "x"})
	if len(got) != 2 || !strings.Contains(got[0], "a TestKnown is not recorded over 12 s any more") {
		t.Errorf("a known test under the bound passes: %q", got)
	}
}
