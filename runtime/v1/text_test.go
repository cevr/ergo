package v1

import (
	"errors"
	"math"
	"math/rand/v2"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
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
		return StringsSlice(s, i, i+len(sub)) == sub && StringsSlice(s, j, j+len(sub)) == sub && i <= j, []string{s, sub}
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

// textSink keeps benchmark results alive.
var textSink any

// BenchmarkText: each new body against the Go function it wraps, same
// inputs (std-align S1c). Parity is the target: the bodies are one-line
// wrappers that Go inlines, and the parse bodies map Go's error once.
func BenchmarkText(b *testing.B) {
	s := strings.Repeat("alpha,beta,gamma ", 8)
	for _, c := range []struct {
		name      string
		ergo, ctl func()
	}{
		{"ReplaceAll", func() { textSink = StringsReplaceAll(s, ",", ";") }, func() { textSink = strings.ReplaceAll(s, ",", ";") }},
		{"Count", func() { textSink = StringsCount(s, "a") }, func() { textSink = strings.Count(s, "a") }},
		{"LastIndex", func() { textSink = StringsLastIndex(s, "beta") }, func() { textSink = strings.LastIndex(s, "beta") }},
		{"SplitN", func() { textSink = StringsSplitN(s, ",", 4) }, func() { textSink = strings.SplitN(s, ",", 4) }},
		{"EqualFold", func() { textSink = StringsEqualFold(s, strings.ToUpper(s)) }, func() { textSink = strings.EqualFold(s, strings.ToUpper(s)) }},
		{"ParseInt", func() { textSink, _ = StrconvParseInt("-1234567890", 10, 64) }, func() { textSink, _ = strconv.ParseInt("-1234567890", 10, 64) }},
		{"ParseIntFails", func() { _, textSink = StrconvParseInt("12x", 10, 64) }, func() { _, textSink = strconv.ParseInt("12x", 10, 64) }},
		{"ParseFloat", func() { textSink, _ = StrconvParseFloat("3.14159e10") }, func() { textSink, _ = strconv.ParseFloat("3.14159e10", 64) }},
		{"Unquote", func() { textSink, _ = StrconvUnquote(`"a\tb\u00e9"`) }, func() { textSink, _ = strconv.Unquote(`"a\tb\u00e9"`) }},
		{"RuneCount", func() { textSink = UTF8RuneCount(s) }, func() { textSink = utf8.RuneCountInString(s) }},
	} {
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

// TestStdTextLaws runs each law of textLaws on 10,000 seeded cases, and
// holds that textLaws restates exactly the ledger rows whose runner is
// this test.
func TestStdTextLaws(t *testing.T) {
	data, err := os.ReadFile("../../docs/spec/stdlib.md")
	if err != nil {
		t.Fatal(err)
	}
	var want, got []string
	for _, line := range strings.Split(string(data), "\n") {
		cells := strings.Split(line, " | ")
		if len(cells) >= 5 && strings.Trim(cells[3], "`") == "TestStdTextLaws" {
			want = append(want, strings.Trim(cells[0], "|` ")+" "+strings.Trim(cells[1], "`"))
		}
	}
	for name := range textLaws {
		got = append(got, name)
	}
	slices.Sort(want)
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Errorf("the Go restatements are %v; the ledger runs %v here", got, want)
	}
	for _, name := range got {
		r := rand.New(rand.NewPCG(1, uint64(len(name))))
		for i := range 10_000 {
			if ok, c := textLaws[name](r); !ok {
				t.Errorf("%s fails on case %d: %#v", name, i, c)
				break
			}
		}
	}
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
