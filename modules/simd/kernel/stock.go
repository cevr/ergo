//go:build !(goexperiment.simd && amd64)

package kernel

// Enabled reports whether the vector kernels run. In this build they do
// not: package scan runs its Ergo reference, and gc removes the call.
//
//ergo:wait none
func Enabled() bool { return false }

// IndexByte is never called in this build (Enabled is false); it answers
// as the reference does, so a call by mistake is still correct.
//
//ergo:wait none
func IndexByte(s string, c uint8) int {
	for i := 0; i < len(s); i++ {
		if s[i] == c {
			return i
		}
	}
	return -1
}

// IndexAny2 is IndexByte for either of two bytes.
//
//ergo:wait none
func IndexAny2(s string, a uint8, b uint8) int {
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
func IndexOutside(s string, lo uint8, hi uint8) int {
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
func CountByte(s string, c uint8) int {
	n := 0
	for i := 0; i < len(s); i++ {
		if s[i] == c {
			n++
		}
	}
	return n
}
