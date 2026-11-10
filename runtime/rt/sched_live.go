//go:build !ergoexplore

package rt

// The live build's scheduling points (sched.go): each hook is an empty
// function whose arguments are plain values, so the inliner removes the
// call and nothing is left in the compiled code
// (TestSchedHooksCostNothingLive).

// Exploring is false: this build has no controlled scheduler. A hook call
// that needs a closure is written inside `if rt.Exploring`, so the
// compiler drops it with the branch.
const Exploring = false

// Point is the scheduling point of t before op (sched.go).
func Point(t *Task, op Op) {}

// PointRunning is Point for the task that runs now, at an operation that
// takes no task (a queue's End).
func PointRunning(op Op) {}

// Block comes just before a wait of t that may block: in the explored
// build a parked t gives the baton away.
func Block(t *Task) {}

// Unblock comes just after the wait that Block began: in the explored
// build t takes the baton back.
func Unblock(t *Task) {}

// Await is the point of a wait that the clock does not see (a runtime
// handshake): in the explored build t gives the baton away until ready
// reports true; a nil t is the task that runs now. Write its call inside
// `if rt.Exploring`.
func Await(t *Task, ready func() bool) {}

// NameHandle names on, a handle the program just made, in an explored
// run's report; the live build has none.
func NameHandle(on any, name string) {}

// schedSpawn registers child, just started by parent.
func schedSpawn(parent, child *Task) {}

// schedStart is the first step of a new task's goroutine.
func schedStart(t *Task) {}

// schedEnd is the last step of an ended task's goroutine.
func schedEnd(t *Task) {}

// schedTick records a move of v, an auto clock. v.mu is held.
func schedTick(v *Virtual) {}
