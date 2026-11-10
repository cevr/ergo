package v1

import (
	"fmt"
	"time"

	"ergo.local/prototype/runtime/rt"
)

// The bodies of the std packages task and scope (docs/spec/runtime.md,
// Tasks and Exit actions), over runtime/rt's task tree. rt gives the tree,
// the stop flag and its cascade, masks, causes and the drain of a task;
// this file adds what Ergo's source contract needs on top: the frame scope
// of one source function, the owned values its frames release (a Task is
// one), a defect's stack, and a Wait that returns a typed failure and
// re-raises everything else.

// Scope is the frame scope of one source function: a declared function, a
// function literal, the block of task.Go or the block of an exit action.
// The lowering makes one in each frame that owns a value, starts a task or
// registers an exit action, and defers its End as the frame's first defer:
//
//	s := ergo.NewScope(t)
//	defer s.End(&err) // or s.End(nil) in a frame with no error row
//
// The values the frame owns (its Tasks among them) and its exit actions are
// entries of one list, released last registered first when the frame
// returns, on every path: so a task started after an exit action is stopped
// and drained before that action runs, and an action registered before a
// task runs after the task's own cleanup (laws R1, R2; structured lend,
// resources.md R27). An owned value's entry leaves the list when it is
// handed off (Hand) or released, so the list holds only what the frame
// still owns.
type Scope struct {
	s     rt.Scope
	ended bool
	// last is the newest entry; n counts the entries.
	last *entry
	n    int
	// exits holds the exits of the entries: each exit action recovers its
	// own panic and its frame's exits, and a drained task's defects join
	// here, flat, in release order.
	exits rt.Cause
	// action is the frame of the exit action this frame runs now: a task
	// or an exit action that code at this frame starts or registers belongs
	// to it, through its whole run and its own release.
	action *Scope
	// withs are the regions open on this scope, the innermost last: the
	// scopes of owning withs (OpenWith) and context regions (OpenContext);
	// parent is the scope an owning with's scope was opened on, nil for a
	// frame.
	withs  []region
	parent *Scope
	// kept are the owned values the frame is returning (Returned, the owned values below):
	// released only when its exit fails.
	kept []*entry
	// plain marks the frame scope of a frame that opens one only for its
	// owned values (NewOwnerScope): its end is no interruption point, as a
	// frame with no scope has none.
	plain bool
}

// entry is one entry of a frame's list: an exit action or an owned value's
// release (the owned values below; a Task and a tasks.Set are owned values,
// #197 L8 B3t).
type entry struct {
	prev, next *entry
	action     func()
	own        func()
	// taken is the fields of an owned record taken in place (TakeField):
	// its generated release skips them (#197 L8 B2; R39, Gap 38).
	taken uint64
}

// NewScope is the frame scope of a frame that runs on task t.
func NewScope(t *rt.Task) Scope { return Scope{s: rt.NewScope(t)} }

// NewOwnerScope is the frame scope of a frame that opens one only to own
// values (#197 L8 B1): it releases them on every exit, and a stop pending
// at its end stays pending for the caller's next checkpoint.
func NewOwnerScope(t *rt.Task) Scope { return Scope{s: rt.NewScope(t), plain: true} }

// current is the frame that owns what the code at s starts now: the frame
// of the running exit action, followed down through nested ones, else s.
func (s *Scope) current() *Scope {
	for s.action != nil {
		s = s.action
	}
	return s
}

// push adds e as the newest entry.
func (s *Scope) push(e *entry) {
	e.prev, e.next = s.last, nil
	if s.last != nil {
		s.last.next = e
	}
	s.last = e
	s.n++
}

// remove takes e out of the list.
func (s *Scope) remove(e *entry) {
	if e.prev != nil {
		e.prev.next = e.next
	}
	if e.next != nil {
		e.next.prev = e.prev
	} else {
		s.last = e.prev
	}
	e.prev, e.next = nil, nil
	s.n--
}

// End ends the frame. Defer it directly, so its recover sees the frame's
// panic. It records that panic (a fresh defect with its stack, a carried
// exit or an interruption) beside *err, then releases the entries, last
// first, masked (I2): it stops and drains each task the frame still owns
// and runs each exit action, keeping every defect they raise (Exit causes
// 4). A stop the task can observe beats success (Exit causes 5). Last it
// hands the exit to the frame: a lone typed failure into *err, which must be
// the frame's named error result and nil in a frame with no error row; any
// other exit is raised as a panic for the caller's boundary.
func (s *Scope) End(err *error) {
	p := recover()
	var e error
	if err != nil {
		e = *err
	}
	if p != nil {
		e = join(e, capture(s.s.Task(), p))
	}
	settle(err, s.end(e))
}

// end ends the scope with the exit e so far and returns the whole exit.
func (s *Scope) end(e error) error {
	e = s.close(e)
	if !s.plain {
		s.s.End(&e) // stop wins (Exit causes 5); rt holds no entry of this frame
	}
	if s.kept != nil {
		e = s.settleKept(e)
	}
	return e
}

// close ends the scope s, a frame or an owning with, and returns the whole
// exit: first the regions still open on it (owning withs and context
// regions), the innermost first, as their blocks would have ended on the
// normal path; then its own entries,
// last first. Every release runs, and each one's exits join s.exits.
func (s *Scope) close(e error) error {
	s.ended = true
	for n := len(s.withs); n > 0; n = len(s.withs) {
		w := s.withs[n-1]
		s.withs = s.withs[:n-1]
		s.exits.Add(w.close(nil))
	}
	if s.last != nil {
		s.release()
	}
	if len(s.exits.Reasons) > 0 {
		e = join(e, s.exits.Err())
	}
	return e
}

// OpenWith opens the scope of an owning with (docs/spec/resources.md,
// Context ownership): a with whose context builds a provider with a checked
// Close. s is the scope of the code at the with, its frame or the next
// owning with out. The with's scope owns the providers the with builds
// (WithAcquire) and the tasks its block starts or receives, as a frame owns
// its own: it releases them, last first, when the block ends (EndWith),
// or when s ends first, on a defect, a stop or a typed failure that leaves
// the block. So tasks that borrow a provider drain before it closes, and
// each provider closes once, at the end of its with, not of its function.
func (s *Scope) OpenWith() *Scope {
	s = s.current()
	if s.ended {
		panic("with: the frame that would own this with's providers has returned")
	}
	w := &Scope{s: rt.NewScope(s.s.Task()), parent: s}
	s.withs = append(s.withs, w)
	return w
}

// WithAcquire constructs a provider and registers its checked Close in
// this with's existing list, as one masked acquisition (R7, R8). open
// yields that release only after construction completes. A failure yields
// no release; a lexical transfer before construction yields nil, nil.
// rt owns the acquisition region and restores it on every exit. In
// particular, a completed open stays successful after a stop request.
// A panic keeps v1's cause transport and reaches the enclosing frame,
// whose End rolls back the with's earlier acquisitions.
// An acquisition begins at an interruption point (I1): a stop that arrived
// while an earlier provider was built masked is observed here, so no later
// provider is built and the earlier ones close.
func (w *Scope) WithAcquire(open func() (func(), error)) error {
	t := w.s.Task()
	t.Check()
	_, err := rt.AcquireInto(t, open, func(close func()) {
		if close != nil {
			w.ownRelease(close)
		}
	})
	return err
}

// ownRelease registers close as the release of a provider the with whose
// scope is w has just built: the call of the provider's checked Close,
// whose body masks itself (Release). The with adds no mask: it catches the
// exit that Release raises and keeps it in w's exits, so the releases
// after it still run and every cause stays. It is unexported: emitted code
// registers a release only through WithAcquire's masked step (R8).
func (w *Scope) ownRelease(close func()) {
	t := w.s.Task()
	w.push(&entry{action: func() {
		defer func() {
			if p := recover(); p != nil {
				w.exits.Add(capture(t, p))
			}
		}()
		close()
	}})
}

// EndWith ends the owning with whose scope is w at the normal end of its
// block. err is the with's failure, nil when the with has no failure row:
// a lone typed failure stays in *err, and any release defect joins it and
// is raised, as a frame's End settles its exit. A stop is not observed
// here; the next checkpoint observes it.
func (w *Scope) EndWith(err *error) {
	p := w.parent
	n := len(p.withs)
	if n == 0 || p.withs[n-1] != region(w) {
		panic("with: a with's scope ended out of its nesting")
	}
	p.withs = p.withs[:n-1]
	var e error
	if err != nil {
		e = *err
	}
	settle(err, w.close(e))
}

// Release runs a checked Close body, masked from its first checkpoint to
// its last instruction (I2, R9). The caller evaluates the receiver before
// entering it. A pending stop remains pending until the caller's next
// checkpoint. A defect or carried exit is raised whole, for the caller's
// frame to join to its earlier exit; it is never turned into a typed error.
func Release(t *rt.Task, body func()) {
	var c rt.Cause
	release(t, &c, body, nil)
	if err := c.Err(); err != nil {
		raise(err)
	}
}

// release is the single mask and cause boundary for a v1 release: written
// Close, exit action or owned-task drain. rt.Release preserves the cause
// already in c, adds each fresh defect with its stack and release marker,
// and keeps carried exits flat. after ends a callback's own frame while
// still masked. The deferred unmask restores the caller's nesting on every
// exit, including a panic in classification or callback-frame cleanup.
func release(t *rt.Task, c *rt.Cause, body func(), after func(error) error) {
	m := t.Mask()
	defer m.Unmask()
	rt.Release(t, c, body, func(p any) (any, error) { return classify(t, p) }, after)
}

// release releases the entries last first. Each action and owned value's
// release enters the shared release boundary on this frame's task.
func (s *Scope) release() {
	for s.last != nil {
		x := s.last
		s.remove(x)
		if x.own != nil {
			s.runOwn(x) // a checked Close, which masks itself, or OwnErr's release
		} else {
			x.action() // a release of its own, through release
		}
	}
}

// OnExit is scope.OnExit() { body } in the frame whose scope is s, the
// hidden frame argument (docs/spec/stdlib.md, Hidden arguments): it
// registers body on the frame, once per call, as Go's defer does. body is
// the lowered block, a closure, so it reads its variables when it runs
// (Exit actions 4). It runs masked when the frame ends, on every path. The
// block is a frame of its own for the tasks it starts and the exit actions
// it registers, nested ones included: they end when the block ends (Tasks
// rule 8). A defect in it joins the frame's exit as a release defect, with
// its stack, and the frame's other reasons stay. Registering on a frame
// that has returned is a defect.
func OnExit(s *Scope, body func()) {
	// Inside an owning with, s is the with's scope; the action belongs to
	// the function's frame all the same (Exit actions 2).
	for s.parent != nil {
		s = s.parent
	}
	s = s.current()
	if s.ended {
		panic("scope.OnExit: the frame that would run this exit action has returned")
	}
	t := s.s.Task()
	s.push(&entry{action: func() {
		action := &Scope{s: rt.NewScope(t)}
		prev := s.action
		s.action = action
		// The route to the action's frame stays through the block and
		// through the frame's own release, where nested exit actions run.
		defer func() { s.action = prev }()
		// The block and its frame's release are one release of s, which
		// release reports on a traced tree: the block's frame ends
		// inside it, so its nested releases report before its own release
		// event. A panic of the block is recorded by rt as a release defect
		// after the release-local unwind and before the block's frame ends
		// with that exit's carried part (after), so the panic event comes
		// before the nested releases it causes.
		var nested error
		release(t, &s.exits, func() {
			body()
			nested = action.end(nil)
		}, func(carried error) error {
			if action.ended {
				return carried // the frame's end itself panicked
			}
			return action.end(carried)
		})
		s.exits.Add(nested)
	}})
}

// Task is a started task, Ergo's Task[T, E]. Its methods are the prelude's
// operations, Wait() T fails E, Stop() and Close(), in the uniform Go shape,
// so *Task[T] is a value of the operations interface the lowering emits for
// Task[T, E]. A Task is a resource (#197 L8 B3t; resources R1): its owner
// is the owner of any value, an entry of a frame's list (Owning), moved by
// a return, into a field, a container or a queue, and released by Close at
// its owner's end when no Wait took it.
type Task[T any] struct {
	h    *rt.Handle[T]
	task *rt.Task // the task that started it
	used bool     // Wait, Stop or Close ran
	// out is the value the body returned, kept past rt's stop-wins, which
	// zeroes it when a stop is pending at the boundary (Gap 47); ended says
	// the body returned one. The child writes them before its handle
	// finishes; the owner reads them after it joined the task. A Wait that
	// delivers the value moves the Task out of its owner, so no Drain
	// follows it.
	out   T
	ended bool
}

// Site is the Ergo identity of a call: the calling function, the called
// declaration and the call's source position, such as "main calls task.Go
// at main.ergo:12:10", the form of a call frame in a defect report. The
// lowering passes it as a hidden argument to a Go body that names a task
// or a span by it (docs/spec/stdlib.md, Hidden arguments; G2, #230). No
// Ergo type lowers to Site, so its Go type alone marks the argument.
type Site string

// Go is task.Go() { body } in the frame whose scope is s: it starts body as
// a child of the frame's task, on its own goroutine, and returns at once
// (parse-v1 ruling 5: the block runs later). s and span are the hidden
// frame and site arguments; the block receives the child's task as its own
// hidden argument (docs/spec/stdlib.md, Hidden arguments). span is the
// task's Ergo identity, the block's definition and source site, which a
// defect chain names (G2). The child starts interruptible, except inside
// an exit action, where rt shields it from the ambient stop so the cleanup
// completes. The lowering registers the Task as an owned value in the step
// that receives it (Owning, R8), so its owner's end releases it (Close).
// Starting a task on a frame that has returned is a defect (law R9).
func Go[T any](s *Scope, span Site, body func(*rt.Task) (T, error)) *Task[T] {
	s = s.current()
	if s.ended {
		panic("task.Go: the frame that would own this task has returned (law R9)")
	}
	// rt registers the handle on the scope it is given. That scope is a
	// throwaway: the Task's owner releases it through its own entry.
	holder := rt.NewScope(s.s.Task())
	t := &Task[T]{task: s.s.Task()}
	t.h = rt.Go(&holder, string(span), func(ct *rt.Task) (T, error) {
		v, err := guarded(ct, body)
		if err == nil {
			t.out, t.ended = v, true
		}
		return v, err
	})
	return t
}

// Wait waits for the task to end and consumes it. It returns the task's
// value, or its typed failure when that failure is the task's whole exit
// (law R6: a catch handles only a lone typed failure). Any other exit is
// raised in the waiter: a defect with the chain and stack recorded in the
// child (Tasks 4), and an interruption. Wait is an interruption point of
// the waiter (I1): when the waiter is stopped first or while it waits, the
// task is stopped and drained, and Wait raises the interruption with the
// task's defects; it delivers neither the task's value nor its typed
// failure (law R4). A Wait after Wait, Stop or Close is a defect.
func (t *Task[T]) Wait() (T, error) {
	if t.used {
		panic("task: Wait on a task that was already waited for, stopped or released")
	}
	t.used = true
	v, err := t.h.Wait()
	if err == nil {
		return v, nil
	}
	if f, ok := rt.Failure(err); ok {
		var zero T
		return zero, f
	}
	raise(err)
	panic("unreachable")
}

// Stop interrupts the task and waits, uninterruptibly, until it has ended,
// its exit actions included (law R5), and consumes it. The task's
// interruption and an abandoned typed failure are the expected result of
// the stop and are dropped (laws R3, R6); its defects are raised in the
// caller. Stop on a consumed task does nothing.
func (t *Task[T]) Stop() {
	if t.used {
		return
	}
	t.used = true
	if err := t.h.Stop(); err != nil {
		raise(err)
	}
}

// Close is the prelude's Task.Close(), the release of a Task its owner did
// not consume (#197 L8 B3t; Tasks 3, 12; Q5): masked on ct, the task that
// releases it, it stops the task and waits until it has ended, as Stop does.
// The task's typed failure is dropped (a traced tree's observe event says
// it was not observed) and its defects are raised for the releasing scope.
// Close on a consumed task does nothing.
func (t *Task[T]) Close(ct *rt.Task) {
	if t.used {
		return
	}
	t.used = true
	Release(ct, func() {
		if err := t.h.Stop(); err != nil {
			raise(err)
		}
	})
}

// Drain is the rest of the release of a Task whose value holds a resource
// (#197 L8 B3t, B4; Q5, Gap 47, resources R13): after Close, it gives the
// value the body returned, for the owner's generated release, also when
// rt's stop-wins dropped it (the body returned it while a stop was pending
// at the task's boundary). A Wait that delivered the value moved the Task
// out of its owner first, so its owner never drains it. It gives nothing
// for a body that failed or was interrupted before it returned (a Close
// ran first, so the task has ended). It never waits.
func (t *Task[T]) Drain() []T {
	if !t.ended {
		return nil
	}
	return []T{t.out}
}

// Expired is timeout.Expired, the typed failure of After when the deadline
// passes before the body ends (docs/spec/runtime.md, Timeouts 4). It is a
// record of a v1 package, so the lowering binds timeout.Expired to this Go
// type (docs/spec/stdlib.md, Types 4), and a catch arm `case
// timeout.Expired:` matches the value After returns.
type Expired struct{}

// Error is the failure's text, its Ergo name.
func (Expired) Error() string { return "timeout.Expired" }

// Sleeper is the program's Scheduler capability, the hidden scheduler
// argument of After (docs/spec/stdlib.md, Hidden arguments): the lowering
// passes the Scheduler field of the caller's environment, an interface of
// the program whose one operation is Sleep(*rt.Task, time.Duration). No
// Ergo type lowers to Sleeper, so its Go type alone marks the argument.
type Sleeper interface {
	Sleep(t *rt.Task, d time.Duration)
}

// After is timeout.After(d) { body } (docs/spec/runtime.md, Timeouts): it
// runs body as a child of t, the caller's task, and races it against a
// deadline that the program's Scheduler gives. span, t and s are the
// hidden site, task and scheduler arguments; body receives the child's
// task. The deadline is a second child of t that calls s.Sleep(d): time is
// a capability, so a test Scheduler controls when the deadline passes, and
// After arms no host timer of its own.
//
// rt.AfterSpan races the two and drains both before it returns. The body
// ends first: its value or its typed failure. The deadline passes first:
// the body is stopped and drained, and After fails with Expired. A defect
// in the body, in the deadline child or in either drain is raised in the
// caller on every path, as Task.Wait raises it; so is an interruption of
// t. A Sleep that ends without a stop, by a defect included, ends the
// race: a deadline that cannot be kept never leaves the body unbounded.
// rel is the hidden release of A: when the body returned a value that
// After does not give (stop wins at the body's boundary, or the caller is
// interrupted), After releases it (Gap 47).
func After[A any](span Site, t *rt.Task, s Sleeper, rel Releaser[A], d time.Duration, body func(*rt.Task) (A, error)) (A, error) {
	var kept A
	returned := false
	v, err := rt.AfterSpan(t, deadlines{t: t, s: s, span: string(span) + " (deadline)"}, d, string(span), func(ct *rt.Task) (A, error) {
		v, err := guarded(ct, body)
		if err == nil {
			kept, returned = v, true
		}
		return v, err
	})
	if err == nil {
		return v, nil
	}
	if returned {
		err = releaseLeft(t, rel, kept, err)
	}
	var zero A
	if f, ok := rt.Failure(err); ok {
		if f == (rt.Expired{}) {
			f = Expired{}
		}
		return zero, f
	}
	raise(err)
	panic("unreachable")
}

// Releaser is the hidden ResultRelease argument (files.go): the generated
// release of a value of the call's result type, run on the releasing task.
// Nil when the type holds no resource.
type Releaser[T any] func(*rt.Task, T)

// releaseLeft releases v, a successful result a boundary dropped (Gap 47),
// masked on t, and gives the call's exit: err with the release's defects.
// A nil rel (the type holds no resource) releases nothing.
func releaseLeft[T any](t *rt.Task, rel Releaser[T], v T, err error) error {
	if rel == nil {
		return err
	}
	var c rt.Cause
	c.Add(err)
	release(t, &c, func() { rel(t, v) }, nil)
	return c.Err()
}

// TasksTransform is tasks.Transform(xs, limit, f) (docs/spec/stdlib.md, Tasks): f on
// each element, each call on a child task of t, at most limit at once, the
// results in the order of xs. span and t are the hidden site and task
// arguments; f receives the child's task. rt.MapSpan waits for whichever
// child ends first: the first failure stops and drains the rest and is the
// result. A defect, in f or in a drain, is raised in the caller, as
// Task.Wait raises it; so is an interruption of t. rel is the hidden
// release of the result: when Transform fails, the results its calls
// returned are released, in the order of xs (Gap 47). A limit below 1 is
// a defect of the caller. Prior art: errgroup.Group with SetLimit;
// Effect's Effect.forEach with { concurrency }.
func TasksTransform[A, B any](span Site, t *rt.Task, rel Releaser[[]B], xs []A, limit int, f func(*rt.Task, A) (B, error)) ([]B, error) {
	if limit < 1 {
		panic(fmt.Sprintf("tasks.Transform: limit %d is less than 1", limit))
	}
	var out []B
	var err error
	if rel == nil {
		// B holds no resource: nothing to keep (as fast as before Gap 47).
		out, err = rt.MapSpan(t, string(span), xs, limit, func(ct *rt.Task, a A) (B, error) {
			return guarded(ct, func(ct *rt.Task) (B, error) { return f(ct, a) })
		})
	} else {
		kept := make([]B, len(xs))
		returned := make([]bool, len(xs))
		idx := make([]int, len(xs))
		for i := range idx {
			idx[i] = i
		}
		out, err = rt.MapSpan(t, string(span), idx, limit, func(ct *rt.Task, i int) (B, error) {
			v, err := guarded(ct, func(ct *rt.Task) (B, error) { return f(ct, xs[i]) })
			if err == nil {
				kept[i], returned[i] = v, true
			}
			return v, err
		})
		if err != nil {
			// MapSpan joined every child: kept is final.
			var left []B
			for i, ok := range returned {
				if ok {
					left = append(left, kept[i])
				}
			}
			if len(left) > 0 {
				err = releaseLeft(t, rel, left, err)
			}
		}
	}
	if err == nil {
		return out, nil
	}
	if f, ok := rt.Failure(err); ok {
		return nil, f
	}
	raise(err)
	panic("unreachable")
}

// TasksRace is the body of tasks.Race over n blocks (docs/spec/stdlib.md,
// Tasks): f(i) runs block i, each on a child task of t, and the first to
// end decides; the rest are stopped and drained before it returns
// (rt.RaceSpan). span and t are the hidden site and task arguments; f
// receives the child's task. Race of no blocks is a defect of the caller:
// it would never end. rel is the hidden release of T: a loser whose block
// returned a value, and every block's value when Race fails, is released,
// in block order (Gap 47). Prior art: Effect.raceAll; Go's select over
// result channels and a cancel of the losers.
func TasksRace[T any](span Site, t *rt.Task, rel Releaser[T], blocks []func(*rt.Task) (T, error)) (T, error) {
	n := len(blocks)
	f := func(ct *rt.Task, i int) (T, error) { return blocks[i](ct) }
	if n < 1 {
		panic("tasks.Race: no blocks to race")
	}
	var v T
	var err error
	if rel == nil {
		// T holds no resource: nothing to keep (as fast as before Gap 47).
		v, err = rt.RaceSpan(t, string(span), n, func(ct *rt.Task, i int) (T, error) {
			return guarded(ct, func(ct *rt.Task) (T, error) { return f(ct, i) })
		})
	} else {
		kept := make([]T, n)
		returned := make([]bool, n)
		var w raced[T]
		w, err = rt.RaceSpan(t, string(span), n, func(ct *rt.Task, i int) (raced[T], error) {
			v, err := guarded(ct, func(ct *rt.Task) (T, error) { return f(ct, i) })
			if err == nil {
				kept[i], returned[i] = v, true
			}
			return raced[T]{v: v, i: i}, err
		})
		// RaceSpan joined every child: kept is final. The winner's value
		// is given; every other value a block returned is released.
		win := -1
		if err == nil {
			v, win = w.v, w.i
		}
		var c rt.Cause
		c.Add(err)
		for i, ok := range returned {
			if ok && i != win {
				release(t, &c, func() { rel(t, kept[i]) }, nil)
			}
		}
		if err = c.Err(); err != nil && win >= 0 {
			// A loser's release raised a defect: the winner's value is
			// not given either.
			release(t, &c, func() { rel(t, v) }, nil)
			err = c.Err()
		}
	}
	if err == nil {
		return v, nil
	}
	if f, ok := rt.Failure(err); ok {
		var zero T
		return zero, f
	}
	raise(err)
	panic("unreachable")
}

// raced is one Race block's value and index, so the winner is known.
type raced[T any] struct {
	v T
	i int
}

// deadlines is the rt.Scheduler of one After call: each timer it arms is a
// child of t that sleeps on the program's Scheduler.
type deadlines struct {
	t    *rt.Task
	s    Sleeper
	span string
}

// NewTimer starts the deadline child. Its timer fires when s.Sleep(d) ends
// while the child is not stopped: a stop of the child (Stop below, or a
// stop of t that cascades to it) never fires it.
func (ds deadlines) NewTimer(d time.Duration) rt.Timer {
	holder := rt.NewScope(ds.t)
	dl := &deadline{fired: make(chan time.Time)}
	dl.h = rt.Go(&holder, ds.span, func(ct *rt.Task) (struct{}, error) {
		return guarded(ct, func(ct *rt.Task) (struct{}, error) {
			defer func() {
				if !ct.Stopped() {
					// The race's caller may be parked on this deadline in
					// an auto clock's domain (rt idle.go).
					w := rt.Waking(ds.t, dl)
					close(dl.fired)
					w.Done()
				}
			}()
			ds.s.Sleep(ct, d)
			return struct{}{}, nil
		})
	})
	return dl
}

// deadline is one armed deadline: its child and the channel the child
// closes when the deadline passes.
type deadline struct {
	h     *rt.Handle[struct{}]
	fired chan time.Time
}

// C delivers when the deadline passes: it closes.
func (dl *deadline) C() <-chan time.Time { return dl.fired }

// WakesItsWaiter marks a deadline as an rt.WakeSource: its fire wakes the
// race's caller through rt.Waking, so the caller may park on it.
func (dl *deadline) WakesItsWaiter() {}

// Stop stops and drains the deadline child, so no child of After outlives
// it, and reports whether the deadline had not passed. A defect of the
// child is raised as a panic, which rt.AfterSpan records as a release
// defect of the call.
func (dl *deadline) Stop() bool {
	dl.h.Close()
	select {
	case <-dl.fired:
		return false
	default:
		return true
	}
}

// Owned values (docs/spec/resources.md R5-R10, R21, R41, R43; #197 L8 B1).
// A value whose type holds a resource has one owner: the frame, owning
// with or statement that the checker's release plan names (check
// Info.Releases). The lowering registers each owned value on its frame's
// one list, in the step that receives it (R8): the list that holds the
// frame's tasks and exit actions, so a frame ends them all last
// registered first (runtime.md, Exit actions 5).
//
// An entry's presence is the value's drop flag (R21). A hand-off (a move
// or a take) removes it (Hand); a scope that ends inside the frame
// releases it (Owned.Release); the frame's End releases what is left on
// every edge that leaves the function: a return, a failure, a defect or a
// stop. A returned value stays registered until the frame's exit is known
// (Returned): it is dropped when the frame returns its value, and released
// when the exit fails, so no defect or stop between the return and the
// caller loses it.
//
// Prior art: Rust's drop elaboration keeps a drop flag per local and
// drops on each exit and unwind edge; Go's defer runs at function exit
// only, so a defer in a loop waits for the function (R6), and drops
// Close's error by habit (Q9).

// Owned is the entry of one owned value in its frame's list; nil, or an
// entry that left the list, does nothing.
type Owned struct {
	s *Scope
	e *entry
}

// Owner is the entry, not yet registered, of a value that code at s will
// own: s is the code's frame scope, or the scope of the innermost owning
// with around it. Owning registers it.
func (s *Scope) Owner() *Owned { return &Owned{s: s} }

// Owning registers v as the owned value of h, released by rel, and returns
// v. It is the step that receives v: no checkpoint runs between the
// expression that produced v and the registration, so a stop cannot leave
// v unowned (R8).
func Owning[T any](h *Owned, v T, rel func(T)) T {
	o := h.s.own(func() { rel(v) })
	h.s, h.e = o.s, o.e
	return v
}

// OwningParts is Owning for a value whose release is generated from its
// parts (#197 L8 B2; check.Drop): rel receives the fields the owner took
// in place (TakeField), which it skips (R39, Gap 38).
func OwningParts[T any](h *Owned, v T, rel func(T, uint64)) T {
	var e *entry
	o := h.s.own(func() { rel(v, e.taken) })
	e = o.e
	h.s, h.e = o.s, o.e
	return v
}

// TakeField marks field k of h's value taken in place: its owner's
// release skips it. A value whose entry left the list ignores it.
func (h *Owned) TakeField(k int) {
	if h != nil && h.e != nil {
		h.e.taken |= 1 << k
	}
}

// Drop runs the releases of one value's parts, a generated release (#197
// L8 B2; resources R3, R9, R37): each part runs after an earlier part's
// defect, every defect is kept, and End raises them together. Each part
// is a checked Close, which masks its own body.
type Drop struct {
	t *rt.Task
	c rt.Cause
}

// Drop starts the generated release of a value owned at s.
func (s *Scope) Drop() *Drop { return &Drop{t: s.s.Task()} }

// NewDrop starts the generated release of a value on t outside a frame:
// the release a std body runs for a result it drops (Releaser; Gap 47).
func NewDrop(t *rt.Task) *Drop { return &Drop{t: t} }

// Part runs one part's release; a defect joins the value's cause.
func (d *Drop) Part(rel func()) {
	defer func() {
		if p := recover(); p != nil {
			d.c.Add(capture(d.t, p))
		}
	}()
	rel()
}

// End raises the defects of the parts, if any.
func (d *Drop) End() {
	if err := d.c.Err(); err != nil {
		raise(err)
	}
}

// own adds a release entry to s, or to the frame of the exit action that
// code at s runs now.
func (s *Scope) own(rel func()) *Owned {
	s = s.current()
	if s.ended {
		panic("ownership: the frame that would own this value has returned")
	}
	e := &entry{own: rel}
	s.push(e)
	return &Owned{s: s, e: e}
}

// OwnErr registers a release that reports a failure, the Close() error of
// a host value (Q9; import go, IG9): a non-nil error is a defect of the
// release, named by site, the Ergo line of the acquisition. The release
// runs inside the release boundary, masked, as a checked Close's body
// does.
func OwnErr(s *Scope, site Site, rel func() error) *Owned {
	return s.own(func() {
		t := s.s.Task()
		var c rt.Cause
		release(t, &c, func() {
			if err := rel(); err != nil {
				panic(fmt.Errorf("release of the value acquired at %s: %w", site, err))
			}
		}, nil)
		if err := c.Err(); err != nil {
			raise(err)
		}
	})
}

// Hand removes h's entry, the value v being handed off: moved to a new
// owner or taken by a call, and returns v.
func Hand[T any](h *Owned, v T) T {
	h.drop()
	return v
}

// Returned keeps h's entry until its frame's exit is known, the value v
// being returned (R10): the frame drops it when it returns v, and
// releases it when its exit fails. It returns v.
func Returned[T any](h *Owned, v T) T {
	if h == nil || h.e == nil {
		return v
	}
	f := h.s
	for f.parent != nil {
		f = f.parent
	}
	h.s.remove(h.e)
	f.kept = append(f.kept, h.e)
	h.e, h.s = nil, nil
	return v
}

// drop removes the entry, if it is still in its list.
func (h *Owned) drop() {
	if h == nil || h.e == nil {
		return
	}
	h.s.remove(h.e)
	h.e, h.s = nil, nil
}

// Release releases the value now, if its entry is still in the list: the
// end of a scope inside the frame (a block, a loop iteration, a
// statement's temporaries, a break or continue), or the old value of an
// assignment (R41). A defect is raised at once; the frame's End then
// releases what it still owns and keeps every defect (R9).
func (h *Owned) Release() {
	if h == nil || h.e == nil {
		return
	}
	e := h.e
	h.drop()
	e.own()
}

// runOwn is the frame's release of an owned value at its End: a defect
// joins the frame's exits, and the releases after it still run (R9).
func (s *Scope) runOwn(e *entry) {
	t := s.s.Task()
	defer func() {
		if p := recover(); p != nil {
			s.exits.Add(capture(t, p))
		}
	}()
	e.own()
}

// settleKept ends the values the frame returned (Returned): on a failed
// exit e, each is released, last first, and its defects join e; on
// success they leave with the value.
func (s *Scope) settleKept(e error) error {
	kept := s.kept
	s.kept = nil
	if e == nil {
		return nil
	}
	s.exits = rt.Cause{}
	for i := len(kept) - 1; i >= 0; i-- {
		s.runOwn(kept[i])
	}
	if len(s.exits.Reasons) > 0 {
		e = join(e, s.exits.Err())
	}
	return e
}
