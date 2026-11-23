package irspike

import (
	"testing"

	"ergo.local/prototype/internal/sketch/lower"
)

// TestIRFlatWorkerQueueTupleMatchSource checks a fallible queue builder
// whose return type contains a tuple. That signature can span multiple
// lines of Go, but must still get a flat worker, not crash in lowering.
// Mutant: flat-worker-refuses-struct-result.
//
// It builds Go because the worker's declaration and IR body must agree
// in the native compiler, and the queue must still hold the seeded values.
func TestIRFlatWorkerQueueTupleMatchSource(t *testing.T) {
	f7PropertyWith(t, "TestIRFlatWorkerQueueTupleMatchSource", GenerateQueueTuple, func(r lower.IRReport) string {
		if lower.FlatOn() && r.FlatWorkers == 0 {
			return "no flat workers were emitted"
		}
		return ""
	}, "filled")
}
