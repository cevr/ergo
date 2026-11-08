package proclimit_test

import (
	"bytes"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"ergo.local/prototype/internal/proclimit"
	"ergo.local/prototype/internal/testinputs"
)

// TestMain runs the tests under observation. The memory limit has its guard
// where programs are generated: lower's TestRunProgramBoundsAGrowingProgram.
func TestMain(m *testing.M) { os.Exit(testinputs.Main(m)) }

// A started program has the highest oom_score_adj, never less than its
// parent's, so the kernel stops it before the agent, the shell and the
// services. A child process is needed: the score is a kernel property of
// the process Start starts. The probe is sh, which reads its score only
// after Start has returned, when the test writes its stdin. Mutants: drop
// the oom_score_adj write in set (the program inherits its parent's 999);
// drop raiseOOMScore in testinputs.MainFunc (this test process keeps the
// score it inherited, -900 under the workbox's SSH service).
func TestStartedProgramsDieFirst(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("only Linux has oom_score_adj")
	}
	if own := readScore(t); own != proclimit.OOMScore {
		t.Fatalf("this test process has oom_score_adj %d; testinputs.Main must raise it to %d", own, proclimit.OOMScore)
	}
	// Lower this process just below the top, which needs no privilege, so a
	// program that only inherits the score is seen.
	setScore(t, proclimit.OOMScore-1)
	t.Cleanup(func() { setScore(t, proclimit.OOMScore) })
	cmd := exec.Command("sh", "-c", `read _ && read v </proc/self/oom_score_adj && echo "$v"`)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if err := proclimit.Start(cmd, proclimit.DefaultLimit); err != nil {
		t.Fatal(err)
	}
	_, _ = stdin.Write([]byte("go\n"))
	_ = stdin.Close()
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	child, err := strconv.Atoi(strings.TrimSpace(stdout.String()))
	if err != nil {
		t.Fatalf("probe printed %q", stdout.String())
	}
	if child != proclimit.OOMScore {
		t.Fatalf("started program has oom_score_adj %d, its parent %d; want %d", child, proclimit.OOMScore-1, proclimit.OOMScore)
	}
}

// readScore and setScore use system calls, not package os, whose test log
// would make /proc/self/oom_score_adj, new on each run, an input of Go's
// test cache.
func readScore(t *testing.T) int {
	t.Helper()
	fd, err := syscall.Open("/proc/self/oom_score_adj", syscall.O_RDONLY|syscall.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.Close(fd)
	data := make([]byte, 16)
	k, err := syscall.Read(fd, data)
	if err != nil {
		t.Fatal(err)
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(data[:k])))
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func setScore(t *testing.T, n int) {
	t.Helper()
	fd, err := syscall.Open("/proc/self/oom_score_adj", syscall.O_WRONLY|syscall.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.Close(fd)
	if _, err := syscall.Write(fd, []byte(strconv.Itoa(n))); err != nil {
		t.Fatal(err)
	}
}
