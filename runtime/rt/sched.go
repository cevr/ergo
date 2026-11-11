package rt

// The scheduling points of the explored build (runtime.md, Explored
// schedules). A test build with the ergoexplore tag runs every task of an
// explored run on one baton: one task runs at a time, and at each point a
// strategy picks the next task (sched_explore.go). Without the tag the
// hooks are empty functions with plain arguments (sched_live.go), so the
// compiler removes every call and the lowered Go text is the same in both
// builds: the tag is a build flag, never an input of the lowering.
//
// A point comes before the operation it names: a task announces its next
// operation, gives the baton back, and does the operation when it is picked.
// So between two points a task runs alone and touches one shared handle.
//
// Prior art: Loom and shuttle run one thread at a time on a baton and
// switch only at their own sync types; Concuerror owns the BEAM's send,
// receive and spawn; Coyote rewrites Task code. Ergo needs neither a type
// swap nor a rewrite: every wait of a lowered program is a call of this
// package or runtime/v1, so the seam is here.

// OpKind is the kind of a scheduling point's operation.
type OpKind uint8

// The operations. On names the handle each one touches; Start, Resume and
// End name the task itself or the source of its wake.
const (
	OpStart  OpKind = iota + 1 // a new task's first step
	OpResume                   // a woken task goes on; On is the source of its wake
	OpEnd                      // a task ends: its handle's done closes
	OpSpawn                    // a task starts a child
	OpJoin                     // a wait for a task's end (Handle.Wait)
	OpStop                     // a stop of a task (Handle.Stop, an actor's Stop)
	OpSleep                    // a sleep arms a timer on a clock
	OpPush                     // a push to a queue or an inbox
	OpPop                      // a take from a queue or an inbox
	OpClose                    // the end of a queue's or an inbox's admission
	OpRead                     // a read of a shared value (Ref.Get, a poll)
	OpWrite                    // a change of a shared value (Ref.Set, Update)
	OpAwait                    // a wait for a runtime handshake (Await)
)

var opNames = [...]string{
	OpStart: "start", OpResume: "resume", OpEnd: "end", OpSpawn: "spawn",
	OpJoin: "join", OpStop: "stop", OpSleep: "sleep", OpPush: "push",
	OpPop: "pop", OpClose: "close", OpRead: "read", OpWrite: "write",
	OpAwait: "await",
}

// String is the operation's word, as a report prints it.
func (k OpKind) String() string {
	if int(k) < len(opNames) && opNames[k] != "" {
		return opNames[k]
	}
	return "op?"
}

// Op is the operation at a scheduling point: its kind and the handle it
// touches (a queue, an inbox, a Ref, an actor, a task, a clock), compared
// by identity.
type Op struct {
	Kind OpKind
	On   any
}
