package eval

import "unsafe"

// Boxed ints (ORACLE O32). A Value is an interface, and Go allocates the
// eight bytes of every int64 it boxes past 255. Most ints a program
// computes are small, so the evaluator boxes an int64 in
// [smallMin, smallMax) as an interface over one shared, never-written
// array: no allocation, and nothing for the collector to sweep.

const (
	smallMin = -1024
	smallMax = 1 << 16
)

var smallInts = func() *[smallMax - smallMin]int64 {
	var a [smallMax - smallMin]int64
	for i := range a {
		a[i] = int64(i) + smallMin
	}
	return &a
}()

// int64Type is the type word of an int64 in an interface.
var int64Type = func() unsafe.Pointer {
	var v any = int64(smallMax)
	return (*[2]unsafe.Pointer)(unsafe.Pointer(&v))[0]
}()

// boxInt is n as a Value, with no allocation when n is small.
func boxInt(n int64) Value {
	if n < smallMin || n >= smallMax {
		return n
	}
	var v any
	w := (*[2]unsafe.Pointer)(unsafe.Pointer(&v))
	w[0], w[1] = int64Type, unsafe.Pointer(&smallInts[n-smallMin])
	return v
}
