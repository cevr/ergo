package v1

import "sync"

// bufferPool keeps the buffers std strings lends (StringsBorrow,
// StringsGive), as encoding/json keeps its encodeState: a writer that
// builds a text in a buffer and is done with it gives the buffer back,
// and the next writer appends to it. Any module's writer borrows from it
// (std json's Encode is one), so no codec has a buffer the others lack.
// It holds arrays of one size, as pointers, so a Put boxes no slice
// header (no allocation). A buffer that grew out of its array is not kept.
var bufferPool = sync.Pool{New: func() any { return new([bufferPoolSize]byte) }}

const bufferPoolSize = 1024

// StringsBorrow is an empty buffer from the pool; StringsGive puts it
// back.
func StringsBorrow() []byte { return bufferPool.Get().(*[bufferPoolSize]byte)[:0] }

// StringsGive puts b's array back in the pool when b still has it, and
// gives b. Its caller uses b no more (a writer's last step, after it took
// the text), so the pool holds an array only after its one use has ended.
func StringsGive(b []byte) []byte {
	if cap(b) == bufferPoolSize {
		bufferPool.Put((*[bufferPoolSize]byte)(b[:bufferPoolSize]))
	}
	return b
}
