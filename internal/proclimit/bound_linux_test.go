//go:build linux

package proclimit

import (
	"bytes"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"ergo.local/prototype/internal/testwait"
)

// TestBoundHoldsFromTheFirstInstruction: a program is bounded before it
// runs, however late its caller is scheduled after starting it. The hook
// holds the caller until the program has either ended or stopped, the
// window a loaded host gives it (the merge tier's
// TestRunProgramBoundsAGrowingProgram ran unbounded there, batch-12
// integration round 7). The probe is sh printing its RLIMIT_DATA in KiB,
// read first thing; a child process is needed, since the limit is a kernel
// property of the process Start starts. Mutant: start the program with a
// plain cmd.Start and set the bound after the hook (the old order); sh then
// ends first, prints "unlimited", and Run returns no error.
func TestBoundHoldsFromTheFirstInstruction(t *testing.T) {
	beforeBound = func(pid int) {
		if !testwait.Until(t, func() bool { return stoppedOrEnded(pid) }) {
			t.Errorf("program %d neither stopped nor ended by the test's deadline", pid)
		}
	}
	t.Cleanup(func() { beforeBound = nil })
	cmd := exec.Command("sh", "-c", "ulimit -d")
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if err := Run(cmd, 256<<20); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(stdout.String()); got != "262144" {
		t.Fatalf("the program's RLIMIT_DATA is %q KiB; want 262144 (256 MiB) from its first instruction", got)
	}
}

// stoppedOrEnded reports whether process pid is stopped (traced or not),
// a zombie, or gone. It reads /proc with system calls, not package os, so
// the file is no input of Go's test cache.
func stoppedOrEnded(pid int) bool {
	fd, err := syscall.Open("/proc/"+strconv.Itoa(pid)+"/stat", syscall.O_RDONLY|syscall.O_CLOEXEC, 0)
	if err != nil {
		return true
	}
	defer syscall.Close(fd)
	stat := make([]byte, 4<<10)
	n, err := syscall.Read(fd, stat)
	if err != nil {
		return true
	}
	// The state follows the command name's closing parenthesis.
	stat = stat[:n]
	end := bytes.LastIndexByte(stat, ')')
	if end < 0 || end+2 >= len(stat) {
		return true
	}
	return strings.IndexByte("tTZX", stat[end+2]) >= 0
}
