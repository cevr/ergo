package eval

import (
	"math"
	"strconv"
	"strings"
	"unicode"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// A program's own extern of a pure Go standard library function (ORACLE
// O20; grammar.md, extern go): the evaluator calls the same Go function,
// which is the host language's library, not the lowering's or runtime/v1's
// code. Only the functions listed here run; any other extern stays a
// skip. Each takes and gives basic values only.
var goNatives = map[string]native{
	"math.Log":          goFloat(math.Log),
	"math.Exp":          goFloat(math.Exp),
	"strings.TrimRight": goStrings(strings.TrimRight),
}

// std math is Go's math for float64 (std/math/math.ergo): the same Go
// functions, as a program's extern calls them.
func registerNativesgo() {
	for name, f := range map[string]func(float64) float64{"Abs": math.Abs, "Ceil": math.Ceil, "Floor": math.Floor, "Round": math.Round, "Sqrt": math.Sqrt, "Log": math.Log, "Exp": math.Exp} {
		natives["math."+name] = goFloat(f)
	}
	two := func(f func(float64, float64) float64) native {
		return func(in *interp, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
			return f(args[0].(float64), args[1].(float64)), in.charge(fr, at, 1)
		}
	}
	natives["math.Pow"] = two(math.Pow)
	natives["math.Mod"] = two(math.Mod)
	natives["math.Inf"] = func(in *interp, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
		return math.Inf(int(args[0].(int64))), in.charge(fr, at, 1)
	}
	natives["math.NaN"] = func(in *interp, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
		return math.NaN(), in.charge(fr, at, 1)
	}
	natives["math.IsNaN"] = func(in *interp, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
		return math.IsNaN(args[0].(float64)), in.charge(fr, at, 1)
	}
	natives["math.IsInf"] = func(in *interp, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
		return math.IsInf(args[0].(float64), int(args[1].(int64))), in.charge(fr, at, 1)
	}
}

// goStdNatives are the pure functions of Go's standard library that a
// program reaches through `import go` (a binding of gostd), keyed by
// import path and name: the evaluator calls the same Go function. Each
// takes and gives basic values only (an Ergo int is an int64, a rune an
// int32). compiler/syntax's scanner calls these (wf10 TP-DIAG).
var goStdNatives = map[string]native{
	"unicode.IsLetter":  goRune(unicode.IsLetter),
	"unicode.IsDigit":   goRune(unicode.IsDigit),
	"strconv.QuoteRune": goText(func(a []Value) string { return strconv.QuoteRune(rune(a[0].(int32))) }),
	"strconv.Quote":     goText(func(a []Value) string { return strconv.Quote(a[0].(string)) }),
	"strconv.Itoa":      goText(func(a []Value) string { return strconv.Itoa(int(a[0].(int64))) }),
	"strconv.FormatInt": goText(func(a []Value) string { return strconv.FormatInt(a[0].(int64), int(a[1].(int64))) }),
}

// builtinAppend is a program's extern of Go's builtin append (check
// builtin.go): append(xs, x) when its second parameter is an element,
// append(xs, ys...) when it is a slice, append(bs, s...) for the bytes of
// a string. xs keeps its elements, as a value does (wf10 EVAL-BUILDER).
func builtinAppend(in *interp, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
	if check.AppendsString(fn) {
		if ab := in.charge(fr, at, 1+argWork(args[1:])); ab != nil {
			return nil, ab
		}
		return appendString(fn, args[0], args[1]), nil
	}
	if _, spread := fn.Sig.Params[1].Type.(*check.Slice); !spread {
		return in.appendTo(fr, at, args[0].(sliceV), args[1], nil)
	}
	if ab := in.charge(fr, at, 1+argWork(args)); ab != nil {
		return nil, ab
	}
	xs, ys := args[0].(sliceV).elems(), args[1].(sliceV).elems()
	return newSlice(append(append(make([]Value, 0, len(xs)+len(ys)), xs...), ys...)), nil
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

// goNativeOf is the native of fn when it is a program's extern of a listed
// Go function, or a listed function of Go's standard library that the
// program imports with `import go`.
func goNativeOf(fn *check.Func) native {
	if fn.Pkg != nil && fn.Pkg.Host() {
		if first, _, _ := strings.Cut(fn.Pkg.Path, "/"); strings.Contains(first, ".") {
			// A module's own binding: Go's standard library paths have
			// no dot in their first element.
			return nil
		}
		return goStdNatives[fn.Pkg.Path+"."+fn.Name]
	}
	if fn.Decl == nil || fn.Decl.Extern == nil || fn.Pkg == nil || fn.Pkg.Std() {
		return nil
	}
	if check.GoBuiltin(fn) == "append" {
		return builtinAppend
	}
	path, name := fn.Decl.Extern.Split()
	return goNatives[path+"."+name]
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
