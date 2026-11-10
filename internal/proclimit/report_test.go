package proclimit_test

import (
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"

	"ergo.local/prototype/internal/proclimit"
)

// queueFault is the stderr of a Go 1.27 program that reached its data limit
// while the collector allocated a span-queue ring (golang/go#82667).
// Measured: the batch-15 probe under a 256 MiB RLIMIT_DATA on go1.27.0,
// with the toolchain path shortened to /goroot and the middle goroutines
// and registers cut.
const queueFault = `probe: filled writable mapping headroom; request one GC cycle
SIGSEGV: segmentation violation
PC=0x42d21c m=3 sigcode=1 addr=0x0

goroutine 0 gp=0x16d1809db2c0 m=3 mp=0x16d180a13008 [idle]:
runtime.(*spanQueue).tryDrain(0x16d100000400?, 0x435545?, 0x55fa48?)
	/goroot/src/runtime/mgcmark_greenteagc.go:520 +0x5c fp=0x16d180a29ea0 sp=0x16d180a29e80 pc=0x42d21c
runtime.(*spanQueue).drain(0x16d180a03278, 0x8)
	/goroot/src/runtime/mgcmark_greenteagc.go:473 +0x66 fp=0x16d180a29ed0 sp=0x16d180a29ea0 pc=0x42d106
runtime.(*spanQueue).flush(...)
	/goroot/src/runtime/mgcmark_greenteagc.go:430
runtime.(*gcWork).dispose(0x16d180a03260)
	/goroot/src/runtime/mgcwork.go:325 +0xbe fp=0x16d180a29ef0 sp=0x16d180a29ed0 pc=0x43513e
runtime.gcMarkDone.func1(0x16d180a02008)
	/goroot/src/runtime/mgc.go:1034 +0x2a fp=0x16d180a29f08 sp=0x16d180a29ef0 pc=0x4764ea

goroutine 1 gp=0x16d1809da1e0 m=nil [wait for GC cycle]:
runtime.GC()
	/goroot/src/runtime/mgc.go:558 +0x45 fp=0x16d180a38e10 sp=0x16d180a38dd8 pc=0x425245
main.main()
	/probe/main.go:55 +0x2d1 fp=0x16d180a38eb8 sp=0x16d180a38e10 pc=0x492cb1

rax    0x16d180a03278
rsi    0x0
r9     0x0
rip    0x42d21c
`

// nilDereference is the stderr of a program whose own code read through a
// nil pointer: a segmentation fault at address 0 that is a defect, not a
// refused allocation. Measured: a three-line program built with go1.27.0
// -trimpath.
const nilDereference = `panic: runtime error: invalid memory address or nil pointer dereference
[signal SIGSEGV: segmentation violation code=0x1 addr=0x0 pc=0x47db10]

goroutine 1 [running]:
main.main()
	nilprobe/main.go:7 +0x10
`

// TestQueueFaultIsNamed reads recorded stderr through Check, whole and
// through a Watcher in writes of several sizes. Only the span-queue fault at
// address 0 is the limit, named with its upstream issue; a nil dereference
// and the same fatal signal at another address or in another frame stay
// crashes. Mutants (testdata/mutants.json): the fault is not read
// (gcoom-fault-unread), any fatal SIGSEGV is the limit (gcoom-any-frame,
// gcoom-any-address), the message omits the issue (gcoom-issue-unnamed),
// a line split across writes is lost (gcoom-split-line).
func TestQueueFaultIsNamed(t *testing.T) {
	const oom = "fatal error: out of memory allocating heap arena metadata\n\ngoroutine 1 gp=0x1 m=0 [running]:\n"
	cases := []struct {
		name   string
		stderr string
		want   string // "" (a crash), "limit", or "issue" (the limit, named with the issue)
	}{
		{"span-queue fault", queueFault, "issue"},
		{"runtime out of memory", oom, "limit"},
		{"nil dereference", nilDereference, ""},
		{"fault in another frame", strings.ReplaceAll(queueFault, "runtime.(*spanQueue)", "runtime.(*mheap)"), ""},
		{"fault at another address", strings.Replace(queueFault, "addr=0x0", "addr=0x8", 1), ""},
	}
	exit := &exec.ExitError{ProcessState: &os.ProcessState{}}
	for _, c := range cases {
		for _, size := range []int{0, 1, 7} {
			stderr := []byte(c.stderr)
			if size > 0 {
				cmd := exec.Command("/nonexistent/proclimit-probe")
				watch := proclimit.Watch(cmd)
				for rest := stderr; len(rest) > 0; rest = rest[min(size, len(rest)):] {
					_, _ = cmd.Stderr.Write(rest[:min(size, len(rest))])
				}
				stderr = watch.Stderr()
			}
			err := proclimit.Check("probe", proclimit.DefaultLimit, exit, stderr)
			var limited *proclimit.LimitError
			got := ""
			if errors.As(err, &limited) {
				got = "limit"
				if strings.Contains(err.Error(), "golang/go#82667") {
					got = "issue"
				}
			}
			if got != c.want {
				t.Errorf("%s, writes of %d bytes (0: whole): Check says %q (%v); want %q", c.name, size, got, err, c.want)
			}
		}
	}
	if err := proclimit.Check("probe", 0, exit, []byte(queueFault)); err != exit {
		t.Errorf("a span-queue fault with no limit: Check returns %v; want the exit error", err)
	}
}
