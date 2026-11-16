package lower

import (
	"fmt"
	"math"
	"math/rand/v2"
	"strconv"
	"strings"
	"testing"

	v1 "ergo.local/prototype/runtime/v1"
)

// TestNP4f1MovesKeepMeaning is NP4f-1's comparing law: bits (10), math's
// 15 wrappers and utf8's RuneCount, ValidString and ValidRune moved from runtime/v1 externs to import go
// calls, and each still answers what its old Go body answers, on edges
// (0, the largest values, ±0, ±Inf, NaN, subnormals, surrogates, runes
// past U+10FFFF, invalid and truncated UTF-8, the empty text) and seeded
// values. NP4f-2 deletes it with the old bodies. Mutant np4f1-* (a moved
// function with its call changed) is killed here.
//
// It builds Go because the old bodies are Go and the new functions run
// only in a built program (compareBodies).
func TestNP4f1MovesKeepMeaning(t *testing.T) {
	needsBuild(t)
	seed := bodySeed(t)
	r := rand.New(rand.NewPCG(seed, 0x4f1))
	const n = 24
	d := strconv.Itoa
	u := func(v uint) string { return strconv.FormatUint(uint64(v), 10) }
	u64 := func(v uint64) string { return strconv.FormatUint(v, 10) }
	uints := []uint{0, 1, 2, 3, 255, 256, 1 << 31, 1<<63 - 1, 1 << 63, math.MaxUint}
	genU := func(r *rand.Rand) uint { return uint(r.Uint64() >> r.UintN(64)) }
	u64s := []uint64{0, 1, 1 << 32, 1<<63 + 1, math.MaxUint64}
	genU64 := func(r *rand.Rand) uint64 { return r.Uint64() >> r.UintN(64) }
	type rot32 struct {
		x uint32
		k int
	}
	type rot64 struct {
		x uint64
		k int
	}
	bitsImp := []string{"bits"}
	cases := []bodyCase{
		bodyRows("bits.OnesCount", bitsImp, "x uint", `"\(bits.OnesCount(x))"`, uints, n, r, genU, u, func(v uint) string { return d(v1.BitsOnesCount(v)) }),
		bodyRows("bits.OnesCount64", bitsImp, "x uint64", `"\(bits.OnesCount64(x))"`, u64s, n, r, genU64, u64, func(v uint64) string { return d(v1.BitsOnesCount64(v)) }),
		bodyRows("bits.LeadingZeros", bitsImp, "x uint", `"\(bits.LeadingZeros(x))"`, uints, n, r, genU, u, func(v uint) string { return d(v1.BitsLeadingZeros(v)) }),
		bodyRows("bits.LeadingZeros64", bitsImp, "x uint64", `"\(bits.LeadingZeros64(x))"`, u64s, n, r, genU64, u64, func(v uint64) string { return d(v1.BitsLeadingZeros64(v)) }),
		bodyRows("bits.TrailingZeros", bitsImp, "x uint", `"\(bits.TrailingZeros(x))"`, uints, n, r, genU, u, func(v uint) string { return d(v1.BitsTrailingZeros(v)) }),
		bodyRows("bits.TrailingZeros64", bitsImp, "x uint64", `"\(bits.TrailingZeros64(x))"`, u64s, n, r, genU64, u64, func(v uint64) string { return d(v1.BitsTrailingZeros64(v)) }),
		bodyRows("bits.Len", bitsImp, "x uint", `"\(bits.Len(x))"`, uints, n, r, genU, u, func(v uint) string { return d(v1.BitsLen(v)) }),
		bodyRows("bits.Len64", bitsImp, "x uint64", `"\(bits.Len64(x))"`, u64s, n, r, genU64, u64, func(v uint64) string { return d(v1.BitsLen64(v)) }),
		bodyRows("bits.RotateLeft32", bitsImp, "x uint32, k int", `"\(bits.RotateLeft32(x, k))"`,
			[]rot32{{0, 0}, {1, -1}, {math.MaxUint32, 7}, {0x80000001, 1}, {3, -33}, {5, 64}}, n, r,
			func(r *rand.Rand) rot32 { return rot32{r.Uint32(), r.IntN(200) - 100} },
			func(v rot32) string { return fmt.Sprintf("%d, %d", v.x, v.k) },
			func(v rot32) string { return strconv.FormatUint(uint64(v1.BitsRotateLeft32(v.x, v.k)), 10) }),
		bodyRows("bits.RotateLeft64", bitsImp, "x uint64, k int", `"\(bits.RotateLeft64(x, k))"`,
			[]rot64{{0, 0}, {1, -1}, {math.MaxUint64, 7}, {1, 63}, {3, -65}, {5, 128}}, n, r,
			func(r *rand.Rand) rot64 { return rot64{r.Uint64(), r.IntN(300) - 150} },
			func(v rot64) string { return fmt.Sprintf("%d, %d", v.x, v.k) },
			func(v rot64) string { return strconv.FormatUint(v1.BitsRotateLeft64(v.x, v.k), 10) }),
	}
	cases = append(cases, np4f1MathCases(r, n)...)
	cases = append(cases, np4f1UTF8Cases(r, n)...)
	compareBodies(t, "TestNP4f1MovesKeepMeaning", seed, cases)
}

// np4fFloat is an Ergo helper that reads a float64 from its exact text, so
// -0, NaN and ±Inf reach the call as values, not as folded constants.
const np4fFloat = `func np4fFl(s string) float64 {
	strconv.ParseFloat(s) catch {
	case strconv.Malformed, strconv.OutOfRange:
		0.0
	}
}`

// np4fFloatEdges are the float64 edges of a math function.
var np4fFloatEdges = []float64{0, math.Copysign(0, -1), 0.5, -0.5, 1.5, 2.5, -2.5, -3.75, 1, -1, 2, 3,
	math.Inf(1), math.Inf(-1), math.NaN(), math.MaxFloat64, -math.MaxFloat64, math.SmallestNonzeroFloat64,
	-math.SmallestNonzeroFloat64, 1 << 53, 1<<53 + 1, 1e308, 1e-310, 709.8, -745.2, 4503599627370495.5}

// np4fGenFloat is a seeded float64: a spread of magnitudes, or any bit pattern.
func np4fGenFloat(r *rand.Rand) float64 {
	if r.IntN(4) == 0 {
		return math.Float64frombits(r.Uint64())
	}
	return r.NormFloat64() * math.Pow(10, float64(r.IntN(40)-20))
}

func np4fFloatArg(f float64) string { return `np4fFl("` + strconv.FormatFloat(f, 'g', -1, 64) + `")` }

func np4fFloatText(f float64) string { return strconv.FormatFloat(f, 'x', -1, 64) }

func np4f1MathCases(r *rand.Rand, n int) []bodyCase {
	imp := []string{"math", "strconv"}
	one := func(name string, ref func(float64) float64) bodyCase {
		c := bodyRows("math."+name, imp, "x float64", `strconv.FormatFloat(math.`+name+`(x), 'x', -1)`,
			np4fFloatEdges, n, r, np4fGenFloat, np4fFloatArg, func(f float64) string { return np4fFloatText(ref(f)) })
		return np4fFl(c)
	}
	type two struct{ x, y float64 }
	genTwo := func(r *rand.Rand) two { return two{np4fGenFloat(r), np4fGenFloat(r)} }
	var twoEdges []two
	for _, x := range []float64{0, math.Copysign(0, -1), 1, -1, 2, -2, 0.5, math.Inf(1), math.Inf(-1), math.NaN(), 3, -7} {
		for _, y := range []float64{0, math.Copysign(0, -1), 1, -1, 0.5, 2, -3, math.Inf(1), math.Inf(-1), math.NaN(), 1e300} {
			twoEdges = append(twoEdges, two{x, y})
		}
	}
	twoArg := func(v two) string { return np4fFloatArg(v.x) + ", " + np4fFloatArg(v.y) }
	type three struct{ x, y, z float64 }
	type sign struct {
		f float64
		s int
	}
	var signEdges []sign
	for _, f := range []float64{0, 1, math.Inf(1), math.Inf(-1), math.NaN(), -1} {
		for _, s := range []int{-2, -1, 0, 1, 2} {
			signEdges = append(signEdges, sign{f, s})
		}
	}
	cases := []bodyCase{
		one("Abs", v1.MathAbs), one("Ceil", v1.MathCeil), one("Floor", v1.MathFloor), one("Trunc", v1.MathTrunc),
		one("Round", v1.MathRound), one("Sqrt", v1.MathSqrt), one("Log", v1.MathLog), one("Exp", v1.MathExp),
		np4fFl(bodyRows("math.Pow", imp, "x float64, y float64", `strconv.FormatFloat(math.Pow(x, y), 'x', -1)`,
			twoEdges, n, r, genTwo, twoArg, func(v two) string { return np4fFloatText(v1.MathPow(v.x, v.y)) })),
		np4fFl(bodyRows("math.Mod", imp, "x float64, y float64", `strconv.FormatFloat(math.Mod(x, y), 'x', -1)`,
			twoEdges, n, r, genTwo, twoArg, func(v two) string { return np4fFloatText(v1.MathMod(v.x, v.y)) })),
		np4fFl(bodyRows("math.FMA", imp, "x float64, y float64, z float64", `strconv.FormatFloat(math.FMA(x, y, z), 'x', -1)`,
			[]three{{0, 0, 0}, {1, 1, math.Copysign(0, -1)}, {math.Inf(1), 0, 1}, {1e308, 10, -1e308}, {0.1, 10, -1}, {math.NaN(), 1, 1}}, n, r,
			func(r *rand.Rand) three { return three{np4fGenFloat(r), np4fGenFloat(r), np4fGenFloat(r)} },
			func(v three) string { return np4fFloatArg(v.x) + ", " + np4fFloatArg(v.y) + ", " + np4fFloatArg(v.z) },
			func(v three) string { return np4fFloatText(v1.MathFMA(v.x, v.y, v.z)) })),
		bodyRows("math.Inf", imp, "s int", `strconv.FormatFloat(math.Inf(s), 'x', -1)`,
			[]int{0, 1, -1, math.MaxInt, math.MinInt}, n, r, func(r *rand.Rand) int { return r.IntN(7) - 3 }, strconv.Itoa,
			func(s int) string { return np4fFloatText(v1.MathInf(s)) }),
		bodyRows("math.NaN", imp, "s int", `"\(math.IsNaN(math.NaN())) \(math.NaN() == math.NaN())"`,
			[]int{0}, 0, r, func(r *rand.Rand) int { return 0 }, strconv.Itoa,
			func(int) string { x := v1.MathNaN(); return fmt.Sprint(math.IsNaN(x), x == x) }),
		np4fFl(bodyRows("math.IsNaN", imp, "x float64", `"\(math.IsNaN(x))"`,
			np4fFloatEdges, n, r, np4fGenFloat, np4fFloatArg, func(f float64) string { return strconv.FormatBool(v1.MathIsNaN(f)) })),
		np4fFl(bodyRows("math.IsInf", imp, "x float64, s int", `"\(math.IsInf(x, s))"`,
			signEdges, n, r, func(r *rand.Rand) sign { return sign{np4fGenFloat(r), r.IntN(5) - 2} },
			func(v sign) string { return np4fFloatArg(v.f) + ", " + strconv.Itoa(v.s) },
			func(v sign) string { return strconv.FormatBool(v1.MathIsInf(v.f, v.s)) })),
	}
	return cases
}

// np4fFl gives the case the float helper.
func np4fFl(c bodyCase) bodyCase {
	c.decls = np4fFloat
	return c
}

// np4fUtf8Pieces are the pieces a seeded text is made of: ASCII, two-, three-
// and four-byte runes, a lone continuation byte, a truncated sequence, an
// encoded surrogate, a sequence past U+10FFFF and an overlong one.
var np4fUtf8Pieces = []string{"a", "Z", " ", "é", "世", "🙂", "\x80", "\xff", "\xe4\xb8", "\xed\xa0\x80", "\xf4\x90\x80\x80", "\xc0\xaf", "\xef\xbf\xbd", "\x00"}

func np4fGenText(r *rand.Rand) string {
	var b strings.Builder
	for range r.IntN(8) {
		b.WriteString(np4fUtf8Pieces[r.IntN(len(np4fUtf8Pieces))])
	}
	return b.String()
}

// np4fErgoString is s as an Ergo string literal, every byte outside printable
// ASCII escaped as \xHH.
func np4fErgoString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '"' || c == '\\':
			b.WriteByte('\\')
			b.WriteByte(c)
		case c >= 0x20 && c < 0x7f:
			b.WriteByte(c)
		default:
			fmt.Fprintf(&b, "\\x%02x", c)
		}
	}
	b.WriteByte('"')
	return b.String()
}

var np4fRuneEdges = []rune{0, 127, 128, 0x7ff, 0x800, 0xd7ff, 0xd800, 0xdfff, 0xe000, 0xfffd, 0xffff, 0x10000, 0x10ffff, 0x110000, -1, math.MaxInt32, math.MinInt32}

func np4fGenRune(r *rand.Rand) rune {
	if r.IntN(3) == 0 {
		return rune(r.Int32() - 1<<30)
	}
	return r.Int32N(0x110000 + 16)
}

func np4fRuneArg(v rune) string { return strconv.Itoa(int(v)) }

func np4f1UTF8Cases(r *rand.Rand, n int) []bodyCase {
	imp := []string{"utf8", "strconv", "strings"}
	texts := []string{"", "a", "é", "世界", "🙂", "\xff", "\xe4\xb8", "a\xffb", "\xed\xa0\x80", "\xf4\x90\x80\x80", "\xc0\xaf", "\x80\x80"}
	return []bodyCase{
		bodyRows("utf8.RuneCount", imp, "s string", `"\(utf8.RuneCount(s))"`, texts, n, r, np4fGenText, np4fErgoString,
			func(s string) string { return strconv.Itoa(v1.UTF8RuneCount(s)) }),
		bodyRows("utf8.ValidString", imp, "s string", `"\(utf8.ValidString(s))"`, texts, n, r, np4fGenText, np4fErgoString,
			func(s string) string { return strconv.FormatBool(v1.UTF8ValidString(s)) }),
		bodyRows("utf8.ValidRune", imp, "x rune", `"\(utf8.ValidRune(x))"`, np4fRuneEdges, n, r, np4fGenRune, np4fRuneArg,
			func(v rune) string { return strconv.FormatBool(v1.UTF8ValidRune(v)) }),
	}
}
