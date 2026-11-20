//go:build !(goexperiment.simd && amd64)

package kernel

// IndexByte is the first index of c in s, or -1. In this build the kernel
// is the scalar loop the Ergo reference is.
//
//ergo:wait none
func IndexByte(s []uint8, c uint8) int {
	for i := 0; i < len(s); i++ {
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
	for i := 0; i < len(s); i++ {
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
	for i := 0; i < len(s); i++ {
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
	n := 0
	for i := 0; i < len(s); i++ {
		if s[i] == c {
			n++
		}
	}
	return n
}
