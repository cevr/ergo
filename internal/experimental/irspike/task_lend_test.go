package irspike

import "testing"

// TestIRTaskLendsMatchSource is the seeded property over task.Go blocks
// that borrow the frame's resources (GenerateTaskLend): the literal a task
// captures, moving nothing, has an IR body, and the program prints the
// same under both lowerings, the task's reads and the closes after the
// waits included. A failing seed prints the Ergo source, both outcomes and
// its replay command.
//
// It builds Go because the block runs on a task of its own and the closes
// run at a block's end and the frame's deferred End; only the compiled
// program runs them.
//
// Mutants: task-lend-refused (a lending task literal refused again, the
// gate before TASK-CAPTURES).
func TestIRTaskLendsMatchSource(t *testing.T) {
	f7Property(t, "TestIRTaskLendsMatchSource", GenerateTaskLend, taskLendFuncs...)
}
