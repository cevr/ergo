package v1

import (
	"sync/atomic"

	"ergo.local/prototype/runtime/rt"
)

// Int64 is atomic.Int64's shared handle. The struct is never copied: Ergo
// values carry its pointer. Operations use Go's sequentially consistent
// atomics directly, with no lock, callback, task wait or unsafe code.
type Int64 struct{ value atomic.Int64 }

func NewInt64(value int64) *Int64 {
	c := &Int64{}
	c.value.Store(value)
	return c
}

// Methods preserve the ordinary operation-type task ABI so the same
// handle can implement a source operation type. Scalar atomics ignore it.
func (c *Int64) Load(_ *rt.Task) int64              { return c.value.Load() }
func (c *Int64) Store(_ *rt.Task, value int64)      { c.value.Store(value) }
func (c *Int64) Add(_ *rt.Task, delta int64) int64  { return c.value.Add(delta) }
func (c *Int64) Swap(_ *rt.Task, value int64) int64 { return c.value.Swap(value) }
func (c *Int64) CompareAndSwap(_ *rt.Task, old, value int64) bool {
	return c.value.CompareAndSwap(old, value)
}

// Bool is a shared flag, with the same ordering and identity as Int64.
type Bool struct{ value atomic.Bool }

func NewBool(value bool) *Bool {
	f := &Bool{}
	f.value.Store(value)
	return f
}

func (f *Bool) Load(_ *rt.Task) bool             { return f.value.Load() }
func (f *Bool) Store(_ *rt.Task, value bool)     { f.value.Store(value) }
func (f *Bool) Swap(_ *rt.Task, value bool) bool { return f.value.Swap(value) }
func (f *Bool) CompareAndSwap(_ *rt.Task, old, value bool) bool {
	return f.value.CompareAndSwap(old, value)
}
