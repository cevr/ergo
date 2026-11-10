package main

import (
	"strings"
	"testing"
)

// TestCappedFaultIsMemory catches gcoom-examples-text-only: the example
// runner calls a span-queue allocation fault a crash. It classifies recorded
// output in process; no program is built or run. The signal, PC line and
// frame below are an excerpt of the measured go1.27.0 batch-15 probe under
// RLIMIT_DATA=256 MiB (paths and other goroutines omitted).
func TestCappedFaultIsMemory(t *testing.T) {
	const fault = "SIGSEGV: segmentation violation\nPC=0x42d21c m=3 sigcode=1 addr=0x0\n\ngoroutine 0 gp=0x16d1809db2c0 m=3 mp=0x16d180a13008 [idle]:\nruntime.(*spanQueue).tryDrain(0x16d100000400?, 0x435545?, 0x55fa48?)\n"
	for _, sample := range []struct {
		name, stderr string
		want         bool
	}{
		{"span queue", fault, true},
		{"out of memory", "fatal error: out of memory allocating heap arena metadata\n", true},
		{"other frame", strings.ReplaceAll(fault, "runtime.(*spanQueue)", "runtime.(*mheap)"), false},
	} {
		for _, status := range []int{2, -1, 0} {
			for _, memory := range []uint64{256 << 20, 0} {
				o := outcome{status: status, stderr: []byte(sample.stderr)}
				o.classifyLimit(memory)
				want := sample.want && status != 0 && memory != 0
				if o.limited != want {
					t.Errorf("%s status=%d cap=%d: limited=%t, want %t", sample.name, status, memory, o.limited, want)
				}
			}
		}
	}
}
