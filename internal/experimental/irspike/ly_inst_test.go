package irspike

import "testing"

// TestIRInstancesMatchSource is the seeded property over generic bodies
// run at the program's types (FLIP F7 LY-FIX, GenerateInstances): each
// program builds and prints the same under both lowerings, and the
// functions that call the copies have IR bodies. A failing seed prints
// the Ergo source, both outcomes and its replay command.
//
// It builds Go because a copy's body and its function's are two Go
// declarations, and only the Go build of the IR file shows that an IR
// body took the wrong one's name (a461a6b13: "Fold is not declared in
// the Go source").
func TestIRInstancesMatchSource(t *testing.T) {
	lyProperty(t, "TestIRInstancesMatchSource", GenerateInstances, nil, "tally", "spans", "size", "sized", "RunSwitch", "stepOnceSwitch")
}
