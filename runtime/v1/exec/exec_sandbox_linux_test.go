//go:build linux

package exec

import (
	"os"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"unsafe"

	"ergo.local/prototype/runtime/rt"

	v1 "ergo.local/prototype/runtime/v1"
)

// TestExecSandboxReexecsOncePerCase is the sandbox's counted cost guard
// (TH1c): the commands of one case's folder, run or started, re-exec this
// binary once between them (the case's helper), however many there are;
// the next case's folder starts a helper of its own, and the case's end
// (TestFolderRemove) ends its helper; a command of the host's own Host
// re-execs nothing. Each program's parent is this process (the helper
// forks it with CLONE_PARENT), so Run's stop protocol holds the program as
// its child. Mutants th1c-helper-per-command and th1c-helper-kept
// (runtime/v1 ledger).
func TestExecSandboxReexecsOncePerCase(t *testing.T) {
	task := rt.Root("exec sandbox count")
	before := execSandboxReexecs.Load()
	self := strconv.Itoa(os.Getpid())
	for c, commands := range []int{3, 1} {
		root := v1.TestFolderOpen()
		for i := range commands {
			res, err := ExecRunIn(task, root, "/bin/sh", []string{"-c", "echo $PPID"}, nil, root, "", 0)
			if err != nil {
				t.Fatalf("case %d run %d: %v", c, i, err)
			}
			if got := strings.TrimSpace(res.Stdout); got != self {
				t.Errorf("case %d run %d: the program's parent is %s, want this process %s", c, i, got, self)
			}
		}
		running, err := ExecStartIn(task, root, "/usr/bin/true", nil, nil, root, "", 0, execDiscard, "", false, execDiscard, "", false)
		if err != nil {
			t.Fatalf("case %d start: %v", c, err)
		}
		running.Wait(task)
		running.Close(task)
		execHelpers.Lock()
		h := execHelpers.m[root]
		execHelpers.Unlock()
		v1.TestFolderRemove(root)
		execHelpers.Lock()
		_, kept := execHelpers.m[root]
		execHelpers.Unlock()
		if kept || h == nil || h.cmd.ProcessState == nil {
			t.Errorf("case %d: its end left its helper (registered %v, reaped %v)", c, kept, h != nil && h.cmd.ProcessState != nil)
		}
	}
	if _, err := ExecRun(task, "/usr/bin/true", nil, nil, "", "", 0); err != nil {
		t.Fatalf("host run: %v", err)
	}
	if got := execSandboxReexecs.Load() - before; got != 2 {
		t.Errorf("2 cases of 4 and 2 sandboxed commands and 1 host command re-exec'd %d times, want 2", got)
	}
}

// TestExecSandboxFailsAsTheHost: a command of the case's folder fails to
// start exactly as the host's command does (TestExecFailures' table, in
// the folder), keeps the call's contract (exactly the environment it is given,
// directory, standard input, status), and a program refused before its
// exec leaves no child of this process unreaped: the helper refuses it
// before the fork, as the parent never learns a failed fork's pid.
// Mutants th1c-no-access-check and exec-sandbox-env-fallback (runtime/v1/exec
// ledger).
func TestExecSandboxFailsAsTheHost(t *testing.T) {
	task := rt.Root("exec sandbox failures")
	root := v1.TestFolderOpen()
	defer v1.TestFolderRemove(root)
	plain := root + "/plain"
	if err := os.WriteFile(plain, []byte("echo x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ name, dir string }{
		{"ergo-exec-test-missing-7f3a", ""},
		{"/nonexistent/loop-probe-x", ""},
		{"", ""},
		{plain, ""},
		{root, ""},
		{"sh", ""},
		{"true", root + "/nowhere"},
		{"true", plain},
	} {
		_, host := ExecRun(task, c.name, nil, nil, c.dir, "", 0)
		_, boxed := ExecRunIn(task, root, c.name, nil, nil, c.dir, "", 0)
		if boxed != host {
			t.Errorf("ExecRunIn(%q, dir %q) failed %#v; the host's command %#v", c.name, c.dir, boxed, host)
		}
		if pid := execUnreapedChild(); pid != 0 {
			t.Fatalf("ExecRunIn(%q, dir %q) left child %d unreaped", c.name, c.dir, pid)
		}
	}
	t.Setenv("ERGO_EXEC_HOST", "host")
	t.Setenv("ERGO_EXEC_OVER", "host")
	res, err := ExecRunIn(task, root, "/bin/sh", []string{"-c", `printf '%s %s %s|' "$ERGO_EXEC_HOST" "$ERGO_EXEC_OVER" "$(pwd)"; cat; echo oops >&2; exit 7`},
		map[string]string{"ERGO_EXEC_OVER": "set"}, root, "in\nput", 0)
	want := ExecResult{Status: 7, Stdout: " set " + root + "|in\nput", Stderr: "oops\n"} // none of this process's environment
	if err != nil || res != want {
		t.Errorf("ExecRunIn = %#v, %v; want %#v", res, err, want)
	}
}

// execUnreapedChild is the pid of a child of this process that has ended
// and is not reaped, or 0: waitid(P_ALL, WEXITED|WNOHANG|WNOWAIT), which
// reaps nothing. The package's tests run one at a time, so a child it
// sees is the test's own.
func execUnreapedChild() int {
	const pAll, wNowait = 0, 0x1000000
	var info [128]byte // siginfo_t; si_pid is at 16 on Linux's 64-bit ABIs
	for {
		_, _, e := syscall.Syscall6(syscall.SYS_WAITID, pAll, 0, uintptr(unsafe.Pointer(&info[0])), syscall.WEXITED|syscall.WNOHANG|wNowait, 0, 0)
		if e == syscall.EINTR {
			continue
		}
		if e != 0 {
			return 0 // ECHILD: no child at all
		}
		return int(*(*int32)(unsafe.Pointer(&info[16])))
	}
}

// TestExecSandboxHelperOutOfReach: a program of the case cannot reach its
// helper, which runs as the same user and whose other threads still hold
// the capabilities it built the sandbox with: the helper is not dumpable,
// so the program can read neither /proc/<helper>/fd nor its memory (nor
// ptrace it). Mutant th1c-helper-dumpable (runtime/v1 ledger).
func TestExecSandboxHelperOutOfReach(t *testing.T) {
	root := v1.TestFolderOpen()
	defer v1.TestFolderRemove(root)
	task := rt.Root("exec sandbox reach")
	if _, err := ExecRunIn(task, root, "/usr/bin/true", nil, nil, root, "", 0); err != nil {
		t.Fatalf("run: %v", err)
	}
	execHelpers.Lock()
	helper := strconv.Itoa(execHelpers.m[root].cmd.Process.Pid)
	execHelpers.Unlock()
	res, err := ExecRunIn(task, root, "/bin/sh", []string{"-c", `ls /proc/$1/fd >/dev/null 2>&1 && echo fd; head -c 1 /proc/$1/environ >/dev/null 2>&1 && echo environ; true`, "sh", helper}, nil, root, "", 0)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if out := strings.TrimSpace(res.Stdout); out != "" {
		t.Errorf("a program of the case read its helper's /proc entries: %q", out)
	}
}

// BenchmarkExecSandbox is the price of the sandbox against the same command
// on the host (`true`, run to its end): "sandboxed" is one more command of
// a case whose helper runs (the per-command price), "case" is a case of
// one command, its folder and helper made and ended (the per-case price,
// TestFolderOpen and TestFolderRemove included; "host-case" is the same
// case with the host's command). Each reports ns/op, and the process
// tree's user-space instructions (instr/op, perf_event_open with inherit,
// this thread's own wait included) and its CPU time (user-ns/op,
// sys-ns/op, rusage of the reaped children: a case's helper is reaped at
// its end, so "sandboxed" counts its helper's share only after the last
// run). It is a measurement, not a test: run it under LOAD WAIT (1-minute
// load < 48),
//
//	go test ./runtime/v1 -run '^$' -bench BenchmarkExecSandbox -benchtime 200x -count 7
//
// and take the median of the 7 runs of each metric.
func BenchmarkExecSandbox(b *testing.B) {
	root := v1.TestFolderOpen()
	defer v1.TestFolderRemove(root)
	task := rt.Root("exec sandbox bench")
	inCase := func(run func(root string) error) func() error {
		return func() error {
			dir := v1.TestFolderOpen()
			defer v1.TestFolderRemove(dir)
			return run(dir)
		}
	}
	for _, c := range []struct {
		name string
		run  func() error
	}{
		{"host", func() error { _, err := ExecRun(task, "/usr/bin/true", nil, nil, root, "", 0); return err }},
		{"sandboxed", func() error { _, err := ExecRunIn(task, root, "/usr/bin/true", nil, nil, root, "", 0); return err }},
		{"host-case", inCase(func(dir string) error { _, err := ExecRun(task, "/usr/bin/true", nil, nil, dir, "", 0); return err })},
		{"case", inCase(func(dir string) error { _, err := ExecRunIn(task, dir, "/usr/bin/true", nil, nil, dir, "", 0); return err })},
	} {
		b.Run(c.name, func(b *testing.B) {
			runtime.LockOSThread()
			defer runtime.UnlockOSThread()
			counter := benchOpenInstructions()
			var before syscall.Rusage
			syscall.Getrusage(syscall.RUSAGE_CHILDREN, &before)
			b.ResetTimer()
			for b.Loop() {
				if err := c.run(); err != nil {
					b.Fatal(err)
				}
			}
			b.StopTimer()
			var after syscall.Rusage
			syscall.Getrusage(syscall.RUSAGE_CHILDREN, &after)
			n := float64(b.N)
			b.ReportMetric(float64(syscall.TimevalToNsec(after.Utime)-syscall.TimevalToNsec(before.Utime))/n, "user-ns/op")
			b.ReportMetric(float64(syscall.TimevalToNsec(after.Stime)-syscall.TimevalToNsec(before.Stime))/n, "sys-ns/op")
			if counter >= 0 {
				b.ReportMetric(float64(benchReadInstructions(counter))/n, "instr/op")
				syscall.Close(counter)
			}
		})
	}
}

// benchPerfAttr is the prefix of struct perf_event_attr this file sets
// (linux/perf_event.h, PERF_ATTR_SIZE_VER0, 64 bytes).
type benchPerfAttr struct {
	Type, Size                                uint32
	Config, SamplePeriod, SampleType, ReadFmt uint64
	Flags                                     uint64
	WakeupEvents, BpType                      uint32
	BpAddr                                    uint64
}

// benchOpenInstructions opens and enables a counter of user-space
// instructions on this thread and the processes it starts (inherit), or
// returns -1 when the host gives none.
func benchOpenInstructions() int {
	attr := benchPerfAttr{Type: 0, Config: 1, Flags: 1<<1 | 1<<5 | 1<<6} // hardware, instructions; inherit, exclude_kernel, exclude_hv
	attr.Size = uint32(unsafe.Sizeof(attr))
	fd, _, errno := syscall.Syscall6(syscall.SYS_PERF_EVENT_OPEN, uintptr(unsafe.Pointer(&attr)), 0, ^uintptr(0), ^uintptr(0), 0, 0)
	if errno != 0 {
		return -1
	}
	return int(fd)
}

func benchReadInstructions(fd int) uint64 {
	var buf [8]byte
	if n, err := syscall.Read(fd, buf[:]); err != nil || n != 8 {
		return 0
	}
	return *(*uint64)(unsafe.Pointer(&buf[0]))
}
