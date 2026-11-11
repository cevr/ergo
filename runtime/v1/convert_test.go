package v1

import (
	"flag"
	"math"
	"math/big"
	"math/rand/v2"
	"testing"
)

var convSeed = flag.Uint64("conv.seed", 0, "run TestFloatToIntegerMatchesModel on this seed alone")

// convTarget is one integer type of Ergo's float conversion: its width,
// its sign, and the runtime/v1 function the lowering calls for it, with
// the result as a big.Int.
type convTarget struct {
	name   string
	bits   uint
	signed bool
	conv   func(float64) *big.Int
}

func signedOf[T int | int8 | int16 | int32 | int64](f func(float64) T) func(float64) *big.Int {
	return func(x float64) *big.Int { return big.NewInt(int64(f(x))) }
}

func unsignedOf[T uint | uint8 | uint16 | uint32 | uint64 | uintptr](f func(float64) T) func(float64) *big.Int {
	return func(x float64) *big.Int { return new(big.Int).SetUint64(uint64(f(x))) }
}

var convTargets = []convTarget{
	{"int", 64, true, signedOf(FloatToInt)},
	{"int8", 8, true, signedOf(FloatToInt8)},
	{"int16", 16, true, signedOf(FloatToInt16)},
	{"int32", 32, true, signedOf(FloatToInt32)},
	{"int64", 64, true, signedOf(FloatToInt64)},
	{"uint", 64, false, unsignedOf(FloatToUint)},
	{"uint8", 8, false, unsignedOf(FloatToUint8)},
	{"uint16", 16, false, unsignedOf(FloatToUint16)},
	{"uint32", 32, false, unsignedOf(FloatToUint32)},
	{"uint64", 64, false, unsignedOf(FloatToUint64)},
	{"uintptr", 64, false, unsignedOf(FloatToUintptr)},
}

// bounds are the least and greatest values of the target.
func (c convTarget) bounds() (lo, hi *big.Int) {
	one := big.NewInt(1)
	if c.signed {
		hi = new(big.Int).Sub(new(big.Int).Lsh(one, c.bits-1), one)
		lo = new(big.Int).Neg(new(big.Int).Lsh(one, c.bits-1))
		return lo, hi
	}
	return big.NewInt(0), new(big.Int).Sub(new(big.Int).Lsh(one, c.bits), one)
}

// convModel is the rule of types.md, Conversions 4, on exact numbers: NaN
// is 0; otherwise f truncated toward zero, clamped to the target's bounds
// (an infinity is past every bound).
func convModel(c convTarget, f float64) *big.Int {
	lo, hi := c.bounds()
	switch {
	case math.IsNaN(f):
		return big.NewInt(0)
	case math.IsInf(f, 1):
		return hi
	case math.IsInf(f, -1):
		return lo
	}
	t, _ := new(big.Float).SetFloat64(f).Int(nil) // Int truncates toward zero
	if t.Cmp(hi) > 0 {
		return hi
	}
	if t.Cmp(lo) < 0 {
		return lo
	}
	return t
}

// edgeFloat is a float of every class the conversion separates: NaN, the
// infinities, both zeros, the largest float, subnormals, every target's
// bounds (±2^7 … ±2^64) and the floats next to them, values half a unit
// inside a bound, random bit patterns, and values in and past every
// range. A float32 is drawn too and widened, as the lowering widens one.
func edgeFloat(r *rand.Rand) float64 {
	pick := func(xs ...float64) float64 { return xs[r.IntN(len(xs))] }
	switch r.IntN(8) {
	case 0:
		return pick(math.NaN(), math.Inf(1), math.Inf(-1), 0, math.Copysign(0, -1), math.MaxFloat64, -math.MaxFloat64,
			math.SmallestNonzeroFloat64, -math.SmallestNonzeroFloat64, 0.5, -0.5, 0.9999999999999999, -0.9999999999999999)
	case 1:
		return math.Float64frombits(r.Uint64()>>12) * pick(1, -1) // a subnormal
	case 2, 3:
		b := math.Ldexp(1, []int{7, 8, 15, 16, 31, 32, 63, 64}[r.IntN(8)]) * pick(1, -1)
		switch r.IntN(6) {
		case 0:
			return b
		case 1:
			return math.Nextafter(b, math.Inf(1))
		case 2:
			return math.Nextafter(b, math.Inf(-1))
		case 3:
			return b + pick(1, -1)
		case 4:
			return b + pick(0.5, -0.5)
		}
		return b + pick(1, -1)*(1+math.Abs(b)*0x1p-52*float64(r.IntN(4)))
	case 4:
		return math.Float64frombits(r.Uint64())
	case 5:
		return float64(math.Float32frombits(r.Uint32()))
	case 6:
		return math.Ldexp(r.Float64()-0.5, r.IntN(70))
	}
	return float64(r.IntN(1000)-500) / 4
}

// TestFloatToIntegerMatchesModel is the seeded property of Ergo's float to
// integer conversion (types.md, Conversions 4): each runtime/v1 function
// the lowering calls for a target type gives what convModel, the rule on
// exact numbers, gives, on generated floats of every class. A failing
// seed prints the float, the type, both answers and the replay command.
//
// Mutants (runtime/v1 ledger, sconv-*): each guard of each width dropped
// or moved by one (the lower and upper bound, the sign tests that pick the
// bound), and NaN sent to a bound.
func TestFloatToIntegerMatchesModel(t *testing.T) {
	seeds := []uint64{1, 2, 3, 4, 5, 6, 7, 8}
	if *convSeed != 0 {
		seeds = []uint64{*convSeed}
	}
	for _, seed := range seeds {
		r := rand.New(rand.NewPCG(seed, 0x5c0))
		for i := range 4000 {
			f := edgeFloat(r)
			for _, c := range convTargets {
				got, want := c.conv(f), convModel(c, f)
				if got.Cmp(want) != 0 {
					t.Fatalf("seed %d case %d: %s(%v) (bits %#x) = %s; the rule gives %s\nreplay: go test ./runtime/v1 -run TestFloatToIntegerMatchesModel -conv.seed=%d",
						seed, i, c.name, f, math.Float64bits(f), got, want, seed)
				}
			}
		}
	}
}
