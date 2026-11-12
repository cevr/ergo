package main

import (
	"bytes"
	"encoding/binary"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"syscall"
	"testing"
	"unsafe"

	"ergo.local/prototype/internal/proclimit"
)

// TestOneBRCCountsAsHandGo is the counted guard of the owner's speed goal
// on examples/onebrc (1BRC): the Ergo program runs no more user-space
// instructions per row than the hand Go control (testdata/onebrc/control,
// the same algorithm), as a ratio of counts, never of time. It builds Go
// because the claim is about the machine code gc makes of the lowered
// program, which no in-process check of the lowered Go shows: it links
// `ergo build` of the example, and `go build` of the control and the
// generator with the go line of the emitted module, so one toolchain
// compiles all. Each program runs bounded by proclimit, and the test
// waits for its exit.
//
// Method: hardware counters (perf_event_open, user space only), opened on
// the test's locked thread and inherited by the child, so they count the
// whole child process. Each program runs one worker with GOMAXPROCS=1, so
// no idle thread spins into the count. A run on 1,000,000 rows is
// subtracted from one on 11,000,000 rows of the same seed, which leaves
// 10,000,000 rows and none of the start-up or the report; the programs
// alternate over 7 rounds and the medians are compared. Both print the
// same summary of each input (checked first, uncounted), so they do the
// same work. The count per row does not depend on the number of rows, so
// 10,000,000 stand for the spec's 100,000,000;
// ONEBRC_COUNT_ROWS=100000000 runs the test on that many.
//
// A host with no counter of user-space instructions (no PMU in the VM, or
// perf_event_paranoid above 2) skips the test and says why.
//
// The bound is 0.95 since BCE1 (2026-11-12): the bounds facts take the
// checks off the fast path's word loads, and onebrc ran 283.24
// instructions per row to the control's 303.81 (0.932; 100,000,000 rows,
// 7 rounds; 299.05 before). The control keeps its checks: it is the
// algorithm as a Go programmer writes it.
//
// Mutants: const-let-never (lower ledger: package lets stay Go variables,
// so the table's mask and bounds are loads) puts onebrc above the control,
// and bounds-unchecked-never (BCE1: no load is unchecked) above the bound.
func TestOneBRCCountsAsHandGo(t *testing.T) {
	needsBuild(t)
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if fd, err := brcOpenCounter(); err != nil {
		t.Skipf("no hardware counter of user-space instructions: %v", err)
	} else {
		syscall.Close(fd)
	}
	rows := 10_000_000
	if s := os.Getenv("ONEBRC_COUNT_ROWS"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 {
			t.Fatalf("ONEBRC_COUNT_ROWS=%q: want a positive count of rows", s)
		}
		rows = n
	}
	ergoBin, work := buildOneBRC(t)
	control := buildOneBRCGo(t, work, "control")
	gen := buildOneBRCGo(t, work, "gen")
	dir := t.TempDir()
	low, high := 1_000_000, 1_000_000+rows
	inputs := map[int]string{}
	for _, n := range []int{low, high} {
		path := filepath.Join(dir, strconv.Itoa(n)+".txt")
		f, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		command := exec.Command(gen, strconv.Itoa(n), "7")
		command.Stdout = f
		err = proclimit.Run(command, 1<<30)
		f.Close()
		if err != nil {
			t.Fatalf("gen %d: %v", n, err)
		}
		inputs[n] = path
		if a, b := brcOutput(t, ergoBin, path), brcOutput(t, control, path); !bytes.Equal(a, b) {
			t.Fatalf("%s: the hand control printed %q, onebrc %q", path, b, a)
		}
	}
	const rounds = 7
	var ergo, hand []float64
	for range rounds {
		for _, p := range []struct {
			bin string
			per *[]float64
		}{{ergoBin, &ergo}, {control, &hand}} {
			lo := brcCount(t, p.bin, inputs[low])
			hi := brcCount(t, p.bin, inputs[high])
			*p.per = append(*p.per, float64(hi-lo)/float64(rows))
		}
	}
	ratio := brcMedian(ergo) / brcMedian(hand)
	t.Logf("per row over %d rows: onebrc %.2f instructions, hand Go %.2f; ratio %.3f", rows, brcMedian(ergo), brcMedian(hand), ratio)
	if ratio > 0.95 {
		t.Errorf("onebrc runs %.3f of the hand control's instructions per row; want at most 0.95", ratio)
	}
}

// brcOutput is what `bin path 1` prints, stdout and stderr.
func brcOutput(t *testing.T, bin, path string) []byte {
	t.Helper()
	command := exec.Command(bin, path, "1")
	var out bytes.Buffer
	command.Stdout, command.Stderr = &out, &out
	if err := proclimit.Run(command, 2<<30); err != nil {
		t.Fatalf("%s %s 1: %v\n%s", bin, path, err, out.Bytes())
	}
	return out.Bytes()
}

// brcCount runs `bin path 1` with GOMAXPROCS=1 under the counter and
// returns its user-space instructions.
func brcCount(t *testing.T, bin, path string) uint64 {
	t.Helper()
	fd, err := brcOpenCounter()
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.Close(fd)
	command := exec.Command(bin, path, "1")
	command.Env = append(os.Environ(), "GOMAXPROCS=1")
	var out bytes.Buffer
	command.Stdout, command.Stderr = &out, &out
	brcIoctl(fd, brcPerfEnable)
	err = proclimit.Run(command, 2<<30)
	brcIoctl(fd, brcPerfDisable)
	if err != nil {
		t.Fatalf("%s %s 1: %v\n%s", bin, path, err, out.Bytes())
	}
	var b [8]byte
	if n, err := syscall.Read(fd, b[:]); err != nil || n != 8 {
		t.Fatalf("read the counter: %d bytes, %v", n, err)
	}
	return binary.NativeEndian.Uint64(b[:])
}

// brcPerfEventAttr is the kernel's struct perf_event_attr (version 8).
type brcPerfEventAttr struct {
	Type, Size                    uint32
	Config, Sample                uint64
	SampleType, ReadFormat, Flags uint64
	Wakeup, BPType                uint32
	BPAddr, BPLen                 uint64
	BranchSampleType              uint64
	SampleRegsUser                uint64
	SampleStackUser               uint32
	ClockID                       int32
	SampleRegsIntr                uint64
	AuxWatermark                  uint32
	SampleMaxStack, _             uint16
	AuxSampleSize, _              uint32
	SigData, Config3              uint64
}

const (
	brcHardwareInstructions = 1
	// disabled | inherit | exclude_kernel | exclude_hv
	brcPerfFlags   = 1<<0 | 1<<1 | 1<<5 | 1<<6
	brcPerfEnable  = 0x2400
	brcPerfDisable = 0x2401
)

// brcOpenCounter opens the instructions counter of the calling thread
// (locked by the caller), disabled and inherited by the processes it
// starts.
func brcOpenCounter() (int, error) {
	attr := brcPerfEventAttr{Config: brcHardwareInstructions, Flags: brcPerfFlags}
	attr.Size = uint32(unsafe.Sizeof(attr))
	fd, _, errno := syscall.Syscall6(syscall.SYS_PERF_EVENT_OPEN, uintptr(unsafe.Pointer(&attr)), 0, ^uintptr(0), ^uintptr(0), 0, 0)
	if errno != 0 {
		return -1, errno
	}
	return int(fd), nil
}

func brcIoctl(fd int, request uintptr) {
	syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), request, 0)
}

func brcMedian(xs []float64) float64 {
	s := slices.Clone(xs)
	slices.Sort(s)
	return s[len(s)/2]
}
