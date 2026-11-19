package irspike

import "testing"

// TestIRStoresMatchSource is the seeded property over stores of calls that
// update their place (B-STOREPLACE, GenerateStore): a Map field written by
// Put and Delete and stored back into itself, and a slice field grown by
// Append, through a pointer and in a local record. Each program prints the
// same under both lowerings, and the functions that store have IR bodies.
// A failing seed prints the Ergo source, both outcomes and its replay
// command.
//
// It builds Go because an update applied to a copy, lost or applied twice
// shows only when the compiled program reads the record back.
func TestIRStoresMatchSource(t *testing.T) {
	f7Property(t, "TestIRStoresMatchSource", GenerateStore, storeFuncs...)
}
