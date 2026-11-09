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
// A Ref owns its value (V6, owner q46): no binding outside the cell sees a
// later write to it. Most functions never write their parameter in place
// (the value rule copies a parameter before a write), but a function
// literal written at the call of Update or Modify takes the held value
// over and may write it in place (RefUpdateOwned, RefModifyOwned), as the
// lowering decides. The cell keeps that sound by copy on write: shared
// records that the held value may be reached from outside the cell (NewRef,
// Get, Set, and the plain Update and Modify, whose function may keep its
// argument or return a shared value). An owned call copies a shared value,
// under the lock, before its function gets it, so a value that Get handed
// out is never written again; the copy happens at most once per share, and
// never when nothing outside holds the value.
//
// A defect in an Update's or Modify's function (a Go panic) poisons the
// cell: an owned function may have written part of the value in place, so
// the cell no longer holds a whole value. The panic goes on to the caller,
// and every later Get, Set, Update and Modify raises the same panic value
// again, as Rust's Mutex poisoning reports a lock whose holder panicked.
// The unlock is deferred, so a poisoned cell is never left locked. Get and
// Set run no program code under the lock and unlock inline. f has an empty
// row (the lowering's adaptPlainFunc), so it neither waits nor needs a
// capability while the lock is held. No operation is an interruption
// point: none waits for another task beyond the length of one operation.
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
// agent's process; Haskell's IORef and atomicModifyIORef; Rust's
// std::sync::Mutex, which a panic in a holder poisons; Swift's copy on
// write, which tests uniqueness by a reference count where this cell keeps
// one flag.
type Ref[T any] struct {
	mu     sync.Mutex
	v      T
	shared bool
	poison *refPoison
}

// refPoison is the panic value of the defect that poisoned a Ref.
type refPoison struct{ p any }

// NewRef is sync.NewRef: a new cell that holds v. v may be reached from
// outside the cell, so it is shared.
func NewRef[T any](v T) *Ref[T] { return &Ref[T]{v: v, shared: true} }

// Get is the value r holds. The caller keeps it, so the cell marks it
// shared: the next owned Update or Modify copies it first. A copy of a T
// cannot panic, so the unlock needs no defer.
func (r *Ref[T]) Get() T {
	r.mu.Lock()
	if p := r.poison; p != nil {
		r.mu.Unlock()
		panic(p.p)
	}
	r.shared = true
	v := r.v
	r.mu.Unlock()
	return v
}

// Set stores v in r. v may be reached from outside the cell, so it is
// shared. A store cannot panic, so the unlock needs no defer.
func (r *Ref[T]) Set(v T) {
	r.mu.Lock()
	if p := r.poison; p != nil {
		r.mu.Unlock()
		panic(p.p)
	}
	r.v, r.shared = v, true
	r.mu.Unlock()
}

// Update stores f of the value r holds, with no other operation of r
// between the read and the store, and calls f once. f may keep its
// argument or return a value reached from elsewhere, so the stored value
// is shared. The unlock is deferred: f is the program's code, and a defect
// in it poisons r and must not leave r locked.
func (r *Ref[T]) Update(f func(v T) T) {
	r.mu.Lock()
	defer r.end()
	r.check()
	r.v = f(r.v)
	r.shared = true
}

// RefModify is sync.Modify: under r's lock, it calls f once on the value
// r holds, stores the second element of f's result and returns the first.
// The result is the Go form of the Ergo tuple (R, T), an unnamed struct
// with the fields F0 and F1 (the lowering's goType of a tuple). As in
// Update, the stored value is shared.
func RefModify[T, R any](r *Ref[T], f func(v T) struct {
	F0 R
	F1 T
}) R {
	r.mu.Lock()
	defer r.end()
	r.check()
	out := f(r.v)
	r.v, r.shared = out.F1, true
	return out.F0
}

// RefUpdateOwned is Ref.Update called with a function literal that takes
// the held value over (the lowering's owned site, V6): f gets a value no
// binding outside the cell reaches, and may write it in place. A shared
// value is copied first, with clone, under the lock. fresh is the
// lowering's proof that f's result is unshared too; without it the stored
// value is shared.
func RefUpdateOwned[T any](r *Ref[T], f func(v T) T, clone func(v T) T, fresh bool) {
	r.mu.Lock()
	defer r.end()
	r.check()
	v := r.v
	if r.shared {
		v = clone(v)
	}
	r.v = f(v)
	r.shared = !fresh
}

// RefModifyOwned is sync.Modify called with a function literal that takes
// the held value over, as RefUpdateOwned is for Update. fresh is the proof
// that both elements of f's result are unshared, so the value returned
// reaches no part of the value stored.
func RefModifyOwned[T, R any](r *Ref[T], f func(v T) struct {
	F0 R
	F1 T
}, clone func(v T) T, fresh bool) R {
	r.mu.Lock()
	defer r.end()
	r.check()
	v := r.v
	if r.shared {
		v = clone(v)
	}
	out := f(v)
	r.v, r.shared = out.F1, !fresh
	return out.F0
}

// check raises the defect that poisoned r, with r locked; end unlocks it.
func (r *Ref[T]) check() {
	if p := r.poison; p != nil {
		panic(p.p)
	}
}

// end unlocks r at the end of an Update or Modify. A panic in the function
// poisons r (a panic that check raised again keeps the first defect) and
// goes on to the caller.
func (r *Ref[T]) end() {
	if p := recover(); p != nil {
		if r.poison == nil {
			r.poison = &refPoison{p: p}
		}
		r.mu.Unlock()
		panic(p)
	}
	r.mu.Unlock()
}
