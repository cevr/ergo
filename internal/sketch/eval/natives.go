package eval

import (
	"cmp"
	"encoding/hex"
	"fmt"
	"math"
	"math/bits"
	"math/rand/v2"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// Natives are the std functions with no Ergo body (`extern go`) that the
// evaluator runs with its own Go code, keyed by the std package path and
// the Ergo name. They are written from the std's spec text (each extern's
// doc comment: "strings.Split is Go's strings.Split", "Seeded(seed) is
// rand.NewPCG(seed, RandSeedHi)"), with Go's standard library, and never
// call runtime/v1: the evaluator is the reference the built program is
// held to, so it shares no body with it (ORACLE O1). TestNativesAgree
// holds each one to runtime/v1's on generated inputs. Any other extern,
// and every host extern, is skipped where it is called.
//
// A native pays a step per byte or element it reads and the bytes it
// builds, before it runs. A Go panic of the function (strings.Repeat of a
// negative count) is a defect with Go's text, as the built program
// panics.
type native func(in *interp, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt)

var natives map[string]native

func init() {
	natives = map[string]native{}
	str := func(v Value) string { return v.(string) }
	i64 := func(v Value) int { return int(v.(int64)) }
	strs := func(v Value) []string {
		s := v.(sliceV)
		out := make([]string, s.n)
		for i, e := range s.elems() {
			out[i] = e.(string)
		}
		return out
	}
	list := func(xs []string) Value {
		vs := make([]Value, len(xs))
		for i, x := range xs {
			vs[i] = x
		}
		return newSlice(vs)
	}
	// pure registers a function of its arguments: cost reads them, or
	// what Go's function reads of them when reads names it (readsOf).
	pure := func(name string, f func(args []Value) Value) {
		reads := readsOf[name]
		natives[name] = func(in *interp, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
			work := argWork(args)
			if reads != nil {
				work = reads(args)
			}
			if ab := in.charge(fr, at, 1+work); ab != nil {
				return nil, ab
			}
			out, why := guard(func() Value { return f(args) })
			if why != "" {
				return nil, fr.defect(at, "%s", why)
			}
			if shares[name] {
				return out, nil
			}
			if ab := in.allocate(fr, at, valueSize(out)); ab != nil {
				return nil, ab
			}
			return out, nil
		}
	}
	pure("strings.Compare", func(a []Value) Value { return int64(strings.Compare(str(a[0]), str(a[1]))) })
	pure("strings.PadLeft", func(a []Value) Value { return pad(str(a[0]), i64(a[1]), a[2].(int32), true) })
	pure("strings.PadRight", func(a []Value) Value { return pad(str(a[0]), i64(a[1]), a[2].(int32), false) })
	pure("strings.Split", func(a []Value) Value { return list(strings.Split(str(a[0]), str(a[1]))) })
	pure("strings.Join", func(a []Value) Value { return strings.Join(strs(a[0]), str(a[1])) })
	pure("strings.HasPrefix", func(a []Value) Value { return strings.HasPrefix(str(a[0]), str(a[1])) })
	pure("strings.HasSuffix", func(a []Value) Value { return strings.HasSuffix(str(a[0]), str(a[1])) })
	pure("strings.TrimSpace", func(a []Value) Value { return strings.TrimSpace(str(a[0])) })
	pure("strings.TrimPrefix", func(a []Value) Value { return strings.TrimPrefix(str(a[0]), str(a[1])) })
	pure("strings.TrimSuffix", func(a []Value) Value { return strings.TrimSuffix(str(a[0]), str(a[1])) })
	pure("strings.Fields", func(a []Value) Value { return list(strings.Fields(str(a[0]))) })
	pure("strings.Repeat", func(a []Value) Value { return strings.Repeat(str(a[0]), i64(a[1])) })
	pure("strings.ToLower", func(a []Value) Value { return strings.ToLower(str(a[0])) })
	pure("strings.ToUpper", func(a []Value) Value { return strings.ToUpper(str(a[0])) })
	pure("strings.Index", func(a []Value) Value { return int64(strings.Index(str(a[0]), str(a[1]))) })
	pure("strings.Contains", func(a []Value) Value { return strings.Contains(str(a[0]), str(a[1])) })
	pure("strings.Slice", func(a []Value) Value { return str(a[0])[i64(a[1]):i64(a[2])] })
	pure("strings.ReplaceAll", func(a []Value) Value { return strings.ReplaceAll(str(a[0]), str(a[1]), str(a[2])) })
	pure("strings.LastIndex", func(a []Value) Value { return int64(strings.LastIndex(str(a[0]), str(a[1]))) })
	pure("strings.Count", func(a []Value) Value { return int64(strings.Count(str(a[0]), str(a[1]))) })
	pure("strings.SplitN", func(a []Value) Value {
		// Go's nil for n == 0 is Ergo's empty slice: Ergo has no nil slice.
		return list(strings.SplitN(str(a[0]), str(a[1]), i64(a[2])))
	})
	pure("strings.EqualFold", func(a []Value) Value { return strings.EqualFold(str(a[0]), str(a[1])) })
	pure("strings.ContainsRune", func(a []Value) Value { return strings.ContainsRune(str(a[0]), rune(a[1].(int32))) })
	pure("strings.ToValidUTF8", func(a []Value) Value { return strings.ToValidUTF8(str(a[0]), str(a[1])) })

	pure("utf8.RuneCount", func(a []Value) Value { return int64(utf8.RuneCountInString(str(a[0]))) })
	pure("utf8.RuneText", func(a []Value) Value { return string(rune(a[0].(int32))) })
	pure("utf8.AppendRune", func(a []Value) Value {
		p := a[0].(sliceV)
		b := make([]byte, 0, p.n+utf8.UTFMax)
		for _, e := range p.elems() {
			b = append(b, e.(uint8))
		}
		b = utf8.AppendRune(b, rune(a[1].(int32)))
		vs := make([]Value, len(b))
		for i, c := range b {
			vs[i] = c
		}
		return newSlice(vs)
	})
	pure("utf8.ValidString", func(a []Value) Value { return utf8.ValidString(str(a[0])) })
	pure("utf8.ValidRune", func(a []Value) Value { return utf8.ValidRune(rune(a[0].(int32))) })
	pure("utf8.RuneLen", func(a []Value) Value { return int64(utf8.RuneLen(rune(a[0].(int32)))) })
	natives["utf8.DecodeRuneInString"] = func(in *interp, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
		if ab := in.charge(fr, at, 2); ab != nil {
			return nil, ab
		}
		r, n := utf8.DecodeRuneInString(args[0].(string))
		return stdRecord(fn, "Decoded", map[string]Value{"Rune": int32(r), "Size": int64(n)}), nil
	}

	pure("strconv.Itoa", func(a []Value) Value { return strconv.Itoa(i64(a[0])) })
	pure("strconv.FormatInt", func(a []Value) Value { return strconv.FormatInt(a[0].(int64), i64(a[1])) })
	pure("strconv.FormatBool", func(a []Value) Value { return strconv.FormatBool(a[0].(bool)) })
	pure("strconv.FormatFloat", func(a []Value) Value {
		return strconv.FormatFloat(a[0].(float64), a[1].(uint8), i64(a[2]), 64)
	})
	pure("strconv.Quote", func(a []Value) Value { return strconv.Quote(str(a[0])) })
	// The parses fail as their rows say: Go's ErrRange is OutOfRange, its
	// ErrSyntax Malformed; any other error (a bad base or bit size) is a
	// precondition of the call, a defect.
	parse := func(name string, f func(s string, a []Value) (Value, error)) {
		natives[name] = func(in *interp, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
			if ab := in.charge(fr, at, 1+argWork(args)); ab != nil {
				return nil, ab
			}
			s := args[0].(string)
			var v Value
			var err error
			_, why := guard(func() Value { v, err = f(s, args); return nil })
			if why != "" {
				return nil, fr.defect(at, "%s", why)
			}
			if err == nil {
				return v, nil
			}
			ne, ok := err.(*strconv.NumError)
			switch {
			case ok && ne.Err == strconv.ErrRange:
				return nil, fr.abrupt(ctlFail, at, stdRecord(fn, "OutOfRange", map[string]Value{"Text": s}), "")
			case ok && ne.Err == strconv.ErrSyntax, name == "strconv.ParseBool", name == "strconv.Unquote",
				name == "strconv.ParseFloat":
				return nil, fr.abrupt(ctlFail, at, stdRecord(fn, "Malformed", map[string]Value{"Text": s}), "")
			}
			return nil, fr.defect(at, "%s: %v", name, err)
		}
	}
	parse("strconv.Atoi", func(s string, _ []Value) (Value, error) {
		n, err := strconv.ParseInt(s, 10, 64)
		return n, err
	})
	parse("strconv.ParseInt", func(s string, a []Value) (Value, error) {
		n, err := strconv.ParseInt(s, i64(a[1]), i64(a[2]))
		return n, err
	})
	parse("strconv.ParseFloat", func(s string, _ []Value) (Value, error) {
		f, err := strconv.ParseFloat(s, 64)
		return f, err
	})
	parse("strconv.ParseBool", func(s string, _ []Value) (Value, error) {
		b, err := strconv.ParseBool(s)
		return b, err
	})
	parse("strconv.Unquote", func(s string, _ []Value) (Value, error) {
		u, err := strconv.Unquote(s)
		return u, err
	})

	pure("filepath.Join", func(a []Value) Value { return filepath.Join(strs(a[0])...) })
	pure("filepath.Base", func(a []Value) Value { return filepath.Base(str(a[0])) })
	pure("filepath.Dir", func(a []Value) Value { return filepath.Dir(str(a[0])) })
	pure("filepath.Ext", func(a []Value) Value { return filepath.Ext(str(a[0])) })
	pure("filepath.Clean", func(a []Value) Value { return filepath.Clean(str(a[0])) })
	pure("filepath.IsAbs", func(a []Value) Value { return filepath.IsAbs(str(a[0])) })
	pure("hex.Encode", func(a []Value) Value { return hex.EncodeToString([]byte(str(a[0]))) })
	pure("time.DurationText", func(a []Value) Value { return time.Duration(a[0].(int64)).String() })
	pure("time.Times", func(a []Value) Value { return times(a[0].(int64), a[1].(int64)) })

	// The slices and maps functions copy: an Ergo collection is a value,
	// so a result never shares a store a caller can write (coll.go).
	elems := func(v Value) []Value { return v.(sliceV).elems() }
	pure("slices.Clone", func(a []Value) Value { return newSlice(append([]Value{}, elems(a[0])...)) })
	pure("slices.Concat", func(a []Value) Value {
		return newSlice(append(append([]Value{}, elems(a[0])...), elems(a[1])...))
	})
	pure("slices.Reverse", func(a []Value) Value {
		xs := append([]Value{}, elems(a[0])...)
		for i, j := 0, len(xs)-1; i < j; i, j = i+1, j-1 {
			xs[i], xs[j] = xs[j], xs[i]
		}
		return newSlice(xs)
	})
	pure("slices.Sort", func(a []Value) Value { return sortValues(elems(a[0])) })
	pure("slices.sortOwned", func(a []Value) Value { return sortValues(elems(a[0])) })
	pure("cmp.Compare", func(a []Value) Value { return int64(compareOrdered(a[0], a[1])) })
	natives["slices.Slice"] = func(in *interp, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
		xs := args[0].(sliceV)
		i, j := args[1].(int64), args[2].(int64)
		if ab := in.build(fr, at, 1+max(j-i, 0), max(j-i, 0), 0); ab != nil {
			return nil, ab
		}
		if j > int64(xs.n) && i >= 0 && i <= j {
			// xs[i:j:j] reads up to the Go target's capacity, which its
			// growth policy decides (expr.go sliceExpr).
			return nil, fr.unsupported(at, "a slice bound past the length, which reads the Go target's capacity")
		}
		if i < 0 || j < i || j > int64(xs.n) {
			return nil, fr.defect(at, "slice bounds out of range [%d:%d] with length %d", i, j, xs.n)
		}
		return newSlice(append([]Value{}, xs.elems()[i:j]...)), nil
	}
	// Contains, Index and Equal compare by Ergo's ==, each comparison
	// paid as == pays it.
	natives["slices.Contains"] = func(in *interp, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
		i, ab := in.indexOf(fr, at, args[0].(sliceV), args[1])
		return i >= 0, ab
	}
	natives["slices.Index"] = func(in *interp, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
		i, ab := in.indexOf(fr, at, args[0].(sliceV), args[1])
		return int64(i), ab
	}
	natives["slices.Take"] = func(in *interp, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
		xs, i := args[0].(sliceV).elems(), args[1].(int64)
		if i < 0 || i >= int64(len(xs)) {
			return nil, fr.defect(at, "index out of range [%d] with length %d", i, len(xs))
		}
		if ab := in.build(fr, at, int64(len(xs)), int64(len(xs)), 0); ab != nil {
			return nil, ab
		}
		rest := make([]Value, 0, len(xs)-1)
		rest = append(append(rest, xs[:i]...), xs[i+1:]...)
		return tuple{elems: []Value{xs[i], newSlice(rest)}}, nil
	}
	natives["slices.SortFunc"] = func(in *interp, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
		xs := args[0].(sliceV).elems()
		if ab := in.build(fr, at, int64(len(xs)), int64(len(xs)), 0); ab != nil {
			return nil, ab
		}
		out := slices.Clone(xs)
		// Go's own sort, so the order it leaves equal elements in is the
		// order the built program's leaves them in; the first stop of a
		// comparison ends the sort.
		var stop *abrupt
		slices.SortFunc(out, func(a, b Value) int {
			if stop != nil {
				return 0
			}
			v, ab := in.callValue(fr, at, args[1], []Value{a, b})
			if ab != nil {
				stop = ab
				return 0
			}
			return cmp.Compare(v.(int64), 0)
		})
		if stop != nil {
			return nil, stop
		}
		return newSlice(out), nil
	}
	natives["time.ParseDuration"] = func(in *interp, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
		s := args[0].(string)
		d, err := time.ParseDuration(s)
		if err != nil {
			return nil, fr.abrupt(ctlFail, at, stdRecord(fn, "ParseError", map[string]Value{"Layout": "", "Value": s, "Message": err.Error()}), "")
		}
		return int64(d), nil
	}
	natives["slices.Equal"] = func(in *interp, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
		if ab := in.charge(fr, at, 1); ab != nil {
			return nil, ab
		}
		eq, ab := in.equal(fr, at, args[0], args[1])
		return eq, ab
	}
	natives["maps.Clone"] = func(in *interp, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
		m, ab := in.ownMap(fr, at, args[0].(mapV), &token{})
		if ab != nil {
			return nil, ab
		}
		m.b.owner = nil
		return m, nil
	}

	// The task's context values (log, trace): the reading package is
	// the namespace, as a with of a value of its type binds it.
	for _, p := range []string{"log", "trace"} {
		natives[p+".contextBound"] = func(in *interp, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
			_, ok := in.contextLookup(fn.Pkg.Path, args[0].(string))
			return ok, nil
		}
		natives[p+".contextValue"] = func(in *interp, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
			// "" when no binding is in force (stdlib: the innermost
			// binding's text, or the empty text).
			text, _ := in.contextLookup(fn.Pkg.Path, args[0].(string))
			return text, nil
		}
	}
	natives["sync.NewRef"] = func(in *interp, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
		if ab := in.build(fr, at, 1, 1, 0); ab != nil {
			return nil, ab
		}
		return &refV{v: args[0]}, nil
	}
	natives["sync.Modify"] = func(in *interp, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
		r, ok := args[0].(*refV)
		if !ok {
			return nil, fr.unsupported(at, "a Ref that is not the std's own")
		}
		out, ab := in.callValue(fr, at, args[1], []Value{r.v})
		if ab != nil {
			return nil, ab
		}
		t := out.(tuple)
		r.v = t.elems[1]
		return t.elems[0], nil
	}
	natives["rand.newStream"] = func(in *interp, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
		if ab := in.build(fr, at, 1, 4, 0); ab != nil {
			return nil, ab
		}
		return &streamV{r: rand.New(rand.NewPCG(args[0].(uint64), randSeedHi))}, nil
	}
}

// randSeedHi is the second word of every seeded PCG, the text "ErgoRand"
// (stdlib.md, rand: Seeded(seed) is rand.NewPCG(seed, "ErgoRand")).
const randSeedHi = 0x45_72_67_6f_52_61_6e_64

// nativeOf is fn's native, nil when the evaluator has none.
func nativeOf(fn *check.Func) native {
	if fn.Pkg == nil || !fn.Pkg.Std() {
		return nil
	}
	return natives[fn.Pkg.Path+"."+fn.Name]
}

// guard runs f and turns a Go panic into its text, as the built program's
// panic is a defect.
func guard(f func() Value) (v Value, why string) {
	defer func() {
		if r := recover(); r != nil {
			why = fmt.Sprint(r)
		}
	}()
	return f(), ""
}

// readsOf is the reading work of the pure natives that read less than
// their whole arguments in Go: a substring is a view of its text, and a
// prefix or suffix test reads the prefix's bytes. Charged as argWork, a
// parser that slices or tests the rest of its input at each token would
// pay its input's length per token: steps quadratic in the input where
// the Go build's work is linear (ORACLE O22).
var readsOf = map[string]func(a []Value) int64{
	"strings.Slice":      func(a []Value) int64 { return 0 },
	"strings.HasPrefix":  func(a []Value) int64 { return int64(len(a[1].(string))) },
	"strings.HasSuffix":  func(a []Value) int64 { return int64(len(a[1].(string))) },
	"strings.TrimPrefix": func(a []Value) int64 { return int64(len(a[1].(string))) },
	"strings.TrimSuffix": func(a []Value) int64 { return int64(len(a[1].(string))) },
}

// shares names the pure natives whose result is a view of an argument's
// bytes (Go's substring), which allocates no value bytes.
var shares = map[string]bool{"strings.Slice": true, "strings.TrimPrefix": true, "strings.TrimSuffix": true}

// argWork is the reading work of a native's arguments: a step per byte of
// each text and per element of each slice.
func argWork(args []Value) int64 {
	var n int64
	for _, a := range args {
		switch a := a.(type) {
		case string:
			n += int64(len(a))
		case sliceV:
			n += int64(a.n)
			for _, e := range a.elems() {
				if s, ok := e.(string); ok {
					n += int64(len(s))
				}
			}
		}
	}
	return n
}

// valueSize is the bytes a native's result builds.
func valueSize(v Value) int64 {
	switch v := v.(type) {
	case string:
		return int64(len(v))
	case sliceV:
		n := int64(v.n) * valueBytes
		for _, e := range v.elems() {
			if s, ok := e.(string); ok {
				n += int64(len(s))
			}
		}
		return n
	}
	return 0
}

// indexOf is the first index of x in xs by Ergo's ==, or -1.
func (in *interp) indexOf(fr *frame, at syntax.Node, xs sliceV, x Value) (int, *abrupt) {
	for i, e := range xs.elems() {
		if ab := in.charge(fr, at, 1); ab != nil {
			return -1, ab
		}
		eq, ab := in.equal(fr, at, e, x)
		if ab != nil {
			return -1, ab
		}
		if eq {
			return i, nil
		}
	}
	return -1, nil
}

// sortValues is a sorted copy of xs, a slice of one ordered basic type,
// sorted by Go's own slices.Sort on that Go type, so equal keys (-0 and
// +0) and NaNs land where the built program puts them.
func sortValues(xs []Value) Value {
	if len(xs) == 0 {
		return newSlice([]Value{})
	}
	switch xs[0].(type) {
	case int8:
		return sortAs[int8](xs)
	case int16:
		return sortAs[int16](xs)
	case int32:
		return sortAs[int32](xs)
	case int64:
		return sortAs[int64](xs)
	case uint8:
		return sortAs[uint8](xs)
	case uint16:
		return sortAs[uint16](xs)
	case uint32:
		return sortAs[uint32](xs)
	case uint64:
		return sortAs[uint64](xs)
	case float32:
		return sortAs[float32](xs)
	case float64:
		return sortAs[float64](xs)
	case string:
		return sortAs[string](xs)
	}
	panic(fmt.Sprintf("a sort of %T", xs[0]))
}

func sortAs[T cmp.Ordered](xs []Value) Value {
	ts := make([]T, len(xs))
	for i, x := range xs {
		ts[i] = x.(T)
	}
	slices.Sort(ts)
	out := make([]Value, len(ts))
	for i, t := range ts {
		out[i] = t
	}
	return newSlice(out)
}

// compareOrdered is cmp.Compare of two values of one ordered basic type:
// a NaN is less than any other float and equal to a NaN.
func compareOrdered(a, b Value) int {
	switch a := a.(type) {
	case int8:
		return cmp.Compare(a, b.(int8))
	case int16:
		return cmp.Compare(a, b.(int16))
	case int32:
		return cmp.Compare(a, b.(int32))
	case int64:
		return cmp.Compare(a, b.(int64))
	case uint8:
		return cmp.Compare(a, b.(uint8))
	case uint16:
		return cmp.Compare(a, b.(uint16))
	case uint32:
		return cmp.Compare(a, b.(uint32))
	case uint64:
		return cmp.Compare(a, b.(uint64))
	case float32:
		return cmp.Compare(a, b.(float32))
	case float64:
		return cmp.Compare(a, b.(float64))
	case string:
		return cmp.Compare(a, b.(string))
	}
	panic(fmt.Sprintf("a comparison of %T", a))
}

// times is d*n, saturating at the bounds of duration on overflow
// (stdlib.md, time: Times never wraps).
func times(d, n int64) int64 {
	if d == 0 || n == 0 {
		return 0
	}
	neg := (d < 0) != (n < 0)
	hi, lo := bits.Mul64(abs64(d), abs64(n))
	limit := uint64(math.MaxInt64)
	if neg {
		limit++
	}
	if hi != 0 || lo > limit {
		if neg {
			return math.MinInt64
		}
		return math.MaxInt64
	}
	if neg {
		return int64(-lo)
	}
	return int64(lo)
}

func abs64(x int64) uint64 {
	if x < 0 {
		return uint64(-x)
	}
	return uint64(x)
}

// pad is s after (left) or before copies of pad, width runes in all,
// counting the runes of s as fmt's %*s does (an invalid byte is one rune).
func pad(s string, width int, r rune, left bool) string {
	n := utf8.RuneCountInString(s)
	if n >= width {
		return s
	}
	p := strings.Repeat(string(r), width-n)
	if left {
		return p + s
	}
	return s + p
}

// stdRecord is a value of the record type name of fn's std package.
func stdRecord(fn *check.Func, name string, fields map[string]Value) *record {
	for _, tn := range fn.Pkg.Types {
		if tn.Name != name {
			continue
		}
		n, ok := tn.Type.(*check.Named)
		if !ok || structOf(n) == nil {
			break
		}
		r := &record{t: n, fields: make([]Value, len(structOf(n).Fields))}
		for k, v := range fields {
			r.fields[fieldIndex(n, k)] = v
		}
		return r
	}
	ice("the std package %s has no record %s", fn.Pkg.Path, name)
	return nil
}

// opObject is a value of a sealed std operation type whose operations the
// evaluator runs natively: a Ref, a rand stream.
type opObject interface {
	opCall(in *interp, fr *frame, at syntax.Node, name string, args []Value) (Value, *abrupt)
	describe() string
}

// refV is a sync.Ref: one cell every copy shares (sync.ergo: "every copy
// of it is the same cell"). The evaluator runs one task at a time, so an
// operation is atomic by construction.
type refV struct{ v Value }

func (r *refV) describe() string { return "a sync.Ref" }

func (r *refV) opCall(in *interp, fr *frame, at syntax.Node, name string, args []Value) (Value, *abrupt) {
	if ab := in.charge(fr, at, 1); ab != nil {
		return nil, ab
	}
	switch name {
	case "Get":
		return r.v, nil
	case "Set":
		r.v = args[0]
		return unit{}, nil
	case "Update":
		v, ab := in.callValue(fr, at, args[0], []Value{r.v})
		if ab != nil {
			return nil, ab
		}
		r.v = v
		return unit{}, nil
	}
	ice("the Ref operation %s", name)
	return nil, nil
}

// streamV is a rand.Stream: Go's rand.Rand over a PCG source.
type streamV struct{ r *rand.Rand }

func (s *streamV) describe() string { return "a rand.Stream" }

func (s *streamV) opCall(in *interp, fr *frame, at syntax.Node, name string, args []Value) (Value, *abrupt) {
	if ab := in.charge(fr, at, 1); ab != nil {
		return nil, ab
	}
	switch name {
	case "Uint64":
		return s.r.Uint64(), nil
	case "IntN":
		n := args[0].(int64)
		if n <= 0 {
			return nil, fr.defect(at, "invalid argument to IntN")
		}
		return int64(s.r.IntN(int(n))), nil
	case "Float64":
		return s.r.Float64(), nil
	}
	ice("the rand.Stream operation %s", name)
	return nil, nil
}
