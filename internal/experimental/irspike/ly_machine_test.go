package irspike

import (
	"testing"

	"ergo.local/prototype/internal/sketch/lower"
)

// TestIRMachinesMatchSource is the seeded property over machine values (F7
// "machine value", GenerateMachine): each program prints the same under
// both lowerings, the bodies that convert a machine have IR bodies, and
// the IR calls each machine's generated value function. A failing seed
// prints the Ergo source, both outcomes and its replay command.
//
// It builds Go because a wrong Machine (its initial value, start or step)
// shows only when the compiled program folds it over signals.
func TestIRMachinesMatchSource(t *testing.T) {
	lyProperty(t, "TestIRMachinesMatchSource", GenerateMachine, []string{lower.HelperSpelling("", "ergoMachineA"), lower.HelperSpelling("", "ergoMachineB"), lower.HelperSpelling("", "ergoMachineC"), "MachineTimerPanic"},
		"planA", "planB", "firstA", "planC", "planOwned", lower.HelperSpelling("", "ergoMachineA"), lower.HelperSpelling("", "ergoMachineB"), lower.HelperSpelling("", "ergoMachineC"), lower.HelperSpelling("", "ergoMachineOwnedMachine"))
}
