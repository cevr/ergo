package lower

import (
	"fmt"
	"math"
	"math/bits"
	"math/rand/v2"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
)

// TestStdMatchesGo is the comparing law of std's import go bodies (NP4f;
// np4f-plan §6.1, kept for good by the orchestrator's ruling of
// 2026-11-16): each function std binds to Go's library through import go
// answers what Go's own function answers, on edges and seeded values. The
// table grows with each NP4f unit; a function NP4f moved into an Ergo body
// (cmp, slices, strings.PadLeft) is held to Go's function too. Mutants
// np4f1-*, np4f2-*, np4f3-*, np4f5-*, np4f7-*, stdsh-log,
// stdsh-exp and math-fma-* (a moved function with its Go call changed) are
// killed here.
//
// It builds Go because the std functions run only in a built program and
// the question is what each returns (compareBodies).
func TestStdMatchesGo(t *testing.T) {
	needsBuild(t)
	seed := bodySeed(t)
	r := rand.New(rand.NewPCG(seed, 0x60))
	const n = 24
	cases := append(stdGoNumberCases(r, n), stdGoTextCases(r, n)...)
	cases = append(cases, stdGo3Cases(r, n)...)
	cases = append(cases, stdGo5Cases(r, n)...)
	cases = append(cases, stdGo7Cases(r, n)...)
	cases = append(cases, stdGo9Cases(r, n)...)
	cases = append(cases, stdGo15Cases(r, n)...)
	cases = append(cases, stdGo16Cases(r, n)...)
	cases = append(cases, stdGo17Cases(r, n)...)
	cases = append(cases, stdGoURLCases(r, n)...)
	cases = append(cases, stdGoSQ5Cases(r, n)...)
	compareBodies(t, "TestStdMatchesGo", seed, cases)
}

// stdGoNumberCases are the moved functions of bits, math and utf8 (NP4f-1),
// each against Go's own function, on edges (0, the largest values, ±0,
// ±Inf, NaN, subnormals, surrogates, runes past U+10FFFF, invalid and
// truncated UTF-8, the empty text) and seeded values.
func stdGoNumberCases(r *rand.Rand, n int) []bodyCase {
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
		bodyRows("bits.OnesCount", bitsImp, "x uint", `"\(bits.OnesCount(x))"`, uints, n, r, genU, u, func(v uint) string { return d(bits.OnesCount(v)) }),
		bodyRows("bits.OnesCount64", bitsImp, "x uint64", `"\(bits.OnesCount64(x))"`, u64s, n, r, genU64, u64, func(v uint64) string { return d(bits.OnesCount64(v)) }),
		bodyRows("bits.LeadingZeros", bitsImp, "x uint", `"\(bits.LeadingZeros(x))"`, uints, n, r, genU, u, func(v uint) string { return d(bits.LeadingZeros(v)) }),
		bodyRows("bits.LeadingZeros64", bitsImp, "x uint64", `"\(bits.LeadingZeros64(x))"`, u64s, n, r, genU64, u64, func(v uint64) string { return d(bits.LeadingZeros64(v)) }),
		bodyRows("bits.TrailingZeros", bitsImp, "x uint", `"\(bits.TrailingZeros(x))"`, uints, n, r, genU, u, func(v uint) string { return d(bits.TrailingZeros(v)) }),
		bodyRows("bits.TrailingZeros64", bitsImp, "x uint64", `"\(bits.TrailingZeros64(x))"`, u64s, n, r, genU64, u64, func(v uint64) string { return d(bits.TrailingZeros64(v)) }),
		bodyRows("bits.Len", bitsImp, "x uint", `"\(bits.Len(x))"`, uints, n, r, genU, u, func(v uint) string { return d(bits.Len(v)) }),
		bodyRows("bits.Len64", bitsImp, "x uint64", `"\(bits.Len64(x))"`, u64s, n, r, genU64, u64, func(v uint64) string { return d(bits.Len64(v)) }),
		bodyRows("bits.RotateLeft32", bitsImp, "x uint32, k int", `"\(bits.RotateLeft32(x, k))"`,
			[]rot32{{0, 0}, {1, -1}, {math.MaxUint32, 7}, {0x80000001, 1}, {3, -33}, {5, 64}}, n, r,
			func(r *rand.Rand) rot32 { return rot32{r.Uint32(), r.IntN(200) - 100} },
			func(v rot32) string { return fmt.Sprintf("%d, %d", v.x, v.k) },
			func(v rot32) string { return strconv.FormatUint(uint64(bits.RotateLeft32(v.x, v.k)), 10) }),
		bodyRows("bits.RotateLeft64", bitsImp, "x uint64, k int", `"\(bits.RotateLeft64(x, k))"`,
			[]rot64{{0, 0}, {1, -1}, {math.MaxUint64, 7}, {1, 63}, {3, -65}, {5, 128}}, n, r,
			func(r *rand.Rand) rot64 { return rot64{r.Uint64(), r.IntN(300) - 150} },
			func(v rot64) string { return fmt.Sprintf("%d, %d", v.x, v.k) },
			func(v rot64) string { return strconv.FormatUint(bits.RotateLeft64(v.x, v.k), 10) }),
	}
	cases = append(cases, stdGoMathCases(r, n)...)
	cases = append(cases, stdGoUTF8Cases(r, n)...)
	return cases
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

func stdGoMathCases(r *rand.Rand, n int) []bodyCase {
	imp := []string{"math", "strconv"}
	one := func(name string, ref func(float64) float64) bodyCase {
		c := bodyRows("math."+name, imp, "x float64", `strconv.FormatFloat(math.`+name+`(x), 'x', -1)`,
			np4fFloatEdges, n, r, np4fGenFloat, np4fFloatArg, func(f float64) string { return np4fFloatText(ref(f)) })
		return np4fFl(c)
	}
	trigEdges := append(append([]float64{}, np4fFloatEdges...), math.Pi, -math.Pi, math.Pi/2, math.Pi/4, 3*math.Pi/2, 2*math.Pi,
		1e22, -1e22, 1<<29*math.Pi, 1e300, 0.999999, -1.0000001, 1e-8, 10, 100, 1e15, 8, 27, -64)
	trig := func(name string, ref func(float64) float64) bodyCase {
		return np4fFl(bodyRows("math."+name, imp, "x float64", `strconv.FormatFloat(math.`+name+`(x), 'x', -1)`,
			trigEdges, n, r, np4fGenFloat, np4fFloatArg, func(f float64) string { return np4fFloatText(ref(f)) }))
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
		one("Abs", math.Abs), one("Ceil", math.Ceil), one("Floor", math.Floor), one("Trunc", math.Trunc),
		one("Round", math.Round), one("Sqrt", math.Sqrt), one("Log", math.Log), one("Exp", math.Exp),
		// SQ1 (MathIsGo): the trig functions on the edges of argument
		// reduction (multiples of Pi, huge arguments) and of their domains.
		trig("Sin", math.Sin), trig("Cos", math.Cos), trig("Tan", math.Tan), trig("Asin", math.Asin), trig("Acos", math.Acos),
		trig("Atan", math.Atan), trig("Cbrt", math.Cbrt), trig("Log2", math.Log2), trig("Log10", math.Log10),
		np4fFl(bodyRows("math.Atan2", imp, "y float64, x float64", `strconv.FormatFloat(math.Atan2(y, x), 'x', -1)`,
			twoEdges, n, r, genTwo, twoArg, func(v two) string { return np4fFloatText(math.Atan2(v.x, v.y)) })),
		np4fFl(bodyRows("math.Hypot", imp, "p float64, q float64", `strconv.FormatFloat(math.Hypot(p, q), 'x', -1)`,
			twoEdges, n, r, genTwo, twoArg, func(v two) string { return np4fFloatText(math.Hypot(v.x, v.y)) })),
		np4fFl(bodyRows("math.Pow", imp, "x float64, y float64", `strconv.FormatFloat(math.Pow(x, y), 'x', -1)`,
			twoEdges, n, r, genTwo, twoArg, func(v two) string { return np4fFloatText(math.Pow(v.x, v.y)) })),
		np4fFl(bodyRows("math.Mod", imp, "x float64, y float64", `strconv.FormatFloat(math.Mod(x, y), 'x', -1)`,
			twoEdges, n, r, genTwo, twoArg, func(v two) string { return np4fFloatText(math.Mod(v.x, v.y)) })),
		np4fFl(bodyRows("math.FMA", imp, "x float64, y float64, z float64", `strconv.FormatFloat(math.FMA(x, y, z), 'x', -1)`,
			[]three{{0, 0, 0}, {1, 1, math.Copysign(0, -1)}, {math.Inf(1), 0, 1}, {1e308, 10, -1e308}, {0.1, 10, -1}, {math.NaN(), 1, 1}}, n, r,
			func(r *rand.Rand) three { return three{np4fGenFloat(r), np4fGenFloat(r), np4fGenFloat(r)} },
			func(v three) string { return np4fFloatArg(v.x) + ", " + np4fFloatArg(v.y) + ", " + np4fFloatArg(v.z) },
			func(v three) string { return np4fFloatText(math.FMA(v.x, v.y, v.z)) })),
		bodyRows("math.Inf", imp, "s int", `strconv.FormatFloat(math.Inf(s), 'x', -1)`,
			[]int{0, 1, -1, math.MaxInt, math.MinInt}, n, r, func(r *rand.Rand) int { return r.IntN(7) - 3 }, strconv.Itoa,
			func(s int) string { return np4fFloatText(math.Inf(s)) }),
		bodyRows("math.NaN", imp, "s int", `"\(math.IsNaN(math.NaN())) \(math.NaN() == math.NaN())"`,
			[]int{0}, 0, r, func(r *rand.Rand) int { return 0 }, strconv.Itoa,
			func(int) string { x := math.NaN(); return fmt.Sprint(math.IsNaN(x), x == x) }),
		np4fFl(bodyRows("math.IsNaN", imp, "x float64", `"\(math.IsNaN(x))"`,
			np4fFloatEdges, n, r, np4fGenFloat, np4fFloatArg, func(f float64) string { return strconv.FormatBool(math.IsNaN(f)) })),
		np4fFl(bodyRows("math.IsInf", imp, "x float64, s int", `"\(math.IsInf(x, s))"`,
			signEdges, n, r, func(r *rand.Rand) sign { return sign{np4fGenFloat(r), r.IntN(5) - 2} },
			func(v sign) string { return np4fFloatArg(v.f) + ", " + strconv.Itoa(v.s) },
			func(v sign) string { return strconv.FormatBool(math.IsInf(v.f, v.s)) })),
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

func stdGoUTF8Cases(r *rand.Rand, n int) []bodyCase {
	imp := []string{"utf8", "strconv"}
	texts := []string{"", "a", "é", "世界", "🙂", "\xff", "\xe4\xb8", "a\xffb", "\xed\xa0\x80", "\xf4\x90\x80\x80", "\xc0\xaf", "\x80\x80"}
	return []bodyCase{
		bodyRows("utf8.RuneCount", imp, "s string", `"\(utf8.RuneCount(s))"`, texts, n, r, np4fGenText, np4fErgoString,
			func(s string) string { return strconv.Itoa(utf8.RuneCountInString(s)) }),
		bodyRows("utf8.ValidString", imp, "s string", `"\(utf8.ValidString(s))"`, texts, n, r, np4fGenText, np4fErgoString,
			func(s string) string { return strconv.FormatBool(utf8.ValidString(s)) }),
		bodyRows("utf8.ValidRune", imp, "x rune", `"\(utf8.ValidRune(x))"`, np4fRuneEdges, n, r, np4fGenRune, np4fRuneArg,
			func(v rune) string { return strconv.FormatBool(utf8.ValidRune(v)) }),
		bodyRows("utf8.RuneLen", imp, "x rune", `"\(utf8.RuneLen(x))"`, np4fRuneEdges, n, r, np4fGenRune, np4fRuneArg,
			func(v rune) string { return strconv.Itoa(utf8.RuneLen(v)) }),
	}
}

// stdGoTextCases are the moved functions of strings and strconv (NP4f-2),
// each against Go's own function, on edges (the empty text and separator,
// invalid UTF-8, case folds such as K and the Kelvin sign, every float
// format and precision) and seeded values. Every answer is printed as its
// bytes, so invalid UTF-8 and line breaks compare exactly.
func stdGoTextCases(r *rand.Rand, n int) []bodyCase {
	imp := []string{"strings", "strconv"}
	with := func(c bodyCase) bodyCase {
		c.decls = stdGo2Decls
		return c
	}
	text := np4fErgoString
	type two struct{ s, t string }
	gen2 := func(r *rand.Rand) two { return two{stdGo2Text(r), stdGo2Text(r)} }
	arg2 := func(v two) string { return text(v.s) + ", " + text(v.t) }
	var edges2 []two
	for _, s := range []string{"", "a", "ab,cd", " x ", "\xff,é", "Kk", "ab"} {
		for _, t := range []string{"", ",", "a", "b", " ", "é", "\xff", "k", "ab"} {
			edges2 = append(edges2, two{s, t})
		}
	}
	// pairs equal under case folding but not as bytes (the Kelvin sign
	// folds to k; ß has no simple fold to ss)
	edges2 = append(edges2, two{"Kk", "kK"}, two{"\u212a", "k"}, two{"ÉÀ", "éà"}, two{"ß", "SS"}, two{"a\xff", "A\xff"})
	edges1 := []string{"", " ", "\t a b \t", "ÉÀ ßİ", "\xffA\xe4", "a,b,,c", "Kk\u212a", "\u00a0x\u2003"}
	one := func(name, show string, ref func(string) string) bodyCase {
		return with(bodyRows("strings."+name, imp, "s string", show, edges1, n, r, stdGo2Text, text, ref))
	}
	pair := func(name, show string, ref func(two) string) bodyCase {
		return with(bodyRows("strings."+name, imp, "s string, t string", show, edges2, n, r, gen2, arg2, ref))
	}
	cases := []bodyCase{
		one("TrimSpace", `bs(strings.TrimSpace(s))`, func(s string) string { return stdGo2Bytes(strings.TrimSpace(s)) }),
		one("ToLower", `bs(strings.ToLower(s))`, func(s string) string { return stdGo2Bytes(strings.ToLower(s)) }),
		one("ToUpper", `bs(strings.ToUpper(s))`, func(s string) string { return stdGo2Bytes(strings.ToUpper(s)) }),
		one("Fields", `qs(strings.Fields(s))`, func(s string) string { return stdGo2List(strings.Fields(s)) }),
		pair("Split", `qs(strings.Split(s, t))`, func(v two) string { return stdGo2List(strings.Split(v.s, v.t)) }),
		pair("HasPrefix", `"\(strings.HasPrefix(s, t))"`, func(v two) string { return strconv.FormatBool(strings.HasPrefix(v.s, v.t)) }),
		pair("HasSuffix", `"\(strings.HasSuffix(s, t))"`, func(v two) string { return strconv.FormatBool(strings.HasSuffix(v.s, v.t)) }),
		pair("TrimPrefix", `bs(strings.TrimPrefix(s, t))`, func(v two) string { return stdGo2Bytes(strings.TrimPrefix(v.s, v.t)) }),
		pair("TrimSuffix", `bs(strings.TrimSuffix(s, t))`, func(v two) string { return stdGo2Bytes(strings.TrimSuffix(v.s, v.t)) }),
		pair("TrimLeft", `bs(strings.TrimLeft(s, t))`, func(v two) string { return stdGo2Bytes(strings.TrimLeft(v.s, v.t)) }),
		pair("TrimRight", `bs(strings.TrimRight(s, t))`, func(v two) string { return stdGo2Bytes(strings.TrimRight(v.s, v.t)) }),
		pair("Index", `"\(strings.Index(s, t))"`, func(v two) string { return strconv.Itoa(strings.Index(v.s, v.t)) }),
		pair("Contains", `"\(strings.Contains(s, t))"`, func(v two) string { return strconv.FormatBool(strings.Contains(v.s, v.t)) }),
		pair("LastIndex", `"\(strings.LastIndex(s, t))"`, func(v two) string { return strconv.Itoa(strings.LastIndex(v.s, v.t)) }),
		pair("Count", `"\(strings.Count(s, t))"`, func(v two) string { return strconv.Itoa(strings.Count(v.s, v.t)) }),
		pair("EqualFold", `"\(strings.EqualFold(s, t))"`, func(v two) string { return strconv.FormatBool(strings.EqualFold(v.s, v.t)) }),
		pair("Compare", `"\(strings.Compare(s, t))"`, func(v two) string { return strconv.Itoa(strings.Compare(v.s, v.t)) }),
		pair("ToValidUTF8", `bs(strings.ToValidUTF8(s, t))`, func(v two) string { return stdGo2Bytes(strings.ToValidUTF8(v.s, v.t)) }),
	}
	type rep struct {
		s string
		n int
	}
	type idx struct {
		s string
		c byte
	}
	type three struct{ s, old, repl string }
	type split struct {
		s, sep string
		n      int
	}
	type has struct {
		s string
		r rune
	}
	cases = append(cases,
		with(bodyRows("strings.Repeat", imp, "s string, k int", `bs(strings.Repeat(s, k))`,
			[]rep{{"", 0}, {"", 5}, {"ab", 0}, {"ab", 3}, {"\xff", 2}}, n, r,
			func(r *rand.Rand) rep { return rep{stdGo2Text(r), r.IntN(5)} },
			func(v rep) string { return text(v.s) + ", " + strconv.Itoa(v.n) },
			func(v rep) string { return stdGo2Bytes(strings.Repeat(v.s, v.n)) })),
		with(bodyRows("strings.IndexByte", imp, "s string, c byte", `"\(strings.IndexByte(s, c))"`,
			[]idx{{"", 'a'}, {"abc", 'c'}, {"abc", 'z'}, {"\xff\x00", 0}, {"\xff\x00", 255}}, n, r,
			func(r *rand.Rand) idx { return idx{stdGo2Text(r), byte(r.IntN(256))} },
			func(v idx) string { return text(v.s) + ", " + strconv.Itoa(int(v.c)) },
			func(v idx) string { return strconv.Itoa(strings.IndexByte(v.s, v.c)) })),
		with(bodyRows("strings.ReplaceAll", imp, "s string, old string, repl string", `bs(strings.ReplaceAll(s, old, repl))`,
			[]three{{"", "", "x"}, {"abc", "", "-"}, {"aaa", "a", "aa"}, {"é\xff", "\xff", ""}, {"abab", "ab", "\xff"}}, n, r,
			func(r *rand.Rand) three { return three{stdGo2Text(r), stdGo2Text(r), stdGo2Text(r)} },
			func(v three) string { return text(v.s) + ", " + text(v.old) + ", " + text(v.repl) },
			func(v three) string { return stdGo2Bytes(strings.ReplaceAll(v.s, v.old, v.repl)) })),
		with(bodyRows("strings.SplitN", imp, "s string, sep string, k int", `qs(strings.SplitN(s, sep, k))`,
			[]split{{"a,b,c", ",", 0}, {"a,b,c", ",", -1}, {"a,b,c", ",", 1}, {"a,b,c", ",", 2}, {"", ",", 0}, {"", "", -1}, {"abc", "", 2}}, n, r,
			func(r *rand.Rand) split { return split{stdGo2Text(r), stdGo2Text(r), r.IntN(7) - 2} },
			func(v split) string { return text(v.s) + ", " + text(v.sep) + ", " + strconv.Itoa(v.n) },
			func(v split) string { return stdGo2List(strings.SplitN(v.s, v.sep, v.n)) })),
		with(bodyRows("strings.ContainsRune", imp, "s string, x rune", `"\(strings.ContainsRune(s, x))"`,
			[]has{{"", 'a'}, {"é", 'é'}, {"\xff", 0xfffd}, {"\xef\xbf\xbd", 0xfffd}, {"a", -1}, {"a", 0x110000}}, n, r,
			func(r *rand.Rand) has { return has{stdGo2Text(r), np4fGenRune(r)} },
			func(v has) string { return text(v.s) + ", " + strconv.Itoa(int(v.r)) },
			func(v has) string { return strconv.FormatBool(strings.ContainsRune(v.s, v.r)) })),
	)
	cases = append(cases, stdGo2StrconvCases(r, n, with)...)
	return cases
}

// stdGo2Decls print a text as its bytes and a list as its length and its
// texts' bytes, so no answer depends on how a line prints.
const stdGo2Decls = `func bs(s string) string {
	let mut out = ""
	for i in 0..len(s) {
		out = out + "\(int(s[i])) "
	}
	out
}

func qs(xs []string) string {
	let mut out = "\(len(xs))"
	for x in xs {
		out = out + "|" + bs(x)
	}
	out
}

func np4fFl(s string) float64 {
	strconv.ParseFloat(s) catch {
	case strconv.Malformed, strconv.OutOfRange:
		0.0
	}
}`

func stdGo2Bytes(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		b.WriteString(strconv.Itoa(int(s[i])) + " ")
	}
	return b.String()
}

func stdGo2List(xs []string) string {
	out := strconv.Itoa(len(xs))
	for _, x := range xs {
		out += "|" + stdGo2Bytes(x)
	}
	return out
}

// stdGo2Pieces are what a seeded text is made of: separators and spaces,
// case pairs (K, k and the Kelvin sign; ß; İ), runes of every width and
// invalid bytes.
var stdGo2Pieces = []string{"a", "b", "ab", "A", "k", "K", "\u212a", ",", ", ", " ", "\t", "\u00a0", "é", "É", "ß", "İ", "世", "🙂", "o", "oo", "\xff", "\xe4\xb8", "\x00"}

func stdGo2Text(r *rand.Rand) string {
	var b strings.Builder
	for range r.IntN(7) {
		b.WriteString(stdGo2Pieces[r.IntN(len(stdGo2Pieces))])
	}
	return b.String()
}

func stdGo2StrconvCases(r *rand.Rand, n int, with func(bodyCase) bodyCase) []bodyCase {
	imp := []string{"strings", "strconv"}
	type base struct {
		v int64
		b int
	}
	type ff struct {
		f    float64
		fmt  byte
		prec int
	}
	fmts := []byte{'e', 'E', 'f', 'g', 'G', 'b', 'x', 'X'}
	var ffEdges []ff
	for _, f := range []float64{0, math.Copysign(0, -1), 1, -1.5, 1e21, 1e-7, 123456789.125, math.Inf(1), math.Inf(-1), math.NaN(), math.MaxFloat64, math.SmallestNonzeroFloat64} {
		for _, c := range fmts {
			ffEdges = append(ffEdges, ff{f, c, -1}, ff{f, c, 3})
		}
	}
	return []bodyCase{
		with(bodyRows("strconv.Itoa", imp, "x int", `bs(strconv.Itoa(x))`,
			[]int{0, -1, 9, 10, math.MaxInt, math.MinInt}, n, r, func(r *rand.Rand) int { return int(r.Uint64()) >> r.UintN(64) }, strconv.Itoa,
			func(v int) string { return stdGo2Bytes(strconv.Itoa(v)) })),
		with(bodyRows("strconv.FormatInt", imp, "x int64, b int", `bs(strconv.FormatInt(x, b))`,
			[]base{{0, 2}, {-255, 2}, {-255, 16}, {math.MaxInt64, 36}, {math.MinInt64, 36}, {math.MinInt64, 10}, {35, 36}}, n, r,
			func(r *rand.Rand) base { return base{int64(r.Uint64()) >> r.UintN(64), 2 + r.IntN(35)} },
			func(v base) string { return strconv.FormatInt(v.v, 10) + ", " + strconv.Itoa(v.b) },
			func(v base) string { return stdGo2Bytes(strconv.FormatInt(v.v, v.b)) })),
		with(bodyRows("strconv.FormatBool", imp, "x bool", `bs(strconv.FormatBool(x))`,
			[]bool{true, false}, 0, r, func(*rand.Rand) bool { return false }, strconv.FormatBool,
			func(v bool) string { return stdGo2Bytes(strconv.FormatBool(v)) })),
		with(bodyRows("strconv.FormatFloat", imp, "x float64, c byte, p int", `bs(strconv.FormatFloat(x, c, p))`,
			ffEdges, n, r,
			func(r *rand.Rand) ff { return ff{np4fGenFloat(r), fmts[r.IntN(len(fmts))], r.IntN(25) - 1} },
			func(v ff) string {
				return np4fFloatArg(v.f) + ", " + strconv.Itoa(int(v.fmt)) + ", " + strconv.Itoa(v.prec)
			},
			func(v ff) string { return stdGo2Bytes(strconv.FormatFloat(v.f, v.fmt, v.prec, 64)) })),
		with(bodyRows("strconv.Quote", imp, "s string", `bs(strconv.Quote(s))`,
			[]string{"", "\"", "\\", "\n\t\x00", "\xff", "é世🙂", "\u2028", "\x7f"}, n, r, stdGo2Text, np4fErgoString,
			func(s string) string { return stdGo2Bytes(strconv.Quote(s)) })),
	}
}
