package irspike

import "testing"

// TestIRDirectEncodesMatchSource is the seeded property over encodes on
// json's Text, a direct writer whose provider is a cell
// (PORT-ENCODE-DIRECT, GenerateDirectEncode): each program prints the
// same under both lowerings, and each site has an IR body. A failing seed
// prints the Ergo source, both outcomes and its replay command.
//
// It builds Go because the cell the encodes write in place, its state
// passed by value through the direct codec functions, shows only when the
// compiled program runs them.
func TestIRDirectEncodesMatchSource(t *testing.T) {
	f7Property(t, "TestIRDirectEncodesMatchSource", GenerateDirectEncode, directEncodeSites...)
}
