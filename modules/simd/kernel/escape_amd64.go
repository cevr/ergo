//go:build goexperiment.simd && amd64

package kernel

import (
	"math/bits"
	"simd/archsimd"
)

// IndexNeedsEscape is the first index of a byte a JSON string cannot
// write as itself, or -1 (escape_stock.go names the bytes): 32 bytes at a
// time with AVX2, then the tail one byte at a time.
//
//ergo:wait none
func IndexNeedsEscape(s []uint8, html bool) int {
	i := 0
	if archsimd.X86.AVX2() {
		ctl, high := archsimd.BroadcastUint8x32(0x20), archsimd.BroadcastUint8x32(0x7f)
		quote, slash := archsimd.BroadcastUint8x32('"'), archsimd.BroadcastUint8x32('\\')
		lt, gt, amp := archsimd.BroadcastUint8x32('<'), archsimd.BroadcastUint8x32('>'), archsimd.BroadcastUint8x32('&')
		for ; i+32 <= len(s); i += 32 {
			v := archsimd.LoadUint8x32(s[i : i+32])
			m := v.Less(ctl).Or(v.Greater(high)).Or(v.Equal(quote)).Or(v.Equal(slash))
			if html {
				m = m.Or(v.Equal(lt)).Or(v.Equal(gt)).Or(v.Equal(amp))
			}
			if b := m.ToBits(); b != 0 {
				return i + bits.TrailingZeros32(b)
			}
		}
	}
	for ; i < len(s); i++ {
		c := s[i]
		if c < 0x20 || c >= 0x80 || c == '"' || c == '\\' || html && (c == '<' || c == '>' || c == '&') {
			return i
		}
	}
	return -1
}
