package v1

import (
	"slices"
	"sync"
)

// Op is testing.Op[A, F], one operation of a testing.Script (docs/spec/
// stdlib.md, Testing 7 to 12): the answer the test set and the arguments of
// each call so far. It is a pointer, so every copy of a Script shares its
// operations, and a lock guards them, so two tasks may call one at once.
// The contract:
//
//   - NewOp() is an operation with no answer and no calls.
//   - Answer(f) makes f the answer of every later call.
//   - Calls() is a copy of the arguments of each call so far, oldest
//     first, so a later call never changes a slice it returned.
//   - Call(a) records a and returns the answer, and false when there is
//     none. The methods the lowering derives for a Script call it before
//     they call the answer, outside the lock, so an answer may call the
//     Script again.
//
// The control is a hand-written Go fake: a struct of a mutex, a slice of
// the calls and a func field, with the same work per call (one lock, one
// append).
//
// Prior art: Elixir's Mox (expect and the calls a mock got), Go's mockgen
// (a generated struct per interface) and testify's mock.Called.
type Op[A, F any] struct {
	mu     sync.Mutex
	calls  []A
	answer F
	set    bool
}

// NewOp is an operation of a new Script, with no answer and no calls.
func NewOp[A, F any]() *Op[A, F] { return &Op[A, F]{} }

// Answer makes f the answer of every later call.
func (o *Op[A, F]) Answer(f F) {
	o.mu.Lock()
	o.answer, o.set = f, true
	o.mu.Unlock()
}

// Calls is a copy of the arguments of each call so far, oldest first.
func (o *Op[A, F]) Calls() []A {
	o.mu.Lock()
	out := slices.Clone(o.calls)
	o.mu.Unlock()
	if out == nil {
		out = []A{}
	}
	return out
}

// Call records the arguments a of one call and returns the answer, or
// false when the test set none.
func (o *Op[A, F]) Call(a A) (F, bool) {
	o.mu.Lock()
	o.calls = append(o.calls, a)
	f, ok := o.answer, o.set
	o.mu.Unlock()
	return f, ok
}

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
