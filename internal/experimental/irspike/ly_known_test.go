package irspike

import "testing"

// TestIRKnownRecordsMatchSource is the seeded property over copies for a
// known record (F7 "copy for a known record", GenerateKnownRecord): each
// program prints the same under both lowerings, the callers and the
// copies have IR bodies, and the IR calls each copy and, inside a copy,
// the step function directly. A failing seed prints the Ergo source, both
// outcomes and its replay command.
//
// It builds Go because a copy that calls the wrong function, or a call
// that takes the wrong copy, shows only when the compiled program folds
// its signals.
func TestIRKnownRecordsMatchSource(t *testing.T) {
	lyProperty(t, "TestIRKnownRecordsMatchSource", GenerateKnownRecord, []string{"foldA", "foldGA", "outerA", "stepA"},
		"runA", "runG", "runO", "foldA", "foldGA", "outerA")
}
