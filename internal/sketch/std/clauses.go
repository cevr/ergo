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
	// returns, on the calling task, one run at a time; kept by no
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
	{"actor.recovering", "recover", "non-escaping", "ActorRecovering calls recover once, through recoverWith, on the task it was given (the caller's), before it calls start and returns; it keeps nothing"},
	{"actor.recovering", "start", "non-escaping", "ActorRecovering calls start once, on the task it was given (the caller's), after recover returned and before it returns; it keeps nothing"},
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
	// http: a streamed client body (httpStreamURL) is read by a goroutine
	// of its own, which hands each part over an unbuffered channel to the
	// caller's task, where accepts and put run; the goroutine is joined
	// before the call returns and never holds either.
	{"http.sendInto", "accepts", "non-escaping", "HTTPDoInto's httpStreamURL calls accepts on the caller's task, once per response head, before it returns; it keeps nothing"},
	{"http.sendInto", "put", "non-escaping", "HTTPDoInto's httpStreamURL calls put on the caller's task, one part at a time, in order, and joins its reader goroutine before it returns; it keeps nothing"},
	{"http.serve", "l", "lent", "HTTPServe serves on l from a goroutine (srv.Serve) that every exit joins (<-served) before it returns, and closes l (Listener.Close is idempotent, so the caller's own release stays sound); it keeps nothing"},
	{"http.serve", "handle", "escaping", "HTTPServe runs handle on a child task per request, many at once, while its own loop runs (lang audit B, F1: as tasks.Transform)"},
	{"http.IntoFile", "fetch", "non-escaping", "HTTPIntoFile calls fetch once, on the caller's task, before it renames or removes the file and returns; it keeps nothing"},
	{"http.fetchInto", "accepts", "non-escaping", "HTTPFetchInto calls accepts through httpStreamURL on the caller's task, once per hop's head, before it returns; it keeps nothing"},
	{"http.fetchInto", "put", "non-escaping", "HTTPFetchInto calls put through httpStreamURL on the caller's task, one part at a time, and joins the reader before it returns; it keeps nothing"},
	{"http.Net.SendInto", "put", "non-escaping", "an operation: its std providers are Ergo bodies the checker holds to the clause (Client.SendInto through sendInto, reviewed above; InMemory's Handler.SendInto); a user's provider is checked the same way"},
	{"http.Network.SendURLInto", "put", "non-escaping", "an operation: its std provider Remote.SendURLInto is an Ergo body the checker holds to the clause, through fetchInto (reviewed above); a user's provider is checked the same way"},
	// rpc: rpcCalls starts each Request's handle on a child task of its
	// own, all at once, and the caller's task takes their ends.
	{"rpc.exits", "handle", "escaping", "RPCExits runs handle on a child task per Request, many at once (rpcCalls), while the caller's task collects their ends"},
	{"rpc.exitLines", "handle", "escaping", "RPCExitLines runs handle on a child task per Request, many at once (rpcCalls), while the caller's task collects their ends"},
	{"rpc.exitLines", "emit", "non-escaping", "RPCExitLines calls emit only on the caller's task (rpcCalls' each, in its select loop), one line at a time, before it returns; it keeps nothing"},
	{"scope.OnExit", "body", "non-escaping", "OnExit registers body on the caller's frame, which runs it masked when that frame ends, on the same task, before the frame returns (runtime.md, Exit actions); the checker's frame rules (frames.go, withown.go) hold its captures to that frame"},
	{"slices.SortFunc", "compare", "non-escaping", "SlicesSortFunc calls Go's slices.SortFunc on the caller's goroutine, which calls compare only during the call (v1.GoSyncCallbacks lists it)"},
	{"slices.SortStableFunc", "compare", "non-escaping", "SlicesSortStableFunc calls Go's slices.SortStableFunc on the caller's goroutine, which calls compare only during the call (v1.GoSyncCallbacks lists it)"},
	{"sync.Modify", "f", "non-escaping", "RefModify calls f once, under r's lock, on the caller's goroutine, and keeps nothing"},
	{"sync.Ref.Update", "f", "non-escaping", "Ref.Update (and RefUpdateOwned) call f once, under the Ref's lock, on the caller's goroutine, and keep nothing"},
	{"task.Go", "body", "escaping", "Go starts body on a task of its own that runs after Go returned, until Wait, Stop or Close"},
	{"tasks.Transform", "f", "escaping", "TasksTransform calls f on child tasks, up to limit of them at once (lang audit B, F1, probes race and race2); it waits for every call before it returns, but the calls run at the same time"},
	{"tasks.Go", "set", "lent", "TasksGo adds a member to set under its lock and keeps set only in that member's task, which set's own Wait, Stop or Close drains (v1.DrainsLends) before set is released"},
	{"tasks.Go", "body", "escaping", "TasksGo runs body as a member of set, on a task of its own, after Go returned"},
	{"timeout.After", "f", "escaping", "After runs f on a child task (rt.AfterSpan); joining it orders the runs but does not make a caller-owned resource's task-affine operations safe on that child"},
}
