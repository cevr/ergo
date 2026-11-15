//go:build linux

package v1

import (
	"os"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"unsafe"

	"ergo.local/prototype/runtime/rt"
)

// TestExecSandboxReexecsOncePerCommand is the sandbox's counted cost guard
// (TH1b): each command of the test's folder, run or started, re-execs this
// binary exactly once, and the helper then becomes the program in place, so
// the program's parent is this process (no process between them); a
// command of the host's own Host re-execs nothing. Mutant
// th1b-reexec-twice (runtime/v1 ledger), which sends a command through the
// helper twice.
func TestExecSandboxReexecsOncePerCommand(t *testing.T) {
	root := TestFolderOpen()
	defer TestFolderRemove(root)
	task := rt.Root("exec sandbox count")
	before := execSandboxReexecs.Load()
	self := strconv.Itoa(os.Getpid())
	for i := range 2 {
		res, err := ExecRunIn(task, root, "sh", []string{"-c", "echo $PPID"}, nil, root, "", 0)
		if err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
		if got := strings.TrimSpace(res.Stdout); got != self {
			t.Errorf("run %d: the program's parent is %s, want this process %s", i, got, self)
		}
	}
	running, err := ExecStartIn(task, root, "true", nil, nil, root, "", 0, execDiscard, "", false, execDiscard, "", false)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	running.Wait(task)
	running.Close(task)
	if _, err := ExecRun(task, "true", nil, nil, "", "", 0); err != nil {
		t.Fatalf("host run: %v", err)
	}
	if got := execSandboxReexecs.Load() - before; got != 3 {
		t.Errorf("3 sandboxed commands and 1 host command re-exec'd %d times, want 3", got)
	}
}

// BenchmarkExecSandbox is the price of one sandboxed command against the
// same command on the host (`true`, run to its end): ns/op, and the process
// tree's user-space instructions (instr/op, perf_event_open with inherit,
// this thread's own wait included, the same in both) and its CPU time
// (user-ns/op, sys-ns/op, rusage of the reaped children). It is a
// measurement, not a test: run it under LOAD WAIT (1-minute load < 48),
//
//	go test ./runtime/v1 -run '^$' -bench BenchmarkExecSandbox -benchtime 200x -count 7
//
// and take the median of the 7 runs of each metric.
func BenchmarkExecSandbox(b *testing.B) {
	root := TestFolderOpen()
	defer TestFolderRemove(root)
	task := rt.Root("exec sandbox bench")
	for _, c := range []struct {
		name string
		run  func() error
	}{
		{"host", func() error { _, err := ExecRun(task, "true", nil, nil, root, "", 0); return err }},
		{"sandboxed", func() error { _, err := ExecRunIn(task, root, "true", nil, nil, root, "", 0); return err }},
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
