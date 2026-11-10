package v1

import (
	"sync/atomic"
	"testing"
)

// BenchmarkAtomic compares the same native operations. Run in the shared
// quiet-host window; these are controls, not checked-in timing claims.
func BenchmarkAtomic(b *testing.B) {
	b.Run("Add/ergo", func(b *testing.B) {
		c := NewInt64(0)
		for b.Loop() {
			c.Add(nil, 1)
		}
	})
	b.Run("Add/go", func(b *testing.B) {
		var c atomic.Int64
		for b.Loop() {
			c.Add(1)
		}
	})
	b.Run("Flag/ergo", func(b *testing.B) {
		f := NewBool(false)
		for b.Loop() {
			f.Swap(nil, true)
		}
	})
	b.Run("Flag/go", func(b *testing.B) {
		var f atomic.Bool
		for b.Loop() {
			f.Swap(true)
		}
	})
}
