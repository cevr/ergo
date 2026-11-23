package main

import (
	"strings"
	"testing"
)

// A package with no row in durations.json weighs its tests counted, and a
// row that names no package of the module fails the gate's declaration
// (GATE-REACH). Killers of unrecorded-weighs-zero and stale-row-accepted:
// the checkout has rows for every package once they are recorded, so only
// a fixture without them reaches either rule.
func TestDurationRowsAreCountedOrCurrent(t *testing.T) {
	root := t.TempDir()
	write(t, root, "go.mod", "module m\n\ngo 1.24\n")
	write(t, root, "a/a_test.go", "package a\n\nimport \"testing\"\n\nfunc TestOne(t *testing.T) {}\n\nfunc TestTwo(t *testing.T) {}\n")
	if got, want := packageSeconds(nil, root+"/a"), 2*unrecordedTestSeconds; got != want {
		t.Errorf("a package of two tests with no row weighs %v; want %v", got, want)
	}
	write(t, root, durationsFile, "{\n  \"m/a\": {\"TestOne\": 1},\n  \"a\": {\"TestOne\": 1}\n}\n")
	err := checkDurationRows(root, []string{"m/a"})
	if err == nil || !strings.Contains(err.Error(), ": a;") {
		t.Errorf("a row under a key that is no package: %v; want it named", err)
	}
	write(t, root, durationsFile, "{\n  \"m/a\": {\"TestOne\": 1}\n}\n")
	if err := checkDurationRows(root, []string{"m/a", "m/b"}); err != nil {
		t.Errorf("current rows and a package with none: %v; want no error", err)
	}
}
