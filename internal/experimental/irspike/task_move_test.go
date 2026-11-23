package irspike

import "testing"

// TestIRTaskMovesMatchSource is the seeded property over task blocks that
// move the frame's resources (GenerateTaskMove): a literal run once owns
// what it moves, a Set's repeated block lends what its member's root
// owns, and the sender hands each capture out at the call. Both literals
// have IR bodies, and the program prints the same under both lowerings,
// each close in its place. A failing seed prints the Ergo source, both
// outcomes and its replay command.
//
// It builds Go because the blocks run on tasks of their own and the
// closes run at their frames' and roots' ends; only the compiled program
// runs them.
//
// Mutants (not run, CRUNCH): task-move-go-hand-dropped (task.Go's sender
// keeps the moved Res), task-move-set-hand-dropped (the Set member's
// sender keeps it), task-move-root-unowned (the member's root owns
// nothing).
func TestIRTaskMovesMatchSource(t *testing.T) {
	f7Property(t, "TestIRTaskMovesMatchSource", GenerateTaskMove, taskMoveFuncs...)
}
