package irspike

import "testing"

// TestIRMapStoresMatchSource is the seeded property over stores to a
// Map's entry (F7 LY-PLACE, GenerateMapStore): each program prints the
// same under both lowerings, and every storing function has an IR body.
// A failing seed prints the Ergo source, both outcomes and its replay
// command.
//
// It builds Go because the entry a store writes, under a key or into a
// map that its value's call moved, shows only when the compiled program
// runs it.
func TestIRMapStoresMatchSource(t *testing.T) {
	lyProperty(t, "TestIRMapStoresMatchSource", GenerateMapStore, nil, "fill", "wipe", "boxed", "heldOwned")
}
