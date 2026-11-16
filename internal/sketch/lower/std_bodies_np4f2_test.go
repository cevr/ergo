package lower

import (
	"errors"
	"math"
	"math/rand/v2"
	"strconv"
	"strings"
	"testing"

	v1 "ergo.local/prototype/runtime/v1"
	"ergo.local/prototype/runtime/v1/digest"
)

// TestNP4f2MovesKeepMeaning is NP4f-2's comparing law: strings' 24
// wrappers, strconv's 5 formatters and hex's 2 functions moved from
// runtime/v1 externs to import go calls (hex.Decode with its offset found
// in Ergo), and each still answers what its old Go body answers, on edges
// (the empty text and separator, invalid UTF-8, case folds such as K and
// the Kelvin sign, every float format and precision, odd and non-hex
// texts) and seeded values. Every answer is printed as its bytes, so
// invalid UTF-8 and line breaks compare exactly. NP4f-3 deletes it with
// the old bodies. Mutants np4f2-* are killed here.
//
// It builds Go because the old bodies are Go and the new functions run
// only in a built program (compareBodies).
func TestNP4f2MovesKeepMeaning(t *testing.T) {
	needsBuild(t)
	seed := bodySeed(t)
	r := rand.New(rand.NewPCG(seed, 0x4f2))
	const n = 24
	imp := []string{"strings", "strconv", "hex"}
	with := func(c bodyCase) bodyCase {
		c.decls = np4f2Decls
		return c
	}
	text := np4fErgoString
	type two struct{ s, t string }
	gen2 := func(r *rand.Rand) two { return two{np4f2Text(r), np4f2Text(r)} }
	arg2 := func(v two) string { return text(v.s) + ", " + text(v.t) }
	var edges2 []two
	for _, s := range []string{"", "a", "ab,cd", " x ", "\xff,é", "Kk", "ab"} {
		for _, t := range []string{"", ",", "a", "b", " ", "é", "\xff", "k", "ab"} {
			edges2 = append(edges2, two{s, t})
		}
	}
	edges1 := []string{"", " ", "\t a b \t", "ÉÀ ßİ", "\xffA\xe4", "a,b,,c", "Kk\u212a", "\u00a0x\u2003"}
	one := func(name, show string, ref func(string) string) bodyCase {
		return with(bodyRows("strings."+name, imp, "s string", show, edges1, n, r, np4f2Text, text, ref))
	}
	pair := func(name, show string, ref func(two) string) bodyCase {
		return with(bodyRows("strings."+name, imp, "s string, t string", show, edges2, n, r, gen2, arg2, ref))
	}
	cases := []bodyCase{
		one("TrimSpace", `bs(strings.TrimSpace(s))`, func(s string) string { return np4f2Bytes(v1.StringsTrimSpace(s)) }),
		one("ToLower", `bs(strings.ToLower(s))`, func(s string) string { return np4f2Bytes(v1.StringsToLower(s)) }),
		one("ToUpper", `bs(strings.ToUpper(s))`, func(s string) string { return np4f2Bytes(v1.StringsToUpper(s)) }),
		one("Fields", `qs(strings.Fields(s))`, func(s string) string { return np4f2List(v1.StringsFields(s)) }),
		pair("Split", `qs(strings.Split(s, t))`, func(v two) string { return np4f2List(v1.StringsSplit(v.s, v.t)) }),
		pair("HasPrefix", `"\(strings.HasPrefix(s, t))"`, func(v two) string { return strconv.FormatBool(v1.StringsHasPrefix(v.s, v.t)) }),
		pair("HasSuffix", `"\(strings.HasSuffix(s, t))"`, func(v two) string { return strconv.FormatBool(v1.StringsHasSuffix(v.s, v.t)) }),
		pair("TrimPrefix", `bs(strings.TrimPrefix(s, t))`, func(v two) string { return np4f2Bytes(v1.StringsTrimPrefix(v.s, v.t)) }),
		pair("TrimSuffix", `bs(strings.TrimSuffix(s, t))`, func(v two) string { return np4f2Bytes(v1.StringsTrimSuffix(v.s, v.t)) }),
		pair("TrimLeft", `bs(strings.TrimLeft(s, t))`, func(v two) string { return np4f2Bytes(v1.StringsTrimLeft(v.s, v.t)) }),
		pair("TrimRight", `bs(strings.TrimRight(s, t))`, func(v two) string { return np4f2Bytes(v1.StringsTrimRight(v.s, v.t)) }),
		pair("Index", `"\(strings.Index(s, t))"`, func(v two) string { return strconv.Itoa(v1.StringsIndex(v.s, v.t)) }),
		pair("Contains", `"\(strings.Contains(s, t))"`, func(v two) string { return strconv.FormatBool(v1.StringsContains(v.s, v.t)) }),
		pair("LastIndex", `"\(strings.LastIndex(s, t))"`, func(v two) string { return strconv.Itoa(v1.StringsLastIndex(v.s, v.t)) }),
		pair("Count", `"\(strings.Count(s, t))"`, func(v two) string { return strconv.Itoa(v1.StringsCount(v.s, v.t)) }),
		pair("EqualFold", `"\(strings.EqualFold(s, t))"`, func(v two) string { return strconv.FormatBool(v1.StringsEqualFold(v.s, v.t)) }),
		pair("Compare", `"\(strings.Compare(s, t))"`, func(v two) string { return strconv.Itoa(v1.StringsCompare(v.s, v.t)) }),
		pair("ToValidUTF8", `bs(strings.ToValidUTF8(s, t))`, func(v two) string { return np4f2Bytes(v1.StringsToValidUTF8(v.s, v.t)) }),
	}
	type join struct {
		xs  []string
		sep string
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
	list := func(xs []string) string {
		parts := make([]string, len(xs))
		for i, x := range xs {
			parts[i] = text(x)
		}
		return "[]string{" + strings.Join(parts, ", ") + "}"
	}
	genList := func(r *rand.Rand) []string {
		xs := make([]string, r.IntN(5))
		for i := range xs {
			xs[i] = np4f2Text(r)
		}
		return xs
	}
	cases = append(cases,
		with(bodyRows("strings.Join", imp, "xs []string, sep string", `bs(strings.Join(xs, sep))`,
			[]join{{nil, ","}, {[]string{""}, ","}, {[]string{"a", "b"}, ""}, {[]string{"a", "", "b"}, "\xff"}}, n, r,
			func(r *rand.Rand) join { return join{genList(r), np4f2Text(r)} },
			func(v join) string { return list(v.xs) + ", " + text(v.sep) },
			func(v join) string { return np4f2Bytes(v1.StringsJoin(v.xs, v.sep)) })),
		with(bodyRows("strings.Repeat", imp, "s string, k int", `bs(strings.Repeat(s, k))`,
			[]rep{{"", 0}, {"", 5}, {"ab", 0}, {"ab", 3}, {"\xff", 2}}, n, r,
			func(r *rand.Rand) rep { return rep{np4f2Text(r), r.IntN(5)} },
			func(v rep) string { return text(v.s) + ", " + strconv.Itoa(v.n) },
			func(v rep) string { return np4f2Bytes(v1.StringsRepeat(v.s, v.n)) })),
		with(bodyRows("strings.IndexByte", imp, "s string, c byte", `"\(strings.IndexByte(s, c))"`,
			[]idx{{"", 'a'}, {"abc", 'c'}, {"abc", 'z'}, {"\xff\x00", 0}, {"\xff\x00", 255}}, n, r,
			func(r *rand.Rand) idx { return idx{np4f2Text(r), byte(r.IntN(256))} },
			func(v idx) string { return text(v.s) + ", " + strconv.Itoa(int(v.c)) },
			func(v idx) string { return strconv.Itoa(v1.StringsIndexByte(v.s, v.c)) })),
		with(bodyRows("strings.ReplaceAll", imp, "s string, old string, repl string", `bs(strings.ReplaceAll(s, old, repl))`,
			[]three{{"", "", "x"}, {"abc", "", "-"}, {"aaa", "a", "aa"}, {"é\xff", "\xff", ""}, {"abab", "ab", "\xff"}}, n, r,
			func(r *rand.Rand) three { return three{np4f2Text(r), np4f2Text(r), np4f2Text(r)} },
			func(v three) string { return text(v.s) + ", " + text(v.old) + ", " + text(v.repl) },
			func(v three) string { return np4f2Bytes(v1.StringsReplaceAll(v.s, v.old, v.repl)) })),
		with(bodyRows("strings.SplitN", imp, "s string, sep string, k int", `qs(strings.SplitN(s, sep, k))`,
			[]split{{"a,b,c", ",", 0}, {"a,b,c", ",", -1}, {"a,b,c", ",", 1}, {"a,b,c", ",", 2}, {"", ",", 0}, {"", "", -1}, {"abc", "", 2}}, n, r,
			func(r *rand.Rand) split { return split{np4f2Text(r), np4f2Text(r), r.IntN(7) - 2} },
			func(v split) string { return text(v.s) + ", " + text(v.sep) + ", " + strconv.Itoa(v.n) },
			func(v split) string { return np4f2List(v1.StringsSplitN(v.s, v.sep, v.n)) })),
		with(bodyRows("strings.ContainsRune", imp, "s string, x rune", `"\(strings.ContainsRune(s, x))"`,
			[]has{{"", 'a'}, {"é", 'é'}, {"\xff", 0xfffd}, {"\xef\xbf\xbd", 0xfffd}, {"a", -1}, {"a", 0x110000}}, n, r,
			func(r *rand.Rand) has { return has{np4f2Text(r), np4fGenRune(r)} },
			func(v has) string { return text(v.s) + ", " + strconv.Itoa(int(v.r)) },
			func(v has) string { return strconv.FormatBool(v1.StringsContainsRune(v.s, v.r)) })),
	)
	cases = append(cases, np4f2StrconvCases(r, n, with)...)
	cases = append(cases, np4f2HexCases(r, n, with)...)
	compareBodies(t, "TestNP4f2MovesKeepMeaning", seed, cases)
}

// np4f2Decls print a text as its bytes and a list as its length and its
// texts' bytes, so no answer depends on how a line prints.
const np4f2Decls = `func bs(s string) string {
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

func np4f2Bytes(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		b.WriteString(strconv.Itoa(int(s[i])) + " ")
	}
	return b.String()
}

func np4f2List(xs []string) string {
	out := strconv.Itoa(len(xs))
	for _, x := range xs {
		out += "|" + np4f2Bytes(x)
	}
	return out
}

// np4f2Pieces are what a seeded text is made of: separators and spaces,
// case pairs (K, k and the Kelvin sign; ß; İ), runes of every width and
// invalid bytes.
var np4f2Pieces = []string{"a", "b", "ab", "A", "k", "K", "\u212a", ",", ", ", " ", "\t", "\u00a0", "é", "É", "ß", "İ", "世", "🙂", "o", "oo", "\xff", "\xe4\xb8", "\x00"}

func np4f2Text(r *rand.Rand) string {
	var b strings.Builder
	for range r.IntN(7) {
		b.WriteString(np4f2Pieces[r.IntN(len(np4f2Pieces))])
	}
	return b.String()
}

func np4f2StrconvCases(r *rand.Rand, n int, with func(bodyCase) bodyCase) []bodyCase {
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
			func(v int) string { return np4f2Bytes(v1.StrconvItoa(v)) })),
		with(bodyRows("strconv.FormatInt", imp, "x int64, b int", `bs(strconv.FormatInt(x, b))`,
			[]base{{0, 2}, {-255, 2}, {-255, 16}, {math.MaxInt64, 36}, {math.MinInt64, 36}, {math.MinInt64, 10}, {35, 36}}, n, r,
			func(r *rand.Rand) base { return base{int64(r.Uint64()) >> r.UintN(64), 2 + r.IntN(35)} },
			func(v base) string { return strconv.FormatInt(v.v, 10) + ", " + strconv.Itoa(v.b) },
			func(v base) string { return np4f2Bytes(v1.StrconvFormatInt(v.v, v.b)) })),
		with(bodyRows("strconv.FormatBool", imp, "x bool", `bs(strconv.FormatBool(x))`,
			[]bool{true, false}, 0, r, func(*rand.Rand) bool { return false }, strconv.FormatBool,
			func(v bool) string { return np4f2Bytes(v1.StrconvFormatBool(v)) })),
		with(bodyRows("strconv.FormatFloat", imp, "x float64, c byte, p int", `bs(strconv.FormatFloat(x, c, p))`,
			ffEdges, n, r,
			func(r *rand.Rand) ff { return ff{np4fGenFloat(r), fmts[r.IntN(len(fmts))], r.IntN(25) - 1} },
			func(v ff) string {
				return np4fFloatArg(v.f) + ", " + strconv.Itoa(int(v.fmt)) + ", " + strconv.Itoa(v.prec)
			},
			func(v ff) string { return np4f2Bytes(v1.StrconvFormatFloat(v.f, v.fmt, v.prec)) })),
		with(bodyRows("strconv.Quote", imp, "s string", `bs(strconv.Quote(s))`,
			[]string{"", "\"", "\\", "\n\t\x00", "\xff", "é世🙂", "\u2028", "\x7f"}, n, r, np4f2Text, np4fErgoString,
			func(s string) string { return np4f2Bytes(v1.StrconvQuote(s)) })),
	}
}

func np4f2HexCases(r *rand.Rand, n int, with func(bodyCase) bodyCase) []bodyCase {
	imp := []string{"strings", "strconv", "hex"}
	hexText := func(r *rand.Rand) string {
		const digits = "0123456789abcdefABCDEFxg \xff"
		b := make([]byte, r.IntN(9))
		for i := range b {
			if r.IntN(8) == 0 {
				b[i] = digits[r.IntN(len(digits))]
			} else {
				b[i] = digits[r.IntN(22)]
			}
		}
		return string(b)
	}
	return []bodyCase{
		with(bodyRows("hex.Encode", imp, "s string", `bs(hex.Encode(s))`,
			[]string{"", "\x00", "\xff\x10", "é"}, n, r, np4f2Text, np4fErgoString,
			func(s string) string { return np4f2Bytes(digest.HexEncode(s)) })),
		with(bodyRows("hex.Decode", imp, "s string", `bs(hex.Decode(s) catch {
	case hex.Malformed as m:
		"bad \(m.Offset)"
	})`,
			[]string{"", "0", "00", "0g", "g0", "abc", "ABcd", "zz1", "123", "12 3", "1\xff"}, n, r, hexText, np4fErgoString,
			func(s string) string {
				out, err := digest.HexDecode(s)
				var m digest.HexMalformed
				if errors.As(err, &m) {
					return np4f2Bytes("bad " + strconv.Itoa(m.Offset))
				}
				return np4f2Bytes(out)
			})),
	}
}
