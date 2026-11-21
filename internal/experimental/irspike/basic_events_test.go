package irspike

import "testing"

// TestIRBasicEventsMatchSource is the seeded property over encodes and
// decodes of basic values, one event each (PORT-ENCODE-DIRECT,
// GenerateBasicEvents): on the reference Writer and on json's Text, a
// direct writer whose provider is a cell. Each program prints the same
// under both lowerings, and each site has an IR body. A failing seed
// prints the Ergo source, both outcomes and its replay command.
//
// It builds Go because the events reach a provider's state (the Events'
// log, the Text's cell) only when the compiled program runs them.
func TestIRBasicEventsMatchSource(t *testing.T) {
	f7Property(t, "TestIRBasicEventsMatchSource", GenerateBasicEvents, basicEventSites...)
}
