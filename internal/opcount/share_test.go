package opcount_test

import (
	"testing"

	"ergo.local/prototype/internal/opcount"
)

// Parts whose sampling events counted less than the run are refused: the
// shares are then of part of the run (a throttled event stops counting
// while stopped). The smallest repro of the rule, since the kernel's
// throttle cannot be caused on demand in a test.
//
// Mutant: parts-short-admitted.
func TestShortPartsAreRefused(t *testing.T) {
	run := opcount.Run{Instructions: 1_000_000}
	for counted, want := range map[uint64]opcount.Refusal{1_000_000: "", 990_000: opcount.PartsShort, 1_010_000: opcount.PartsShort} {
		s := opcount.Shares{Counted: counted}
		s.Samples[opcount.Program] = 10
		s.Check(run)
		if s.Refused != want {
			t.Errorf("sampling events counted %d of %d: refusal %q (%s), want %q", counted, run.Instructions, s.Refused, s.Why, want)
		}
	}
}
