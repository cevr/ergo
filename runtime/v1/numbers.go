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
