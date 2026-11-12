package eval

import (
	"fmt"
	"math"
	"math/rand/v2"
	"os"
	"strconv"
	"testing"
	"time"

	"ergo.local/prototype/internal/sketch/check"
	v1 "ergo.local/prototype/runtime/v1"
	v1digest "ergo.local/prototype/runtime/v1/digest"
	v1rand "ergo.local/prototype/runtime/v1/rand"
)

// TestNativesAgreeWithRuntime holds each native of the evaluator
// (natives.go) to the runtime/v1 body the built program calls, on
// generated inputs: texts with multi-byte and invalid UTF-8, empty
// separators, negative and huge counts, every strconv base and bit size,
// NaN and -0 in sorts, and the seeded rand stream draw for draw. The
// natives are written from Go's standard library and the std's spec, never
// by calling runtime/v1, so this is two implementations agreeing, value
// for value, failure for failure (record and text) and defect for defect
// (Go's panic text). A failing seed prints the native, the arguments and
// both results, and the replay command.
//
// Mutants only it kills (testdata/mutants.json): oracle-o1-native-pad-bytes,
// oracle-o1-native-parse-range, oracle-o1-native-times-wraps,
// oracle-o1-native-intn-modulo, oracle-o1-native-splitn-count,
// oracle-o1-native-split-empty-sep.
func TestNativesAgreeWithRuntime(t *testing.T) {
	n := 300
	if testing.Short() {
		n = 100
	}
	first := uint64(0)
	if s := os.Getenv("ERGO_EVAL_NATIVE_SEED"); s != "" {
		v, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		first, n = v, 1
	}
	pkg := checked(t, "import strconv\nimport time\nimport utf8\n\ntest T {\n\tassert strconv.Itoa(utf8.RuneCount(\"a\")) == \"1\" && time.DurationText(1s) == \"1s\"\n}\n")
	funcs := map[string]*check.Func{}
	for _, p := range imports(pkg) {
		for _, f := range p.Funcs {
			funcs[p.Path+"."+f.Name] = f
		}
	}
	in := &interp{budget: DefaultBudget()}
	fr := in.newFrame(pkg, pkg.Tests[0].Decl)
	for seed := first; seed < first+uint64(n); seed++ {
		r := rand.New(rand.NewPCG(seed, 0x0a))
		g := nativeGen{r}
		for _, c := range nativeCases(g) {
			in.steps, in.bytes = 0, 0
			nat := natives[c.name]
			if nat == nil {
				t.Fatalf("no native %s", c.name)
			}
			fn := funcs[c.name]
			got := outcome(func() (Value, *abrupt) { return nat(in, fr, fr.decl, fn, c.args) })
			want := runtimeOutcome(fn, c.v1)
			if got != want && !(c.anyDefect && isDefect(got) && isDefect(want)) {
				t.Fatalf("seed %d: %s%s: the evaluator gives %s, runtime/v1 %s\nreplay: ERGO_EVAL_NATIVE_SEED=%d go test -run TestNativesAgreeWithRuntime ./internal/sketch/eval",
					seed, c.name, describeArgs(c.args), got, want, seed)
			}
		}
		// The rand stream, draw for draw.
		s := r.Uint64N(1 << 20)
		st, _ := natives["rand.newStream"](in, fr, fr.decl, nil, []Value{s})
		ref := v1rand.NewStream(s)
		for k := 0; k < 20; k++ {
			var got, want string
			switch r.IntN(3) {
			case 0:
				got = outcome(func() (Value, *abrupt) { return st.(opObject).opCall(in, fr, fr.decl, "Uint64", nil) })
				want = describe(ref.Uint64())
			case 1:
				m := int64(r.IntN(40)) + 1
				got = outcome(func() (Value, *abrupt) { return st.(opObject).opCall(in, fr, fr.decl, "IntN", []Value{m}) })
				want = describe(int64(ref.IntN(int(m))))
			default:
				got = outcome(func() (Value, *abrupt) { return st.(opObject).opCall(in, fr, fr.decl, "Float64", nil) })
				want = describe(ref.Float64())
			}
			if got != want {
				t.Fatalf("seed %d: rand stream %d draw %d: the evaluator gives %s, runtime/v1 %s", seed, s, k, got, want)
			}
		}
	}
}

type nativeCase struct {
	name      string
	args      []Value
	v1        func() any // runtime/v1's result, an error for a failure
	anyDefect bool       // both defect; Go's panic texts differ by form
}

type nativeGen struct{ r *rand.Rand }

var textParts = []string{"", "a", "b", "ab", " ", "\t", "\n", "é", "\xff", "-", "+", "0", "1", "9", "7", ".", "/", "e", "x", "\"", "\\", "inf", "_", "..", "A"}

func (g nativeGen) text() string {
	s := ""
	for k := g.r.IntN(6); k > 0; k-- {
		s += textParts[g.r.IntN(len(textParts))]
	}
	return s
}

func (g nativeGen) small() int { return g.r.IntN(16) - 3 }

func (g nativeGen) runeV() int32 {
	rs := []int32{'a', 'é', ' ', 0, -1, 0xD800, 0x10FFFF, 0x110000, '日', 'x'}
	return rs[g.r.IntN(len(rs))]
}

func (g nativeGen) texts() []string {
	out := make([]string, g.r.IntN(5))
	for i := range out {
		out[i] = g.text()
	}
	return out
}

func (g nativeGen) floats() []float64 {
	fs := []float64{0, math.Copysign(0, -1), 1, -2.5, math.NaN(), math.Inf(1), math.Inf(-1), 3}
	out := make([]float64, g.r.IntN(6))
	for i := range out {
		out[i] = fs[g.r.IntN(len(fs))]
	}
	return out
}

func (g nativeGen) ints() []int64 {
	out := make([]int64, g.r.IntN(6))
	for i := range out {
		out[i] = int64(g.r.IntN(7) - 3)
	}
	return out
}

func nativeCases(g nativeGen) []nativeCase {
	s, u, w := g.text(), g.text(), g.text()
	i, j := g.small(), g.small()
	rn := g.runeV()
	xs := g.texts()
	big := []int64{0, 1, -1, 7, math.MaxInt64, math.MinInt64, 1 << 40, -(1 << 33)}
	d, m := big[g.r.IntN(len(big))], big[g.r.IntN(len(big))]
	base, bits := g.r.IntN(40)-1, g.r.IntN(70)-2
	fmtc := []byte("efgxbEG?")[g.r.IntN(8)]
	f := []float64{0, 1.5, -2.25, 1e21, math.NaN(), math.Inf(-1), 123456.789}[g.r.IntN(7)]
	num := []string{"12", "-0", "+7", "9223372036854775807", "9223372036854775808", "-9223372036854775809", "0x1f", "1e3", "1_000", "", "-", "abc", "1.5", "1e400", "true", "F", "\"a\\tb\"", "`x`", "'c'"}[g.r.IntN(19)]
	ints, ints2 := g.ints(), g.ints()
	fl := g.floats()
	fa, fb := 0.0, 0.0
	if f2 := g.floats(); len(f2) > 1 {
		fa, fb = f2[0], f2[1]
	}
	x := int64(g.r.IntN(5) - 2)
	dur := []string{"1h2m3.5s", "-1.5ms", "0", "", "1", "3µs", "9223372036854775807ns", "9223372036854775808ns", "1e3s", "1.h", ".5s", "+2us", "1d", "-0"}[g.r.IntN(14)]
	ls := func(ss []string) Value {
		vs := make([]Value, len(ss))
		for k, v := range ss {
			vs[k] = v
		}
		return newSlice(vs)
	}
	li := func(ns []int64) Value {
		vs := make([]Value, len(ns))
		for k, v := range ns {
			vs[k] = v
		}
		return newSlice(vs)
	}
	lf := func(ns []float64) Value {
		vs := make([]Value, len(ns))
		for k, v := range ns {
			vs[k] = v
		}
		return newSlice(vs)
	}
	sj := int64(len(ints))
	if g.r.IntN(4) == 0 {
		// A bound past the length is skipped: it reads the Go target's
		// capacity, which the evaluator does not guess.
		sj = int64(g.r.IntN(len(ints)+4) - 3)
	}
	si := int64(g.r.IntN(3) - 1)
	// time text: a seeded instant in a seeded layout, read back in that
	// layout or another, sometimes cut short.
	layouts := []string{time.RFC3339, time.RFC3339Nano, time.DateTime, time.DateOnly, time.Kitchen, time.RFC1123Z, time.RFC1123, "Jan 2 15:04", "02/01/2006 15:04:05.000"}
	lay := layouts[g.r.IntN(len(layouts))]
	sec, nsec := g.r.Int64N(1<<34)-(1<<33), g.r.Int64N(1e9)
	text := time.Unix(sec, nsec).UTC().Format(layouts[g.r.IntN(len(layouts))])
	if g.r.IntN(4) == 0 {
		text = text[:g.r.IntN(len(text)+1)]
	}
	return []nativeCase{
		{name: "time.format", args: []Value{sec, nsec, lay}, v1: func() any { return v1.TimeFormat(sec, nsec, lay) }},
		{name: "time.parse", args: []Value{lay, text}, v1: func() any { return result(v1.TimeParse(lay, text)) }},
		{name: "strings.Compare", args: []Value{s, u}, v1: func() any { return int64(v1.StringsCompare(s, u)) }},
		{name: "strings.PadLeft", args: []Value{s, int64(i), rn}, v1: func() any { return v1.StringsPadLeft(s, i, rn) }},
		{name: "strings.PadRight", args: []Value{s, int64(j), rn}, v1: func() any { return v1.StringsPadRight(s, j, rn) }},
		{name: "strings.Split", args: []Value{s, u}, v1: func() any { return v1.StringsSplit(s, u) }},
		{name: "strings.Join", args: []Value{ls(xs), u}, v1: func() any { return v1.StringsJoin(xs, u) }},
		{name: "strings.HasPrefix", args: []Value{s, u}, v1: func() any { return v1.StringsHasPrefix(s, u) }},
		{name: "strings.HasSuffix", args: []Value{s, u}, v1: func() any { return v1.StringsHasSuffix(s, u) }},
		{name: "strings.TrimSpace", args: []Value{s}, v1: func() any { return v1.StringsTrimSpace(s) }},
		{name: "strings.TrimPrefix", args: []Value{s, u}, v1: func() any { return v1.StringsTrimPrefix(s, u) }},
		{name: "strings.TrimSuffix", args: []Value{s, u}, v1: func() any { return v1.StringsTrimSuffix(s, u) }},
		{name: "strings.Fields", args: []Value{s}, v1: func() any { return v1.StringsFields(s) }},
		{name: "strings.Repeat", args: []Value{s, int64(i)}, v1: func() any { return v1.StringsRepeat(s, i) }},
		{name: "strings.ToLower", args: []Value{s}, v1: func() any { return v1.StringsToLower(s) }},
		{name: "strings.ToUpper", args: []Value{s}, v1: func() any { return v1.StringsToUpper(s) }},
		{name: "strings.Index", args: []Value{s, u}, v1: func() any { return int64(v1.StringsIndex(s, u)) }},
		{name: "strings.Contains", args: []Value{s, u}, v1: func() any { return v1.StringsContains(s, u) }},
		{name: "strings.Slice", args: []Value{s, int64(i), int64(j)}, v1: func() any { return v1.StringsSlice(s, i, j) }},
		{name: "strings.ReplaceAll", args: []Value{s, u, w}, v1: func() any { return v1.StringsReplaceAll(s, u, w) }},
		{name: "strings.LastIndex", args: []Value{s, u}, v1: func() any { return int64(v1.StringsLastIndex(s, u)) }},
		{name: "strings.Count", args: []Value{s, u}, v1: func() any { return int64(v1.StringsCount(s, u)) }},
		{name: "strings.SplitN", args: []Value{s, u, int64(i)}, v1: func() any { return v1.StringsSplitN(s, u, i) }},
		{name: "strings.EqualFold", args: []Value{s, u}, v1: func() any { return v1.StringsEqualFold(s, u) }},
		{name: "strings.ContainsRune", args: []Value{s, rn}, v1: func() any { return v1.StringsContainsRune(s, rn) }},
		{name: "strings.ToValidUTF8", args: []Value{s, u}, v1: func() any { return v1.StringsToValidUTF8(s, u) }},
		{name: "utf8.RuneCount", args: []Value{s}, v1: func() any { return int64(v1.UTF8RuneCount(s)) }},
		{name: "utf8.RuneText", args: []Value{rn}, v1: func() any { return v1.UTF8RuneText(rn) }},
		{name: "utf8.ValidString", args: []Value{s}, v1: func() any { return v1.UTF8ValidString(s) }},
		{name: "utf8.ValidRune", args: []Value{rn}, v1: func() any { return v1.UTF8ValidRune(rn) }},
		{name: "utf8.RuneLen", args: []Value{rn}, v1: func() any { return int64(v1.UTF8RuneLen(rn)) }},
		{name: "utf8.DecodeRuneInString", args: []Value{s}, v1: func() any { return v1.UTF8DecodeRuneInString(s) }},
		{name: "utf8.AppendRune", args: []Value{bytesValue(s), rn}, v1: func() any { return v1.UTF8AppendRune([]byte(s), rn) }},
		{name: "strconv.Itoa", args: []Value{d}, v1: func() any { return v1.StrconvItoa(int(d)) }},
		{name: "strconv.FormatInt", args: []Value{d, int64(base)}, v1: func() any { return v1.StrconvFormatInt(d, base) }},
		{name: "strconv.FormatBool", args: []Value{i > 4}, v1: func() any { return v1.StrconvFormatBool(i > 4) }},
		{name: "strconv.FormatFloat", args: []Value{f, fmtc, int64(i)}, v1: func() any { return v1.StrconvFormatFloat(f, fmtc, i) }},
		{name: "strconv.Quote", args: []Value{s}, v1: func() any { return v1.StrconvQuote(s) }},
		{name: "strconv.Atoi", args: []Value{num}, v1: func() any { return result(v1.StrconvAtoi(num)) }},
		{name: "strconv.ParseInt", args: []Value{num, int64(base), int64(bits)}, v1: func() any { return result(v1.StrconvParseInt(num, base, bits)) }},
		{name: "strconv.ParseFloat", args: []Value{num}, v1: func() any { return result(v1.StrconvParseFloat(num)) }},
		{name: "strconv.ParseBool", args: []Value{num}, v1: func() any { return result(v1.StrconvParseBool(num)) }},
		{name: "strconv.Unquote", args: []Value{num}, v1: func() any { return result(v1.StrconvUnquote(num)) }},
		{name: "filepath.Join", args: []Value{ls(xs)}, v1: func() any { return v1.FilepathJoin(xs) }},
		{name: "filepath.Base", args: []Value{s}, v1: func() any { return v1.FilepathBase(s) }},
		{name: "filepath.Dir", args: []Value{s}, v1: func() any { return v1.FilepathDir(s) }},
		{name: "filepath.Ext", args: []Value{s}, v1: func() any { return v1.FilepathExt(s) }},
		{name: "filepath.Clean", args: []Value{s}, v1: func() any { return v1.FilepathClean(s) }},
		{name: "filepath.IsAbs", args: []Value{s}, v1: func() any { return v1.FilepathIsAbs(s) }},
		{name: "hex.Encode", args: []Value{s}, v1: func() any { return v1digest.HexEncode(s) }},
		{name: "time.DurationText", args: []Value{d}, v1: func() any { return v1.TimeDurationText(durationOf(d)) }},
		{name: "time.Times", args: []Value{d, m}, v1: func() any { return int64(v1.TimeTimes(durationOf(d), int(m))) }},
		{name: "time.subMono", args: []Value{d, m}, v1: func() any { return int64(v1.TimeSubMono(d, m)) }},
		{name: "time.subWall", args: []Value{d % (1 << 34), x, m % (1 << 34), int64(i)}, v1: func() any { return int64(v1.TimeSub(d%(1<<34), x, m%(1<<34), int64(i))) }},
		{name: "time.Divide", args: []Value{d, m}, anyDefect: true, v1: func() any { return int64(v1.TimeDivide(durationOf(d), int(m))) }},
		{name: "time.Count", args: []Value{d, m}, anyDefect: true, v1: func() any { return int64(v1.TimeCount(durationOf(d), durationOf(m))) }},
		{name: "time.ParseDuration", args: []Value{dur}, v1: func() any { return result(v1.TimeParseDuration(dur)) }},
		{name: "slices.Take", args: []Value{li(ints), x}, anyDefect: true, v1: func() any { return takeOf(v1.SlicesTake(ints, int(x))) }},
		{name: "slices.Clone", args: []Value{li(ints)}, v1: func() any { return v1.SlicesClone(ints) }},
		{name: "slices.Concat", args: []Value{li(ints), li(ints2)}, v1: func() any { return v1.SlicesConcat(ints, ints2) }},
		{name: "slices.Reverse", args: []Value{ls(xs)}, v1: func() any { return v1.SlicesReverse(xs) }},
		{name: "slices.Sort", args: []Value{li(ints)}, v1: func() any { return v1.SlicesSort(ints) }},
		{name: "slices.Sort", args: []Value{lf(fl)}, v1: func() any { return v1.SlicesSort(fl) }},
		{name: "slices.Sort", args: []Value{ls(xs)}, v1: func() any { return v1.SlicesSort(xs) }},
		{name: "slices.Contains", args: []Value{li(ints), x}, v1: func() any { return v1.SlicesContains(ints, x) }},
		{name: "slices.Index", args: []Value{li(ints), x}, v1: func() any { return int64(v1.SlicesIndex(ints, x)) }},
		{name: "slices.Equal", args: []Value{li(ints), li(ints2)}, v1: func() any { return v1.SlicesEqual(ints, ints2) }},
		{name: "slices.Slice", args: []Value{li(ints), si, sj}, anyDefect: true, v1: func() any { return v1.SlicesSlice(ints, int(si), int(sj)) }},
		{name: "cmp.Compare", args: []Value{fa, fb}, v1: func() any { return int64(v1.CmpCompare(fa, fb)) }},
		{name: "cmp.Compare", args: []Value{s, u}, v1: func() any { return int64(v1.CmpCompare(s, u)) }},
	}
}

func durationOf(d int64) time.Duration { return time.Duration(d) }

// takeOf is slices.Take's two results as one outcome.
func takeOf(v int64, rest []int64) any {
	r := make([]Value, len(rest))
	for k, x := range rest {
		r[k] = x
	}
	return tuple{elems: []Value{v, newSlice(r)}}
}

// result is a v1 (value, error) as one outcome.
func result[T any](v T, err error) any {
	if err != nil {
		return err
	}
	return v
}

func bytesValue(s string) Value {
	vs := make([]Value, len(s))
	for k := 0; k < len(s); k++ {
		vs[k] = s[k]
	}
	return newSlice(vs)
}

// outcome is a native's result as text: its value, its failure or its
// defect, or a skip.
func outcome(f func() (Value, *abrupt)) string {
	v, ab := f()
	if ab == nil {
		return describe(v)
	}
	switch ab.kind {
	case ctlFail:
		return "fails " + describe(ab.val)
	case ctlDefect:
		return "defect " + ab.msg
	}
	return fmt.Sprintf("abrupt %d %s", ab.kind, ab.msg)
}

func isDefect(s string) bool { return len(s) >= 6 && s[:6] == "defect" }

// runtimeOutcome is runtime/v1's result in outcome's form.
func runtimeOutcome(fn *check.Func, f func() any) (out string) {
	defer func() {
		if r := recover(); r != nil {
			out = "defect " + fmt.Sprint(r)
		}
	}()
	switch e := f().(type) {
	case v1.Malformed:
		return "fails " + describe(stdRecord(fn, "Malformed", map[string]Value{"Text": e.Text}))
	case v1.OutOfRange:
		return "fails " + describe(stdRecord(fn, "OutOfRange", map[string]Value{"Text": e.Text}))
	case v1.ParseError:
		return "fails " + describe(stdRecord(fn, "ParseError", map[string]Value{"Layout": e.Layout, "Value": e.Value, "Message": e.Message}))
	case error:
		return "unexpected error " + e.Error()
	default:
		return describe(toValue(fn, e))
	}
}

func toValue(fn *check.Func, v any) Value {
	switch v := v.(type) {
	case []string:
		vs := make([]Value, len(v))
		for k, x := range v {
			vs[k] = x
		}
		return newSlice(vs)
	case []int64:
		vs := make([]Value, len(v))
		for k, x := range v {
			vs[k] = x
		}
		return newSlice(vs)
	case []float64:
		vs := make([]Value, len(v))
		for k, x := range v {
			vs[k] = x
		}
		return newSlice(vs)
	case []byte:
		return bytesValue(string(v))
	case int:
		return int64(v)
	case time.Duration:
		return int64(v)
	case v1.Reading:
		return stdRecord(fn, "Reading", map[string]Value{"Sec": v.Sec, "Nsec": v.Nsec, "Mono": v.Mono})
	case v1.Decoded:
		return stdRecord(fn, "Decoded", map[string]Value{"Rune": v.Rune, "Size": int64(v.Size)})
	}
	return v
}

func describeArgs(args []Value) string {
	s := "("
	for k, a := range args {
		if k > 0 {
			s += ", "
		}
		s += describe(a)
	}
	return s + ")"
}
