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
// Each function guards one Go conversion with range compares and is within
// Go's inlining budget, so the call inlines and costs two compares beside
// the conversion; for a constant argument Go folds the compares away
// (lower TestFloatConversionInlines). Inside the open interval the guards
// admit, the truncated value fits the target, where Go's conversion is
// defined on every host. The interval's ends are float64 constants: 2^(n-1)
// for an n-bit signed type and 2^n for an unsigned one are exact, and so
// is -2^(n-1)-1 for n up to 32; for 64 bits -2^63 is itself the least
// value, so the lower guard is >=.
//
// Prior art: Rust 1.45 made `as` saturate and send NaN to 0 for the same
// reason (rust-lang/rust#10184); Java's (int) and (long) casts and
// WebAssembly's trunc_sat instructions are the same rule. Gleam's
// float.truncate makes an Erlang bignum on the BEAM, ReScript's
// Float.toInt is JavaScript's `|0`, which wraps modulo 2^32, and Elixir's
// trunc/1 makes a bignum.

// FloatToInt8 is Ergo's int8(f): f toward zero, saturated, NaN 0.
func FloatToInt8(f float64) int8 {
	if f > -129 && f < 128 {
		return int8(f)
	}
	if f > 0 {
		return math.MaxInt8
	}
	if f < 0 {
		return math.MinInt8
	}
	return 0
}

// FloatToInt16 is Ergo's int16(f): f toward zero, saturated, NaN 0.
func FloatToInt16(f float64) int16 {
	if f > -32769 && f < 32768 {
		return int16(f)
	}
	if f > 0 {
		return math.MaxInt16
	}
	if f < 0 {
		return math.MinInt16
	}
	return 0
}

// FloatToInt32 is Ergo's int32(f): f toward zero, saturated, NaN 0.
func FloatToInt32(f float64) int32 {
	if f > -2147483649 && f < 2147483648 {
		return int32(f)
	}
	if f > 0 {
		return math.MaxInt32
	}
	if f < 0 {
		return math.MinInt32
	}
	return 0
}

// FloatToInt64 is Ergo's int64(f): f toward zero, saturated, NaN 0.
func FloatToInt64(f float64) int64 {
	if f >= -0x1p63 && f < 0x1p63 {
		return int64(f)
	}
	if f > 0 {
		return math.MaxInt64
	}
	if f < 0 {
		return math.MinInt64
	}
	return 0
}

// FloatToInt is Ergo's int(f): f toward zero, saturated, NaN 0. Ergo's
// targets have a 64-bit int (stdlib.md, math and bits 1).
func FloatToInt(f float64) int { return int(FloatToInt64(f)) }

// FloatToUint8 is Ergo's uint8(f): f toward zero, saturated, NaN 0.
func FloatToUint8(f float64) uint8 {
	if f > -1 && f < 256 {
		return uint8(f)
	}
	if f > 0 {
		return math.MaxUint8
	}
	return 0
}

// FloatToUint16 is Ergo's uint16(f): f toward zero, saturated, NaN 0.
func FloatToUint16(f float64) uint16 {
	if f > -1 && f < 65536 {
		return uint16(f)
	}
	if f > 0 {
		return math.MaxUint16
	}
	return 0
}

// FloatToUint32 is Ergo's uint32(f): f toward zero, saturated, NaN 0.
func FloatToUint32(f float64) uint32 {
	if f > -1 && f < 4294967296 {
		return uint32(f)
	}
	if f > 0 {
		return math.MaxUint32
	}
	return 0
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

// MathToInt64 is std math.ToInt64, the checked form of int64(f): f toward
// zero when the result fits int64, and MathOutOfRange for NaN, an infinity
// or a value out of range, where int64(f) saturates.
//
//ergo:pure
func MathToInt64(f float64) (int64, error) {
	if f >= -0x1p63 && f < 0x1p63 {
		return int64(f), nil
	}
	return 0, MathOutOfRange{Value: f}
}
