package irspike

import "testing"

// TestIRUpdatesMatchSource is the seeded property over record updates
// (F7, GenerateUpdate): R{..r, F: v} with a parameter and a package let as
// the base. Each program prints the same under both lowerings, and the
// update functions have IR bodies. A failing seed prints the Ergo source,
// both outcomes and its replay command.
//
// It builds Go because the literal's kept fields are read from the base by
// Go names the patched compile resolves, and a field kept wrong shows only
// in the compiled program.
func TestIRUpdatesMatchSource(t *testing.T) {
	f7Property(t, "TestIRUpdatesMatchSource", GenerateUpdate, "R.up0", "R.up1", "R.up2")
}
