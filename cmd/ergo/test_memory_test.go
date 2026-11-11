//go:build unix

package main

import (
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"

	"ergo.local/prototype/internal/proclimit"
)

// TestTestProgramCollectsWithinItsLimit builds Go because only the emitted
// program's Go collector under RLIMIT_DATA can reproduce the inherited-GC
// failure (batch-15 r7). The fixture has bounded work and at most two live
// 128MiB strings; it is not an unbounded allocation or a timing check.
// A 512MiB cap leaves mapping headroom for Go's heap index and GC queues;
// the old 256MiB cap could exhaust a Green Tea queue's unchecked mmap (r9).
// Scaling the chunks too keeps inherited GOGC=400 red, with similar total work.
// Mutants: inherit both GC settings, drop either child setting. Existing
// test-command checks allocate no substantial transient heap.
func TestTestProgramCollectsWithinItsLimit(t *testing.T) {
	needsBuild(t)
	binary := buildTestCLI(t)
	root := physicalTempDir(t)
	source := smokeWrite(t, root, "memory.ergo", `import go strings

test Collects {
	let mut previous = strings.Repeat("x", 128 * 1024 * 1024)
	for _ in 0..6 {
		let next = strings.Repeat("y", 128 * 1024 * 1024)
		assert len(previous) == 128 * 1024 * 1024
		previous = next
	}
	assert len(previous) == 128 * 1024 * 1024
}

`)
	command := exec.Command(binary, "test", source)
	command.Dir = root
	command.Env = append(os.Environ(), "ERGO_PROGRAM_MEMORY=512MiB", "GOGC=400", "GOMEMLIMIT=6GiB", "XDG_CACHE_HOME="+t.TempDir())
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	t.Cleanup(func() {
		if command.Process != nil && command.Process.Pid > 1 {
			_ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
		}
	})
	stdout, stderr, code := runSmokeCommand(t, command)
	if code != 0 || !strings.Contains(string(stdout), "PASS (1 tests)") {
		lines := strings.Split(string(stderr), "\n")
		for _, line := range lines {
			if strings.HasPrefix(line, "SIG") || strings.HasPrefix(line, "PC=") {
				t.Logf("child signal: %s", line)
			}
		}
		// The sweep retains file:line messages and the output's tail. Give
		// the head file:line messages, so the faulting stack survives even
		// when idle goroutines fill the tail. Pair source lines to fit the
		// sweep's forty-message budget alongside the signal and exit code.
		head := lines[:min(40, len(lines))]
		for i := 0; i < len(head); i += 2 {
			end := min(i+2, len(head))
			t.Logf("child stderr %02d-%02d: %s", i+1, end, strings.Join(head[i:end], " | "))
		}
		t.Fatalf("bounded transient allocations: exit %d\nstdout: %s", code, stdout)
	}
}

// TestTestProgramEnvironment catches either child collector setting being
// dropped independently: one alone can keep the allocation repro below
// its cap. It also catches applying the bounded policy to an explicit off.
// The prepared exec.Cmd is the environment actually passed to the child;
// no process or compiler is needed to observe it. Both plain and explored
// children are checked: x6-worker-inherits-collector escaped the plain path.
func TestTestProgramEnvironment(t *testing.T) {
	t.Setenv("GOGC", "off")
	t.Setenv("GOMEMLIMIT", "6GiB")
	for _, limit := range []uint64{0, 256 << 20, proclimit.DefaultLimit} {
		for _, child := range []*exec.Cmd{
			testProgramCommand("not-started", nil, limit),
			exploreWorkerCommand("not-started", nil, nil, nil, nil, limit),
		} {
			env := child.Environ()
			gc, memory := "", ""
			for _, entry := range env {
				if value, ok := strings.CutPrefix(entry, "GOGC="); ok {
					gc = value
				}
				if value, ok := strings.CutPrefix(entry, "GOMEMLIMIT="); ok {
					memory = value
				}
			}
			wantGC, wantMemory := "100", "1610612736"
			if limit == 0 {
				wantGC, wantMemory = "off", "6GiB"
			} else if limit == 256<<20 {
				wantMemory = "201326592"
			}
			if gc != wantGC || memory != wantMemory {
				t.Errorf("limit %d: GOGC=%q GOMEMLIMIT=%q; want %q %q", limit, gc, memory, wantGC, wantMemory)
			}
		}
	}
}

// needsBuild keeps the process-only GC acceptance in the full tier.
func needsBuild(t *testing.T) {
	t.Helper()
	if testing.Short() {
		t.Skip("builds and runs the bounded GC regression program")
	}
}
