package v1

import "sync"

// Ref is sync.Ref[T], the body of the std package sync (docs/spec/stdlib.md,
// Sync and STM 1): one value of T that several tasks may read and change.
// It is a pointer, so every copy of a Ref, in a field, an argument, a
// capture or a task, is the same cell: a Ref is a synchronised handle,
// which runtime.md, Tasks rule 6, lets tasks share. The contract:
//
//   - NewRef(v) is a new cell that holds v.
//   - Get() is the value the last Set, Update or Modify stored, or the
//     value of NewRef when none has.
//   - Set(v) stores v.
//   - Update(f) stores f(v), with v the value held. No other operation of
//     the cell runs between the read of v and the store, so two Updates at
//     once never lose one of them. f is called exactly once, never
//     retried, so a function that counts its calls sees one.
//   - RefModify(r, f) is sync.Modify: it stores the second element of
//     f(v) and returns the first, under the same lock as Update.
//
// The lock is a sync.Mutex held for the length of one operation. Update
// and RefModify unlock by a defer, so a defect in f (a Go panic) leaves
// the cell usable by the other tasks, holding the value it held before f;
// Get and Set run no program code under the lock and unlock inline. f has
// an empty row (the lowering's adaptPlainFunc), so it neither waits nor
// needs a capability while the lock is held. No operation is an
// interruption point: none waits for another task beyond the length of
// one operation.
//
// The control is a hand-written Go value guarded by a sync.Mutex
// (sync_bench_test.go); the body is that value. A Ref built on
// atomic.Pointer reads without the lock, but each store allocates a new
// value, and an Update still needs the lock to call f once; the benchmark
// measures it as a second control.
//
// Prior art: Effect's Ref (an AtomicReference, whose update is a pure
// function and whose SynchronizedRef serialises effectful updates); Go's
// sync.Mutex and sync/atomic; Elixir's Agent, whose update runs in the
// agent's process; Haskell's IORef and atomicModifyIORef.
type Ref[T any] struct {
	mu sync.Mutex
	v  T
}

// NewRef is sync.NewRef: a new cell that holds v.
func NewRef[T any](v T) *Ref[T] { return &Ref[T]{v: v} }

// Get is the value r holds. A copy of a T cannot panic, so the unlock
// needs no defer.
func (r *Ref[T]) Get() T {
	r.mu.Lock()
	v := r.v
	r.mu.Unlock()
	return v
}

// Set stores v in r. A store cannot panic, so the unlock needs no defer.
func (r *Ref[T]) Set(v T) {
	r.mu.Lock()
	r.v = v
	r.mu.Unlock()
}

// Update stores f of the value r holds, with no other operation of r
// between the read and the store, and calls f once. The unlock is
// deferred: f is the program's code, and a defect in it must not leave r
// locked.
func (r *Ref[T]) Update(f func(v T) T) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.v = f(r.v)
}

// RefModify is sync.Modify: under r's lock, it calls f once on the value
// r holds, stores the second element of f's result and returns the first.
// The result is the Go form of the Ergo tuple (R, T), an unnamed struct
// with the fields F0 and F1 (the lowering's goType of a tuple).
func RefModify[T, R any](r *Ref[T], f func(v T) struct {
	F0 R
	F1 T
}) R {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := f(r.v)
	r.v = out.F1
	return out.F0
}
