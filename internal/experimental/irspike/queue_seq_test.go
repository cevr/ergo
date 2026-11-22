package irspike

import "testing"

// TestIRQueueSeqsMatchSource is the seeded property over bodies that
// iterate queues' Values (GenerateQueueSeq): an operation whose runtime
// method returns Go's iter.Seq[T] has an IR body that makes the Seq[T],
// and the program prints the same under both lowerings: the method runs
// when the Seq is iterated, a break stops it, a failure in the loop body
// leaves through it. A failing seed prints the Ergo source, both outcomes
// and its replay command.
//
// It builds Go because the Seq runs inside Go's range-over-func rewrite;
// only the compiled program iterates it.
//
// Mutants: adapted-seq-refused (the Seq refused again, the gate before
// ADAPTED-RESULTS), adapted-seq-false-ignored (the Seq goes on after yield
// returns false). adapted-seq-error-dropped is the ledger's gap: no Ergo
// consumer passes a yield that fails.
func TestIRQueueSeqsMatchSource(t *testing.T) {
	f7Property(t, "TestIRQueueSeqsMatchSource", GenerateQueueSeq, queueSeqFuncs...)
}
