package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSweepsShareOneStdStore: every closure sweep step names the same std
// store directory (sweepStdVariable), outside each step's own TMPDIR, which
// the gate empties and which differs per step. Under the step's TMPDIR each
// sweep filled a 781 MB store of its own: ten of them took 7.7 GB of a
// merge tier sandbox's 12 GB (fu-gate-sandbox). It kills
// gatesandbox-sweep-store-per-step; internal/closure's
// TestStdStoreHoldsOnlyStdCompiles counts that a sweep given the directory
// builds nothing.
func TestSweepsShareOneStdStore(t *testing.T) {
	root := t.TempDir()
	var dirs []string
	for _, pkg := range []string{"internal/sketch/lower", "cmd/ergo"} {
		step := sweepStep(pkg, 1)
		step.Name = "closure sweep ./" + pkg
		dir := ""
		for _, kv := range step.Env {
			if v, ok := strings.CutPrefix(kv, sweepStdVariable+"="); ok {
				dir = v
			}
		}
		if dir == "" {
			t.Fatalf("%s sets no %s: %q", step.Name, sweepStdVariable, step.Env)
		}
		if rel, err := filepath.Rel(stepTempDir(root, step.Name), dir); err == nil && !strings.HasPrefix(rel, "..") {
			t.Errorf("%s's std store %s is inside its own TMPDIR", step.Name, dir)
		}
		dirs = append(dirs, dir)
	}
	if dirs[0] != dirs[1] {
		t.Errorf("two sweep steps name two std stores: %q", dirs)
	}
}

// TestStepBelowDiskFloorIsNotStarted: a step that would start with less free
// disk than the floor under its TMPDIR or the gate cache fails before its
// command runs, naming the disk and the floor; above the floor it runs; an
// unreadable free space fails too (fail closed); 0 turns the check off. A
// merge tier whose sweeps filled the disk failed them partway with ENOSPC
// (fu-gate-sandbox). It kills the mutants gatesandbox-floor-unchecked,
// gatesandbox-floor-compare and gatesandbox-floor-unreadable-passes.
func TestStepBelowDiskFloorIsNotStarted(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	root, cache := t.TempDir(), t.TempDir()
	free := map[string]uint64{}
	var unreadable error
	saved := freeBytes
	t.Cleanup(func() { freeBytes = saved })
	freeBytes = func(path string) (uint64, error) {
		if unreadable != nil {
			return 0, unreadable
		}
		if path == cache {
			return free["cache"], nil
		}
		return free["tmp"], nil
	}
	marker := filepath.Join(t.TempDir(), "ran")
	step := &Step{Name: "floor probe", Argv: []string{"/bin/sh", "-c", "touch " + marker}}
	g := &gate{root: root, cacheDir: cache}

	t.Setenv(diskFloorVariable, "1G")
	for _, c := range []struct {
		name      string
		tmp, gate uint64
	}{
		{"TMPDIR below", 1<<30 - 1, 8 << 30},
		{"gate cache below", 8 << 30, 1<<30 - 1},
	} {
		free["tmp"], free["cache"] = c.tmp, c.gate
		r := g.execute(step)
		if r.outcome != failed || !strings.Contains(string(r.output), "disk floor") || !strings.Contains(string(r.output), diskFloorVariable) {
			t.Errorf("%s: outcome %v, output %q; want a failure naming the disk floor", c.name, r.outcome, r.output)
		}
		if _, err := os.Stat(marker); err == nil {
			t.Fatalf("%s: the step ran below the floor", c.name)
		}
	}
	unreadable = errors.New("statfs refused")
	if r := g.execute(step); r.outcome != failed || !strings.Contains(string(r.output), "statfs refused") {
		t.Errorf("unreadable free space: outcome %v, output %q; want a failure", r.outcome, r.output)
	}
	unreadable = nil

	// At the floor exactly, and with the check off, the step is past the
	// floor: belowDiskFloor says nothing (execute then runs it under the
	// budget, which this test does not set up).
	free["tmp"], free["cache"] = 1<<30, 1<<30
	if reason := belowDiskFloor(1<<30, root, cache); reason != "" {
		t.Errorf("exactly at the floor: %q, want no refusal", reason)
	}
	free["tmp"], free["cache"] = 0, 0
	t.Setenv(diskFloorVariable, "0")
	if floor, err := diskFloor(); err != nil || floor != 0 || belowDiskFloor(floor, root, cache) != "" {
		t.Errorf("%s=0: floor %d (%v); want the check off", diskFloorVariable, floor, err)
	}
	t.Setenv(diskFloorVariable, "")
	if floor, err := diskFloor(); err != nil || floor != defaultDiskFloor {
		t.Errorf("unset: floor %d (%v), want the default %d", floor, err, defaultDiskFloor)
	}
}
