package v1

import (
	"sync"
	"sync/atomic"
)

// The buffers std strings lends (StringsBorrow, StringsGive), as
// encoding/json keeps its encodeState: a writer that builds a text in a
// buffer and is done with it gives the buffer back, and the next writer
// appends to it. Any module's writer borrows from them (std json's Encode
// is one, examples/cborpack's codec another), so no codec has a buffer the
// others lack.
//
// The pools hold arrays of thirteen sizes, the powers of two from 1 KiB to
// 4 MiB, as pointers, so a Put boxes no slice header (no allocation). Each
// pair of a writer type W and a value type T has a size hint
// (StringsHint), the length of its last text: a borrow takes an array of
// the least size that holds it, so a text written after a GC emptied the
// pools (sync.Pool drops its contents over two GCs) allocates one buffer
// of at most twice its size, not a 1 KiB buffer regrown by append.
var (
	buffers1K   = sync.Pool{New: func() any { return new([1 << 10]byte) }}
	buffers2K   = sync.Pool{New: func() any { return new([2 << 10]byte) }}
	buffers4K   = sync.Pool{New: func() any { return new([4 << 10]byte) }}
	buffers8K   = sync.Pool{New: func() any { return new([8 << 10]byte) }}
	buffers16K  = sync.Pool{New: func() any { return new([16 << 10]byte) }}
	buffers32K  = sync.Pool{New: func() any { return new([32 << 10]byte) }}
	buffers64K  = sync.Pool{New: func() any { return new([64 << 10]byte) }}
	buffers128K = sync.Pool{New: func() any { return new([128 << 10]byte) }}
	buffers256K = sync.Pool{New: func() any { return new([256 << 10]byte) }}
	buffers512K = sync.Pool{New: func() any { return new([512 << 10]byte) }}
	buffers1M   = sync.Pool{New: func() any { return new([1 << 20]byte) }}
	buffers2M   = sync.Pool{New: func() any { return new([2 << 20]byte) }}
	buffers4M   = sync.Pool{New: func() any { return new([4 << 20]byte) }}
)

// The hints: hintSlots maps a bufferHint[W, T]{} (a zero-size key, so
// boxing it allocates nothing) to its index in hintTable, which is copied
// on write under hintGrow, so a read takes no lock.
var (
	hintSlots sync.Map
	hintGrow  sync.Mutex
	hintTable atomic.Pointer[[]*atomic.Int64]
)

type bufferHint[W, T any] struct{}

// StringsHint is the size hint of the writer type W writing a T: a handle
// a writer passes to StringsBorrow and StringsGive. Each pair has one.
func StringsHint[W, T any]() int {
	k := any(bufferHint[W, T]{})
	if i, ok := hintSlots.Load(k); ok {
		return i.(int)
	}
	hintGrow.Lock()
	defer hintGrow.Unlock()
	if i, ok := hintSlots.Load(k); ok {
		return i.(int)
	}
	var next []*atomic.Int64
	if t := hintTable.Load(); t != nil {
		next = append(next, *t...)
	}
	next = append(next, new(atomic.Int64))
	hintTable.Store(&next)
	hintSlots.Store(k, len(next)-1)
	return len(next) - 1
}

// hintAt is the hint of handle h, or nil for a number StringsHint never
// gave: such a writer borrows with no hint and records none.
func hintAt(h int) *atomic.Int64 {
	t := hintTable.Load()
	if t == nil || h < 0 || h >= len(*t) {
		return nil
	}
	return (*t)[h]
}

// StringsBorrow is an empty buffer for the writer of hint h: an array from
// the pool of the least size that holds the hint, or, past the largest
// size, a new buffer of the hint's capacity.
func StringsBorrow(h int) []byte {
	var n int64
	if p := hintAt(h); p != nil {
		n = p.Load()
	}
	switch {
	case n <= 1<<10:
		return buffers1K.Get().(*[1 << 10]byte)[:0]
	case n <= 2<<10:
		return buffers2K.Get().(*[2 << 10]byte)[:0]
	case n <= 4<<10:
		return buffers4K.Get().(*[4 << 10]byte)[:0]
	case n <= 8<<10:
		return buffers8K.Get().(*[8 << 10]byte)[:0]
	case n <= 16<<10:
		return buffers16K.Get().(*[16 << 10]byte)[:0]
	case n <= 32<<10:
		return buffers32K.Get().(*[32 << 10]byte)[:0]
	case n <= 64<<10:
		return buffers64K.Get().(*[64 << 10]byte)[:0]
	case n <= 128<<10:
		return buffers128K.Get().(*[128 << 10]byte)[:0]
	case n <= 256<<10:
		return buffers256K.Get().(*[256 << 10]byte)[:0]
	case n <= 512<<10:
		return buffers512K.Get().(*[512 << 10]byte)[:0]
	case n <= 1<<20:
		return buffers1M.Get().(*[1 << 20]byte)[:0]
	case n <= 2<<20:
		return buffers2M.Get().(*[2 << 20]byte)[:0]
	case n <= 4<<20:
		return buffers4M.Get().(*[4 << 20]byte)[:0]
	}
	return make([]byte, 0, n)
}

// StringsGive records len(b) as hint h, puts b's array back in the pool of
// the largest size it holds (an array grown by append as well), and gives
// b. An array of less than 1 KiB, or of 8 MiB or more, is not kept. Its
// caller uses b no more (a writer's last step, after it took the text), so
// a pool holds an array only after its one use has ended.
func StringsGive(b []byte, h int) []byte {
	if p := hintAt(h); p != nil {
		p.Store(int64(len(b)))
	}
	switch c := cap(b); {
	case c < 1<<10 || c >= 8<<20:
	case c < 2<<10:
		buffers1K.Put((*[1 << 10]byte)(b[:1<<10]))
	case c < 4<<10:
		buffers2K.Put((*[2 << 10]byte)(b[:2<<10]))
	case c < 8<<10:
		buffers4K.Put((*[4 << 10]byte)(b[:4<<10]))
	case c < 16<<10:
		buffers8K.Put((*[8 << 10]byte)(b[:8<<10]))
	case c < 32<<10:
		buffers16K.Put((*[16 << 10]byte)(b[:16<<10]))
	case c < 64<<10:
		buffers32K.Put((*[32 << 10]byte)(b[:32<<10]))
	case c < 128<<10:
		buffers64K.Put((*[64 << 10]byte)(b[:64<<10]))
	case c < 256<<10:
		buffers128K.Put((*[128 << 10]byte)(b[:128<<10]))
	case c < 512<<10:
		buffers256K.Put((*[256 << 10]byte)(b[:256<<10]))
	case c < 1<<20:
		buffers512K.Put((*[512 << 10]byte)(b[:512<<10]))
	case c < 2<<20:
		buffers1M.Put((*[1 << 20]byte)(b[:1<<20]))
	case c < 4<<20:
		buffers2M.Put((*[2 << 20]byte)(b[:2<<20]))
	default:
		buffers4M.Put((*[4 << 20]byte)(b[:4<<20]))
	}
	return b
}
