package v1

import (
	"sync"
	"sync/atomic"
	"testing"

	"ergo.local/prototype/runtime/rt"
)

// Benchmarks of std sync's Ref against hand-written Go (stdlib.md, Sync and
// STM 1). The ergo side is the shape of the Go the lowering emits for
// r.Update(func(v int) int { v + 1 }): the adapter of an Ergo function
// literal (lowerer.adaptPlainFunc), which calls the literal's uniform
// shape with the caller's environment and drops its error. The controls
// are a value guarded by a sync.Mutex, written by hand, and an
// atomic.Pointer, whose Get takes no lock and whose Set allocates.

type benchMutexInt struct {
	mu sync.Mutex
	v  int
}

func (m *benchMutexInt) get() int {
	m.mu.Lock()
	v := m.v
	m.mu.Unlock()
	return v
}

func (m *benchMutexInt) set(v int) {
	m.mu.Lock()
	m.v = v
	m.mu.Unlock()
}

func (m *benchMutexInt) update(f func(int) int) {
	m.mu.Lock()
	m.v = f(m.v)
	m.mu.Unlock()
}

type benchAtomicInt struct {
	mu sync.Mutex // serialises update, so f runs once
	p  atomic.Pointer[int]
}

func (a *benchAtomicInt) get() int { return *a.p.Load() }

func (a *benchAtomicInt) set(v int) { a.p.Store(&v) }

func (a *benchAtomicInt) update(f func(int) int) {
	a.mu.Lock()
	v := f(*a.p.Load())
	a.p.Store(&v)
	a.mu.Unlock()
}

var benchRefSink int

// benchErgoIncrement is the uniform shape of the literal
// func(v int) int { v + 1 } (typeCtx.funcType); a literal with no call
// and no loop polls no stop, so it has no env.task.Check (measured in the
// emitted Go of TestSyncRuns' program).
func benchErgoIncrement(env *benchEnv, v int) (int, error) {
	_ = env
	return v + 1, nil
}

// updateDeferred is benchMutexInt's update with the unlock deferred, so a
// panic in f leaves the value unlocked: the guarantee the ergo body keeps
// (TestRefSurvivesADefect), written by hand.
func (m *benchMutexInt) updateDeferred(f func(int) int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.v = f(m.v)
}

// BenchmarkRef: Get, Set and Update of one Ref on one goroutine, and
// Update from every processor at once, for the ergo body and each
// control.
func BenchmarkRef(b *testing.B) {
	env := &benchEnv{task: rt.Root("bench")}
	b.Run("Get/ergo", func(b *testing.B) {
		r := NewRef(1)
		for b.Loop() {
			benchRefSink = r.Get()
		}
	})
	b.Run("Get/mutex", func(b *testing.B) {
		m := &benchMutexInt{v: 1}
		for b.Loop() {
			benchRefSink = m.get()
		}
	})
	b.Run("Get/atomic", func(b *testing.B) {
		a := &benchAtomicInt{}
		a.set(1)
		for b.Loop() {
			benchRefSink = a.get()
		}
	})
	b.Run("Set/ergo", func(b *testing.B) {
		r := NewRef(1)
		i := 0
		for b.Loop() {
			i++
			r.Set(i)
		}
	})
	b.Run("Set/mutex", func(b *testing.B) {
		m := &benchMutexInt{}
		i := 0
		for b.Loop() {
			i++
			m.set(i)
		}
	})
	b.Run("Set/atomic", func(b *testing.B) {
		a := &benchAtomicInt{}
		i := 0
		for b.Loop() {
			i++
			a.set(i)
		}
	})
	b.Run("Update/ergo", func(b *testing.B) {
		r := NewRef(0)
		for b.Loop() {
			r.Update(func(p int) int {
				v, _ := benchErgoIncrement(env, p)
				return v
			})
		}
	})
	b.Run("Update/mutex", func(b *testing.B) {
		m := &benchMutexInt{}
		for b.Loop() {
			m.update(func(v int) int { return v + 1 })
		}
	})
	b.Run("Update/mutexdefer", func(b *testing.B) {
		m := &benchMutexInt{}
		for b.Loop() {
			m.updateDeferred(func(v int) int { return v + 1 })
		}
	})
	b.Run("Update/atomic", func(b *testing.B) {
		a := &benchAtomicInt{}
		a.set(0)
		for b.Loop() {
			a.update(func(v int) int { return v + 1 })
		}
	})
	b.Run("UpdateParallel/ergo", func(b *testing.B) {
		r := NewRef(0)
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				r.Update(func(p int) int {
					v, _ := benchErgoIncrement(env, p)
					return v
				})
			}
		})
	})
	b.Run("UpdateParallel/mutex", func(b *testing.B) {
		m := &benchMutexInt{}
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				m.update(func(v int) int { return v + 1 })
			}
		})
	})
}
