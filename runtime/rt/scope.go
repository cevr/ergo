package rt

// Resource is a value with a release (resources.md R1, R2). Close has no
// error result; a panic in Close is a defect that joins the exit cause.
type Resource interface {
	Close()
}

// entry is one registered release: a releaser and its resource. Each releaser
// is a struct of at most one func field, which Go stores in an interface
// without allocating, so a pointer resource and a static close function
// register with no allocation. (A func value of a generic instantiation would
// instead bind its dictionary in a heap closure.)
type entry struct {
	run releaser
	v   any
}

type releaser interface{ release(v any) }

const inlineEntries = 4

// Scope is an owner scope (resources.md R4, R5): it releases what it owns in
// reverse acquisition order when it ends, on every exit. Make one with
// NewScope and end it with a deferred End. A Scope belongs to one task's
// goroutine; do not copy it after first use.
type Scope struct {
	task   *Task
	n      int
	inline [inlineEntries]entry
	more   []entry
	// depth is how many capability calls were open in task when the scope
	// began, on a traced tree: an unwind End recovers closes only the calls
	// opened inside the scope.
	depth int
}

// NewScope returns an empty scope owned by task t.
func NewScope(t *Task) Scope {
	if t.trace != nil {
		return Scope{task: t, depth: t.traceDepth()}
	}
	return Scope{task: t}
}

// Task is the task that owns s.
func (s *Scope) Task() *Task { return s.task }

func (s *Scope) push(e entry) {
	if s.n < inlineEntries {
		s.inline[s.n] = e
	} else {
		s.more = append(s.more, e)
	}
	s.n++
}

func (s *Scope) pop() entry {
	s.n--
	if s.n >= inlineEntries {
		i := s.n - inlineEntries
		e := s.more[i]
		s.more = s.more[:i]
		return e
	}
	return s.inline[s.n]
}

type closeWith[R any] struct{ close func(R) }

func (c closeWith[R]) release(v any) { c.close(v.(R)) }

type closeResource struct{}

func (closeResource) release(v any) { v.(Resource).Close() }

type callFunc struct{ f func() }

func (c callFunc) release(any) { c.f() }

// Acquire runs open and, if it succeeds, registers close(r) with s in the same
// masked step (R8): a stop that arrives during open or between open and the
// registration is observed only after the release is registered, at the next
// poll. A failed open registers nothing.
func Acquire[R any](s *Scope, open func() (R, error), close func(R)) (R, error) {
	return AcquireInto(s.task, open, func(r R) {
		s.push(entry{run: closeWith[R]{close}, v: r})
	})
}

// AcquireInto is Acquire's masked construction and registration boundary
// for an owner with its own release transport. register must publish one
// completed acquisition to that owner's existing list, without running
// user code. A failure or a panic in open never calls register. The
// acquiring region masks t without changing child shielding (I2).
func AcquireInto[R any](t *Task, open func() (R, error), register func(R)) (R, error) {
	defer t.regions.truncate(t.acquire())
	r, err := open()
	if err == nil {
		register(r)
	}
	return r, err
}

// Own registers r's Close with s. Call it inside the mask that acquired r.
func (s *Scope) Own(r Resource) { s.push(entry{run: closeResource{}, v: r}) }

// Defer registers f as a release of s. Like every release it runs masked, and
// a panic in it is a defect, not a lost failure.
func (s *Scope) Defer(f func()) { s.push(entry{run: callFunc{f}}) }

// End ends the scope; defer it directly, as `defer s.End(&err)`. It recovers a
// panic of the body as a defect, combines it with *err, then runs every
// release in reverse order, masked, recording each release defect (R5, R9).
// Last it applies stop-wins, as a task boundary does (Run): a stop the task
// can observe, including one that arrived during a release, makes the exit
// an interruption.
// With a non-nil err it stores the exit in canonical form; with a nil err (a
// body with no error row) it panics with the exit, and the next boundary
// recovers it.
func (s *Scope) End(err *error) {
	p := recover()
	if p == nil && s.n == 0 && !s.task.Stopped() {
		return
	}
	var c Cause
	if err != nil {
		c.Add(*err)
	}
	if p != nil {
		c.recovered(s.task, p, false, s.depth)
	}
	s.release(&c)
	s.task.settle(&c, nil)
	exit := c.Err()
	if err != nil {
		*err = exit
	} else if exit != nil {
		panic(transport{exit})
	}
}

func (s *Scope) release(c *Cause) {
	m := s.task.Mask()
	for s.n > 0 {
		s.runRelease(s.pop(), c)
	}
	m.Unmask()
}

func (s *Scope) runRelease(e entry, c *Cause) {
	if s.task.trace != nil {
		s.runReleaseTraced(e, c)
		return
	}
	defer func() {
		if p := recover(); p != nil {
			c.recovered(s.task, p, true, 0)
		}
	}()
	e.run.release(e.v)
}

// runReleaseTraced is runRelease on a traced tree. It notes the calls open
// when the release begins, so an unwind closes only the calls the release
// opened, and raises the release event. It is a separate frame so the
// untraced release keeps its size: the floor and the extra call grew
// runRelease's frame from 0x48 to 0x70 bytes.
//
//go:noinline
func (s *Scope) runReleaseTraced(e entry, c *Cause) {
	floor := s.task.traceDepth()
	defer func() {
		if p := recover(); p != nil {
			c.recovered(s.task, p, true, floor)
		}
		s.task.traceTask(TraceRelease, nil)
	}()
	e.run.release(e.v)
}

// Release runs f as one release on t for a scope whose entries rt does not
// hold, such as runtime/v1's frame list, and reports it as a Scope reports
// its own releases (runReleaseTraced). The caller masks, as Scope.End does.
// A panic of f is recovered and recorded into c, in causal order:
//
//  1. On a traced tree, the trace calls f opened and left open are closed,
//     never an outer one (the release-local unwind).
//  2. classify, called inside the recover while the panicking frame is still
//     on the stack, returns the value of the fresh defect the panic is (nil
//     when it carries an exit instead) and the exit it carries. c records
//     the defect as raised in a release; its event links the innermost
//     unwound call.
//  3. after, when not nil, runs what the release ends after its body (the
//     body's own frame, whose nested releases then report) with the carried
//     exit, and returns the exits to add; with a nil after, the carried exit
//     is added.
//
// A panic that escapes classify or after is recorded as one more defect of
// the release, so the caller's later releases and its unmask still run.
// When f ends, on every path, a traced tree raises the release event.
func Release(t *Task, c *Cause, f func(), classify func(p any) (defect any, carried error), after func(carried error) error) {
	floor := 0
	if t.trace != nil {
		floor = t.traceDepth()
	}
	defer func() {
		if p := recover(); p != nil {
			var left uint64
			if t.trace != nil {
				left = t.traceUnwind(p, floor)
			}
			func() {
				defer func() {
					if q := recover(); q != nil {
						c.recovered(t, q, true, floor)
					}
				}()
				defect, carried := classify(p)
				if defect != nil {
					c.defect(t, defect, true, left)
				}
				if after != nil {
					carried = after(carried)
				}
				c.Add(carried)
			}()
		}
		if t.trace != nil {
			t.traceTask(TraceRelease, nil)
		}
	}()
	f()
}

// Scoped runs body with a fresh scope owned by t and ends the scope on every
// exit, returning the body's value or the combined exit. If body succeeds
// but scope shutdown fails (including stop-wins), release owns the returned
// value and runs once, masked; its defects join that exit. Pass nil only for
// a result that needs no release. A failed or panicking body gives no value.
func Scoped[T any](t *Task, release func(T), body func(*Scope) (T, error)) (v T, err error) {
	s := NewScope(t)
	returned := false
	defer func() {
		if err != nil {
			left := v
			var zero T
			v = zero
			if returned && release != nil {
				var c Cause
				c.Add(err)
				m := t.Mask()
				s.runRelease(entry{run: closeWith[T]{release}, v: left}, &c)
				m.Unmask()
				t.settle(&c, nil)
				err = c.Err()
			}
		}
	}()
	defer s.End(&err)
	v, err = body(&s)
	returned = err == nil
	return
}
