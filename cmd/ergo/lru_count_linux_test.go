package main

import (
	"bytes"
	"encoding/binary"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"syscall"
	"testing"
	"unsafe"

	"ergo.local/prototype/internal/proclimit"
)

// TestLRUCountsAsHandGo is the counted guard of the owner's speed goal on
// examples/lru (OPT9): `lru bench` runs no more user-space instructions and
// no more branches per operation than the hand-written Go control
// (testdata/lruhand, the same loop on a generational arena), as a ratio of
// counts, never of time. It builds Go because the claim is about the
// machine code gc makes of the lowered program (OPT9's gap was a branch gc
// did not thread), which no in-process check of the lowered Go shows: it
// links `ergo build` of the example and `go build` of the control, with the
// go line of the emitted module, so one toolchain compiles both. Each
// program runs bounded by proclimit (RLIMIT_DATA, inherited by the go
// command it starts), and the test waits for its exit.
//
// Method: hardware counters (perf_event_open, user space only), opened on
// the test's locked thread and inherited by the child, so they count the
// whole child process. A run of 200,000 operations is subtracted from one
// of 2,200,000, which leaves 2,000,000 operations and none of the
// start-up; the two programs alternate over 7 rounds and the medians are
// compared. Both print the same checksum, so they do the same work. It
// kills lower's mutant opt9-fail-around-alias, which no other test or law
// sees (a catch whose value is named keeps its failure at the join: same
// behaviour, same counts of placed labels); with every inlined failure at
// the join, lru counted 2.9% more branches than the control (OPT9).
//
// A host with no counter of user-space instructions (no PMU in the VM, or
// perf_event_paranoid above 2) skips the test and says why.
func TestLRUCountsAsHandGo(t *testing.T) {
	needsBuild(t)
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if fds, err := openCounters(); err != nil {
		t.Skipf("no hardware counters: %v", err)
	} else {
		closeCounters(fds)
	}
	ergoBin, work := buildLRU(t)
	handBin := buildLRUHand(t, work)

	const rounds, low, high = 7, 200_000, 2_200_000
	type counts struct{ ins, br []float64 }
	var lru, hand counts
	for range rounds {
		for _, p := range []struct {
			bin string
			c   *counts
		}{{ergoBin, &lru}, {handBin, &hand}} {
			loIns, loBr := countRun(t, p.bin, low, ergoBin)
			hiIns, hiBr := countRun(t, p.bin, high, ergoBin)
			p.c.ins = append(p.c.ins, float64(hiIns-loIns)/(high-low))
			p.c.br = append(p.c.br, float64(hiBr-loBr)/(high-low))
		}
	}
	ins, br := median(lru.ins)/median(hand.ins), median(lru.br)/median(hand.br)
	t.Logf("per operation: lru %.1f instructions, %.1f branches; hand Go %.1f, %.1f; ratios %.3f, %.3f",
		median(lru.ins), median(lru.br), median(hand.ins), median(hand.br), ins, br)
	// Instructions may be 1% over the control's (one program's count
	// varies by about that between rounds); branches may not be over it
	// (OPT9 measured lru 2.5% under it, and 2.9% over at the join).
	if ins > 1.01 || br > 1.00 {
		t.Errorf("lru bench runs %.3f of the hand control's instructions and %.3f of its branches per operation; want at most 1.01 and 1.00", ins, br)
	}
}

// buildLRU links examples/lru with `ergo build` and returns the binary
// and the emitted module's directory.
func buildLRU(t *testing.T) (bin, work string) {
	t.Helper()
	program := filepath.Join(smokeWorkspace(t, "examples/lru"), "examples", "lru", "cmd", "lru")
	bin = filepath.Join(t.TempDir(), "lru")
	command := exec.Command(buildTestCLI(t), "build", "-work", "-o", bin, program)
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := proclimit.Run(command, 4<<30); err != nil {
		t.Fatalf("ergo build: %v\n%s%s", err, stdout.Bytes(), stderr.Bytes())
	}
	return bin, workDir(t, stderr.String())
}

// buildLRUHand links the hand control in a module with the go line of
// lru's emitted module in work (buildLRU) and returns the binary.
func buildLRUHand(t *testing.T, work string) string {
	t.Helper()
	mod, err := os.ReadFile(filepath.Join(work, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	goLine := regexp.MustCompile(`(?m)^go [0-9.]+$`).Find(mod)
	if goLine == nil {
		t.Fatalf("lru's emitted go.mod has no go line:\n%s", mod)
	}
	src, err := os.ReadFile(filepath.Join("testdata", "lruhand", "main.go"))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module lruhand\n\n"+string(goLine)+"\n")
	writeFile(t, filepath.Join(dir, "main.go"), string(src))
	bin := filepath.Join(dir, "lruhand")
	command := exec.Command("go", "build", "-o", bin, ".")
	command.Dir = dir
	var out bytes.Buffer
	command.Stdout, command.Stderr = &out, &out
	if err := proclimit.Run(command, 4<<30); err != nil {
		t.Fatalf("go build of the hand control: %v\n%s", err, out.Bytes())
	}
	return bin
}

// countRun runs `bin bench n` under counters and returns its user-space
// instructions and branches. Its output must be lru's (`ref bench n`, run
// uncounted), so both programs do the same work.
func countRun(t *testing.T, bin string, n int, ref string) (ins, br uint64) {
	t.Helper()
	fds, err := openCounters()
	if err != nil {
		t.Fatal(err)
	}
	defer closeCounters(fds)
	command := exec.Command(bin, "bench", strconv.Itoa(n))
	command.Env = append(os.Environ(), "GOMAXPROCS=1")
	var out bytes.Buffer
	command.Stdout, command.Stderr = &out, &out
	for _, fd := range fds {
		ioctl(fd, perfEnable)
	}
	err = proclimit.Run(command, 1<<30)
	for _, fd := range fds {
		ioctl(fd, perfDisable)
	}
	if err != nil {
		t.Fatalf("%s bench %d: %v\n%s", bin, n, err, out.Bytes())
	}
	if bin != ref {
		want := exec.Command(ref, "bench", strconv.Itoa(n))
		var wantOut bytes.Buffer
		want.Stdout, want.Stderr = &wantOut, &wantOut
		if err := proclimit.Run(want, 1<<30); err != nil {
			t.Fatalf("%s bench %d: %v\n%s", ref, n, err, wantOut.Bytes())
		}
		if !bytes.Equal(out.Bytes(), wantOut.Bytes()) {
			t.Fatalf("bench %d: the hand control printed %q, lru %q", n, out.Bytes(), wantOut.Bytes())
		}
	}
	return readCounter(t, fds[0]), readCounter(t, fds[1])
}

// perfEventAttr is the kernel's struct perf_event_attr (version 8).
type perfEventAttr struct {
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
	perfHardwareInstructions = 1
	perfHardwareBranches     = 4
	// disabled | inherit | exclude_kernel | exclude_hv
	perfFlags   = 1<<0 | 1<<1 | 1<<5 | 1<<6
	perfEnable  = 0x2400
	perfDisable = 0x2401
)

// openCounters opens the instructions and branches counters of the
// calling thread (locked by the caller), disabled and inherited by the
// processes it starts.
func openCounters() ([]int, error) {
	var fds []int
	for _, config := range []uint64{perfHardwareInstructions, perfHardwareBranches} {
		attr := perfEventAttr{Config: config, Flags: perfFlags}
		attr.Size = uint32(unsafe.Sizeof(attr))
		fd, _, errno := syscall.Syscall6(syscall.SYS_PERF_EVENT_OPEN, uintptr(unsafe.Pointer(&attr)), 0, ^uintptr(0), ^uintptr(0), 0, 0)
		if errno != 0 {
			closeCounters(fds)
			return nil, errno
		}
		fds = append(fds, int(fd))
	}
	return fds, nil
}

func closeCounters(fds []int) {
	for _, fd := range fds {
		syscall.Close(fd)
	}
}

func ioctl(fd int, request uintptr) {
	syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), request, 0)
}

func readCounter(t *testing.T, fd int) uint64 {
	t.Helper()
	var b [8]byte
	if n, err := syscall.Read(fd, b[:]); err != nil || n != 8 {
		t.Fatalf("read a counter: %d bytes, %v", n, err)
	}
	return binary.NativeEndian.Uint64(b[:])
}

func median(xs []float64) float64 {
	s := slices.Clone(xs)
	slices.Sort(s)
	return s[len(s)/2]
}
