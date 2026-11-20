package eval

import (
	"crypto/sha256"
	"hash/crc32"
	"math"
	"strconv"
	"strings"
	"unicode"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// A pure Go standard library function that a program reaches through its
// own extern (ORACLE O20; grammar.md, extern go) or an import go, by Go
// path and name: the evaluator calls the same Go function, which is the
// host language's library, not the lowering's or runtime/v1's code. A std
// package reaches it as a user does (NP4f: std binds Go through import
// go), so its Ergo body runs here and calls the same entry. Only the
// functions listed here run; any other Go function stays a skip. Each
// takes and gives basic values, a []byte or a [N]byte (goBytes).
// registerNativesgo adds the rest to these.
func goNatives() map[string]native {
	return map[string]native{
		"math.Log":          goFloat(math.Log),
		"math.Exp":          goFloat(math.Exp),
		"strings.TrimLeft":  goStrings(strings.TrimLeft),
		"strings.TrimRight": goStrings(strings.TrimRight),
		// The bit conversions reinterpret, as Go's do (cborpack, MX-EVMATHBITS).
		"math.Float64bits":     goBits(math.Float64bits),
		"math.Float64frombits": goBits(math.Float64frombits),
		"math.Float32bits":     goBits(math.Float32bits),
		"math.Float32frombits": goBits(math.Float32frombits),
		// compiler/syntax's scanner calls these through import go (TP-DIAG):
		// an Ergo rune is an int32.
		"unicode.IsLetter":  goRune(unicode.IsLetter),
		"unicode.IsDigit":   goRune(unicode.IsDigit),
		"strconv.QuoteRune": goText(func(a []Value) string { return strconv.QuoteRune(rune(a[0].(int32))) }),
	}
}

// Go's math for float64, which std/math/math.ergo binds through import go,
// and strings.IndexByte, which std strings binds.
func registerNativesgo(t nativeTables) {
	natives := t.std
	goNatives := t.goLib
	for name, f := range map[string]func(float64) float64{"Abs": math.Abs, "Ceil": math.Ceil, "Floor": math.Floor, "Round": math.Round, "Sqrt": math.Sqrt, "Trunc": math.Trunc,
		"Sin": math.Sin, "Cos": math.Cos, "Tan": math.Tan, "Asin": math.Asin, "Acos": math.Acos, "Atan": math.Atan, "Cbrt": math.Cbrt, "Log2": math.Log2, "Log10": math.Log10} {
		goNatives["math."+name] = goFloat(f)
	}
	// math.Float64bits, which std explore's key binds through import go
	// (a float keyed by its bits; MODELCHECK E1).
	goNatives["math.Float64bits"] = func(in *interp, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
		return math.Float64bits(args[0].(float64)), in.charge(fr, at, 1)
	}
	two := func(f func(float64, float64) float64) native {
		return func(in *interp, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
			return f(args[0].(float64), args[1].(float64)), in.charge(fr, at, 1)
		}
	}
	goNatives["strings.IndexByte"] = func(in *interp, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
		if ab := in.charge(fr, at, 1+readsOf["strings.IndexByte"](args)); ab != nil {
			return nil, ab
		}
		return int64(strings.IndexByte(args[0].(string), args[1].(uint8))), nil
	}
	// std sha256 and crc32 bind Go's digests on the bytes of a string
	// (NP4f-5); std strings' Bytes gives them.
	goNatives["crypto/sha256.Sum256"] = goBytes(func(b []byte) Value {
		d := sha256.Sum256(b)
		return byteArray(d[:])
	})
	goNatives["hash/crc32.ChecksumIEEE"] = goBytes(func(b []byte) Value { return crc32.ChecksumIEEE(b) })
	natives["strings.stringsBytes"] = func(in *interp, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
		s := args[0].(string)
		if ab := in.build(fr, at, 1+int64(len(s)), int64(len(s)), 0); ab != nil {
			return nil, ab
		}
		out := make([]Value, len(s))
		for i := 0; i < len(s); i++ {
			out[i] = s[i]
		}
		return newSlice(out), nil
	}
	goNatives["math.Pow"] = two(math.Pow)
	goNatives["math.Mod"] = two(math.Mod)
	goNatives["math.Atan2"] = two(math.Atan2)
	goNatives["math.Hypot"] = two(math.Hypot)
	goNatives["math.FMA"] = func(in *interp, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
		return math.FMA(args[0].(float64), args[1].(float64), args[2].(float64)), in.charge(fr, at, 1)
	}
	goNatives["math.Inf"] = func(in *interp, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
		return math.Inf(int(args[0].(int64))), in.charge(fr, at, 1)
	}
	goNatives["math.NaN"] = func(in *interp, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
		return math.NaN(), in.charge(fr, at, 1)
	}
	goNatives["math.IsNaN"] = func(in *interp, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
		return math.IsNaN(args[0].(float64)), in.charge(fr, at, 1)
	}
	goNatives["math.IsInf"] = func(in *interp, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
		return math.IsInf(args[0].(float64), int(args[1].(int64))), in.charge(fr, at, 1)
	}
}

func goRune(f func(rune) bool) native {
	return func(in *interp, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
		return f(rune(args[0].(int32))), in.charge(fr, at, 1)
	}
}

// goText runs f, whose Go function may panic (FormatInt's base), as the
// built program would: a panic is a defect.
func goText(f func([]Value) string) native {
	return func(in *interp, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
		if ab := in.charge(fr, at, 1+argWork(args)); ab != nil {
			return nil, ab
		}
		out, why := guard(func() Value { return f(args) })
		if why != "" {
			return nil, fr.defect(at, "%s", why)
		}
		return out, in.allocate(fr, at, int64(len(out.(string))))
	}
}

// goNativeOf is the native of fn when it is a listed Go function: a
// program's extern of one, or a function of an import go package. A std
// extern binds runtime/v1, not Go's library, so it has none here.
func goNativeOf(fn *check.Func) native {
	if fn.Decl == nil || fn.Decl.Extern == nil || fn.Decl.Extern.Target == nil || fn.Pkg == nil || fn.Pkg.Std() {
		return nil
	}
	path, name := fn.Decl.Extern.Split()
	if nat := loadedNatives().goLib[path+"."+name]; nat != nil {
		return nat
	}
	return goReflectNative(fn) // gohost.go
}

// goBits is a one-argument conversion of basic values, one step.
func goBits[A, R Value](f func(A) R) native {
	return func(in *interp, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
		if ab := in.charge(fr, at, 1); ab != nil {
			return nil, ab
		}
		return f(args[0].(A)), nil
	}
}

func goFloat(f func(float64) float64) native {
	return func(in *interp, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
		if ab := in.charge(fr, at, 1); ab != nil {
			return nil, ab
		}
		return f(args[0].(float64)), nil
	}
}

func goStrings(f func(string, string) string) native {
	return func(in *interp, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
		if ab := in.charge(fr, at, 1+argWork(args)); ab != nil {
			return nil, ab
		}
		out := f(args[0].(string), args[1].(string))
		return out, in.allocate(fr, at, int64(len(out)))
	}
}

// goBytes is a Go function of a []byte: the evaluator holds the slice's
// bytes as uint8 values, so a slice of any other value is a skip.
func goBytes(f func([]byte) Value) native {
	return func(in *interp, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
		sl, ok := args[0].(sliceV)
		var b string
		if ok {
			b, ok = byteString(sl)
		}
		if !ok {
			return nil, fr.unsupported(at, "%s of bytes the evaluator does not hold", fn.Name)
		}
		if ab := in.charge(fr, at, 1+int64(len(b))); ab != nil {
			return nil, ab
		}
		return f([]byte(b)), nil
	}
}

// byteArray is the [N]byte of b.
func byteArray(b []byte) Value {
	out := make([]Value, len(b))
	for i, c := range b {
		out[i] = c
	}
	a := newSlice(out)
	a.array = true
	return a
}
