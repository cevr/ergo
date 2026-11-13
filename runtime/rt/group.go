package rt

// Group is a node of the task tree that runs no body: the parent of tasks
// that other goroutines start and run on themselves (RunIn). A server that
// runs each request on the goroutine that read it (runtime/v1 HTTPServe)
// keeps its requests under one group, so they need no dispatcher
// goroutine to start them and no registry to stop them: an Interrupt of
// the group reaches every request that runs, and a stop of the group's
// parent cascades into it as into any child.
//
// The group is made and ended on its parent's goroutine. Between the two,
// any goroutine may start a member with RunIn; the group's context values,
// regions and tracer are fixed when it is made, so a member reads them
// with no lock. End runs only after every member has returned: the owner
// counts them itself (HTTPServe's workers), since a group keeps no count.
//
// A member is a task as one Go starts: it has the group's context values,
// is interruptible, is born stopped when the group is stopped, joins the
// group's auto clock domain, and its exit is settled by Run (a panic is a
// defect with its chain). Unlike Go's child, nothing waits on it: RunIn
// returns its exit to the caller.
//
// Prior art: Erlang and Elixir servers (Cowboy, Bandit) and Gleam's mist
// run each connection's requests in the connection's own process, linked
// to a supervisor that stops them all; Trio's nursery and Go's errgroup
// start members from the owner only.
type Group struct {
	task Task
}

// NewGroup makes a group under parent, on parent's goroutine. span names
// it in defect chains and traces.
func NewGroup(parent *Task, span string) *Group {
	g := &Group{}
	g.task.init(parent, span, false, false)
	// The group runs nothing and never waits, so it is no live member of
	// its parent's auto clock domain: it only passes the domain on to its
	// members, which join it as Go's children do.
	if m := g.task.member.Load(); m != nil {
		m.v.mu.Lock()
		m.v.live--
		m.v.mu.Unlock()
	}
	if g.task.trace != nil {
		g.task.traceTask(TraceStart, nil)
	}
	return g
}

// Interrupt stops every member that runs and every member that starts
// after it. Safe from any goroutine.
func (g *Group) Interrupt() { g.task.Interrupt() }

// End unlinks the group from its parent, on the parent's goroutine, after
// every member has returned.
func (g *Group) End() {
	if g.task.trace != nil {
		g.task.traceExit(nil)
		g.task.traceForget()
	}
	g.task.detach()
}

// RunIn runs body as a member of g on the calling goroutine, which may be
// any goroutine, and returns its exit in canonical form, as Run does.
func RunIn[T any](g *Group, span string, body func(*Task) (T, error)) (T, error) {
	t := &Task{}
	t.init(&g.task, span, false, false)
	v, err := Run(t, body)
	if t.trace != nil {
		t.childEvent(TraceObserve, &g.task, false)
	}
	t.detach()
	if own := t.member.Load(); own != nil {
		own.leave()
	}
	return v, err
}
