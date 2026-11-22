package eval

import (
	"cmp"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
	v1 "ergo.local/prototype/runtime/v1"
	v1digest "ergo.local/prototype/runtime/v1/digest"
	v1http "ergo.local/prototype/runtime/v1/http"
	v1rand "ergo.local/prototype/runtime/v1/rand"
)

// TestNativesAgreeWithRuntime holds each native of the evaluator
// (natives.go) to the runtime/v1 body the built program calls, on
// generated inputs: texts with multi-byte and invalid UTF-8, empty
// separators, negative and huge counts, every strconv base and bit size,
// NaN and -0 in sorts, instants past the years 0 to 9999, and the seeded
// rand stream draw for draw. Most natives are written from Go's standard
// library and the std's spec, so this is two implementations agreeing,
// value for value, failure for failure (record and text) and defect for
// defect (Go's panic text). time.format and time.parse call their runtime
// bodies (nativestime.go), so a law run at check time runs the build's
// code; their rows hold that the native is that body
// (eval-time-format-is-gos, eval-time-parse-is-gos). A failing seed
// prints the native, the arguments and both results, and the replay
// command.
//
// Mutants only it kills (testdata/mutants.json): oracle-o1-native-pad-bytes,
// oracle-o1-native-parse-range, oracle-o1-native-times-wraps,
// oracle-o1-native-intn-modulo, oracle-o1-native-splitn-count,
// oracle-o1-native-split-empty-sep, evbuilder-appendfrom-whole-run,
// evbuilder-appendfrom-end-start, evbuilder-appendbytes-drops-last,
// eval-time-format-is-gos, eval-time-parse-is-gos,
// eval-sortfunc-equal-on-greater, eval-repeat-drops-one, and
// ST4's st4-* rows but st4-native-missing (nativespure.go).
// The tests read the natives through loadedNatives, which builds them.

// byteSliceV is b as the evaluator holds a []byte: a slice of uint8s.
func byteSliceV(b []byte) Value {
	out := make([]Value, len(b))
	for i, c := range b {
		out[i] = c
	}
	return newSlice(out)
}

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
	// The packages of the pure externs ST4 gave natives, whose failures
	// and records are their own package's types.
	for _, path := range []string{"base64", "bits", "cmp", "filepath", "hex", "http", "math"} {
		p, err := check.Std.Import(path)
		if err != nil {
			t.Fatalf("check.Std.Import(%q): %v", path, err)
		}
		for _, f := range p.Funcs {
			funcs[p.Path+"."+f.Name] = f
		}
	}
	queryFields = func(names, values []string) Value {
		vs := make([]Value, len(names))
		for k := range names {
			vs[k] = stdRecord(funcs["http.EncodeQuery"], "Field", map[string]Value{"Name": names[k], "Value": values[k]})
		}
		return newSlice(vs)
	}
	in := &interp{budget: DefaultBudget()}
	fr := in.newFrame(pkg, pkg.Tests[0].Decl)
	for seed := first; seed < first+uint64(n); seed++ {
		r := rand.New(rand.NewPCG(seed, 0x0a))
		g := nativeGen{r}
		for _, c := range nativeCases(g) {
			in.steps, in.bytes = 0, 0
			nat := loadedNatives().std[c.name]
			if nat == nil {
				nat = loadedNatives().goLib[c.name]
			}
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
		st, _ := loadedNatives().std["rand.newStream"](in, fr, fr.decl, nil, []Value{s})
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

// TestNativeWorkIsWhatGoReads is a growth guard over counted work: a
// substring, a prefix or suffix test and a trim of one spend the same
// steps, and a substring and a trim the same value bytes, for a text of
// 8 bytes and of 800, as Go's own functions read only the prefix and
// share the text (natives.go, readsOf and shares). Charged as the whole
// text, a parser that slices the rest of its input at each token would
// pay steps quadratic in its input. IndexByte reads up to the byte it
// finds (std io's lines call it on the rest of the text at each line).
// Mutants only it kills (testdata/mutants.json): oracle-o22-slice-reads-all,
// oracle-o22-prefix-reads-all, oracle-o22-slice-allocates,
// evbuilder-indexbyte-reads-all.
func TestNativeWorkIsWhatGoReads(t *testing.T) {
	pkg := checked(t, "import strings\n\ntest T {\n\tassert strings.Slice(\"ab\", 0, 1) == \"a\"\n}\n")
	in := &interp{budget: DefaultBudget()}
	fr := in.newFrame(pkg, pkg.Tests[0].Decl)
	work := func(name string, s string) (steps, bytes int64) {
		args := []Value{s, "ab"}
		switch name {
		case "strings.Slice":
			args = []Value{s, int64(1), int64(5)}
		case "strings.IndexByte":
			args = []Value{s, uint8('b')}
		}
		in.steps, in.bytes = 0, 0
		nat := loadedNatives().std[name]
		if nat == nil {
			nat = loadedNatives().goLib[name] // reached through std's import go strings
		}
		if _, ab := nat(in, fr, fr.decl, nil, args); ab != nil {
			t.Fatalf("%s: %s", name, ab.msg)
		}
		return in.steps, in.bytes
	}
	short, long := "ab"+strings.Repeat("x", 4)+"ab", "ab"+strings.Repeat("x", 796)+"ab"
	for _, name := range []string{"strings.Slice", "strings.HasPrefix", "strings.HasSuffix", "strings.TrimPrefix", "strings.TrimSuffix", "strings.IndexByte"} {
		s1, b1 := work(name, short)
		s2, b2 := work(name, long)
		if s1 != s2 {
			t.Errorf("%s spends %d steps on %d bytes and %d on %d", name, s1, len(short), s2, len(long))
		}
		if name != "strings.HasPrefix" && name != "strings.HasSuffix" && name != "strings.IndexByte" && (b1 != 0 || b2 != 0) {
			t.Errorf("%s allocates %d and %d value bytes for a view of its text", name, b1, b2)
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
	c := []uint8{'a', 'b', ' ', '\n', 0xff}[g.r.IntN(5)]
	xs := g.texts()
	big := []int64{0, 1, -1, 7, math.MaxInt64, math.MinInt64, 1 << 40, -(1 << 33)}
	d, m := big[g.r.IntN(len(big))], big[g.r.IntN(len(big))]
	base, bits := g.r.IntN(40)-1, g.r.IntN(70)-2
	fmtc := []byte("efgxbEG?")[g.r.IntN(8)]
	f := []float64{0, 1.5, -2.25, 1e21, math.NaN(), math.Inf(-1), 123456.789}[g.r.IntN(7)]
	// cmp.Max and Min's float operands reach Go's special cases: NaN
	// and the two zeros (EVAL-BUILDER).
	edge := []float64{0, math.Copysign(0, -1), math.NaN(), 1.5, -2.25, math.Inf(1)}
	fa, fb := edge[g.r.IntN(len(edge))], edge[g.r.IntN(len(edge))]
	num := []string{"12", "-0", "+7", "9223372036854775807", "9223372036854775808", "-9223372036854775809", "0x1f", "1e3", "1_000", "", "-", "abc", "1.5", "1e400", "true", "F", "\"a\\tb\"", "`x`", "'c'"}[g.r.IntN(19)]
	ints, ints2 := g.ints(), g.ints()
	fl := g.floats()
	x := int64(g.r.IntN(5) - 2)
	key := []string{"apiToken", "server.port", "HTTPStatus", "v2Path", "aÉb", "a\xffB", "", "a..B"}[g.r.IntN(8)] + g.text()
	// hmac's key states: a key of each length class (empty, short, past
	// a block of either size), each hash, and sometimes a state that Key
	// did not give (cut short).
	alg := []int64{1, 256, 512}[g.r.IntN(3)]
	hkey := s + strings.Repeat("k", []int{0, 60, 64, 65, 128, 129, 300}[g.r.IntN(7)])
	hin, hout := v1digest.HmacKeyState(int(alg), hkey)
	if g.r.IntN(8) == 0 {
		hin = hin[:g.r.IntN(len(hin))]
	}
	step := int64(g.r.Uint64())
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
	// Sometimes an instant at or past the years 0 to 9999, which std
	// writes in ISO 8601's expanded year (time.Format), or any int64.
	switch g.r.IntN(4) {
	case 0:
		bound := []int64{-62167219200, 253402300800}[g.r.IntN(2)]
		sec = bound + g.r.Int64N(5) - 2
	case 1:
		sec = int64(g.r.Uint64())
	}
	text := v1.TimeFormat(sec, nsec, layouts[g.r.IntN(len(layouts))])
	if g.r.IntN(4) == 0 {
		text = text[:g.r.IntN(len(text)+1)]
	}
	// SortStableFunc by the remainder mod 3, so equal keys keep their
	// order only when the sort is stable.
	byMod3 := &nativeFunc{name: "byMod3", call: func(in *interp, fr *frame, at syntax.Node, args []Value) (Value, *abrupt) {
		return int64(cmp.Compare(mod3(args[0].(int64)), mod3(args[1].(int64)))), nil
	}}
	return append([]nativeCase{
		{name: "time.format", args: []Value{sec, nsec, lay}, v1: func() any { return v1.TimeFormat(sec, nsec, lay) }},
		{name: "time.parse", args: []Value{lay, text}, v1: func() any { return result(v1.TimeParse(lay, text)) }},
		{name: "config.flagName", args: []Value{key}, v1: func() any { return v1.ConfigFlagName(key) }},
		{name: "config.envName", args: []Value{w, key}, v1: func() any { return v1.ConfigEnvName(w, key) }},
		{name: "config.flagKey", args: []Value{s}, v1: func() any { return v1.ConfigFlagKey(s) }},
		{name: "config.envKey", args: []Value{u}, v1: func() any { return v1.ConfigEnvKey(u) }},
		{name: "strings.Compare", args: []Value{s, u}, v1: func() any { return int64(strings.Compare(s, u)) }},
		{name: "strings.Split", args: []Value{s, u}, v1: func() any { return strings.Split(s, u) }},
		{name: "strings.Join", args: []Value{ls(xs), u}, v1: func() any { return v1.StringsJoin(xs, u) }},
		{name: "strings.HasPrefix", args: []Value{s, u}, v1: func() any { return strings.HasPrefix(s, u) }},
		{name: "strings.HasSuffix", args: []Value{s, u}, v1: func() any { return strings.HasSuffix(s, u) }},
		{name: "strings.TrimSpace", args: []Value{s}, v1: func() any { return strings.TrimSpace(s) }},
		{name: "strings.TrimPrefix", args: []Value{s, u}, v1: func() any { return strings.TrimPrefix(s, u) }},
		{name: "strings.TrimSuffix", args: []Value{s, u}, v1: func() any { return strings.TrimSuffix(s, u) }},
		{name: "strings.Fields", args: []Value{s}, v1: func() any { return strings.Fields(s) }},
		{name: "strings.Repeat", args: []Value{s, int64(i)}, v1: func() any { return strings.Repeat(s, i) }},
		{name: "strings.ToLower", args: []Value{s}, v1: func() any { return strings.ToLower(s) }},
		{name: "strings.ToUpper", args: []Value{s}, v1: func() any { return strings.ToUpper(s) }},
		{name: "strings.Index", args: []Value{s, u}, v1: func() any { return int64(strings.Index(s, u)) }},
		{name: "strings.IndexByte", args: []Value{s, c}, v1: func() any { return int64(strings.IndexByte(s, c)) }},
		{name: "strings.builderAppendBytes", args: []Value{bytesValue(s), bytesValue(u)}, v1: func() any { return v1.StringsBuilderAppendBytes([]byte(s), []byte(u)) }},
		{name: "strings.builderAppendFrom", args: []Value{bytesValue(s), int64(i), int64(j)}, v1: func() any { return v1.StringsBuilderAppendFrom([]byte(s), i, j) }},
		{name: "strings.Contains", args: []Value{s, u}, v1: func() any { return strings.Contains(s, u) }},
		{name: "strings.Slice", args: []Value{s, int64(i), int64(j)}, v1: func() any { return v1.StringsSlice(s, i, j) }},
		{name: "strings.ReplaceAll", args: []Value{s, u, w}, v1: func() any { return strings.ReplaceAll(s, u, w) }},
		{name: "strings.LastIndex", args: []Value{s, u}, v1: func() any { return int64(strings.LastIndex(s, u)) }},
		{name: "strings.Count", args: []Value{s, u}, v1: func() any { return int64(strings.Count(s, u)) }},
		{name: "strings.SplitN", args: []Value{s, u, int64(i)}, v1: func() any { return strings.SplitN(s, u, i) }},
		{name: "strings.EqualFold", args: []Value{s, u}, v1: func() any { return strings.EqualFold(s, u) }},
		{name: "strings.ContainsRune", args: []Value{s, rn}, v1: func() any { return strings.ContainsRune(s, rn) }},
		{name: "strings.ToValidUTF8", args: []Value{s, u}, v1: func() any { return strings.ToValidUTF8(s, u) }},
		{name: "utf8.RuneText", args: []Value{rn}, v1: func() any { return v1.UTF8RuneText(rn) }},
		{name: "unicode/utf8.RuneLen", args: []Value{rn}, v1: func() any { return int64(utf8.RuneLen(rn)) }},
		{name: "utf8.DecodeRuneInString", args: []Value{s}, v1: func() any { return v1.UTF8DecodeRuneInString(s) }},
		{name: "utf8.AppendRune", args: []Value{bytesValue(s), rn}, v1: func() any { return v1.UTF8AppendRune([]byte(s), rn) }},
		{name: "strconv.Itoa", args: []Value{d}, v1: func() any { return strconv.Itoa(int(d)) }},
		{name: "strconv.FormatInt", args: []Value{d, int64(base)}, v1: func() any { return strconv.FormatInt(d, base) }},
		{name: "strconv.FormatBool", args: []Value{i > 4}, v1: func() any { return strconv.FormatBool(i > 4) }},
		{name: "strconv.FormatFloat", args: []Value{f, fmtc, int64(i), int64(64)}, v1: func() any { return strconv.FormatFloat(f, fmtc, i, 64) }},
		{name: "strconv.Quote", args: []Value{s}, v1: func() any { return strconv.Quote(s) }},
		{name: "strconv.Atoi", args: []Value{num}, v1: func() any { return result(v1.StrconvAtoi(num)) }},
		{name: "strconv.ParseInt", args: []Value{num, int64(base), int64(bits)}, v1: func() any { return result(v1.StrconvParseInt(num, base, bits)) }},
		{name: "strconv.ParseFloat", args: []Value{num}, v1: func() any { return result(v1.StrconvParseFloat(num)) }},
		{name: "strconv.Unquote", args: []Value{num}, v1: func() any { return result(v1.StrconvUnquote(num)) }},
		{name: "filepath.Join", args: []Value{ls(xs)}, v1: func() any { return v1.FilepathJoin(xs) }},
		{name: "path/filepath.Base", args: []Value{s}, v1: func() any { return filepath.Base(s) }},
		{name: "path/filepath.Dir", args: []Value{s}, v1: func() any { return filepath.Dir(s) }},
		{name: "filepath.Ext", args: []Value{s}, v1: func() any { return v1.FilepathExt(s) }},
		{name: "path/filepath.Clean", args: []Value{s}, v1: func() any { return filepath.Clean(s) }},
		{name: "path/filepath.IsAbs", args: []Value{s}, v1: func() any { return filepath.IsAbs(s) }},
		{name: "hex.Encode", args: []Value{s}, v1: func() any { return v1digest.HexEncode(s) }},
		{name: "hmac.keyState", args: []Value{alg, hkey}, v1: func() any { a, b := v1digest.HmacKeyState(int(alg), hkey); return tuple{elems: []Value{a, b}} }},
		{name: "totp.keyState", args: []Value{hkey}, v1: func() any {
			a, b := v1digest.HotpKeyState(hkey)
			return tuple{elems: []Value{byteSliceV(a), byteSliceV(b)}}
		}},
		{name: "totp.hotpNumber", args: []Value{byteSliceV([]byte(hin)), byteSliceV([]byte(hout)), step}, anyDefect: true, v1: func() any { return int64(v1digest.HotpNumber([]byte(hin), []byte(hout), step)) }},
		{name: "hmac.keyedSum", args: []Value{alg, hin, hout, u}, anyDefect: true, v1: func() any { return v1digest.HmacKeyedSum(int(alg), hin, hout, u) }},
		{name: "time.DurationText", args: []Value{d}, v1: func() any { return v1.TimeDurationText(durationOf(d)) }},
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
		{name: "cmp.Max", args: []Value{fa, fb}, v1: func() any { return v1.CmpMax(fa, fb) }},
		{name: "cmp.Min", args: []Value{fa, fb}, v1: func() any { return v1.CmpMin(fa, fb) }},
		{name: "cmp.Max", args: []Value{s, u}, v1: func() any { return v1.CmpMax(s, u) }},
		{name: "cmp.Min", args: []Value{d, m}, v1: func() any { return v1.CmpMin(d, m) }},
		{name: "slices.Slice", args: []Value{li(ints), si, sj}, anyDefect: true, v1: func() any { return v1.SlicesSlice(ints, int(si), int(sj)) }},
		{name: "slices.SortStableFunc", args: []Value{li(ints), byMod3}, v1: func() any {
			return v1.SlicesSortStableFunc(ints, func(a, b int64) int { return cmp.Compare(mod3(a), mod3(b)) })
		}},
		{name: "slices.SortFunc", args: []Value{li(ints), byMod3}, v1: func() any {
			return v1.SlicesSortFunc(ints, func(a, b int64) int { return cmp.Compare(mod3(a), mod3(b)) })
		}},
		{name: "slices.Repeat", args: []Value{li(ints), int64(i)}, anyDefect: true, v1: func() any { return v1.SlicesRepeat(ints, i) }},
		{name: "machines.notPositive", args: []Value{d, m}, v1: func() any { v1.MachineTimerNotPositiveAt(durationOf(d), durationOf(m)); return nil }},
	}, pureCases(g, s, u, d, m, f, ints, ints2, x, sec, nsec, lay)...)
}

// queryFields is a []http.Field of the names and values, built with the
// http package's record type (TestNativesAgreeWithRuntime sets it).
var queryFields func(names, values []string) Value

// pureCases are the rows of the pure externs ST4 gave natives
// (nativespure.go), on the generator's values and their own: hex and
// base64 texts sometimes cut or spoiled, ISO durations, conversions at
// each width's edges.
func pureCases(g nativeGen, s, u string, d, m int64, f float64, ints, ints2 []int64, x, sec, nsec int64, lay string) []nativeCase {
	ua, ub, uc := uint64(d), uint64(m), uint64(g.r.Int64N(3))
	if g.r.IntN(3) == 0 {
		ub = ua // Div64 of hi == y overflows
	}
	edge := []float64{0, math.Copysign(0, -1), -0.5, -1, 127.9, 128, -128.9, -129, 255.5, 256, 32767.5, 65536, 2147483647.5, 4294967296, 0x1p63, -0x1p63, 0x1p64, 1e300, math.NaN(), math.Inf(1), math.Inf(-1)}[g.r.IntN(21)]
	hexText := hex.EncodeToString([]byte(s))
	b64 := base64.StdEncoding.EncodeToString([]byte(s))
	b64url := base64.URLEncoding.EncodeToString([]byte(u))
	spoil := func(t string) string {
		switch g.r.IntN(4) {
		case 0:
			return t[:g.r.IntN(len(t)+1)]
		case 1:
			if t != "" {
				k := g.r.IntN(len(t))
				return t[:k] + []string{"g", "=", "-", "+", "/", "_", " "}[g.r.IntN(7)] + t[k+1:]
			}
		}
		return t
	}
	hexText, b64, b64url = spoil(hexText), spoil(b64), spoil(b64url)
	iso := []string{"PT1H30M", "-PT0.5S", "PT2562047H47M16.854775807S", "-PT2562047H47M16.854775808S", "PT2562047H47M16.854775808S", "PT0S", "P1D", "pt1h", "PT", "PT1.5H", "PT1M1H", "PT1H1H", "+PT2,25S", "PT1.S", "PT1.1234567891S", "PT99999999999999999999H", "PT9223372036854775808S", "PT1", "", "-", "PT1H2M3S"}[g.r.IntN(21)]
	base := int64(g.r.IntN(40) - 1)
	fmtc := []byte("efgxbEG?")[g.r.IntN(8)]
	html := g.r.IntN(2) == 0
	hs := s + []string{"", "<a>", "&", "\u2028"}[g.r.IntN(4)] // the bytes html escapes
	dirs := []string{"/a/b", "a/b", "/", "", "../x", "/a/../c", "a"}
	rb, rt := dirs[g.r.IntN(len(dirs))], dirs[g.r.IntN(len(dirs))]+"/"+s
	gs := func(xs []int64) []string {
		out := make([]string, len(xs))
		for k, v := range xs {
			out[k] = strconv.FormatInt(v, 10) + s
		}
		return out
	}
	names, values := gs(ints), gs(ints)
	fields := make([]v1http.HTTPField, len(names))
	for k := range names {
		fields[k] = v1http.HTTPField{Name: names[k], Value: values[k]}
	}
	tuple2 := func(a, b uint64) any { return tuple{elems: []Value{a, b}} }
	fx, fy := []float64{0, math.Copysign(0, -1), 1, math.NaN(), -2}[g.r.IntN(5)], []float64{0, math.Copysign(0, -1), 1, math.NaN(), -2}[g.r.IntN(5)]
	bi := int64(g.r.IntN(len(ints)+3) - 1)
	bj := int64(g.r.IntN(len(ints)+3) - 1)
	crc := uint32(ua)
	cs := []nativeCase{
		{name: "bits.Add64", args: []Value{ua, ub, uc}, v1: func() any { return tuple2(v1.BitsAdd64(ua, ub, uc)) }},
		{name: "bits.Sub64", args: []Value{ua, ub, uc}, v1: func() any { return tuple2(v1.BitsSub64(ua, ub, uc)) }},
		{name: "bits.Mul64", args: []Value{ua, ub}, v1: func() any { return tuple2(v1.BitsMul64(ua, ub)) }},
		{name: "bits.Div64", args: []Value{uc, ua, ub}, anyDefect: true, v1: func() any { return tuple2(v1.BitsDiv64(uc, ua, ub)) }},
		{name: "cmp.Max", args: []Value{d, m}, v1: func() any { return v1.CmpMax(d, m) }},
		{name: "cmp.Min", args: []Value{d, m}, v1: func() any { return v1.CmpMin(d, m) }},
		{name: "cmp.Max", args: []Value{fx, fy}, v1: func() any { return v1.CmpMax(fx, fy) }},
		{name: "cmp.Min", args: []Value{fx, fy}, v1: func() any { return v1.CmpMin(fx, fy) }},
		{name: "cmp.Max", args: []Value{s, u}, v1: func() any { return v1.CmpMax(s, u) }},
		{name: "cmp.Min", args: []Value{s, u}, v1: func() any { return v1.CmpMin(s, u) }},
		{name: "cmp.Min", args: []Value{uint8(ua), uint8(ub)}, v1: func() any { return v1.CmpMin(uint8(ua), uint8(ub)) }},
		{name: "math.ToInt8", args: []Value{edge}, v1: func() any { return result(v1.MathToInt8(edge)) }},
		{name: "math.ToInt16", args: []Value{edge}, v1: func() any { return result(v1.MathToInt16(edge)) }},
		{name: "math.ToInt32", args: []Value{edge}, v1: func() any { return result(v1.MathToInt32(edge)) }},
		{name: "math.ToUint8", args: []Value{edge}, v1: func() any { return result(v1.MathToUint8(edge)) }},
		{name: "math.ToUint16", args: []Value{edge}, v1: func() any { return result(v1.MathToUint16(edge)) }},
		{name: "math.ToUint32", args: []Value{edge}, v1: func() any { return result(v1.MathToUint32(edge)) }},
		{name: "sha1.Sum", args: []Value{s}, v1: func() any { return v1digest.Sha1Sum(s) }},
		{name: "sha512.Sum", args: []Value{s}, v1: func() any { return v1digest.Sha512Sum(s) }},
		{name: "hmac.sum1", args: []Value{s, u}, v1: func() any { return v1digest.HmacSHA1(s, u) }},
		{name: "hmac.sum256", args: []Value{s, u}, v1: func() any { return v1digest.HmacSHA256(s, u) }},
		{name: "hmac.sum512", args: []Value{s, u}, v1: func() any { return v1digest.HmacSHA512(s, u) }},
		{name: "hmac.Equal", args: []Value{s, u}, v1: func() any { return v1digest.HmacEqual(s, u) }},
		{name: "hmac.Equal", args: []Value{s, s}, v1: func() any { return v1digest.HmacEqual(s, s) }},
		{name: "hex.Decode", args: []Value{hexText}, v1: func() any { return result(v1digest.HexDecode(hexText)) }},
		{name: "hex.Decode", args: []Value{s}, v1: func() any { return result(v1digest.HexDecode(s)) }},
		{name: "base64.decodeStd", args: []Value{b64}, v1: func() any { return result(v1digest.Base64StdDecode(b64)) }},
		{name: "base64.decodeURL", args: []Value{b64url}, v1: func() any { return result(v1digest.Base64URLDecode(b64url)) }},
		{name: "base64.decodeURL", args: []Value{b64}, v1: func() any { return result(v1digest.Base64URLDecode(b64)) }},
		{name: "filepath.Rel", args: []Value{rb, rt}, v1: func() any { return result(v1.FilepathRel(rb, rt)) }},
		{name: "path/filepath.Split", args: []Value{rt}, v1: func() any { a, b := filepath.Split(rt); return tuple{elems: []Value{a, b}} }},
		{name: "fs.Clone", args: []Value{s}, v1: func() any { return v1.FSClone(s) }},
		{name: "utf8.String", args: []Value{bytesValue(s)}, v1: func() any { return v1.UTF8String([]byte(s)) }},
		{name: "slices.Insert", args: []Value{li64(ints), bi, li64(ints2)}, anyDefect: true, v1: func() any { return v1.SlicesInsert(ints, int(bi), ints2) }},
		{name: "slices.Delete", args: []Value{li64(ints), bi, bj}, anyDefect: true, v1: func() any { return v1.SlicesDelete(ints, int(bi), int(bj)) }},
		{name: "strconv.AppendInt", args: []Value{bytesValue(s), d, base}, anyDefect: true, v1: func() any { return v1.StrconvAppendInt([]byte(s), d, int(base)) }},
		{name: "strconv.AppendUint", args: []Value{bytesValue(s), ua, base}, anyDefect: true, v1: func() any { return v1.StrconvAppendUint([]byte(s), ua, int(base)) }},
		{name: "strconv.AppendFloat", args: []Value{bytesValue(s), f, fmtc, x}, v1: func() any { return v1.StrconvAppendFloat([]byte(s), f, fmtc, int(x)) }},
		{name: "json.appendQuotedBytes", args: []Value{bytesValue(u), bytesValue(hs), html}, v1: func() any { return v1.JSONAppendQuotedBytes([]byte(u), []byte(hs), html) }},
		{name: "time.appendUTC", args: []Value{bytesValue(u), sec, nsec, lay}, v1: func() any { return v1.TimeAppendUTC([]byte(u), sec, nsec, lay) }},
		{name: "time.truncate", args: []Value{sec, nsec, m}, v1: func() any { return v1.TimeTruncate(sec, nsec, durationOf(m)) }},
		{name: "time.round", args: []Value{sec, nsec, m}, v1: func() any { return v1.TimeRound(sec, nsec, durationOf(m)) }},
		{name: "time.DurationISO", args: []Value{d}, v1: func() any { return v1.TimeDurationISO(durationOf(d)) }},
		{name: "time.DurationISO", args: []Value{nsec * int64(x+3)}, v1: func() any { return v1.TimeDurationISO(durationOf(nsec * int64(x+3))) }},
		{name: "time.ParseDurationISO", args: []Value{iso}, v1: func() any { return result(v1.TimeParseDurationISO(iso)) }},
		{name: "hash/crc32.Update", args: []Value{crc, goVars()["hash/crc32.IEEETable"], bytesValue(s)}, v1: func() any { return v1digest.Crc32Update(crc, s) }},
		{name: "encoding/base64.Encoding.EncodeToString", args: []Value{goVars()["encoding/base64.StdEncoding"], bytesValue(s)}, v1: func() any { return v1digest.Base64StdEncode(s) }},
		{name: "encoding/base64.Encoding.EncodeToString", args: []Value{goVars()["encoding/base64.URLEncoding"], bytesValue(u)}, v1: func() any { return v1digest.Base64URLEncode(u) }},
	}
	return cs
}

// li64 is a []int64 as the evaluator's slice.
func li64(ns []int64) Value {
	vs := make([]Value, len(ns))
	for k, v := range ns {
		vs[k] = v
	}
	return newSlice(vs)
}

func durationOf(d int64) time.Duration { return time.Duration(d) }

func mod3(n int64) int64 { return (n%3 + 3) % 3 }

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
	case v1.MathOutOfRange:
		return "fails " + describe(stdRecord(fn, "OutOfRange", map[string]Value{"Value": e.Value}))
	case v1.NotRelative:
		return "fails " + describe(stdRecord(fn, "NotRelative", map[string]Value{"Base": e.Base, "Target": e.Target}))
	case v1digest.HexMalformed:
		return "fails " + describe(stdRecord(fn, "Malformed", map[string]Value{"Offset": int64(e.Offset)}))
	case v1digest.Base64Malformed:
		return "fails " + describe(stdRecord(fn, "Malformed", map[string]Value{"Offset": int64(e.Offset)}))
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
	case uint:
		return uint64(v)
	case uintptr:
		return uint64(v)
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
