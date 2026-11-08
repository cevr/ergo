package v1

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"math"
	"math/rand/v2"
	"slices"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"ergo.local/prototype/internal/lawledger"
)

// textAlphabet is the generator's alphabet: separators, white space of
// several kinds, ASCII letters of both cases, multi-byte UTF-8 (a letter
// whose case maps change its length, Kelvin and the Turkish dotted I), and
// bytes that are not valid UTF-8, so a law is checked over the whole
// domain of string, not ASCII only.
var textAlphabet = []string{"a", "b", "A", "Z", ",", ", ", "\n", "\r", " ", "\t", "\u00a0", "\u2028", "é", "É", "ß", "ẞ", "K", "\u212a", "İ", "ı", "ǅ", "€", "\xff", "\xc3", "\xa9", "\xe2\x82", "0", "1", "-", "+", "_", "x", "\"", "\\", "'"}

// randomString is a seeded string of 0 to 8 alphabet entries.
func randomString(r *rand.Rand) string {
	var b strings.Builder
	for range r.IntN(9) {
		b.WriteString(textAlphabet[r.IntN(len(textAlphabet))])
	}
	return b.String()
}

// randomNumberText is a text that is often an integer: digits with signs,
// prefixes, underscores, spaces and huge values mixed in.
func randomNumberText(r *rand.Rand) string {
	parts := []string{"0", "1", "7", "9", "-", "+", "00", "_", " ", "x", "0x", "9223372036854775807", "9223372036854775808", "-9223372036854775808", "99999999999999999999"}
	var b strings.Builder
	for range 1 + r.IntN(4) {
		b.WriteString(parts[r.IntN(len(parts))])
	}
	return b.String()
}

// randomFloat is a seeded float64: any bit pattern, or a special value.
func randomFloat(r *rand.Rand) float64 {
	switch r.IntN(6) {
	case 0:
		return []float64{0, math.Copysign(0, -1), math.Inf(1), math.Inf(-1), math.NaN(), math.MaxFloat64, math.SmallestNonzeroFloat64, 0.1, 1e23}[r.IntN(9)]
	case 1:
		return r.NormFloat64() * 1e6
	}
	return math.Float64frombits(r.Uint64())
}

// textLaws restates each law of std strings, strconv and utf8 whose ledger
// runner is TestStdTextLaws over the Go bodies those externs bind
// (docs/spec/stdlib.md, Std laws). Each takes the generator and reports
// whether one case holds, with the case for a failure.
var textLaws = map[string]func(r *rand.Rand) (bool, any){
	"strings JoinInvertsSplit": func(r *rand.Rand) (bool, any) {
		s, sep := randomString(r), randomString(r)
		return sep == "" || StringsJoin(StringsSplit(s, sep), sep) == s, []string{s, sep}
	},
	"strings CountMatchesSplit": func(r *rand.Rand) (bool, any) {
		s, sep := randomString(r), randomString(r)
		return sep == "" || StringsCount(s, sep) == len(StringsSplit(s, sep))-1, []string{s, sep}
	},
	"strings EqualFoldIsSymmetric": func(r *rand.Rand) (bool, any) {
		s, t := randomString(r), randomString(r)
		if r.IntN(2) == 0 { // often a case change of s
			t = []func(string) string{StringsToUpper, StringsToLower}[r.IntN(2)](s)
		}
		return StringsEqualFold(s, s) && StringsEqualFold(s, t) == StringsEqualFold(t, s), []string{s, t}
	},
	"strings TrimSpaceIsIdempotent": func(r *rand.Rand) (bool, any) {
		s := randomString(r)
		return StringsTrimSpace(StringsTrimSpace(s)) == StringsTrimSpace(s), s
	},
	"strings ToLowerIsIdempotent": func(r *rand.Rand) (bool, any) {
		s := randomString(r)
		return StringsToLower(StringsToLower(s)) == StringsToLower(s), s
	},
	"strings IndexFinds": func(r *rand.Rand) (bool, any) {
		s, sub := randomString(r), randomString(r)
		if r.IntN(2) == 0 && len(s) > 0 { // often a sub that is there
			i := r.IntN(len(s))
			sub = s[i : i+r.IntN(len(s)-i+1)]
		}
		i, j := StringsIndex(s, sub), StringsLastIndex(s, sub)
		if i < 0 {
			return j < 0 && !StringsContains(s, sub), []string{s, sub}
		}
		if sub == "" {
			return i == 0 && j == len(s), []string{s, sub}
		}
		return StringsSlice(s, i, i+len(sub)) == sub && StringsSlice(s, j, j+len(sub)) == sub && i <= j &&
			StringsLastIndex(StringsSlice(s, 0, i+len(sub)-1), sub) < 0 && StringsIndex(StringsSlice(s, j+1, len(s)), sub) < 0, []string{s, sub}
	},
	"strings HasPrefixOfConcat": func(r *rand.Rand) (bool, any) {
		p, s := randomString(r), randomString(r)
		return StringsHasPrefix(p+s, p) && StringsHasSuffix(p+s, s), []string{p, s}
	},
	"strings RepeatLength": func(r *rand.Rand) (bool, any) {
		s, n := randomString(r), r.IntN(80)-8
		return n < 0 || n > 64 || len(StringsRepeat(s, n)) == n*len(s), []any{s, n}
	},
	"strings FieldsHaveNoSpace": func(r *rand.Rand) (bool, any) {
		s := randomString(r)
		for _, f := range StringsFields(s) {
			again := StringsFields(f)
			if len(again) != 1 || StringsJoin(again, "") != f {
				return false, s
			}
		}
		return true, s
	},
	"strings ReplaceAllLength": func(r *rand.Rand) (bool, any) {
		s, old, repl := randomString(r), randomString(r), randomString(r)
		if r.IntN(2) == 0 && len(s) > 0 {
			i := r.IntN(len(s))
			old = s[i : i+r.IntN(len(s)-i+1)]
		}
		return old == "" || len(StringsReplaceAll(s, old, repl)) == len(s)+StringsCount(s, old)*(len(repl)-len(old)), []string{s, old, repl}
	},
	"utf8 RuneCountAtMostLen": func(r *rand.Rand) (bool, any) {
		s := randomString(r)
		return UTF8RuneCount(s) <= len(s) && UTF8RuneCount(s) >= 0, s
	},
	"utf8 RuneCountOfConcat": func(r *rand.Rand) (bool, any) {
		a, b := randomString(r), randomString(r)
		return UTF8RuneCount(a+b) <= UTF8RuneCount(a)+UTF8RuneCount(b), []string{a, b}
	},
	"utf8 RuneCountAddsRune": func(r *rand.Rand) (bool, any) {
		s := randomString(r)
		return UTF8RuneCount(s+"é") == UTF8RuneCount(s)+1, s
	},
	"strconv AtoiInvertsItoa": func(r *rand.Rand) (bool, any) {
		n := int(r.Int64()) >> r.IntN(64)
		m, err := StrconvAtoi(StrconvItoa(n))
		return err == nil && m == n, n
	},
	"strconv ItoaInvertsAtoi": func(r *rand.Rand) (bool, any) {
		s := randomNumberText(r)
		n, err := StrconvAtoi(s)
		if err != nil {
			var m Malformed
			var o OutOfRange
			return errors.As(err, &m) || errors.As(err, &o), s
		}
		m, err := StrconvAtoi(StrconvItoa(n))
		return err == nil && m == n, s
	},
	"strconv UnquoteInvertsQuote": func(r *rand.Rand) (bool, any) {
		s := randomString(r)
		u, err := StrconvUnquote(StrconvQuote(s))
		return err == nil && u == s, s
	},
	"strconv ParseBoolInvertsFormat": func(r *rand.Rand) (bool, any) {
		b := r.IntN(2) == 0
		got, err := StrconvParseBool(StrconvFormatBool(b))
		return err == nil && got == b, b
	},
	"strconv FormatFloatRoundTrips": func(r *rand.Rand) (bool, any) {
		f := randomFloat(r)
		if f != f {
			return true, f
		}
		got, err := StrconvParseFloat(StrconvFormatFloat(f, 'g', -1))
		return err == nil && got == f, f
	},
	"strconv ParseIntInvertsFormat": func(r *rand.Rand) (bool, any) {
		n := int(r.Int64()) >> r.IntN(64)
		got, err := StrconvParseInt(StrconvItoa(n), 10, 64)
		return err == nil && got == int64(n), n
	},
}

// textLawStatements is the statement each textLaws entry restates, the
// law's source text as the ledger writes it; lawledger.Run holds it equal
// to the ledger, so a law changed in source fails here until its
// restatement above is changed with it.
var textLawStatements = map[string]string{
	"strconv AtoiInvertsItoa":        `AtoiInvertsItoa(n int) { try Atoi(Itoa(n)) == n }`,
	"strconv FormatFloatRoundTrips":  `FormatFloatRoundTrips(f float64) { if f != f { return true } try ParseFloat(FormatFloat(f, 'g', -1)) == f }`,
	"strconv ItoaInvertsAtoi":        `ItoaInvertsAtoi(s string) { let n = Atoi(s) catch { case Malformed, OutOfRange: return true } try Atoi(Itoa(n)) == n }`,
	"strconv ParseBoolInvertsFormat": `ParseBoolInvertsFormat(b bool) { try ParseBool(FormatBool(b)) == b }`,
	"strconv ParseIntInvertsFormat":  `ParseIntInvertsFormat(n int) { try ParseInt(Itoa(n), 10, 64) == int64(n) }`,
	"strconv UnquoteInvertsQuote":    `UnquoteInvertsQuote(s string) { try Unquote(Quote(s)) == s }`,
	"strings CountMatchesSplit":      `CountMatchesSplit(s string, sep string) { if sep == "" { return true } Count(s, sep) == len(Split(s, sep))-1 }`,
	"strings EqualFoldIsSymmetric":   `EqualFoldIsSymmetric(s string, t string) { EqualFold(s, s) && EqualFold(s, t) == EqualFold(t, s) }`,
	"strings FieldsHaveNoSpace":      `FieldsHaveNoSpace(s string) { for f in Fields(s) { let again = Fields(f) if len(again) != 1 || Join(again, "") != f { return false } } true }`,
	"strings HasPrefixOfConcat":      `HasPrefixOfConcat(p string, s string) { HasPrefix(p+s, p) && HasSuffix(p+s, s) }`,
	"strings IndexFinds":             `IndexFinds(s string, sub string) { let i = Index(s, sub) let j = LastIndex(s, sub) if i < 0 { return j < 0 && !Contains(s, sub) } if sub == "" { return i == 0 && j == len(s) } Slice(s, i, i+len(sub)) == sub && Slice(s, j, j+len(sub)) == sub && i <= j && LastIndex(Slice(s, 0, i+len(sub)-1), sub) < 0 && Index(Slice(s, j+1, len(s)), sub) < 0 }`,
	"strings JoinInvertsSplit":       `JoinInvertsSplit(s string, sep string) { if sep == "" { return true } Join(Split(s, sep), sep) == s }`,
	"strings RepeatLength":           `RepeatLength(s string, n int) { if n < 0 || n > 64 { return true } len(Repeat(s, n)) == n*len(s) }`,
	"strings ReplaceAllLength":       `ReplaceAllLength(s string, old string, repl string) { if old == "" { return true } len(ReplaceAll(s, old, repl)) == len(s)+Count(s, old)*(len(repl)-len(old)) }`,
	"strings ToLowerIsIdempotent":    `ToLowerIsIdempotent(s string) { ToLower(ToLower(s)) == ToLower(s) }`,
	"strings TrimSpaceIsIdempotent":  `TrimSpaceIsIdempotent(s string) { TrimSpace(TrimSpace(s)) == TrimSpace(s) }`,
	"utf8 RuneCountAddsRune":         `RuneCountAddsRune(s string) { RuneCount(s+"é") == RuneCount(s)+1 }`,
	"utf8 RuneCountAtMostLen":        `RuneCountAtMostLen(s string) { RuneCount(s) <= len(s) && RuneCount(s) >= 0 }`,
	"utf8 RuneCountOfConcat":         `RuneCountOfConcat(a string, b string) { RuneCount(a+b) <= RuneCount(a)+RuneCount(b) }`,
}

// textSink keeps benchmark results alive.
var textSink any

// textBench is one BenchmarkText case: the text.go body it measures, and
// a call of it and of the Go function it stands for, on the same inputs.
type textBench struct {
	name, body string
	ergo, ctl  func()
}

// textBenches is every BenchmarkText case. TestTextBodiesHaveBenchmarks
// holds that each //ergo:pure body of text.go has one.
func textBenches() []textBench {
	s := strings.Repeat("alpha,beta,gamma ", 8)
	xs := strings.Split(s, ",")
	return []textBench{
		{"Split", "StringsSplit", func() { textSink = StringsSplit(s, ",") }, func() { textSink = strings.Split(s, ",") }},
		{"Join", "StringsJoin", func() { textSink = StringsJoin(xs, ";") }, func() { textSink = strings.Join(xs, ";") }},
		{"HasPrefix", "StringsHasPrefix", func() { textSink = StringsHasPrefix(s, "alpha,") }, func() { textSink = strings.HasPrefix(s, "alpha,") }},
		{"HasSuffix", "StringsHasSuffix", func() { textSink = StringsHasSuffix(s, "gamma ") }, func() { textSink = strings.HasSuffix(s, "gamma ") }},
		{"TrimSpace", "StringsTrimSpace", func() { textSink = StringsTrimSpace(s) }, func() { textSink = strings.TrimSpace(s) }},
		{"TrimPrefix", "StringsTrimPrefix", func() { textSink = StringsTrimPrefix(s, "alpha,") }, func() { textSink = strings.TrimPrefix(s, "alpha,") }},
		{"TrimSuffix", "StringsTrimSuffix", func() { textSink = StringsTrimSuffix(s, "gamma ") }, func() { textSink = strings.TrimSuffix(s, "gamma ") }},
		{"Fields", "StringsFields", func() { textSink = StringsFields(s) }, func() { textSink = strings.Fields(s) }},
		{"Repeat", "StringsRepeat", func() { textSink = StringsRepeat("ab", 64) }, func() { textSink = strings.Repeat("ab", 64) }},
		{"ToLower", "StringsToLower", func() { textSink = StringsToLower("ALPHA,Beta") }, func() { textSink = strings.ToLower("ALPHA,Beta") }},
		{"ToUpper", "StringsToUpper", func() { textSink = StringsToUpper(s) }, func() { textSink = strings.ToUpper(s) }},
		{"Index", "StringsIndex", func() { textSink = StringsIndex(s, "gamma alpha") }, func() { textSink = strings.Index(s, "gamma alpha") }},
		{"Contains", "StringsContains", func() { textSink = StringsContains(s, "delta") }, func() { textSink = strings.Contains(s, "delta") }},
		{"Slice", "StringsSlice", func() { textSink = StringsSlice(s, 6, 10) }, func() { textSink = s[6:10] }},
		{"ReplaceAll", "StringsReplaceAll", func() { textSink = StringsReplaceAll(s, ",", ";") }, func() { textSink = strings.ReplaceAll(s, ",", ";") }},
		{"Count", "StringsCount", func() { textSink = StringsCount(s, "a") }, func() { textSink = strings.Count(s, "a") }},
		{"LastIndex", "StringsLastIndex", func() { textSink = StringsLastIndex(s, "beta") }, func() { textSink = strings.LastIndex(s, "beta") }},
		{"SplitN", "StringsSplitN", func() { textSink = StringsSplitN(s, ",", 4) }, func() { textSink = strings.SplitN(s, ",", 4) }},
		{"EqualFold", "StringsEqualFold", func() { textSink = StringsEqualFold(s, strings.ToUpper(s)) }, func() { textSink = strings.EqualFold(s, strings.ToUpper(s)) }},
		{"ContainsRune", "StringsContainsRune", func() { textSink = StringsContainsRune(s, 'é') }, func() { textSink = strings.ContainsRune(s, 'é') }},
		{"RuneCount", "UTF8RuneCount", func() { textSink = UTF8RuneCount(s) }, func() { textSink = utf8.RuneCountInString(s) }},
		{"Itoa", "StrconvItoa", func() { textSink = StrconvItoa(-1234567890) }, func() { textSink = strconv.Itoa(-1234567890) }},
		{"Atoi", "StrconvAtoi", func() { textSink, _ = StrconvAtoi("-1234567890") }, func() { textSink, _ = strconv.Atoi("-1234567890") }},
		{"AtoiFails", "StrconvAtoi", func() { _, textSink = StrconvAtoi("12x") }, func() { _, textSink = strconv.Atoi("12x") }},
		{"ParseInt", "StrconvParseInt", func() { textSink, _ = StrconvParseInt("-1234567890", 10, 64) }, func() { textSink, _ = strconv.ParseInt("-1234567890", 10, 64) }},
		{"ParseIntFails", "StrconvParseInt", func() { _, textSink = StrconvParseInt("12x", 10, 64) }, func() { _, textSink = strconv.ParseInt("12x", 10, 64) }},
		{"ParseIntHex", "StrconvParseInt", func() { textSink, _ = StrconvParseInt("0x1f2e3d", 0, 64) }, func() { textSink, _ = strconv.ParseInt("0x1f2e3d", 0, 64) }},
		{"ParseFloat", "StrconvParseFloat", func() { textSink, _ = StrconvParseFloat("3.14159e10") }, func() { textSink, _ = strconv.ParseFloat("3.14159e10", 64) }},
		{"ParseBool", "StrconvParseBool", func() { textSink, _ = StrconvParseBool("false") }, func() { textSink, _ = strconv.ParseBool("false") }},
		{"FormatBool", "StrconvFormatBool", func() { textSink = StrconvFormatBool(true) }, func() { textSink = strconv.FormatBool(true) }},
		{"FormatFloat", "StrconvFormatFloat", func() { textSink = StrconvFormatFloat(3.14159e10, 'g', -1) }, func() { textSink = strconv.FormatFloat(3.14159e10, 'g', -1, 64) }},
		{"Unquote", "StrconvUnquote", func() { textSink, _ = StrconvUnquote(`"a\tb\u00e9"`) }, func() { textSink, _ = strconv.Unquote(`"a\tb\u00e9"`) }},
		{"Quote", "StrconvQuote", func() { textSink = StrconvQuote("a\tb\u00e9\xff") }, func() { textSink = strconv.Quote("a\tb\u00e9\xff") }},
	}
}

// TestStrconvBase10MatchesGo: the base-10 reads that Atoi and ParseInt
// write out give strconv's value and class (ErrSyntax is Malformed,
// ErrRange is OutOfRange) on 200,000 seeded number-like texts at every
// bit size 0 to 64, and on the edges of each size (the largest and
// smallest values, one past them, with signs, leading zeros and a bad
// byte after an overflow).
func TestStrconvBase10MatchesGo(t *testing.T) {
	class := func(err error) string {
		var m Malformed
		var o OutOfRange
		var ne *strconv.NumError
		switch {
		case err == nil:
			return "ok"
		case errors.As(err, &m):
			return "malformed " + m.Text
		case errors.As(err, &o):
			return "range " + o.Text
		case errors.As(err, &ne) && ne.Err == strconv.ErrSyntax:
			return "malformed " + ne.Num
		case errors.As(err, &ne) && ne.Err == strconv.ErrRange:
			return "range " + ne.Num
		}
		return "other " + err.Error()
	}
	check := func(s string, bits int) {
		n, err := StrconvParseInt(s, 10, bits)
		m, gerr := strconv.ParseInt(s, 10, bits)
		if class(err) != class(gerr) || err == nil && n != m {
			t.Fatalf("ParseInt(%q, 10, %d) = %d, %s; Go %d, %s", s, bits, n, class(err), m, class(gerr))
		}
		if bits == 0 {
			a, err := StrconvAtoi(s)
			g, gerr := strconv.Atoi(s)
			if class(err) != class(gerr) || err == nil && a != g {
				t.Fatalf("Atoi(%q) = %d, %s; Go %d, %s", s, a, class(err), g, class(gerr))
			}
		}
	}
	var edges []string
	for bits := 1; bits <= 64; bits++ {
		hi := uint64(1)<<uint(bits-1) - 1
		lo := uint64(1) << uint(bits-1)
		for _, v := range []uint64{hi, hi + 1, lo, lo + 1, 1<<uint(bits) - 1} {
			d := strconv.FormatUint(v, 10)
			edges = append(edges, d, "-"+d, "+"+d, "00"+d, d+"0", d+"x", d+"0x", "-"+d+"9")
		}
	}
	edges = append(edges, "", "+", "-", "+-1", "--1", "1_0", " 1", "1 ", "0", "-0", "18446744073709551616", "99999999999999999999x", "x99999999999999999999", "٣")
	for _, s := range edges {
		for bits := 0; bits <= 64; bits++ {
			check(s, bits)
		}
	}
	digits := []string{"0", "1", "5", "9", "9", "8", "7", "+", "-", "x", "_", " ", "é"}
	r := rand.New(rand.NewPCG(3, 10))
	for range 200_000 {
		var b strings.Builder
		for range r.IntN(24) {
			b.WriteString(digits[r.IntN(len(digits))])
		}
		check(b.String(), r.IntN(65))
	}
}

// TestTextBodiesHaveBenchmarks: every //ergo:pure body that text.go
// declares (read with go/parser, not from a list) has a BenchmarkText
// case against its Go control.
func TestTextBodiesHaveBenchmarks(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "text.go", nil, parser.ParseComments)
	if err != nil {
		t.Fatal(err)
	}
	benched := map[string]bool{}
	for _, c := range textBenches() {
		benched[c.body] = true
	}
	n := 0
	for _, d := range file.Decls {
		f, ok := d.(*ast.FuncDecl)
		if !ok || f.Doc == nil || !slices.ContainsFunc(f.Doc.List, func(c *ast.Comment) bool { return c.Text == "//ergo:pure" }) {
			continue
		}
		n++
		if !benched[f.Name.Name] {
			t.Errorf("%s is an //ergo:pure body of text.go with no BenchmarkText case", f.Name.Name)
		}
	}
	if n < 30 {
		t.Errorf("found %d pure bodies in text.go; the directive scan is broken", n)
	}
}

// BenchmarkText: each body of text.go against the Go function it stands
// for, same inputs (std-align S1c). Parity is the target: most bodies are
// one-line wrappers that Go inlines; the base-10 parses read the digits
// themselves, so their failure path saves strconv's *NumError.
func BenchmarkText(b *testing.B) {
	for _, c := range textBenches() {
		b.Run(c.name+"/ergo", func(b *testing.B) {
			b.ReportAllocs()
			for range b.N {
				c.ergo()
			}
		})
		b.Run(c.name+"/go", func(b *testing.B) {
			b.ReportAllocs()
			for range b.N {
				c.ctl()
			}
		})
	}
}

// TestStdTextLaws runs each law of textLaws on 10,000 seeded cases;
// lawledger.Run holds that textLaws restates exactly the ledger rows whose
// runner is this test, each with the row's statement.
func TestStdTextLaws(t *testing.T) {
	laws := map[string]lawledger.Law{}
	for name, holds := range textLaws {
		laws[name] = lawledger.Law{Statement: textLawStatements[name], Check: func(t *testing.T) {
			r := rand.New(rand.NewPCG(1, uint64(len(name))))
			for i := range 10_000 {
				if ok, c := holds(r); !ok {
					t.Errorf("%s fails on case %d: %#v", name, i, c)
					break
				}
			}
		}}
	}
	lawledger.Run(t, laws)
}

// TestStrconvFailures: each parse fails Malformed for a text it cannot
// read and OutOfRange for a number that does not fit, as Go's ErrSyntax
// and ErrRange; an invalid base is a defect.
func TestStrconvFailures(t *testing.T) {
	malformed := func(err error, s string) bool {
		var m Malformed
		return errors.As(err, &m) && m.Text == s
	}
	outOfRange := func(err error, s string) bool {
		var o OutOfRange
		return errors.As(err, &o) && o.Text == s
	}
	for _, s := range []string{"", "x", "1x", " 1", "1.5", "0x10"} {
		if _, err := StrconvAtoi(s); !malformed(err, s) {
			t.Errorf("Atoi(%q): %v", s, err)
		}
	}
	for _, s := range []string{"99999999999999999999", "-9223372036854775809"} {
		if _, err := StrconvAtoi(s); !outOfRange(err, s) {
			t.Errorf("Atoi(%q): %v", s, err)
		}
	}
	for _, c := range []struct {
		s          string
		base, bits int
		want       int64
	}{{"0x10", 0, 64, 16}, {"0o17", 0, 64, 15}, {"1_000", 0, 64, 1000}, {"-80", 16, 8, -128}, {"z", 36, 64, 35}} {
		if n, err := StrconvParseInt(c.s, c.base, c.bits); err != nil || n != c.want {
			t.Errorf("ParseInt(%q, %d, %d) = %d, %v", c.s, c.base, c.bits, n, err)
		}
	}
	if _, err := StrconvParseInt("128", 10, 8); !outOfRange(err, "128") {
		t.Errorf("ParseInt(128, 10, 8): %v", err)
	}
	if _, err := StrconvParseInt("1_000", 10, 64); !malformed(err, "1_000") {
		t.Errorf("ParseInt(1_000, 10, 64): %v", err)
	}
	if _, err := StrconvParseFloat("1e400"); !outOfRange(err, "1e400") {
		t.Errorf("ParseFloat(1e400): %v", err)
	}
	if _, err := StrconvParseFloat("1.2.3"); !malformed(err, "1.2.3") {
		t.Errorf("ParseFloat(1.2.3): %v", err)
	}
	if _, err := StrconvParseBool("maybe"); !malformed(err, "maybe") {
		t.Errorf("ParseBool(maybe): %v", err)
	}
	if _, err := StrconvUnquote(`"a`); !malformed(err, `"a`) {
		t.Errorf("Unquote: %v", err)
	}
	if got := (OutOfRange{Text: "9"}).Error(); got != "strconv.OutOfRange{Text:9}" {
		t.Errorf("OutOfRange text %q", got)
	}
	if parts := StringsSplitN("a,b,c", ",", 0); parts == nil || len(parts) != 0 {
		t.Errorf("SplitN n=0 = %#v, want an empty slice", parts)
	}
	defer func() {
		if recover() == nil {
			t.Error("ParseInt with base 1 did not panic")
		}
	}()
	_, _ = StrconvParseInt("1", 1, 64)
}

// TestStrconvAtoiFailsMalformed: every text Go's strconv.Atoi refuses as
// syntax is the typed failure Malformed with the text (out of range is
// OutOfRange, TestStrconvFailures), and its Error is the record's text the
// lowering writes.
func TestStrconvAtoiFailsMalformed(t *testing.T) {
	for _, s := range []string{"", "x", "1x", " 1", "1.5"} {
		n, err := StrconvAtoi(s)
		var m Malformed
		if n != 0 || !errors.As(err, &m) || m.Text != s {
			t.Errorf("StrconvAtoi(%q) = %d, %v; want 0, Malformed{Text:%q}", s, n, err, s)
		}
	}
	if n, err := StrconvAtoi("-42"); n != -42 || err != nil {
		t.Errorf("StrconvAtoi(-42) = %d, %v", n, err)
	}
	if got := (Malformed{Text: "1x"}).Error(); got != "strconv.Malformed{Text:1x}" {
		t.Errorf("Malformed.Error() = %q", got)
	}
}

// TestStringsSliceIsADefect: a bound out of range panics, as an index out
// of range does; the runtime reports it as a defect.
func TestStringsSliceIsADefect(t *testing.T) {
	if got := StringsSlice("hello", 1, 3); got != "el" {
		t.Fatalf("StringsSlice = %q", got)
	}
	defer func() {
		if recover() == nil {
			t.Fatal("StringsSlice(hello, 2, 9) did not panic")
		}
	}()
	StringsSlice("hello", 2, 9)
}
