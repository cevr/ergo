package eval

import (
	"math"
	"strings"

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
func init() {
	for name, f := range map[string]func(float64) float64{"Abs": math.Abs, "Ceil": math.Ceil, "Floor": math.Floor, "Round": math.Round, "Sqrt": math.Sqrt} {
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

// goNativeOf is the native of fn when it is a program's extern of a listed
// Go function.
func goNativeOf(fn *check.Func) native {
	if fn.Decl == nil || fn.Decl.Extern == nil || fn.Pkg == nil || fn.Pkg.Std() || fn.Pkg.Host() {
		return nil
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
