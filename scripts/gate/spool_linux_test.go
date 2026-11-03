package main

import (
	"os"
	"strings"
	"testing"
)

// A gate whose output spool would be on tmpfs refuses to run: the spool's
// pages would be charged to the gate's memory, which is what the spool is
// there to bound. No step runs, and the gate says why.
func TestGateRefusesAnOutputSpoolInMemory(t *testing.T) {
	inMemory, err := memoryFileSystem("/dev/shm")
	if err != nil || !inMemory {
		t.Skipf("/dev/shm is not tmpfs here (%v)", err)
	}
	cache, err := os.MkdirTemp("/dev/shm", "gate-spool-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(cache) })
	root := fixture(t)
	code, out := runGate(t, root, cache, []*Step{{Name: "counted", Argv: count("counted")}})
	if code != 2 || !strings.Contains(out, "gate: output spool "+cache+"/output is on a file system in memory") {
		t.Fatalf("exit %d, want 2 with the refusal\n%s", code, out)
	}
	if runs(t, root, "counted") != 0 {
		t.Fatalf("a step ran with its output spool in memory\n%s", out)
	}
}
