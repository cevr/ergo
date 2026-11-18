package v1

// The Go bodies of std math's conversions (docs/spec/stdlib.md, math):
// a float to an integer, saturating or failing. Std math's other functions
// and std bits are Go's own, bound through import go (NP4f).
//
// Prior art: Gleam's gleam/float binds Erlang's math functions as
// externals (floor, ceiling, truncate); ReScript's Math module binds
// JavaScript's Math directly; Elixir's Bitwise is BEAM instructions.

import (
	"math"
	"strconv"
)

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
// inside the open interval (min-1, max+1) where the truncation fits, the
// Go conversion itself; outside it, FloatToInt64's result clamped to the
// width's bounds with Go's min and max, then converted, an integer
// conversion Go defines on every host. The interval's compares are the
// ones std math.ToT guards with, so in a checked conversion the guard's
// compares dominate the helper's, Go's CSE makes them one value, and its
// prove pass drops the helper's branch (NP4f-16). Each function is within
// Go's inlining budget, so the call inlines; for a constant argument Go
// folds the compares away (lower TestFloatConversionInlines).
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
	if f > -129 && f < 128 {
		return int8(f)
	}
	return int8(min(max(FloatToInt64(f), math.MinInt8), math.MaxInt8))
}

// FloatToInt16 is Ergo's int16(f): f toward zero, saturated, NaN 0.
func FloatToInt16(f float64) int16 {
	if f > -32769 && f < 32768 {
		return int16(f)
	}
	return int16(min(max(FloatToInt64(f), math.MinInt16), math.MaxInt16))
}

// FloatToInt32 is Ergo's int32(f): f toward zero, saturated, NaN 0.
func FloatToInt32(f float64) int32 {
	if f > -2147483649 && f < 2147483648 {
		return int32(f)
	}
	return int32(min(max(FloatToInt64(f), math.MinInt32), math.MaxInt32))
}

// FloatToUint8 is Ergo's uint8(f): f toward zero, saturated, NaN 0.
func FloatToUint8(f float64) uint8 {
	if f > -1 && f < 256 {
		return uint8(f)
	}
	return uint8(min(max(FloatToInt64(f), 0), math.MaxUint8))
}

// FloatToUint16 is Ergo's uint16(f): f toward zero, saturated, NaN 0.
func FloatToUint16(f float64) uint16 {
	if f > -1 && f < 65536 {
		return uint16(f)
	}
	return uint16(min(max(FloatToInt64(f), 0), math.MaxUint16))
}

// FloatToUint32 is Ergo's uint32(f): f toward zero, saturated, NaN 0.
func FloatToUint32(f float64) uint32 {
	if f > -1 && f < 4294967296 {
		return uint32(f)
	}
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
