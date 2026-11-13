package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"testing"

	"ergo.local/prototype/internal/opcount"
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
// Method: internal/opcount's counter of user-space instructions (the
// repository's one perf_event_open site), enabled at the child's exec and
// inherited, so it counts the whole child process; a multiplexed (short)
// count or a run that fails is refused, never compared. Each program runs one worker with GOMAXPROCS=1, so
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
	if err := opcount.Available(); err != nil {
		t.Skipf("no hardware counter of user-space instructions: %v", err)
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
// returns its user-space instructions; a refused count fails the test.
func brcCount(t *testing.T, bin, path string) uint64 {
	t.Helper()
	command := exec.Command(bin, path, "1")
	command.Env = append(os.Environ(), "GOMAXPROCS=1")
	var out bytes.Buffer
	command.Stdout, command.Stderr = &out, &out
	r, err := opcount.Count(command, 2<<30)
	if err != nil {
		t.Fatalf("%s %s 1: %v\n%s", bin, path, err, out.Bytes())
	}
	if id, why := r.Refusal(); id != "" {
		t.Fatalf("%s %s 1: refused %s: %s\n%s", bin, path, id, why, out.Bytes())
	}
	return r.Instructions
}

func brcMedian(xs []float64) float64 {
	s := slices.Clone(xs)
	slices.Sort(s)
	return s[len(s)/2]
}
