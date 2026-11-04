package main

import (
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"testing"
)

// Each host package has one step that never replays (-count=1) and bounds
// its parallel tests explicitly (-parallel hostTestParallel): Go's default,
// GOMAXPROCS, would start as many layout gates and systemd scopes at once as
// the host has CPUs.
func TestHostPackagesRunOnEveryGateWithBoundedParallelism(t *testing.T) {
	t.Parallel()
	if n, err := strconv.Atoi(hostTestParallel); err != nil || n < 1 {
		t.Fatalf("hostTestParallel %q is not a positive count", hostTestParallel)
	}
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	steps, err := gateSteps(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, relative := range hostTestPackages {
		pkg := modulePath(root) + "/" + relative
		var found []*Step
		for _, step := range steps {
			if runsGoTest(step) && slices.Contains(step.Argv, pkg) {
				found = append(found, step)
			}
		}
		if len(found) != 1 {
			t.Fatalf("host package %s is in %d go test steps, want 1", pkg, len(found))
		}
		argv := found[0].Argv
		if !slices.Contains(argv, "-count=1") {
			t.Errorf("step %q may replay %s: %q has no -count=1", found[0].Name, pkg, argv)
		}
		if i := slices.Index(argv, "-parallel"); i < 0 || i+1 >= len(argv) || argv[i+1] != hostTestParallel {
			t.Errorf("step %q does not bound %s's parallel tests to %s: %q", found[0].Name, pkg, hostTestParallel, argv)
		}
	}
}

// The mapping step keys on the directories of the evidence tests the
// mapping names, wherever they live, each once and sorted; a mapping that
// does not parse adds none.
func TestEvidenceDirectoriesFollowTheMapping(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	write := func(content string) {
		t.Helper()
		path := filepath.Join(root, "conformance", "effect-cases.json")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if got := evidenceDirectories(root); got != nil {
		t.Fatalf("no mapping gave %v", got)
	}
	write(`{"cases": [
		{"evidence": [{"file": "runtime/rt/scope_test.go"}, {"file": "cmd/ergo/a_test.go"}]},
		{"evidence": []},
		{"evidence": [{"file": "runtime/rt/other_test.go"}, {"file": "top_test.go"}]}
	]}`)
	want := []string{"cmd/ergo/", "runtime/rt/", "top_test.go"}
	if got := evidenceDirectories(root); !slices.Equal(got, want) {
		t.Fatalf("evidence directories %v, want %v", got, want)
	}
	write(`{"cases": [`)
	if got := evidenceDirectories(root); got != nil {
		t.Fatalf("a malformed mapping gave %v", got)
	}
}
