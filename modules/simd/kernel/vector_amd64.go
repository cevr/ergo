//go:build goexperiment.simd && amd64

package kernel

import (
	"math/bits"
	"simd/archsimd"
)

// IndexByte is the first index of c in s, or -1: 32 bytes at a time with
// AVX2, then the tail one byte at a time.
//
//ergo:wait none
func IndexByte(s []uint8, c uint8) int {
	i := 0
	if archsimd.X86.AVX2() {
		n := archsimd.BroadcastUint8x32(c)
		for ; i+32 <= len(s); i += 32 {
			if m := archsimd.LoadUint8x32(s[i : i+32]).Equal(n).ToBits(); m != 0 {
				return i + bits.TrailingZeros32(m)
			}
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
func IndexAny2(s []uint8, a uint8, b uint8) int {
	i := 0
	if archsimd.X86.AVX2() {
		na, nb := archsimd.BroadcastUint8x32(a), archsimd.BroadcastUint8x32(b)
		for ; i+32 <= len(s); i += 32 {
			v := archsimd.LoadUint8x32(s[i : i+32])
			if m := v.Equal(na).Or(v.Equal(nb)).ToBits(); m != 0 {
				return i + bits.TrailingZeros32(m)
			}
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
func IndexOutside(s []uint8, lo uint8, hi uint8) int {
	i := 0
	if archsimd.X86.AVX2() {
		nl, nh := archsimd.BroadcastUint8x32(lo), archsimd.BroadcastUint8x32(hi)
		for ; i+32 <= len(s); i += 32 {
			v := archsimd.LoadUint8x32(s[i : i+32])
			if m := v.Less(nl).Or(v.Greater(nh)).ToBits(); m != 0 {
				return i + bits.TrailingZeros32(m)
			}
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
func CountByte(s []uint8, c uint8) int {
	count, i := 0, 0
	if archsimd.X86.AVX2() {
		n := archsimd.BroadcastUint8x32(c)
		for ; i+32 <= len(s); i += 32 {
			count += bits.OnesCount32(archsimd.LoadUint8x32(s[i : i+32]).Equal(n).ToBits())
		}
	}
	for ; i < len(s); i++ {
		if s[i] == c {
			count++
		}
	}
	return count
}
