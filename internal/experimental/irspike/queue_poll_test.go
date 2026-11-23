package irspike

import "testing"

// TestIRQueuePollsMatchSource is the seeded property over bodies that poll
// queues (GenerateQueuePoll): an operation whose runtime method returns
// Go's (T, bool) has an IR body that makes the Option[T], and the program
// prints the same under both lowerings, for ints, strings and records,
// empty and filled, closed and open. A failing seed prints the Ergo
// source, both outcomes and its replay command.
//
// It builds Go because the Option is a Go value only the compiled program
// matches; in process the IR's text says what it builds, not what it
// returns.
//
// Mutants: adapted-poll-refused (Poll's (T, bool) refused again, the gate
// before ADAPTED-RESULTS), adapted-poll-ok-ignored (the Some is made
// whatever ok is), adapted-poll-some-when-not-ok (Some when not ok, None
// when ok).
func TestIRQueuePollsMatchSource(t *testing.T) {
	f7Property(t, "TestIRQueuePollsMatchSource", GenerateQueuePoll, queuePollFuncs...)
}
