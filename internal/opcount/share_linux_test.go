//go:build linux

package opcount_test

import (
	"math"
	"os"
	"os/exec"
	"testing"

	"ergo.local/prototype/internal/opcount"
)

// shares runs this test binary as the child program mode under
// CountShares.
func shares(t *testing.T, mode string) (opcount.Run, opcount.Shares) {
	t.Helper()
	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(), "OPCOUNT_TEST_CHILD="+mode)
	r, s, err := opcount.CountShares(cmd, 1<<30, os.Args[0])
	if err != nil {
		t.Fatalf("%s: %v", mode, err)
	}
	return r, s
}

// The parts of a run sum to its count: the sampling events count the
// instructions the counting event counts, over the whole process tree (a
// child the program waits for, the threads the runtime starts), and each
// part is its share of the samples of that count. Real processes, because
// the claim is about the kernel's sampling of a real process tree, which
// no in-process check shows; it builds no Go (the children are this test
// binary), each child is bounded by proclimit, and the test waits for its
// exit. A host that cannot count or sample skips it and says why.
//
// Mutants: share-no-inherit (the sampling events count none of the
// program: PartsShort), share-runtime-as-program (the allocating child's
// allocation and collector parts are 0).
func TestPartsSumToTheCount(t *testing.T) {
	if err := opcount.Available(); err != nil {
		t.Skipf("no counter: %v", err)
	}
	for _, mode := range []string{"spawn:100000000", "alloc:10000000"} {
		r, s := shares(t, mode)
		if s.Refused == opcount.NoSampler {
			t.Skipf("no sampler: %s", s.Why)
		}
		if s.Refused != "" {
			t.Fatalf("%s: parts refused %s: %s (%d samples of period %d; %d counted, %d sampled-counted)", mode, s.Refused, s.Why, s.Total(), opcount.Period(), r.Instructions, s.Counted)
		}
		sum := 0.0
		for _, p := range opcount.Parts {
			sum += s.Instructions(p)
		}
		if d := math.Abs(sum - float64(r.Instructions)); d > opcount.PartsTolerance*float64(r.Instructions)+1 {
			t.Errorf("%s: the parts sum to %.0f of the run's %d instructions", mode, sum, r.Instructions)
		}
		t.Logf("%s: %d instructions, %d sampled-counted, %d samples: %v", mode, r.Instructions, s.Counted, s.Total(), s.Samples)
	}
	_, s := shares(t, "alloc:10000000")
	if s.Samples[opcount.Alloc] == 0 || s.Samples[opcount.GC] == 0 {
		t.Errorf("a child that allocates 10M objects: %d allocation and %d collector samples of %d", s.Samples[opcount.Alloc], s.Samples[opcount.GC], s.Total())
	}
}
