package std

// ArgClause is the reviewed fact of one parameter of a std declaration
// whose body is Go: what the callee does with a function or resource
// argument (docs/spec/types.md, Function values 2; resources.md R17;
// ESC1). The signature declares the clause and the checker holds every
// caller to it, but no checker reads a Go body, so each clause that trusts
// the body is reviewed against it here. TestExternArgumentClauses fails
// when a declaration's clause differs from its entry, or a parameter has
// no entry, so a new one cannot be added unexamined.
type ArgClause struct {
	// Func is pkg.Name for a function, pkg.Type.Op for an operation.
	Func  string
	Param string
	// Clause is the signature's: "non-escaping" (runs only before the call
	// returns, one run at a time, never while its caller runs; kept by no
	// one), "escaping" (anything else), "lent" (a resource used only
	// before the call returns, kept by no one) or "takes" (owned by the
	// callee).
	Clause string
	// Reason is the body fact that makes the clause true, with the Go body.
	Reason string
}

// ArgClauses is the inventory, in package order.
var ArgClauses = []ArgClause{
	// actor: the runner (MachineSupervise, MachineChild) keeps every
	// function of the machine and calls it on the actor's task, after
	// start or child returned, while the caller runs on.
	{"actor.start", "fresh", "escaping", "MachineSupervise keeps it and calls it on the actor's task at each restart, after start returned"},
	{"actor.start", "step", "escaping", "MachineSupervise keeps it and calls it on the actor's task for each signal, after start returned"},
	{"actor.start", "fire", "escaping", "MachineSupervise keeps it and calls it on the actor's task for each timer, after start returned"},
	{"actor.start", "delay", "escaping", "MachineSupervise keeps it and calls it on the actor's task at each entry, after start returned"},
	{"actor.start", "workOf", "escaping", "MachineSupervise keeps it and calls it on the actor's task at each entry, after start returned"},
	{"actor.start", "hasWork", "escaping", "MachineSupervise keeps it and calls it on the actor's task at each entry, after start returned"},
	{"actor.start", "work", "escaping", "MachineSupervise keeps it and runs it on a work task of the entry, after start returned"},
	{"actor.start", "kind", "escaping", "MachineSupervise keeps it and calls it on the actor's task for each decision, after start returned"},
	{"actor.start", "next", "escaping", "MachineSupervise keeps it and calls it on the actor's task for each decision, after start returned"},
	{"actor.start", "out", "escaping", "MachineSupervise keeps it and calls it on the actor's task for each decision, after start returned: StartMachine's sink runs there (lang audit B, F1, probe sink)"},
	{"actor.keep", "save", "non-escaping", "ActorKeep calls save once, on the task it was given (the step's), and returns after it; it keeps nothing"},
	{"actor.child", "step", "escaping", "MachineChild keeps it and calls it on the child's task, after child returned"},
	{"actor.child", "fire", "escaping", "MachineChild keeps it and calls it on the child's task, after child returned"},
	{"actor.child", "delay", "escaping", "MachineChild keeps it and calls it on the child's task, after child returned"},
	{"actor.child", "workOf", "escaping", "MachineChild keeps it and calls it on the child's task, after child returned"},
	{"actor.child", "hasWork", "escaping", "MachineChild keeps it and calls it on the child's task, after child returned"},
	{"actor.child", "work", "escaping", "MachineChild keeps it and runs it on a work task of the child, after child returned"},
	{"actor.child", "kind", "escaping", "MachineChild keeps it and calls it on the child's task, after child returned"},
	{"actor.child", "next", "escaping", "MachineChild keeps it and calls it on the child's task, after child returned"},
	{"actor.child", "out", "escaping", "MachineChild keeps it and calls it on the child's task, after child returned"},
	{"actor.feed", "run", "escaping", "ActorFeed admits it as a source that the actor's runner starts on a task of its own, after feed returned"},
	{"csv.scan", "each", "non-escaping", "CSVScan calls each in a loop over Go's csv.Reader on the caller's goroutine, one record at a time, and keeps nothing"},
	{"scope.OnExit", "body", "non-escaping", "OnExit registers body on the caller's frame, which runs it masked when that frame ends, on the same task, before the frame returns (runtime.md, Exit actions); the checker's frame rules (frames.go, withown.go) hold its captures to that frame"},
	{"slices.SortFunc", "compare", "non-escaping", "SlicesSortFunc calls Go's slices.SortFunc on the caller's goroutine, which calls compare only during the call (v1.GoSyncCallbacks lists it)"},
	{"slices.SortStableFunc", "compare", "non-escaping", "SlicesSortStableFunc calls Go's slices.SortStableFunc on the caller's goroutine, which calls compare only during the call (v1.GoSyncCallbacks lists it)"},
	{"sync.Modify", "f", "non-escaping", "RefModify calls f once, under r's lock, on the caller's goroutine, and keeps nothing"},
	{"sync.Ref.Update", "f", "non-escaping", "Ref.Update (and RefUpdateOwned) call f once, under the Ref's lock, on the caller's goroutine, and keep nothing"},
	{"task.Go", "body", "escaping", "Go starts body on a task of its own that runs after Go returned, until Wait, Stop or Close"},
	{"tasks.Transform", "f", "escaping", "TasksTransform calls f on child tasks, up to limit of them at once (lang audit B, F1, probes race and race2); it waits for every call before it returns, but the calls run at the same time"},
	{"tasks.Go", "set", "lent", "TasksGo adds a member to set under its lock and keeps set only in that member's task, which set's own Wait, Stop or Close drains (v1.DrainsLends) before set is released"},
	{"tasks.Go", "body", "escaping", "TasksGo runs body as a member of set, on a task of its own, after Go returned"},
	{"timeout.After", "f", "non-escaping", "After runs f once on a child task and waits for it to end before it returns, on the deadline too (it stops the child and joins it, rt.AfterSpan), so the run is ordered with the caller before and after and is the only run; it keeps nothing"},
}
