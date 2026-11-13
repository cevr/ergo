package v1

import (
	"slices"
	"strconv"
	"strings"
	"sync"

	"ergo.local/prototype/runtime/rt"
)

// Op is testing.Op[A, F], one operation of a testing.Script (docs/spec/
// stdlib.md, Testing 7 to 12): the answer the test set and the arguments of
// each call so far. It is a pointer, so every copy of a Script shares its
// operations, and a lock guards them, so two tasks may call one at once.
// The contract:
//
//   - NewOp(script, op) is an operation with no answer and no calls; script
//     and op name it in a defect and in the end-of-test report.
//   - Answer(f) makes f the answer of every later call, and ends a hold:
//     a later call is answered by f, a call held already stays held.
//   - Hold() makes every later call wait, held, until a Release answers it.
//   - Release(f) answers the oldest held call with f. With no call held,
//     the next call that would be held takes f and does not wait, so a test
//     may release before the code under test calls. A release no call took
//     fails the test at its end, as a held call does.
//   - Held() is the arguments of each held call no Release answered, oldest
//     first. A stop of a held caller ends its wait, not the hold: the call
//     stays held until a Release takes it, so the test accounts for it.
//   - Calls() is a copy of the arguments of each call so far, oldest
//     first, so a later call never changes a slice it returned; it marks
//     them read. Unread() is the arguments of the calls no Calls() read.
//   - Call(t, a) records a and returns the answer, and false when there is
//     none. A held call waits on t for its Release. The methods the
//     lowering derives for a Script call it before they call the answer,
//     outside the lock, so an answer may call the Script again.
//
// The control is a hand-written Go fake: a struct of a mutex, a slice of
// the calls and a func field, with the same work per call (one lock, one
// append).
//
// Prior art: Elixir's Mox (expect and the calls a mock got), Go's mockgen
// (a generated struct per interface) and testify's mock.Called; Foldkit's
// Command.resolve (a command the scene resolves later) and expectExact (a
// scene that ends with a command it did not resolve fails).
type Op[A, F any] struct {
	mu     sync.Mutex
	calls  []A
	read   int // calls[:read] were returned by Calls
	answer F
	set    bool
	hold   bool
	held   []*heldCall[A, F] // oldest first
	early  []F               // releases no held call has taken yet
	wake   chan struct{}     // closed by a Release while a caller waits
	domain []*rt.Task        // the held callers parked in an auto clock's domain
	script string
	name   string
}

// heldCall is one held call: its arguments, and its answer once a Release
// gave one.
type heldCall[A, F any] struct {
	args   A
	answer F
	done   bool
}

// NewOp is the operation op of a new Script of the type script, with no
// answer and no calls.
func NewOp[A, F any](script, op string) *Op[A, F] { return &Op[A, F]{script: script, name: op} }

// Answer makes f the answer of every later call and ends the hold.
func (o *Op[A, F]) Answer(f F) {
	o.mu.Lock()
	o.answer, o.set, o.hold = f, true, false
	o.mu.Unlock()
}

// Calls is a copy of the arguments of each call so far, oldest first. It
// marks them read.
func (o *Op[A, F]) Calls() []A {
	o.mu.Lock()
	out := slices.Clone(o.calls)
	o.read = len(o.calls)
	o.mu.Unlock()
	if out == nil {
		out = []A{}
	}
	return out
}

// Unread is the arguments of each call no Calls read, oldest first.
func (o *Op[A, F]) Unread() []A {
	o.mu.Lock()
	out := slices.Clone(o.calls[o.read:])
	o.mu.Unlock()
	if out == nil {
		out = []A{}
	}
	return out
}

// Hold makes every later call wait until a Release answers it.
func (o *Op[A, F]) Hold() {
	o.mu.Lock()
	o.hold = true
	o.mu.Unlock()
	scriptHeld.add(o)
}

// Release answers the oldest held call with f, or the next call held when
// none is. A release no call takes is reported at the end of the test.
func (o *Op[A, F]) Release(f F) {
	o.mu.Lock()
	if len(o.held) == 0 {
		o.early = append(o.early, f)
		o.mu.Unlock()
		scriptHeld.add(o)
		return
	}
	h := o.held[0]
	o.held[0] = nil
	o.held = o.held[1:]
	h.answer, h.done = f, true
	o.signal()
	o.mu.Unlock()
}

// Held is the arguments of each held call no Release answered, oldest
// first.
func (o *Op[A, F]) Held() []A {
	o.mu.Lock()
	out := make([]A, len(o.held))
	for i, h := range o.held {
		out[i] = h.args
	}
	o.mu.Unlock()
	return out
}

// Call records the arguments a of one call and returns the answer, or
// false when the test set none. A held call waits on t for its Release
// and returns the released answer; a stop of t ends the wait with
// rt.Interrupted and leaves the call held.
func (o *Op[A, F]) Call(t *rt.Task, a A) (F, bool) {
	o.mu.Lock()
	o.calls = append(o.calls, a)
	if !o.hold {
		f, ok := o.answer, o.set
		o.mu.Unlock()
		return f, ok
	}
	if len(o.early) > 0 {
		f := o.early[0]
		var zero F
		o.early[0] = zero
		o.early = o.early[1:]
		o.mu.Unlock()
		return f, true
	}
	h := &heldCall[A, F]{args: a}
	o.held = append(o.held, h)
	stop := t.Done()
	for !h.done {
		if !o.wait(t, stop) && !h.done {
			o.mu.Unlock()
			panic(rt.Interrupted)
		}
	}
	o.mu.Unlock()
	return h.answer, true
}

// wait releases o.mu until the next Release or until stop closes, then
// takes it again, as Queue.wait does: in an auto clock's domain the caller
// parks under o.mu, so a VirtualClock moves past a held call. It reports
// false when stop woke it; a nil stop (a masked task) never does.
func (o *Op[A, F]) wait(t *rt.Task, stop <-chan struct{}) bool {
	if o.wake == nil {
		o.wake = make(chan struct{})
	}
	ch := o.wake
	var p rt.Parked
	if t.InDomain() {
		p = rt.Park(t, o, stop, nil)
		o.domain = append(o.domain, t)
	}
	o.mu.Unlock()
	rt.Block(t)
	woke := true
	select {
	case <-ch:
	case <-stop:
		woke = false
	}
	rt.Unblock(t)
	o.mu.Lock()
	if t.InDomain() {
		p.Unpark()
		if i := slices.Index(o.domain, t); i >= 0 {
			o.domain = slices.Delete(o.domain, i, i+1)
		}
	}
	return woke
}

// signal wakes every held caller, which looks again; a caller of an auto
// clock's domain is unparked first. o.mu is held.
func (o *Op[A, F]) signal() {
	if o.wake == nil {
		return
	}
	for _, w := range o.domain {
		rt.Waking(w, o).Done()
	}
	close(o.wake)
	o.wake = nil
}

// heldText is the end-of-test report of o: a clause for its held calls no
// Release answered and one for its releases no call took, joined by "; ",
// and "" when there are neither. A release no call took is a script that
// expected a call the code never made.
func (o *Op[A, F]) heldText() string {
	o.mu.Lock()
	n, r := len(o.held), len(o.early)
	o.mu.Unlock()
	var parts []string
	switch {
	case n == 1:
		parts = append(parts, o.script+": "+o.name+" has a held call that no Release answered")
	case n > 1:
		parts = append(parts, o.script+": "+o.name+" has "+strconv.Itoa(n)+" held calls that no Release answered")
	}
	switch {
	case r == 1:
		parts = append(parts, o.script+": "+o.name+" was released with no held call")
	case r > 1:
		parts = append(parts, o.script+": "+o.name+" was released "+strconv.Itoa(r)+" times with no held call")
	}
	return strings.Join(parts, "; ")
}

// scriptHeld is the ledger of the operations a Hold or an early Release
// touched since the last end of a test case, in the order of their first
// touch. Its lock is never taken under an Op's, and an Op's never under it.
var scriptHeld heldLedger

type heldLedger struct {
	mu  sync.Mutex
	ops []interface{ heldText() string }
}

func (l *heldLedger) add(o interface{ heldText() string }) {
	l.mu.Lock()
	if !slices.Contains(l.ops, o) {
		l.ops = append(l.ops, o)
	}
	l.mu.Unlock()
}

// report is the text of every held call of the ledger's operations, joined
// by "; ", and "" when none is held. take empties the ledger.
func (l *heldLedger) report(take bool) string {
	l.mu.Lock()
	ops := l.ops
	if take {
		l.ops = nil
	}
	l.mu.Unlock()
	var parts []string
	for _, o := range ops {
		if s := o.heldText(); s != "" {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, "; ")
}

// ScriptUnanswered is testing.Unanswered(): for each Script operation, its
// held calls no Release answered, "<Script>: <Op> has a held call that no
// Release answered", and its releases no call took, "<Script>: <Op> was
// released with no held call", joined by "; ", and "" when there are none.
func ScriptUnanswered() string { return scriptHeld.report(false) }

// ScriptHeldTake is the end-of-test check (stdlib.md, Testing 10): the
// text ScriptUnanswered gives, and the ledger starts again empty for the
// next case. A test case that left a call held or a release untaken fails
// with it.
func ScriptHeldTake() string { return scriptHeld.report(true) }

// NoAnswerDefect is the defect of a call of a Script operation with no
// answer: Script is the Script's type, testing.Script[Photos], and Op the
// operation.
type NoAnswerDefect struct {
	Script string
	Op     string
}

// String is the defect's text, which the entry's report writes after
// "panic: ".
func (d *NoAnswerDefect) String() string { return d.Script + ": " + d.Op + " has no answer" }

// NoAnswer panics with the defect of a call of op of script with no answer.
// A derived method of a Script calls it in a position that expects a value
// of type T, and returns no value.
func NoAnswer[T any](script, op string) T { panic(&NoAnswerDefect{Script: script, Op: op}) }
