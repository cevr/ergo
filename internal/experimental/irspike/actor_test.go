package irspike

import "testing"

// TestIRActorsMatchSource is the seeded property over actors started on
// machines (hidden arguments, the graph; GenerateActor): each program
// prints the same under both lowerings, and the bodies that start an
// actor have IR bodies, std actor's launch included, whose call of the
// runtime's start passes the machine's graph accessor (v1.Graph). A
// failing seed prints the Ergo source, both outcomes and its replay
// command. Mutant ir-graph-refused (the graph argument refused again)
// fails it.
//
// It builds Go because the graph is read by the running actor (its
// trace and its state's type), which only a compiled run shows.
func TestIRActorsMatchSource(t *testing.T) {
	lyProperty(t, "TestIRActorsMatchSource", GenerateActor, nil, "runA", "planA", "launch")
}
