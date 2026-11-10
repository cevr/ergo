package v1

// movesOnly is the reviewed table of generic bodies in this package that
// only move the values of their type parameters: each value it is given
// is stored once, handed back once, or released, and never duplicated
// (#197 L8 A3; plan 1.3, D-B). The checker reads it through MovesOnly: a
// generic extern with no entry copies each type parameter, so a call that
// binds one to a resource is refused (resources R44). The names are
// BodyOf names. A body joins the table only after review of its Go code;
// TestMovesOnlyNamesBodies checks that each name is a generic body here.
//
// Not listed, so refused with a resource: the machine runner and the actor
// (they copy the state into snapshots), sync.Ref's Modify (it returns the
// old value beside the new), and the slice and Map helpers that return a
// second slice or Map over the same elements.
var movesOnly = map[string]bool{
	"New":            true, // queue.New: an empty queue; Push takes, Pop gives (Q7)
	"Append":         true, // prelude Append: takes xs and x, gives one slice
	"WithCapacity":   true, // an empty slice
	"Go":             true, // task.Go: the body's result moves to Wait
	"TasksRace":      true, // the winner's result moves out; a loser's is released (Gap 47, B4)
	"TasksTransform": true, // each result moves into the output once
	"After":          true, // timeout.After: the body's result moves out
}

// MovesOnly reports whether the generic body name only moves its type
// parameters' values (movesOnly).
func MovesOnly(name string) bool { return movesOnly[name] }

// running is the reviewed table of std releases that stop code still
// running (#197 L8 B3t): a value whose release is one of them runs the
// code it was started with, in the environment it was started in, until
// it is released. The checker reads it through StopsRunning: such a value
// never leaves the with whose providers its code may use (resources.md,
// Release before the end). The names are BodyOf names.
var running = map[string]bool{
	"Task.Close": true, // task.Go's body runs until Wait, Stop or Close
	"Set.Close":  true, // a Set's members run until Wait, Stop or Close
}

// StopsRunning reports whether the release name stops running code
// (running).
func StopsRunning(name string) bool { return running[name] }
