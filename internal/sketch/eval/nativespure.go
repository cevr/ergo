package eval

import (
	"crypto/hmac"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"hash"
	"hash/crc32"
	"math"
	"math/bits"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// The natives of the std externs marked pure that had none (STD-SURVEY
// ST4; PKG G1, G2): each is written from its std doc comment with Go's
// standard library, never by calling runtime/v1 (natives.go, ORACLE O1),
// and TestNativesAgreeWithRuntime holds each to the runtime/v1 body on
// generated inputs. TestEveryPureStdExternHasANative lists any pure
// extern, or pure Go function std reaches through import go, that has
// none: a law over it would stop at ergo check as a Go function.
func registerNativespure(t nativeTables) {
	natives := t.std
	goNatives := t.goLib
	// pureFail registers a native of its arguments that gives a value or
	// a failure record: it pays a step per byte or element it reads and
	// the bytes it builds, and a Go panic is a defect with Go's text.
	pureFail := func(into map[string]native, name string, f func(fn *check.Func, a []Value) (v Value, fail Value)) {
		into[name] = func(in *interp, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
			if ab := in.charge(fr, at, 1+argWork(args)); ab != nil {
				return nil, ab
			}
			var fail Value
			out, why := guard(func() Value {
				v, f := f(fn, args)
				fail = f
				return v
			})
			if why != "" {
				return nil, fr.defect(at, "%s", why)
			}
			if fail != nil {
				return nil, fr.abrupt(ctlFail, at, fail, "")
			}
			if ab := in.allocate(fr, at, valueSize(out)); ab != nil {
				return nil, ab
			}
			return out, nil
		}
	}
	pure := func(name string, f func(a []Value) Value) {
		pureFail(natives, name, func(_ *check.Func, a []Value) (Value, Value) { return f(a), nil })
	}
	str := func(v Value) string { return v.(string) }
	u64 := func(v Value) uint64 { return v.(uint64) }
	pair := func(a, b Value) Value { return tuple{elems: []Value{a, b}} }

	// bits: Go's math/bits, each a sum, difference, product or quotient
	// and its carry, borrow, high word or remainder.
	t.reads["bits.Add64"] = "uint64 uint64 uint64"
	pure("bits.Add64", func(a []Value) Value {
		s, c := bits.Add64(u64(a[0]), u64(a[1]), u64(a[2]))
		return pair(s, c)
	})
	t.reads["bits.Sub64"] = "uint64 uint64 uint64"
	pure("bits.Sub64", func(a []Value) Value {
		d, b := bits.Sub64(u64(a[0]), u64(a[1]), u64(a[2]))
		return pair(d, b)
	})
	t.reads["bits.Mul64"] = "uint64 uint64"
	pure("bits.Mul64", func(a []Value) Value {
		hi, lo := bits.Mul64(u64(a[0]), u64(a[1]))
		return pair(hi, lo)
	})
	t.reads["bits.Div64"] = "uint64 uint64 uint64"
	pure("bits.Div64", func(a []Value) Value {
		q, r := bits.Div64(u64(a[0]), u64(a[1]), u64(a[2]))
		return pair(q, r)
	})

	// cmp: Go's builtin max and min over one ordered basic type; a NaN
	// gives NaN, max(-0, +0) is +0 and min(-0, +0) is -0.
	t.reads["cmp.Max"] = "T T"
	t.reads["cmp.Min"] = "T T"
	pure("cmp.Max", func(a []Value) Value { return ordered2(a[0], a[1], true) })
	pure("cmp.Min", func(a []Value) Value { return ordered2(a[0], a[1], false) })

	// math: the checked conversions, f toward zero when the result fits
	// the type, OutOfRange{f} for NaN, an infinity or a value out of
	// range. The fitting truncations are the half-open interval
	// [-2^(n-1), 2^(n-1)) for n signed bits and [0, 2^n) for n unsigned,
	// each bound a power of two a float64 holds exactly.
	conv := func(name string, n int, signed bool, of func(t float64) Value) {
		t.reads["math."+name] = "float64"
		lo, hi := 0.0, math.Ldexp(1, n)
		if signed {
			lo, hi = -math.Ldexp(1, n-1), math.Ldexp(1, n-1)
		}
		pureFail(natives, "math."+name, func(fn *check.Func, a []Value) (Value, Value) {
			f := a[0].(float64)
			if t := math.Trunc(f); t >= lo && t < hi {
				return of(t), nil
			}
			return nil, stdRecord(fn, "OutOfRange", map[string]Value{"Value": f})
		})
	}
	conv("ToInt", 64, true, func(t float64) Value { return int64(t) })
	conv("ToInt8", 8, true, func(t float64) Value { return int8(t) })
	conv("ToInt16", 16, true, func(t float64) Value { return int16(t) })
	conv("ToInt32", 32, true, func(t float64) Value { return int32(t) })
	conv("ToInt64", 64, true, func(t float64) Value { return int64(t) })
	conv("ToUint", 64, false, func(t float64) Value { return uint64(t) })
	conv("ToUint8", 8, false, func(t float64) Value { return uint8(t) })
	conv("ToUint16", 16, false, func(t float64) Value { return uint16(t) })
	conv("ToUint32", 32, false, func(t float64) Value { return uint32(t) })
	conv("ToUint64", 64, false, func(t float64) Value { return uint64(t) })
	conv("ToUintptr", 64, false, func(t float64) Value { return uint64(t) })

	// Digests: Go's crypto over the bytes of the texts, each digest the
	// text of its bytes.
	sum := func(h func() hash.Hash, s string) string {
		d := h()
		d.Write([]byte(s))
		return string(d.Sum(nil))
	}
	t.reads["sha1.Sum"] = "string"
	pure("sha1.Sum", func(a []Value) Value { return sum(sha1.New, str(a[0])) })
	t.reads["sha512.Sum"] = "string"
	pure("sha512.Sum", func(a []Value) Value { return sum(sha512.New, str(a[0])) })
	keyed := func(h func() hash.Hash) func(a []Value) Value {
		return func(a []Value) Value {
			m := hmac.New(h, []byte(str(a[0])))
			m.Write([]byte(str(a[1])))
			return string(m.Sum(nil))
		}
	}
	t.reads["hmac.sum1"] = "string string"
	pure("hmac.sum1", keyed(sha1.New))
	t.reads["hmac.sum256"] = "string string"
	pure("hmac.sum256", keyed(sha256.New))
	t.reads["hmac.sum512"] = "string string"
	pure("hmac.sum512", keyed(sha512.New))
	t.reads["hmac.Equal"] = "string string"
	pure("hmac.Equal", func(a []Value) Value { return hmac.Equal([]byte(str(a[0])), []byte(str(a[1]))) })

	// hex.Decode: the bytes two hex digits each give, or Malformed at the
	// first byte that is no hex digit, else at len(s) when the length is
	// odd.
	t.reads["hex.Decode"] = "string"
	pureFail(natives, "hex.Decode", func(fn *check.Func, a []Value) (Value, Value) {
		s := str(a[0])
		digit := func(c byte) (byte, bool) {
			switch {
			case '0' <= c && c <= '9':
				return c - '0', true
			case 'a' <= c && c <= 'f':
				return c - 'a' + 10, true
			case 'A' <= c && c <= 'F':
				return c - 'A' + 10, true
			}
			return 0, false
		}
		for i := 0; i < len(s); i++ {
			if _, ok := digit(s[i]); !ok {
				return nil, stdRecord(fn, "Malformed", map[string]Value{"Offset": int64(i)})
			}
		}
		if len(s)%2 == 1 {
			return nil, stdRecord(fn, "Malformed", map[string]Value{"Offset": int64(len(s))})
		}
		out := make([]byte, len(s)/2)
		for i := range out {
			hi, _ := digit(s[2*i])
			lo, _ := digit(s[2*i+1])
			out[i] = hi<<4 | lo
		}
		return string(out), nil
	})

	// base64: Go's padded standard and URL alphabets; a failure is
	// Malformed at Go's CorruptInputError offset.
	decode64 := func(enc *base64.Encoding) func(fn *check.Func, a []Value) (Value, Value) {
		return func(fn *check.Func, a []Value) (Value, Value) {
			b, err := enc.DecodeString(str(a[0]))
			if err != nil {
				off, _ := err.(base64.CorruptInputError)
				return nil, stdRecord(fn, "Malformed", map[string]Value{"Offset": int64(off)})
			}
			return string(b), nil
		}
	}
	t.reads["base64.decodeStd"] = "string"
	pureFail(natives, "base64.decodeStd", decode64(base64.StdEncoding))
	t.reads["base64.decodeURL"] = "string"
	pureFail(natives, "base64.decodeURL", decode64(base64.URLEncoding))

	// filepath.Rel is Go's filepath.Rel; any error is NotRelative{base,
	// target}. Split is Go's, which std reaches through import go.
	t.reads["filepath.Rel"] = "string string"
	pureFail(natives, "filepath.Rel", func(fn *check.Func, a []Value) (Value, Value) {
		rel, err := filepath.Rel(str(a[0]), str(a[1]))
		if err != nil {
			return nil, stdRecord(fn, "NotRelative", map[string]Value{"Base": a[0], "Target": a[1]})
		}
		return rel, nil
	})
	t.reads["path/filepath.Split"] = "string"
	pureFail(goNatives, "path/filepath.Split", func(_ *check.Func, a []Value) (Value, Value) {
		dir, file := filepath.Split(str(a[0]))
		return pair(dir, file), nil
	})

	// http.EncodeQuery: each field name=value in its order, joined by
	// "&", each part escaped as Go's url.QueryEscape escapes it.
	t.reads["http.EncodeQuery"] = "sliceV"
	pure("http.EncodeQuery", func(a []Value) Value {
		var b strings.Builder
		for i, e := range a[0].(sliceV).elems() {
			r := e.(*record)
			if i > 0 {
				b.WriteByte('&')
			}
			b.WriteString(url.QueryEscape(r.fields[fieldIndex(r.t, "Name")].(string)))
			b.WriteByte('=')
			b.WriteString(url.QueryEscape(r.fields[fieldIndex(r.t, "Value")].(string)))
		}
		return b.String()
	})

	// fs: a Mapped in memory, whose Text is its text, and Clone, a copy
	// of a text (the evaluator's texts are values, so the copy is the
	// text).
	t.reads["fs.Clone"] = "string"
	pure("fs.Clone", func(a []Value) Value { return str(a[0]) })
	t.reads["fs.MapText"] = "string"
	natives["fs.MapText"] = func(in *interp, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
		if ab := in.build(fr, at, 1, 1, 0); ab != nil {
			return nil, ab
		}
		return &mappedV{text: args[0].(string)}, nil
	}
	t.reads["utf8.String"] = "sliceV"
	pure("utf8.String", func(a []Value) Value { return mustBytes(a[0]) })

	// slices: Insert is xs with ys before index i, Delete xs without
	// xs[i:j], each in a new array; a bound out of range is Go's panic.
	t.reads["slices.Insert"] = "sliceV int64 sliceV"
	pure("slices.Insert", func(a []Value) Value {
		xs, i, ys := a[0].(sliceV).elems(), a[1].(int64), a[2].(sliceV).elems()
		if i < 0 || i > int64(len(xs)) {
			panic("runtime error: slice bounds out of range [" + strconv.FormatInt(i, 10) + ":" + strconv.Itoa(len(xs)) + "]")
		}
		out := make([]Value, 0, len(xs)+len(ys))
		out = append(append(append(out, xs[:i]...), ys...), xs[i:]...)
		return newSlice(out)
	})
	t.reads["slices.Delete"] = "sliceV int64 int64"
	pure("slices.Delete", func(a []Value) Value {
		xs, i, j := a[0].(sliceV).elems(), a[1].(int64), a[2].(int64)
		if i < 0 || j < i || j > int64(len(xs)) {
			panic("runtime error: slice bounds out of range [" + strconv.FormatInt(i, 10) + ":" + strconv.FormatInt(j, 10) + "] with length " + strconv.Itoa(len(xs)))
		}
		out := make([]Value, 0, int64(len(xs))-(j-i))
		out = append(append(out, xs[:i]...), xs[j:]...)
		return newSlice(out)
	})

	// The appenders: the buffer, args[0], with the text at its end, a
	// byte at a time as Go's append grows it.
	appender := func(name string, text func(a []Value) string) {
		natives[name] = func(in *interp, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
			out, ok := args[0].(sliceV)
			if !ok {
				return nil, fr.unsupported(at, "%s on bytes the evaluator does not hold", fn.Name)
			}
			if ab := in.charge(fr, at, 1+argWork(args[1:])); ab != nil {
				return nil, ab
			}
			t, why := guard(func() Value { return text(args[1:]) })
			if why != "" {
				return nil, fr.defect(at, "%s", why)
			}
			var v Value = out
			for _, c := range []byte(t.(string)) {
				var ab *abrupt
				if v, ab = in.appendTo(fr, at, v.(sliceV), c, nil); ab != nil {
					return nil, ab
				}
			}
			return v, nil
		}
	}
	t.reads["strconv.AppendInt"] = "sliceV int64 int64"
	appender("strconv.AppendInt", func(a []Value) string { return strconv.FormatInt(a[0].(int64), int(a[1].(int64))) })
	t.reads["strconv.AppendUint"] = "sliceV uint64 int64"
	appender("strconv.AppendUint", func(a []Value) string { return strconv.FormatUint(u64(a[0]), int(a[1].(int64))) })
	t.reads["strconv.AppendFloat"] = "sliceV float64 uint8 int64"
	appender("strconv.AppendFloat", func(a []Value) string {
		return strconv.FormatFloat(a[0].(float64), a[1].(uint8), int(a[2].(int64)), 64)
	})
	t.reads["json.appendQuotedBytes"] = "sliceV sliceV bool"
	appender("json.appendQuotedBytes", func(a []Value) string { return jsonQuote(mustBytes(a[0]), a[1].(bool)) })
	t.reads["time.appendUTC"] = "sliceV int64 int64 string"
	appender("time.appendUTC", func(a []Value) string {
		return time.Unix(a[0].(int64), a[1].(int64)).UTC().Format(str(a[2]))
	})

	// time: Truncate and Round of the instant (sec, nsec) since the zero
	// time, as a Reading with no monotonic part; the ISO 8601 durations
	// of time.
	reading := func(op func(t time.Time, d time.Duration) time.Time) func(fn *check.Func, a []Value) (Value, Value) {
		return func(fn *check.Func, a []Value) (Value, Value) {
			t := op(time.Unix(a[0].(int64), a[1].(int64)), time.Duration(a[2].(int64)))
			return stdRecord(fn, "Reading", map[string]Value{"Sec": t.Unix(), "Nsec": int64(t.Nanosecond()), "Mono": int64(0)}), nil
		}
	}
	t.reads["time.truncate"] = "int64 int64 int64"
	pureFail(natives, "time.truncate", reading(time.Time.Truncate))
	t.reads["time.round"] = "int64 int64 int64"
	pureFail(natives, "time.round", reading(time.Time.Round))
	t.reads["time.DurationISO"] = "int64"
	pure("time.DurationISO", func(a []Value) Value { return durationISO(a[0].(int64)) })
	t.reads["time.ParseDurationISO"] = "string"
	pureFail(natives, "time.ParseDurationISO", func(fn *check.Func, a []Value) (Value, Value) {
		d, msg := parseDurationISO(str(a[0]))
		if msg != "" {
			return nil, stdRecord(fn, "ParseError", map[string]Value{"Layout": "ISO 8601 duration", "Value": a[0], "Message": msg})
		}
		return d, nil
	})

	// Go's functions over the read-only Go variables std passes (goVars):
	// crc32.Update with IEEETable, and EncodeToString of base64's
	// encodings. Any other table or encoding is a value the evaluator
	// does not hold.
	goValues := map[goVar]any{
		"hash/crc32.IEEETable":           crc32.IEEETable,
		"encoding/base64.StdEncoding":    base64.StdEncoding,
		"encoding/base64.URLEncoding":    base64.URLEncoding,
		"encoding/base64.RawStdEncoding": base64.RawStdEncoding,
		"encoding/base64.RawURLEncoding": base64.RawURLEncoding,
	}
	overGo := func(name string, at int, run func(v any, a []Value) Value) {
		inner := func(_ *check.Func, a []Value) (Value, Value) { return run(goValues[a[at].(goVar)], a), nil }
		pureFail(goNatives, name, inner)
		wrapped := goNatives[name]
		goNatives[name] = func(in *interp, fr *frame, at2 syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
			if v, ok := args[at].(goVar); !ok || goValues[v] == nil {
				return nil, fr.unsupported(at2, "%s of a Go value the evaluator does not hold", fn.Name)
			}
			return wrapped(in, fr, at2, fn, args)
		}
	}
	t.reads["hash/crc32.Update"] = "uint32 mut:sliceV sliceV"
	overGo("hash/crc32.Update", 1, func(v any, a []Value) Value {
		return crc32.Update(a[0].(uint32), v.(*crc32.Table), []byte(mustBytes(a[2])))
	})
	t.reads["encoding/base64.Encoding.EncodeToString"] = "sliceV"
	overGo("encoding/base64.Encoding.EncodeToString", 0, func(v any, a []Value) Value {
		return v.(*base64.Encoding).EncodeToString([]byte(mustBytes(a[1])))
	})
}

// goVar is a read-only Go package variable the evaluator holds by name
// (runtime/v1 GoReadOnlyVars): only a native of a Go function reads it.
type goVar string

// goVars are the read-only Go variables a std body passes to a Go
// function that has a native (nativespure.go, overGo).
var goVars = sync.OnceValue(func() map[string]Value {
	return map[string]Value{
		"hash/crc32.IEEETable":           goVar("hash/crc32.IEEETable"),
		"encoding/base64.StdEncoding":    goVar("encoding/base64.StdEncoding"),
		"encoding/base64.URLEncoding":    goVar("encoding/base64.URLEncoding"),
		"encoding/base64.RawStdEncoding": goVar("encoding/base64.RawStdEncoding"),
		"encoding/base64.RawURLEncoding": goVar("encoding/base64.RawURLEncoding"),
	}
})

// mustBytes is the text of a []byte the evaluator holds as uint8 values;
// any other slice panics, a defect of the native's guard.
func mustBytes(v Value) string {
	s, ok := byteString(v.(sliceV))
	if !ok {
		panic("bytes the evaluator does not hold")
	}
	return s
}

// ordered2 is Go's max(a, b) (larger) or min(a, b) of two values of one
// ordered basic type.
func ordered2(a, b Value, larger bool) Value {
	pick := func(x, y Value, less bool) Value {
		if less == larger {
			return y
		}
		return x
	}
	switch x := a.(type) {
	case float64:
		if larger {
			return max(x, b.(float64))
		}
		return min(x, b.(float64))
	case float32:
		if larger {
			return max(x, b.(float32))
		}
		return min(x, b.(float32))
	case int64:
		return pick(a, b, x < b.(int64))
	case int32:
		return pick(a, b, x < b.(int32))
	case int16:
		return pick(a, b, x < b.(int16))
	case int8:
		return pick(a, b, x < b.(int8))
	case uint64:
		return pick(a, b, x < b.(uint64))
	case uint32:
		return pick(a, b, x < b.(uint32))
	case uint16:
		return pick(a, b, x < b.(uint16))
	case uint8:
		return pick(a, b, x < b.(uint8))
	case string:
		return pick(a, b, x < b.(string))
	}
	panic("cmp of a value that is not ordered")
}

// durationISO is d as ISO 8601's duration of time: a minus for a
// negative d, PT, then the hours, minutes and seconds that are not zero,
// the seconds with up to nine fraction digits less their trailing zeros;
// zero is PT0S. Hours are never folded into days.
func durationISO(d int64) string {
	if d == 0 {
		return "PT0S"
	}
	var b strings.Builder
	n := uint64(d)
	if d < 0 {
		b.WriteByte('-')
		n = -n
	}
	b.WriteString("PT")
	const sec = uint64(time.Second)
	h, rest := n/(3600*sec), n%(3600*sec)
	m, rest := rest/(60*sec), rest%(60*sec)
	s, ns := rest/sec, rest%sec
	if h > 0 {
		b.WriteString(strconv.FormatUint(h, 10) + "H")
	}
	if m > 0 {
		b.WriteString(strconv.FormatUint(m, 10) + "M")
	}
	if s > 0 || ns > 0 {
		b.WriteString(strconv.FormatUint(s, 10))
		if ns > 0 {
			b.WriteString("." + strings.TrimRight(strconv.FormatUint(ns+sec, 10)[1:], "0"))
		}
		b.WriteByte('S')
	}
	return b.String()
}

// parseDurationISO reads ISO 8601's duration of time: an optional sign,
// PT, then one or more parts, hours, minutes and seconds in that order,
// each once, each digits and its letter; only the seconds take a fraction
// (. or , and one to nine digits). It gives the duration, or the message
// of the first rule the text breaks, read left to right: a number whose
// next digit would pass the range, a date part, lower case, an empty part
// or a total past the range.
func parseDurationISO(s string) (int64, string) {
	i, neg := 0, false
	if i < len(s) && (s[i] == '+' || s[i] == '-') {
		neg = s[i] == '-'
		i++
	}
	if !strings.HasPrefix(s[i:], "P") {
		return 0, "want P"
	}
	if !strings.HasPrefix(s[i+1:], "T") {
		return 0, "want T: a duration of years, months, weeks or days has no fixed length; civil.ParsePeriod reads it"
	}
	i += 2
	if i == len(s) {
		return 0, "want a number after PT"
	}
	limit := uint64(math.MaxInt64)
	if neg {
		limit = 1 << 63
	}
	var total uint64
	next := "HMS"
	digits := func() (v uint64, k int, over bool) {
		for i < len(s) && '0' <= s[i] && s[i] <= '9' {
			if v > limit/10 {
				return v, k, true
			}
			v = v*10 + uint64(s[i]-'0')
			i++
			k++
		}
		return v, k, false
	}
	for i < len(s) {
		v, k, over := digits()
		if over {
			return 0, "out of range"
		}
		if k == 0 {
			return 0, "want a digit"
		}
		var frac uint64
		fraction := i < len(s) && (s[i] == '.' || s[i] == ',')
		if fraction {
			i++
			start := i
			for i < len(s) && '0' <= s[i] && s[i] <= '9' {
				if i-start == 9 {
					return 0, "a fraction has at most nine digits"
				}
				frac = frac*10 + uint64(s[i]-'0')
				i++
			}
			if i == start {
				return 0, "want a digit after the point"
			}
			for n := i - start; n < 9; n++ {
				frac *= 10
			}
		}
		if i == len(s) {
			return 0, "want H, M or S"
		}
		at := strings.IndexByte(next, s[i])
		if at < 0 {
			return 0, "want H, M or S, in that order, each once"
		}
		next = next[at+1:]
		if fraction && s[i] != 'S' {
			return 0, "only seconds take a fraction"
		}
		unit := map[byte]uint64{'H': uint64(time.Hour), 'M': uint64(time.Minute), 'S': uint64(time.Second)}[s[i]]
		hi, part := bits.Mul64(v, unit)
		if hi != 0 || part > limit-total {
			return 0, "out of range"
		}
		total += part
		if frac > limit-total {
			return 0, "out of range"
		}
		total += frac
		i++
	}
	if neg {
		return int64(-total), ""
	}
	return int64(total), ""
}

// mappedV is an fs.Mapped held in memory (fs.MapText): Text is its text,
// and Close releases nothing.
type mappedV struct{ text string }

func (m *mappedV) describe() string { return "an fs.Mapped" }

func (m *mappedV) opCall(in *interp, fr *frame, at syntax.Node, name string, args []Value) (Value, *abrupt) {
	if ab := in.charge(fr, at, 1); ab != nil {
		return nil, ab
	}
	switch name {
	case "Text":
		return m.text, nil
	case "Close":
		return unit{}, nil
	}
	ice("the fs.Mapped operation %s", name)
	return nil, nil
}
