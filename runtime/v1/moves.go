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
	"SlicesTake":     true, // the element and the disjoint remainder move out
	"MapTake":        true, // a found value and the disjoint remainder move out
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

// Inert reports a body that acts on nothing outside the value it makes:
// it reads its hidden arguments, so it is not marked pure, but it runs no
// capability, starts no task and calls no Ergo code, so a call made again
// in a replay acts on nothing (a reviewed body fact; the checker reads it
// for any extern that binds the body, check/body.go). NewHost makes an
// empty host cell, which the actor's entry binds (actor_host.go; DH1;
// runtime.md, Machines and actors 17): a durable step may make hosts.
func Inert(name string) bool { return name == "NewHost" }

// MovingBody names the callback whose captures move to a child task's
// root lifetime (R12). Repeated is true when the callback can run again:
// it only lends those captures, as any repeated nested frame does. This
// is a reviewed body fact, not a check on an Ergo library name.
func MovingBody(name string) (param int, repeated, ok bool) {
	switch name {
	case "Go":
		return 0, false, true
	case "TasksGo":
		return 1, true, true
	}
	return 0, false, false
}

// CallbackKeeper is the reviewed owner of a MovingBody's callback. -1 is
// the result; any other index is a parameter. Go's Task drains its body;
// TasksGo's Set drains every member, including restart runs.
func CallbackKeeper(name string) (int, bool) {
	switch name {
	case "Go":
		return -1, true
	case "TasksGo":
		return 0, true
	}
	return 0, false
}

// DrainsLends names operations which cannot return while their keeper's
// callback still runs. These facts describe the reviewed runtime bodies;
// no user operation is trusted from its spelling alone.
func DrainsLends(name string) bool {
	switch name {
	case "Task.Wait", "Task.Stop", "Task.Close", "Set.Wait", "Set.Stop", "Set.Close":
		return true
	}
	return false
}
