package v1

// The Go bodies of the std packages math and bits (docs/spec/stdlib.md,
// math and bits; std-text-runes). Each calls Go's function of the same
// name and is marked pure: math and math/bits are in PureImports. Go
// compiles most of them to one instruction, so a call costs what Go's own
// call costs once inlined.
//
// Prior art: Gleam's gleam/float binds Erlang's math functions as
// externals (floor, ceiling, truncate); ReScript's Math module binds
// JavaScript's Math directly; Elixir's Bitwise is BEAM instructions.

import (
	"math"
	"math/bits"
	"strconv"
)

// MathAbs is math.Abs(f).
//
//ergo:pure
func MathAbs(f float64) float64 { return math.Abs(f) }

// MathCeil is math.Ceil(f).
//
//ergo:pure
func MathCeil(f float64) float64 { return math.Ceil(f) }

// MathFloor is math.Floor(f).
//
//ergo:pure
func MathFloor(f float64) float64 { return math.Floor(f) }

// MathTrunc is math.Trunc(f).
//
//ergo:pure
func MathTrunc(f float64) float64 { return math.Trunc(f) }

// MathRound is math.Round(f).
//
//ergo:pure
func MathRound(f float64) float64 { return math.Round(f) }

// MathSqrt is math.Sqrt(f).
//
//ergo:pure
func MathSqrt(f float64) float64 { return math.Sqrt(f) }

// MathPow is math.Pow(x, y).
//
//ergo:pure
func MathPow(x float64, y float64) float64 { return math.Pow(x, y) }

// MathMod is math.Mod(x, y).
//
//ergo:pure
func MathMod(x float64, y float64) float64 { return math.Mod(x, y) }

// MathInf is math.Inf(sign).
//
//ergo:pure
func MathInf(sign int) float64 { return math.Inf(sign) }

// MathNaN is math.NaN().
//
//ergo:pure
func MathNaN() float64 { return math.NaN() }

// MathIsNaN is math.IsNaN(f).
//
//ergo:pure
func MathIsNaN(f float64) bool { return math.IsNaN(f) }

// MathIsInf is math.IsInf(f, sign).
//
//ergo:pure
func MathIsInf(f float64, sign int) bool { return math.IsInf(f, sign) }

// BitsOnesCount is bits.OnesCount(x).
//
//ergo:pure
func BitsOnesCount(x uint) int { return bits.OnesCount(x) }

// BitsOnesCount64 is bits.OnesCount64(x).
//
//ergo:pure
func BitsOnesCount64(x uint64) int { return bits.OnesCount64(x) }

// BitsLeadingZeros is bits.LeadingZeros(x).
//
//ergo:pure
func BitsLeadingZeros(x uint) int { return bits.LeadingZeros(x) }

// BitsLeadingZeros64 is bits.LeadingZeros64(x).
//
//ergo:pure
func BitsLeadingZeros64(x uint64) int { return bits.LeadingZeros64(x) }

// BitsTrailingZeros is bits.TrailingZeros(x).
//
//ergo:pure
func BitsTrailingZeros(x uint) int { return bits.TrailingZeros(x) }

// BitsTrailingZeros64 is bits.TrailingZeros64(x).
//
//ergo:pure
func BitsTrailingZeros64(x uint64) int { return bits.TrailingZeros64(x) }

// BitsLen is bits.Len(x).
//
//ergo:pure
func BitsLen(x uint) int { return bits.Len(x) }

// BitsLen64 is bits.Len64(x).
//
//ergo:pure
func BitsLen64(x uint64) int { return bits.Len64(x) }

// BitsRotateLeft32 is bits.RotateLeft32(x, k).
//
//ergo:pure
func BitsRotateLeft32(x uint32, k int) uint32 { return bits.RotateLeft32(x, k) }

// BitsRotateLeft64 is bits.RotateLeft64(x, k).
//
//ergo:pure
func BitsRotateLeft64(x uint64, k int) uint64 { return bits.RotateLeft64(x, k) }

// Float to integer conversion (docs/spec/types.md, Conversions 4;
// GLOSSARY "Saturating float conversion"). Go leaves a non-constant
// conversion of a float that the integer type cannot hold to the
// implementation: linux/amd64 gives the most negative value, arm64
// saturates, so one Go program means two things on two hosts. Ergo has
// one rule: the conversion truncates toward zero and saturates at the
// target's bounds, and NaN gives 0. The lowering writes every T(x) of a
// float x and an integer T as a call of the function below for T, with x
// widened to float64 first (a float32 widens exactly).
//
// The functions have two forms. FloatToInt64 and FloatToUint64 each guard
// one Go conversion with range compares. Every narrower width is one form:
// FloatToInt64's result clamped to the width's bounds with Go's min and
// max, then converted, an integer conversion Go defines on every host.
// Each function is within Go's inlining budget, so the call inlines to the
// conversion, a compare and the clamps; for a constant argument Go folds
// the compares away (lower TestFloatConversionInlines).
//
// FloatToInt64 guards only the upper side. Below -2^63, Go's int64(f)
// gives math.MinInt64, the rule's answer, on both supported platforms
// (interop.md, Platforms: linux/amd64, where CVTTSD2SQ gives its
// indefinite value 0x8000000000000000, and darwin/arm64, where FCVTZS
// saturates); measured on linux/amd64, and TestFloatToIntegerMatchesModel
// holds it on every gate host. A lower guard would be code no input on a
// supported platform tells apart (S-conv round 2). A host where it is not
// so (386's runtime helper wraps, MIPS gives 2^63-1) is not supported.
// FloatToUint64's lower guard is needed: linux/amd64's uint64(-1.5) is
// 2^64-1.
//
// Prior art: Rust 1.45 made `as` saturate and send NaN to 0 for the same
// reason (rust-lang/rust#10184); Java's (int) and (long) casts and
// WebAssembly's trunc_sat instructions are the same rule. Gleam's
// float.truncate makes an Erlang bignum on the BEAM, ReScript's
// Float.toInt is JavaScript's `|0`, which wraps modulo 2^32, and Elixir's
// trunc/1 makes a bignum.

// FloatToInt64 is Ergo's int64(f): f toward zero, saturated, NaN 0. NaN
// fails both compares.
func FloatToInt64(f float64) int64 {
	if f < 0x1p63 {
		return int64(f)
	}
	if f > 0 {
		return math.MaxInt64
	}
	return 0
}

// FloatToInt is Ergo's int(f): f toward zero, saturated, NaN 0. Ergo's
// targets have a 64-bit int (stdlib.md, math and bits 1).
func FloatToInt(f float64) int { return int(FloatToInt64(f)) }

// FloatToInt8 is Ergo's int8(f): f toward zero, saturated, NaN 0.
func FloatToInt8(f float64) int8 {
	return int8(min(max(FloatToInt64(f), math.MinInt8), math.MaxInt8))
}

// FloatToInt16 is Ergo's int16(f): f toward zero, saturated, NaN 0.
func FloatToInt16(f float64) int16 {
	return int16(min(max(FloatToInt64(f), math.MinInt16), math.MaxInt16))
}

// FloatToInt32 is Ergo's int32(f): f toward zero, saturated, NaN 0.
func FloatToInt32(f float64) int32 {
	return int32(min(max(FloatToInt64(f), math.MinInt32), math.MaxInt32))
}

// FloatToUint8 is Ergo's uint8(f): f toward zero, saturated, NaN 0.
func FloatToUint8(f float64) uint8 {
	return uint8(min(max(FloatToInt64(f), 0), math.MaxUint8))
}

// FloatToUint16 is Ergo's uint16(f): f toward zero, saturated, NaN 0.
func FloatToUint16(f float64) uint16 {
	return uint16(min(max(FloatToInt64(f), 0), math.MaxUint16))
}

// FloatToUint32 is Ergo's uint32(f): f toward zero, saturated, NaN 0.
func FloatToUint32(f float64) uint32 {
	return uint32(min(max(FloatToInt64(f), 0), math.MaxUint32))
}

// FloatToUint64 is Ergo's uint64(f): f toward zero, saturated, NaN 0.
func FloatToUint64(f float64) uint64 {
	if f > -1 && f < 0x1p64 {
		return uint64(f)
	}
	if f > 0 {
		return math.MaxUint64
	}
	return 0
}

// FloatToUint is Ergo's uint(f), 64 bits wide, as int is.
func FloatToUint(f float64) uint { return uint(FloatToUint64(f)) }

// FloatToUintptr is Ergo's uintptr(f), 64 bits wide, as int is.
func FloatToUintptr(f float64) uintptr { return uintptr(FloatToUint64(f)) }

// MathOutOfRange is std math.OutOfRange, the failure of a float that an
// integer type cannot hold: NaN, an infinity, or a value whose truncation
// lies outside the type's range. Value is the float. It is a record of a
// v1 package, so the lowering binds math.OutOfRange to this Go type.
type MathOutOfRange struct{ Value float64 }

// Error is the failure's text: its Ergo name and field.
func (e MathOutOfRange) Error() string {
	return "math.OutOfRange{Value:" + strconv.FormatFloat(e.Value, 'g', -1, 64) + "}"
}

// The checked forms (std math.ToInt, ToInt8 … ToUintptr): each is f
// toward zero when the result fits the type, and MathOutOfRange for NaN,
// an infinity or a value out of range, where the conversion saturates.
// Each guards one Go conversion with the open interval in which the
// truncation fits: (min-1, max+1) as float64 constants, exact for every
// width up to 32 bits and for 2^63 and 2^64; for 64 signed bits -2^63 is
// itself the least value, so the lower compare is >=. NaN fails both
// compares. A pure body names no other top-level function, so each width
// spells out its compares.

// MathToInt is std math.ToInt, the checked form of int(f).
//
//ergo:pure
func MathToInt(f float64) (int, error) {
	if f >= -0x1p63 && f < 0x1p63 {
		return int(f), nil
	}
	return 0, MathOutOfRange{Value: f}
}

// MathToInt8 is std math.ToInt8, the checked form of int8(f).
//
//ergo:pure
func MathToInt8(f float64) (int8, error) {
	if f > -129 && f < 128 {
		return int8(f), nil
	}
	return 0, MathOutOfRange{Value: f}
}

// MathToInt16 is std math.ToInt16, the checked form of int16(f).
//
//ergo:pure
func MathToInt16(f float64) (int16, error) {
	if f > -32769 && f < 32768 {
		return int16(f), nil
	}
	return 0, MathOutOfRange{Value: f}
}

// MathToInt32 is std math.ToInt32, the checked form of int32(f).
//
//ergo:pure
func MathToInt32(f float64) (int32, error) {
	if f > -2147483649 && f < 2147483648 {
		return int32(f), nil
	}
	return 0, MathOutOfRange{Value: f}
}

// MathToInt64 is std math.ToInt64, the checked form of int64(f).
//
//ergo:pure
func MathToInt64(f float64) (int64, error) {
	if f >= -0x1p63 && f < 0x1p63 {
		return int64(f), nil
	}
	return 0, MathOutOfRange{Value: f}
}

// MathToUint is std math.ToUint, the checked form of uint(f).
//
//ergo:pure
func MathToUint(f float64) (uint, error) {
	if f > -1 && f < 0x1p64 {
		return uint(f), nil
	}
	return 0, MathOutOfRange{Value: f}
}

// MathToUint8 is std math.ToUint8, the checked form of uint8(f).
//
//ergo:pure
func MathToUint8(f float64) (uint8, error) {
	if f > -1 && f < 256 {
		return uint8(f), nil
	}
	return 0, MathOutOfRange{Value: f}
}

// MathToUint16 is std math.ToUint16, the checked form of uint16(f).
//
//ergo:pure
func MathToUint16(f float64) (uint16, error) {
	if f > -1 && f < 65536 {
		return uint16(f), nil
	}
	return 0, MathOutOfRange{Value: f}
}

// MathToUint32 is std math.ToUint32, the checked form of uint32(f).
//
//ergo:pure
func MathToUint32(f float64) (uint32, error) {
	if f > -1 && f < 4294967296 {
		return uint32(f), nil
	}
	return 0, MathOutOfRange{Value: f}
}

// MathToUint64 is std math.ToUint64, the checked form of uint64(f).
//
//ergo:pure
func MathToUint64(f float64) (uint64, error) {
	if f > -1 && f < 0x1p64 {
		return uint64(f), nil
	}
	return 0, MathOutOfRange{Value: f}
}

// MathToUintptr is std math.ToUintptr, the checked form of uintptr(f).
//
//ergo:pure
func MathToUintptr(f float64) (uintptr, error) {
	if f > -1 && f < 0x1p64 {
		return uintptr(f), nil
	}
	return 0, MathOutOfRange{Value: f}
}
