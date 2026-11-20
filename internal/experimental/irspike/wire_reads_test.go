package irspike

import "testing"

// TestIRWireReadsMatchSource is PORT-WIRE-READ's property: decode on a
// direct reader (json's Reader, a user reader rd, and rd through a
// generic's codec witness) takes the IR, and its program prints what the
// source path's prints, with the build-time fold (CF1) on and off.
// GenerateWireReads's rd reads String and StringBytes apart, so the
// evaluator's reading of an append-form pair shows in the folded program.
// Mutants: pwr-direct-decode-adapted (the reader goes through its Reader
// adapter: the decode function of the interface path is called with the
// direct reader's value), pwr-direct-read-witness-dec (a type parameter's
// direct decode calls the witness's Dec), and eval's
// eval-pair-append-reads-string (the fold reads the pair through String).
//
// It builds Go because the other side of the agreement is the built
// program, by both paths.
func TestIRWireReadsMatchSource(t *testing.T) {
	f7Property(t, "TestIRWireReadsMatchSource", GenerateWireReads, wireReadSites...)
}
