package irspike

import "testing"

// TestIRQueueTuplePopsMatchSource is the seeded property over bodies that
// pop a queue of pairs (GenerateQueueTuple): an operation whose runtime
// method returns one value and an error, the value a tuple's struct, has
// an IR body, and the program prints the same under both lowerings: the
// pair destructured, returned, or replaced when Closed is caught. A
// failing seed prints the Ergo source, both outcomes and its replay
// command.
//
// It builds Go because the queue's Pop runs only in the compiled program.
//
// Mutants: adapted-pop-tuple-refused (a tuple-valued Pop refused again,
// the gate before ADAPTED-RESULTS).
func TestIRQueueTuplePopsMatchSource(t *testing.T) {
	f7Property(t, "TestIRQueueTuplePopsMatchSource", GenerateQueueTuple, queueTupleFuncs...)
}
