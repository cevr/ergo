//go:build goexperiment.simd && amd64

package kernel

import (
	"math/bits"
	"simd/archsimd"
	"unsafe"
)

// width is the bytes of one vector: AVX2's 256 bits.
const width = 32

// Enabled reports whether the vector kernels run: on a CPU with AVX2. At
// GOAMD64=v3 and above the check folds away.
//
//ergo:wait none
func Enabled() bool { return archsimd.X86.AVX2() }

// load is the 32 bytes of s at i, read in place. Safety invariant: the
// caller holds i+width <= len(s), so every byte read is a byte of s; the
// vector is only compared, never stored, so s's bytes are never written.
// A string's bytes are immutable in Go, and the view lives only for the
// load. A read past the end would be a defect: the tests run every
// length from 0 to 3 widths plus a tail at every offset.
func load(s string, i int) archsimd.Uint8x32 {
	return archsimd.LoadUint8x32Array((*[width]uint8)(unsafe.Pointer(unsafe.StringData(s[i:]))))
}

// IndexByte is the first index of c in s, or -1.
//
//ergo:wait none
func IndexByte(s string, c uint8) int {
	n := archsimd.BroadcastUint8x32(c)
	i := 0
	for ; i+width <= len(s); i += width {
		if m := load(s, i).Equal(n).ToBits(); m != 0 {
			return i + bits.TrailingZeros32(m)
		}
	}
	for ; i < len(s); i++ {
		if s[i] == c {
			return i
		}
	}
	return -1
}

// IndexAny2 is the first index of a or b in s, or -1.
//
//ergo:wait none
func IndexAny2(s string, a uint8, b uint8) int {
	na, nb := archsimd.BroadcastUint8x32(a), archsimd.BroadcastUint8x32(b)
	i := 0
	for ; i+width <= len(s); i += width {
		v := load(s, i)
		if m := v.Equal(na).Or(v.Equal(nb)).ToBits(); m != 0 {
			return i + bits.TrailingZeros32(m)
		}
	}
	for ; i < len(s); i++ {
		if s[i] == a || s[i] == b {
			return i
		}
	}
	return -1
}

// IndexOutside is the first index of a byte below lo or above hi, or -1.
//
//ergo:wait none
func IndexOutside(s string, lo uint8, hi uint8) int {
	nl, nh := archsimd.BroadcastUint8x32(lo), archsimd.BroadcastUint8x32(hi)
	i := 0
	for ; i+width <= len(s); i += width {
		v := load(s, i)
		if m := v.Less(nl).Or(v.Greater(nh)).ToBits(); m != 0 {
			return i + bits.TrailingZeros32(m)
		}
	}
	for ; i < len(s); i++ {
		if s[i] < lo || s[i] > hi {
			return i
		}
	}
	return -1
}

// CountByte is the number of bytes of s equal to c.
//
//ergo:wait none
func CountByte(s string, c uint8) int {
	n := archsimd.BroadcastUint8x32(c)
	count, i := 0, 0
	for ; i+width <= len(s); i += width {
		count += bits.OnesCount32(load(s, i).Equal(n).ToBits())
	}
	for ; i < len(s); i++ {
		if s[i] == c {
			count++
		}
	}
	return count
}
