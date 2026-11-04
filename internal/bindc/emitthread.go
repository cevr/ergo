package bindc

import (
	"strings"
)

// The panic containment and the owner thread of the Go package
// (GoPackage):
//
//   - each trampoline runs its Go function under cbContain, a deferred
//     recover. A panic becomes a *CallbackPanic (the C typedef, the value
//     and the stack; a value that is a *CallbackPanic already is kept), C
//     gets the callback's default, and the panic is pending on the OS
//     thread of the invocation (cbThread). While it is pending, a
//     trampoline on that thread runs no Go code and gives C the default.
//     A runtime.Goexit cannot be stopped (Go resumes it after a recover),
//     so cbContain ends the process with exit status 2 and one stderr line
//     that names the typedef, before any C frame unwinds;
//   - each typed call of a binding with bound callbacks keeps its goroutine
//     on its OS thread (runtime.LockOSThread, which nests) from before its
//     C call until it has taken the panic pending on that thread (cbTake;
//     one atomic load when none is pending). It does what the C call's
//     status asks of the registry, then panics with the pending panic
//     before it converts a result. So the panic surfaces in the Go frame
//     directly above the C frame, and the registry stays what C did;
//   - Owner (a binding with a handle whose thread is owner) is one
//     goroutine locked to one OS thread and never unlocked, so the thread
//     ends with it. Do from another goroutine runs a function there and
//     waits; Do on the owner thread runs it inline; a panic panics again in
//     the caller of Do, and the runner keeps serving. Close: the jobs that
//     Do handed over before Close run, a later Do returns ErrOwnerClosed,
//     Close returns after the runner goroutine stopped, and Close on the
//     owner thread returns ErrOwnerThread and does nothing. The design is
//     the model owner.qnt of bind-c-2 (noStranded, noStartAfterClose,
//     noSelfWait).

// ownerHandles are the C names of the handle types whose thread is owner.
func (e *goEmitter) ownerHandles() []string {
	var out []string
	for _, h := range e.b.Contract.Handles {
		if h.Thread == "owner" {
			out = append(out, h.Type)
		}
	}
	return out
}

// guardsCalls reports whether the typed calls take pending panics: the
// binding has a bound callback.
func (e *goEmitter) guardsCalls() bool { return len(e.boundCbs()) > 0 }

// threadHelper emits cbThread, the id of the calling OS thread, when the
// containment or the Owner needs it.
func (e *goEmitter) threadHelper() {
	if !e.guardsCalls() && len(e.ownerHandles()) == 0 {
		return
	}
	sc, rt := e.use("syscall"), e.use("runtime")
	e.p(`// cbThread is the id of the calling OS thread: gettid on linux, the
// thread_selfid system call on darwin (pthread_threadid_np reads the same
// id). A goroutine keeps its thread while it is locked with
// runtime.LockOSThread.
func cbThread() uintptr {
	trap := uintptr(%[1]s.SYS_GETTID)
	if %[2]s.GOOS == "darwin" {
		trap = 372 // SYS_thread_selfid
	}
	t, _, _ := %[1]s.RawSyscall(trap, 0, 0, 0)
	return t
}

`, sc, rt)
}

// containment emits CallbackPanic, the pending panics and their helpers.
func (e *goEmitter) containment() {
	pkg := e.b.Contract.Go.Package
	fmtPkg, osPkg, dbg := e.use("fmt"), e.use("os"), e.use("runtime/debug")
	e.p(`// CallbackPanic is a panic of the Go function of a callback that C
// called. The trampoline recovers it before it can unwind C frames, gives C
// the callback's default and keeps it pending on the OS thread of the
// invocation; while it is pending, no Go code runs for an invocation on
// that thread. The typed call that made the C call panics with it after C
// returns: in the outermost typed call, or in a typed call inside an outer
// callback, whose trampoline keeps it (it does not wrap it again).
type CallbackPanic struct {
	// Typedef is the C callback typedef.
	Typedef string
	// Value is the value the Go function panicked with.
	Value any
	// Stack is the stack of the panic (runtime/debug.Stack).
	Stack []byte
}

func (p *CallbackPanic) Error() string {
	return %[1]s.Sprintf(%[2]q, p.Typedef, p.Value)
}

// Unwrap is the panic value when it is an error, else nil.
func (p *CallbackPanic) Unwrap() error {
	err, _ := p.Value.(error)
	return err
}

// The pending panics, by OS thread (cbThread), under cbPendMu. cbPendN
// counts them, so a thread pays one atomic load when none is pending.
var (
	cbPendMu sync.Mutex
	cbPendN  atomic.Int32
	cbPendOn = map[uintptr]*CallbackPanic{}
)

// cbContain is the deferred call of a trampoline: done is true when the Go
// function returned. Else it recovers the panic and keeps it pending on
// this thread. When there is none to recover, the Go function called
// runtime.Goexit, which cannot be stopped: cbContain ends the process with
// exit status 2 before a C frame unwinds. (With GODEBUG=panicnil=1, a
// panic(nil) recovers as nil and ends the process too.)
func cbContain(typedef string, done *bool) {
	if *done {
		return
	}
	v := recover()
	if v == nil {
		%[3]s.Stderr.WriteString(%[4]q + typedef + %[5]q)
		%[3]s.Exit(2)
	}
	p, ok := v.(*CallbackPanic)
	if !ok {
		p = &CallbackPanic{Typedef: typedef, Value: v, Stack: %[6]s.Stack()}
	}
	t := cbThread()
	cbPendMu.Lock()
	if _, had := cbPendOn[t]; !had {
		cbPendOn[t] = p
		cbPendN.Add(1)
	}
	cbPendMu.Unlock()
}

// cbPending reports whether a panic is pending on this thread.
func cbPending() bool {
	return cbPendN.Load() != 0 && cbPendingHere()
}

func cbPendingHere() bool {
	t := cbThread()
	cbPendMu.Lock()
	_, had := cbPendOn[t]
	cbPendMu.Unlock()
	return had
}

// cbTake removes the panic pending on this thread and returns it, or nil.
func cbTake() *CallbackPanic {
	if cbPendN.Load() == 0 {
		return nil
	}
	return cbTakeHere()
}

func cbTakeHere() *CallbackPanic {
	t := cbThread()
	cbPendMu.Lock()
	p, had := cbPendOn[t]
	if had {
		delete(cbPendOn, t)
		cbPendN.Add(-1)
	}
	cbPendMu.Unlock()
	return p
}

`, fmtPkg, pkg+": panic in the callback %s: %v", osPkg, pkg+": runtime.Goexit in the callback ", " would unwind C frames; exit status 2\n", dbg)
}

// ownerRunner emits the owner-thread runner when the binding has a handle
// whose thread is owner.
func (e *goEmitter) ownerRunner() {
	hs := e.ownerHandles()
	if len(hs) == 0 {
		return
	}
	pkg := e.b.Contract.Go.Package
	errs, osPkg, rt := e.use("errors"), e.use("os"), e.use("runtime")
	e.use("sync")
	e.p(`var (
	// ErrOwnerClosed is the error of Do after Close: the function did not run.
	ErrOwnerClosed = %[1]s.New(%[2]q)
	// ErrOwnerThread is the error of Close on the owner thread: Close waits
	// for the runner, which would wait on itself. Close then does nothing.
	ErrOwnerThread = %[1]s.New(%[3]q)
)

// Owner runs functions on one OS thread, for the handles whose thread is
// owner (%[4]s): C lets only the thread that created such a handle use
// it. Its runner is one goroutine locked to that thread with
// runtime.LockOSThread and never unlocked, so the thread ends with it.
type Owner struct {
	// mu: Do reads closed and hands its job over under the read lock; Close
	// sets closed and closes jobs under the lock.
	mu      sync.RWMutex
	closed  bool
	jobs    chan *ownerJob
	stopped chan struct{}
	thread  uintptr
}

// ownerJob is one Do from another goroutine: its function, and the value
// it panicked with.
type ownerJob struct {
	fn       func()
	done     chan struct{}
	panicked bool
	value    any
}

// NewOwner starts the runner of an Owner and returns when its thread is
// known.
func NewOwner() *Owner {
	o := &Owner{jobs: make(chan *ownerJob), stopped: make(chan struct{})}
	ready := make(chan uintptr)
	go ownerServe(o, ready)
	o.thread = <-ready
	return o
}

// ownerServe is the runner: it runs each job it receives, until Close
// closes jobs, and then it stops.
func ownerServe(o *Owner, ready chan<- uintptr) {
	%[5]s.LockOSThread() // never unlocked: the thread ends with the runner
	ready <- cbThread()
	for j := range o.jobs {
		j.value, j.panicked = ownerRun(j.fn)
		close(j.done)
	}
	close(o.stopped)
}

// ownerRun runs fn and returns the value it panicked with. A
// runtime.Goexit in fn would stop the runner and strand every later Do, and
// it cannot be stopped, so ownerRun ends the process with exit status 2.
func ownerRun(fn func()) (v any, panicked bool) {
	done := false
	defer func() {
		if done {
			return
		}
		if v = recover(); v == nil {
			%[6]s.Stderr.WriteString(%[7]q)
			%[6]s.Exit(2)
		}
		panicked = true
	}()
	fn()
	done = true
	return nil, false
}

// Do runs fn on the owner thread and waits for it to return. On the owner
// thread (in a function that Do runs, or in a callback that C calls inside
// one) Do runs fn inline. A panic in fn panics again in the caller of Do,
// and the runner keeps serving. After Close, Do returns ErrOwnerClosed and
// fn does not run.
func (o *Owner) Do(fn func()) error {
	if cbThread() == o.thread {
		fn()
		return nil
	}
	j := &ownerJob{fn: fn, done: make(chan struct{})}
	o.mu.RLock()
	if o.closed {
		o.mu.RUnlock()
		return ErrOwnerClosed
	}
	o.jobs <- j
	o.mu.RUnlock()
	<-j.done
	if j.panicked {
		panic(j.value)
	}
	return nil
}

// Close stops the runner. The functions that Do handed over before Close
// run; a later Do returns ErrOwnerClosed; Close returns after the runner
// goroutine stopped. A second Close waits for the same and returns
// ErrOwnerClosed. On the owner thread Close returns ErrOwnerThread and does
// nothing.
func (o *Owner) Close() error {
	if cbThread() == o.thread {
		return ErrOwnerThread
	}
	o.mu.Lock()
	first := !o.closed
	if first {
		o.closed = true
		close(o.jobs)
	}
	o.mu.Unlock()
	<-o.stopped
	if !first {
		return ErrOwnerClosed
	}
	return nil
}

`, errs, pkg+": the owner is closed", pkg+": Close on the owner thread", strings.Join(hs, ", "), rt, osPkg,
		pkg+": runtime.Goexit in a function of Owner.Do would stop the owner thread; exit status 2\n")
}
