package opcount_test

import (
	"testing"

	"ergo.local/prototype/internal/opcount"
)

// Ergo's runtime is the package path an emitted program links it under,
// ergo.local/prototype/runtime/..., not the module folder that holds its
// copy. The smallest repro of the bug the backfill found: the first
// classifier matched ergo.local/program/ergo/runtime/v1, so every receipt
// read InstrErgo 0 and put Ergo's runtime in InstrProgram.
//
// Mutant: share-ergo-runtime-path.
func TestErgoRuntimeIsItsPart(t *testing.T) {
	for _, name := range []string{"ergo.local/prototype/runtime/v1.LineageAppend[go.shape.struct { Text string; Mine bool }]", "ergo.local/prototype/runtime/v1.(*Lineage).take", "ergo.local/prototype/runtime/rt.(*Cause).Add"} {
		if got := opcount.Classify(name); got != opcount.Ergo {
			t.Errorf("%s: part %s, want %s", name, got.Counter(), opcount.Ergo.Counter())
		}
	}
}

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
