package main

import (
	"fmt"
	"path/filepath"
	"slices"
	"testing"
)

// redsOfOutput is the red lines of one failed step's printed output.
func redsOfOutput(t *testing.T, set redSet, step, output string) {
	t.Helper()
	w := redsOf(set, step)
	if _, err := fmt.Fprint(w, output); err != nil {
		t.Fatal(err)
	}
	w.Close()
}

// A test red on the base hides no new line in it: the comparison is by
// failure line (kills reds-compare-by-test-name), and a line that only
// moved, to another line number, another shard or another time, is the
// base's line (kills reds-position-sensitive). The record a run writes
// reads back as the set it was (the base's side of -new-reds).
func TestRedsCompareByFailureLine(t *testing.T) {
	base, head := redSet{}, redSet{}
	redsOfOutput(t, base, "go test ./internal/sketch/stdlib [1/2]",
		"--- FAIL: TestStdGoShareOnlyFalls (0.20s)\n"+
			"    goshare_test.go:45: hmac.keyState has no row\n"+
			"    goshare_test.go:45: sql.pool has no row\n"+
			"FAIL\n"+
			"FAIL\tergo.local/prototype/internal/sketch/stdlib\t0.31s\n")
	redsOfOutput(t, head, "go test ./internal/sketch/stdlib [2/3]",
		"--- FAIL: TestStdGoShareOnlyFalls (0.42s)\n"+
			"    goshare_test.go:46: hmac.keyState has no row\n"+
			"    goshare_test.go:46: json.isNumber has no row\n"+
			"FAIL\n"+
			"FAIL\tergo.local/prototype/internal/sketch/stdlib\t0.29s")
	added, fixed := compareReds(base, head)
	want := redKey("go test ./internal/sketch/stdlib", "TestStdGoShareOnlyFalls", "goshare_test.go:46: json.isNumber has no row")
	if !slices.Equal(added, []string{want}) {
		t.Errorf("new lines %q; want only %q", added, want)
	}
	fixedWant := redKey("go test ./internal/sketch/stdlib", "TestStdGoShareOnlyFalls", "goshare_test.go:45: sql.pool has no row")
	if !slices.Equal(fixed, []string{fixedWant}) {
		t.Errorf("fixed lines %q; want only %q", fixed, fixedWant)
	}
	dir := t.TempDir()
	if err := writeReds(dir, "tree", "fast", base); err != nil {
		t.Fatal(err)
	}
	read, err := readReds(filepath.Join(dir, redsSubdir, "tree.fast"))
	if err != nil {
		t.Fatal(err)
	}
	if added, fixed := compareReds(base, read); len(added)+len(fixed) != 0 {
		t.Errorf("the record reads back with %q new and %q fixed", added, fixed)
	}
}
