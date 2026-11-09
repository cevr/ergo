//go:build unix

package main

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// sketchSleepingTest passes one test, which prints its ok line, and then
// runs in the next until the signal ends it. A test cannot hold Ambient, so
// it cannot sleep on the host's clock, and a sleep on a time.Manual that no
// one advances leaves no goroutine to run, which Go's runtime reports as a
// deadlock: the test loops instead, and each pass is an interruption point.
const sketchSleepingTest = `test First { assert true }

test Sleeps {
	let mut n = 0
	for {
		n = n + 1
	}
}
`

// TestTestSignals: `ergo test` forwards SIGINT and SIGTERM to the test
// binary it runs, as `ergo run` does. The signal ends the binary during its
// second test, and `ergo test` exits 128 plus the signal number (130 for
// SIGINT, 143 for SIGTERM) with no message of its own and no summary line.
// The binary's temporary directory is removed after the signal too.
func TestTestSignals(t *testing.T) {
	binary := buildTestCLI(t)
	root := smokeDir(t)
	file := smokeWrite(t, root, "sleeps.ergo", sketchSleepingTest)
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	for signal, want := range map[syscall.Signal]int{syscall.SIGINT: 130, syscall.SIGTERM: 143} {
		stdout, stderr, code := runInterruptedEntry(t, binary, root, signal, "test", file)
		if stdout != "ok First\n" || code != want || stderr != "" {
			t.Errorf("%v: code=%d stdout=%q stderr=%q; want exit %d, the test binary ended by the forwarded signal", signal, code, stdout, stderr, want)
		}
		if entries, err := os.ReadDir(tmp); err != nil || len(entries) != 0 {
			t.Errorf("%v: the temporary directory holds %v (%v) after the signal", signal, entries, err)
		}
	}
	if entries, _ := os.ReadDir(root); len(entries) != 1 || entries[0].Name() != filepath.Base(file) {
		t.Errorf("ergo test wrote into the source directory: %v", entries)
	}
}
