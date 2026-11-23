package v1

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"math/rand/v2"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
)

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
	buf := make([]byte, 0, 8)
	built := []byte(s)
	goBuilt := &strings.Builder{}
	goBuilt.WriteString(s)
	return []textBench{
		{"Join", "StringsJoin", func() { textSink = StringsJoin(xs, ";") }, func() { textSink = strings.Join(xs, ";") }},
		{"String", "UTF8String", func() { textSink = UTF8String(buf[:8]) }, func() { textSink = string(buf[:8]) }},
		// std build's fold queries: a built program answers false, as Go's
		// constant does.
		{"InFold", "BuildInFold", func() { textSink = BuildInFold() }, func() { textSink = false }},
		{"Known", "BuildKnown", func() { textSink = BuildKnown(len(s)) }, func() { textSink = false }},
		{"Hole", "BuildHole", func() { textSink = BuildHole(s) }, func() { textSink = false }},
		// The builder bodies against Go's strings.Builder: a loop of
		// writes, a Grow, and the text of a built buffer (Go's String
		// shares its bytes; StringsBuilderText copies them).
		{"BuilderAppend", "StringsBuilderAppend", func() {
			var b []byte
			for range 16 {
				b = StringsBuilderAppend(b, "alpha,")
			}
			textSink = len(b)
		}, func() {
			var g strings.Builder
			for range 16 {
				g.WriteString("alpha,")
			}
			textSink = g.Len()
		}},
		{"BuilderAppendByte", "StringsBuilderAppendByte", func() {
			var b []byte
			for i := range 64 {
				b = StringsBuilderAppendByte(b, byte(i))
			}
			textSink = len(b)
		}, func() {
			var g strings.Builder
			for i := range 64 {
				g.WriteByte(byte(i))
			}
			textSink = g.Len()
		}},
		{"BuilderGrow", "StringsBuilderGrow", func() { textSink = cap(StringsBuilderGrow(nil, 256)) }, func() {
			var g strings.Builder
			g.Grow(256)
			textSink = g.Cap()
		}},
		// SB2: a write of bytes, an LZ77 run from the buffer's own text
		// (Go has no such write: hand Go appends its own slice, byte by
		// byte where the run overlaps), and []byte(s).
		{"BuilderAppendBytes", "StringsBuilderAppendBytes", func() {
			var b []byte
			for range 16 {
				b = StringsBuilderAppendBytes(b, built[:6])
			}
			textSink = len(b)
		}, func() {
			var g []byte
			for range 16 {
				g = append(g, built[:6]...)
			}
			textSink = len(g)
		}},
		{"BuilderAppendFrom", "StringsBuilderAppendFrom", func() {
			b := append([]byte(nil), "abc"...)
			for range 16 {
				b = StringsBuilderAppendFrom(b, len(b)-3, 40)
			}
			textSink = len(b)
		}, func() {
			b := append([]byte(nil), "abc"...)
			for range 16 {
				start := len(b) - 3
				for k := range 40 {
					b = append(b, b[start+k])
				}
			}
			textSink = len(b)
		}},
		{"Bytes", "StringsBytes", func() { textSink = len(StringsBytes(s)) }, func() { textSink = len([]byte(s)) }},
		{"BuilderText", "StringsBuilderText", func() { textSink = StringsBuilderText(built) }, func() { textSink = goBuilt.String() }},
		{"BuilderTextOwned", "StringsBuilderTextOwned", func() { textSink = StringsBuilderTextOwned(built) }, func() { textSink = goBuilt.String() }},
		{"Slice", "StringsSlice", func() { textSink = StringsSlice(s, 6, 10) }, func() { textSink = s[6:10] }},
		{"DecodeRuneInString", "UTF8DecodeRuneInString", func() { textSink = UTF8DecodeRuneInString("世界").Size }, func() { _, textSink = utf8.DecodeRuneInString("世界") }},
		{"RuneText", "UTF8RuneText", func() { textSink = UTF8RuneText('世') }, func() { textSink = string(rune('世')) }},
		{"AppendRune", "UTF8AppendRune", func() { textSink = len(UTF8AppendRune(buf[:0], '世')) }, func() { textSink = len(utf8.AppendRune(buf[:0], '世')) }},
		{"Atoi", "StrconvAtoi", func() { textSink, _ = StrconvAtoi("-1234567890") }, func() { textSink, _ = strconv.Atoi("-1234567890") }},
		{"AtoiFails", "StrconvAtoi", func() { _, textSink = StrconvAtoi("12x") }, func() { _, textSink = strconv.Atoi("12x") }},
		{"ParseInt", "StrconvParseInt", func() { textSink, _ = StrconvParseInt("-1234567890", 10, 64) }, func() { textSink, _ = strconv.ParseInt("-1234567890", 10, 64) }},
		{"ParseIntFails", "StrconvParseInt", func() { _, textSink = StrconvParseInt("12x", 10, 64) }, func() { _, textSink = strconv.ParseInt("12x", 10, 64) }},
		{"ParseIntHex", "StrconvParseInt", func() { textSink, _ = StrconvParseInt("0x1f2e3d", 0, 64) }, func() { textSink, _ = strconv.ParseInt("0x1f2e3d", 0, 64) }},
		{"ParseFloat", "StrconvParseFloat", func() { textSink, _ = StrconvParseFloat("3.14159e10") }, func() { textSink, _ = strconv.ParseFloat("3.14159e10", 64) }},
		{"AppendInt", "StrconvAppendInt", func() { buf = StrconvAppendInt(buf[:0], -1234567890, 10) }, func() { buf = strconv.AppendInt(buf[:0], -1234567890, 10) }},
		{"AppendUint", "StrconvAppendUint", func() { buf = StrconvAppendUint(buf[:0], 1234567890, 16) }, func() { buf = strconv.AppendUint(buf[:0], 1234567890, 16) }},
		{"AppendFloat", "StrconvAppendFloat", func() { buf = StrconvAppendFloat(buf[:0], 3.14159e10, 'g', -1) }, func() { buf = strconv.AppendFloat(buf[:0], 3.14159e10, 'g', -1, 64) }},
		{"Unquote", "StrconvUnquote", func() { textSink, _ = StrconvUnquote(`"a\tb\u00e9"`) }, func() { textSink, _ = strconv.Unquote(`"a\tb\u00e9"`) }},
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
	if n < 15 {
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
	if _, err := StrconvUnquote(`"a`); !malformed(err, `"a`) {
		t.Errorf("Unquote: %v", err)
	}
	if got := (OutOfRange{Text: "9"}).Error(); got != "strconv.OutOfRange{Text:9}" {
		t.Errorf("OutOfRange text %q", got)
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

// TestBuilderAppendFromIsADefect: WriteFrom out of range is a defect
// (stdlib.md strings 11): a negative count, a start outside the text, or a
// start at its end with a count above 0 panics with Go's slice or index
// error; a count of 0 inside the text or at its end is no write. Each call
// returns or panics at once: the law WriteFromRepeats covers every start
// and count in range. Mutant sb2-appendfrom-unchecked (the start check
// before the loop dropped) returns the buffer unchanged for a start at or
// past the end, since the loop runs only while start is inside the text,
// so the rows {4, 1} and {3, 1} fail.
func TestBuilderAppendFromIsADefect(t *testing.T) {
	for _, start := range []int{0, 3} {
		if got := string(StringsBuilderAppendFrom([]byte("abc"), start, 0)); got != "abc" {
			t.Fatalf("AppendFrom(abc, %d, 0) = %q", start, got)
		}
	}
	for _, c := range []struct{ start, n int }{{0, -1}, {2, -1}, {5, -1}, {-1, 0}, {4, 0}, {-1, 1}, {4, 1}, {3, 1}} {
		func() {
			defer func() {
				if _, bounds := recover().(runtime.Error); !bounds {
					t.Fatalf("AppendFrom(abc, %d, %d) did not return its defect, a slice or index error", c.start, c.n)
				}
			}()
			StringsBuilderAppendFrom([]byte("abc"), c.start, c.n)
		}()
	}
}
