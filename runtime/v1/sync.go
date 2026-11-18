package v1

import (
	"sync"

	"ergo.local/prototype/runtime/rt"
)

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
// the cell no longer holds a whole value. The panic goes on to the caller
// as it is: the cell does not recover it (only a task's boundary records a
// defect, runtime199 DefectOwner), so the defect keeps its own stack. Every
// later Get, Set, Update and Modify raises the defect refPoisoned, as
// Rust's Mutex reports a lock whose holder panicked with a PoisonError.
// The unlock is deferred, so a poisoned cell is never left locked. Get and
// Set run no program code under the lock and unlock inline. f has an empty
// row (the lowering's adaptPlainFunc), so it needs no capability while the
// lock is held. It may still wait on a handle it captured (a Timeline's
// Wait, a Queue's Pop); the checker does not refuse that yet (follow-up
// REF-WAIT), and masked, a stop does not end that wait. No operation is an
// interruption point: none waits for another task beyond the length of
// one operation.
//
// A stop is not a defect (REF-MASK; ruling of 2026-11-16). f runs with the
// caller's environment, and its lowered loop heads and impure entries
// poll a stop there, so a stop requested while f runs would unwind it
// half way: an owned f leaves a half-written value, and the cell would
// poison itself on a request. So f runs masked on the caller's task t: a
// stop requested meanwhile is observed at the caller's next interruption
// point after the update, and the cell always holds a whole value. Only a
// defect poisons it. Prior art: Effect's Ref.update and modify are atomic
// and never interrupted part way; Rust's Mutex is poisoned by a panic, and
// a cancellation cannot come inside a sync guard.
//
// While the program runs one task (rt.Alone: no task or group has been
// started yet), no other goroutine can reach the cell, and an operation
// takes no lock (lock, unlock; OWN1 REF-ALONE): json's Stream, a Ref
// read and written about 13 times per decoded event, pays the lock only
// in a program that runs tasks.
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
	poison bool
}

// refPoisoned is the defect an operation of a poisoned Ref raises.
const refPoisoned = "sync.Ref is poisoned: a defect in an earlier Update or Modify may have left its value half written"

// NewRef is sync.NewRef: a new cell that holds v. v may be reached from
// outside the cell, so it is shared.
//
//ergo:wait none
func NewRef[T any](v T) *Ref[T] {
	r := &Ref[T]{v: v, shared: true}
	if rt.Exploring {
		nameHandle([]string{"sync.NewRef"}, "", r)
	}
	return r
}

// Get is the value r holds. The caller keeps it, so the cell marks it
// shared: the next owned Update or Modify copies it first. A copy of a T
// cannot panic, so the unlock needs no defer.
//
//ergo:wait none
func (r *Ref[T]) Get() T {
	rt.PointRunning(rt.Op{Kind: rt.OpRead, On: r})
	var op refOp
	op.lock(&r.mu)
	if r.poison {
		op.unlock(&r.mu)
		panic(refPoisoned)
	}
	r.shared = true
	v := r.v
	op.unlock(&r.mu)
	return v
}

// Set stores v in r. v may be reached from outside the cell, so it is
// shared. A store cannot panic, so the unlock needs no defer.
//
//ergo:wait none
func (r *Ref[T]) Set(v T) {
	rt.PointRunning(rt.Op{Kind: rt.OpWrite, On: r})
	var op refOp
	op.lock(&r.mu)
	if r.poison {
		op.unlock(&r.mu)
		panic(refPoisoned)
	}
	r.v, r.shared = v, true
	op.unlock(&r.mu)
}

// Update stores f of the value r holds, with no other operation of r
// between the read and the store, and calls f once. f may keep its
// argument or return a value reached from elsewhere, so the stored value
// is shared. The unlock is deferred: f is the program's code, and a defect
// in it poisons r and must not leave r locked.
//
//ergo:wait none
func (r *Ref[T]) Update(t *rt.Task, f func(v T) T) {
	rt.PointRunning(rt.Op{Kind: rt.OpWrite, On: r})
	r.mu.Lock()
	defer r.mu.Unlock()
	m := t.Mask() // f runs masked on the caller's task (REF-MASK)
	defer m.Unmask()
	r.check()
	r.poison = true // until f returns (check)
	r.v = f(r.v)
	r.shared, r.poison = true, false
}

// RefModify is sync.Modify: under r's lock, it calls f once on the value
// r holds, stores the second element of f's result and returns the first.
// The result is the Go form of the Ergo tuple (R, T), an unnamed struct
// with the fields F0 and F1 (the lowering's goType of a tuple). As in
// Update, the stored value is shared.
//
//ergo:wait none
func RefModify[T, R any](t *rt.Task, r *Ref[T], f func(v T) struct {
	F0 R
	F1 T
}) R {
	rt.PointRunning(rt.Op{Kind: rt.OpWrite, On: r})
	r.mu.Lock()
	defer r.mu.Unlock()
	m := t.Mask() // f runs masked on the caller's task (REF-MASK)
	defer m.Unmask()
	r.check()
	r.poison = true // until f returns (check)
	out := f(r.v)
	r.v, r.shared, r.poison = out.F1, true, false
	return out.F0
}

// RefUpdateOwned is Ref.Update called with a function literal that takes
// the held value over (the lowering's owned site, V6): f gets a value no
// binding outside the cell reaches, and may write it in place. A shared
// value is copied first, with clone, under the lock. fresh is the
// lowering's proof that f's result is unshared too; without it the stored
// value is shared. alone is the lowering's proof that f starts no task and
// runs no code it does not know (its footprint is empty, check.Footprint):
// then, while the program is alone (rt.Alone), the operation takes no lock,
// since nothing f runs can start a second goroutine that reaches r.
//
//ergo:wait none
func RefUpdateOwned[T any](t *rt.Task, r *Ref[T], f func(v T) T, clone func(v T) T, fresh, alone bool) {
	rt.PointRunning(rt.Op{Kind: rt.OpWrite, On: r})
	if !alone || !rt.Alone() {
		r.mu.Lock()
		defer r.mu.Unlock()
	}
	if !alone {
		// f runs masked on the caller's task (REF-MASK); an alone f reaches
		// no interruption point, so it needs no mask.
		m := t.Mask()
		defer m.Unmask()
	}
	r.check()
	r.poison = true // until f returns (check)
	v := r.v
	if r.shared {
		v = clone(v)
	}
	r.v = f(v)
	r.shared, r.poison = !fresh, false
}

// RefModifyOwned is sync.Modify called with a function literal that takes
// the held value over, as RefUpdateOwned is for Update. fresh is the proof
// that both elements of f's result are unshared, so the value returned
// reaches no part of the value stored.
//
//ergo:wait none
func RefModifyOwned[T, R any](t *rt.Task, r *Ref[T], f func(v T) struct {
	F0 R
	F1 T
}, clone func(v T) T, fresh, alone bool) R {
	rt.PointRunning(rt.Op{Kind: rt.OpWrite, On: r})
	if !alone || !rt.Alone() {
		r.mu.Lock()
		defer r.mu.Unlock()
	}
	if !alone {
		// f runs masked on the caller's task (REF-MASK); an alone f reaches
		// no interruption point, so it needs no mask.
		m := t.Mask()
		defer m.Unmask()
	}
	r.check()
	r.poison = true // until f returns (check)
	v := r.v
	if r.shared {
		v = clone(v)
	}
	out := f(v)
	r.v, r.shared, r.poison = out.F1, !fresh, false
	return out.F0
}

// RefUpdateInPlace is RefUpdateOwned for a literal that binds the held
// value to one let mut and returns it (G1, refown.go, inPlace): f writes
// the held value through p, under the lock, and the cell keeps it, so no
// copy of T is made in or out. A shared value is copied first, as
// RefUpdateOwned does. A defect poisons the cell, which then holds a
// value f may have written in part.
//
//ergo:wait none
func RefUpdateInPlace[T any](t *rt.Task, r *Ref[T], f func(p *T), clone func(v T) T, fresh, alone bool) {
	rt.PointRunning(rt.Op{Kind: rt.OpWrite, On: r})
	if !alone || !rt.Alone() {
		r.mu.Lock()
		defer r.mu.Unlock()
	}
	if !alone {
		// f runs masked on the caller's task (REF-MASK); an alone f reaches
		// no interruption point, so it needs no mask.
		m := t.Mask()
		defer m.Unmask()
	}
	r.check()
	r.poison = true // until f returns (check)
	if r.shared {
		r.v = clone(r.v)
	}
	f(&r.v)
	r.shared, r.poison = !fresh, false
}

// RefModifyInPlace is RefModifyOwned for a literal that binds the held
// value to one let mut and returns (x, it): f writes the held value
// through p and returns x (RefUpdateInPlace).
//
//ergo:wait none
func RefModifyInPlace[T, R any](t *rt.Task, r *Ref[T], f func(p *T) R, clone func(v T) T, fresh, alone bool) R {
	rt.PointRunning(rt.Op{Kind: rt.OpWrite, On: r})
	if !alone || !rt.Alone() {
		r.mu.Lock()
		defer r.mu.Unlock()
	}
	if !alone {
		// f runs masked on the caller's task (REF-MASK); an alone f reaches
		// no interruption point, so it needs no mask.
		m := t.Mask()
		defer m.Unmask()
	}
	r.check()
	r.poison = true // until f returns (check)
	if r.shared {
		r.v = clone(r.v)
	}
	x := f(&r.v)
	r.shared, r.poison = !fresh, false
	return x
}

// check raises refPoisoned when r is poisoned, before an Update or Modify
// touches the value.
//
// An Update or Modify then poisons r while f runs: it sets poison before
// it calls f and clears it once f has returned and the value is stored,
// so a defect in f (or in clone) leaves r poisoned, with no deferred flag
// to read. The lock, when the program is not alone (rt.Alone), is
// unlocked by a defer, so a defect never leaves r locked. An owned or
// in-place form told alone (the lowering's proof that f starts no task and
// runs no unknown code) skips the lock while the program is alone: no
// goroutine but this one can reach r, and f cannot start one. Every other
// Update or Modify locks, since its function may start a task that uses r
// before the operation stores (TestRefFlipInsideUpdate).
func (r *Ref[T]) check() {
	if r.poison {
		panic(refPoisoned)
	}
}

// refOp is a Get or Set's hold of a Ref: whether it locked the cell's
// mutex.
type refOp struct{ locked bool }

// lock locks mu unless the program runs one goroutine of Ergo code
// (rt.Alone, OWN1 REF-ALONE): then no other goroutine can reach the cell,
// and the operation runs as the hand-written Go value would, with no
// lock. op remembers whether it locked, for unlock, so the pair never
// depends on Alone staying the same between them. The locking half is out
// of line, so the alone path inlines to one load.
func (op *refOp) lock(mu *sync.Mutex) {
	if !rt.Alone() {
		op.lockMu(mu)
	}
}

// lockMu is lock's locking half, kept out of line (noinline) so that lock
// itself inlines.
//
//go:noinline
func (op *refOp) lockMu(mu *sync.Mutex) {
	mu.Lock()
	op.locked = true
}

// unlock unlocks mu when lock locked it.
func (op *refOp) unlock(mu *sync.Mutex) {
	if op.locked {
		mu.Unlock()
	}
}
