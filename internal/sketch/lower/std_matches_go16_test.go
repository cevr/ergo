package lower

import (
	"math"
	"math/rand/v2"
	"strconv"
	"strings"
)

// np4f16Kind is one checked conversion of std math: its Ergo name and the
// old runtime/v1 body (MathToT) written out, which gives the truncation as
// text, or false where it failed OutOfRange.
type np4f16Kind struct {
	name string
	old  func(float64) (string, bool)
}

func np4f16Signed[T int | int64](lo, hi float64, loClosed bool) func(float64) (string, bool) {
	return func(f float64) (string, bool) {
		if (f > lo || loClosed && f == lo) && f < hi {
			return strconv.FormatInt(int64(T(f)), 10), true
		}
		return "", false
	}
}

func np4f16Unsigned[T uint | uint64 | uintptr](hi float64) func(float64) (string, bool) {
	return func(f float64) (string, bool) {
		if f > -1 && f < hi {
			return strconv.FormatUint(uint64(T(f)), 10), true
		}
		return "", false
	}
}

// np4f16Kinds are the old bodies' guards: (-2^63, 2^63) closed below for
// 64 signed bits, (-1, 2^64) for 64 unsigned bits.
var np4f16Kinds = []np4f16Kind{
	{"Int", np4f16Signed[int](-0x1p63, 0x1p63, true)},
	{"Int64", np4f16Signed[int64](-0x1p63, 0x1p63, true)},
	{"Uint", np4f16Unsigned[uint](0x1p64)},
	{"Uint64", np4f16Unsigned[uint64](0x1p64)},
	{"Uintptr", np4f16Unsigned[uintptr](0x1p64)},
}

// np4f16Edges are the floats at and next to every bound of the eleven
// types: min-1, min, max and max+1 and their float neighbours, the halves
// between, the floats next to 2^63 and 2^64, both zeros, the infinities
// and NaN.
func np4f16Edges() []float64 {
	out := []float64{0, math.Copysign(0, -1), 0.5, -0.5, -0.99, -1, -1.5, math.Inf(1), math.Inf(-1), math.NaN(), math.MaxFloat64, -math.MaxFloat64}
	for _, b := range []float64{128, 256, 32768, 65536, 2147483648, 4294967296, 0x1p63, 0x1p64} {
		for _, v := range []float64{b, -b, b - 1, -b - 1, 1 - b, b + 1} {
			out = append(out, v, math.Nextafter(v, math.Inf(1)), math.Nextafter(v, math.Inf(-1)), v+0.5, v-0.5)
		}
	}
	return out
}

// np4f16Gen is a seeded float near a bound, or a spread float.
func np4f16Gen(r *rand.Rand) float64 {
	if r.IntN(2) == 0 {
		return np4fGenFloat(r)
	}
	b := []float64{128, 256, 32768, 65536, 2147483648, 4294967296, 0x1p63, 0x1p64}[r.IntN(8)]
	if r.IntN(2) == 0 {
		b = -b
	}
	return b + (r.Float64()-0.5)*4
}

// stdGo16Cases are NP4f-16's moved math.ToInt, ToInt64, ToUint, ToUint64
// and ToUintptr against the old
// runtime/v1 bodies, on every bound and seeded floats near them; a failure
// prints x, and xbad when OutOfRange's Value is not f. Mutants (std
// ledger): np4f16-*-lower and np4f16-*-upper, each bound moved by one.
func stdGo16Cases(r *rand.Rand, n int) []bodyCase {
	var cases []bodyCase
	edges := np4f16Edges()
	for _, k := range np4f16Kinds {
		helper := `func np4f16` + k.name + `(f float64) string {
	let v = math.To` + k.name + `(f) catch {
	case math.OutOfRange as o:
		if o.Value == f || math.IsNaN(f) {
			return "x"
		}
		return "xbad"
	}
	"\(v)"
}`
		c := bodyRows("math.To"+k.name, []string{"math", "strconv"}, "f float64", `np4f16`+k.name+`(f)`,
			edges, n, r, np4f16Gen, np4fFloatArg, func(f float64) string {
				if s, ok := k.old(f); ok {
					return s
				}
				return "x"
			})
		c.decls = strings.Join([]string{np4fFloat, helper}, "\n\n")
		cases = append(cases, c)
	}
	return cases
}
