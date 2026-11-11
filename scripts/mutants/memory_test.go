package main

import (
	"errors"
	"os/exec"
	"strings"
	"testing"

	"ergo.local/prototype/internal/proclimit"
)

// TestCappedFaultIsMemory catches gcoom-mutants-text-only: a capped child's
// span-queue allocation fault is reported as fail, not memory. The excerpt
// is from the measured go1.27.0 batch-15 probe at RLIMIT_DATA=256 MiB; the
// PC and frame are recorded output, not a process started by this test.
// Streaming through Watch also checks that a fault lost from the displayed
// tail still reaches the classifier.
func TestCappedFaultIsMemory(t *testing.T) {
	const fault = "SIGSEGV: segmentation violation\nPC=0x42d21c m=3 sigcode=1 addr=0x0\n\ngoroutine 0 gp=0x16d1809db2c0 m=3 mp=0x16d180a13008 [idle]:\nruntime.(*spanQueue).tryDrain(0x16d100000400?, 0x435545?, 0x55fa48?)\n"
	exit := errors.New("exit status 2")
	for _, sample := range []struct {
		name, report string
		want         string
	}{
		{"span queue", fault, "memory"},
		{"out of memory", "fatal error: out of memory allocating heap arena metadata\n", "memory"},
		{"mapping refused", "fatal error: runtime: cannot allocate memory\n", "memory"},
		{"other frame", strings.ReplaceAll(fault, "runtime.(*spanQueue)", "runtime.(*mheap)"), "fail"},
	} {
		for _, size := range []int{1, 7} {
			out := &tail{}
			cmd := &exec.Cmd{Stdout: out, Stderr: out}
			watch := proclimit.Watch(cmd)
			// Keep the report outside both retained ends of the display.
			text := strings.Repeat("x", tailSize) + "\n" + sample.report + strings.Repeat("x", 3*tailSize)
			for len(text) > 0 {
				n := min(size, len(text))
				_, _ = cmd.Stderr.Write([]byte(text[:n]))
				text = text[n:]
			}
			for _, memory := range []uint64{256 << 20, 0} {
				want := sample.want
				if memory == 0 {
					want = "fail"
				}
				if got := childReason(exit, false, memory, watch.Stderr()); got != want {
					t.Errorf("%s writes=%d cap=%d: reason=%q, want %q", sample.name, size, memory, got, want)
				}
			}
			if got := childReason(nil, false, 256<<20, watch.Stderr()); got != "" {
				t.Errorf("success: reason=%q", got)
			}
			if got := childReason(exit, true, 256<<20, watch.Stderr()); got != "deadline" {
				t.Errorf("deadline: reason=%q", got)
			}
		}
	}
}
