package eval

import (
	"math"
	"strings"

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
// takes and gives basic values only.
var goNatives = map[string]native{
	"math.Log":          goFloat(math.Log),
	"math.Exp":          goFloat(math.Exp),
	"strings.TrimRight": goStrings(strings.TrimRight),
}

// Go's math for float64, which std/math/math.ergo binds through import go.
func registerNativesgo() {
	for name, f := range map[string]func(float64) float64{"Abs": math.Abs, "Ceil": math.Ceil, "Floor": math.Floor, "Round": math.Round, "Sqrt": math.Sqrt, "Trunc": math.Trunc} {
		goNatives["math."+name] = goFloat(f)
	}
	two := func(f func(float64, float64) float64) native {
		return func(in *interp, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
			return f(args[0].(float64), args[1].(float64)), in.charge(fr, at, 1)
		}
	}
	goNatives["math.Pow"] = two(math.Pow)
	goNatives["math.Mod"] = two(math.Mod)
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

// goNativeOf is the native of fn when it is a listed Go function: a
// program's extern of one, or a function of an import go package. A std
// extern binds runtime/v1, not Go's library, so it has none here.
func goNativeOf(fn *check.Func) native {
	if fn.Decl == nil || fn.Decl.Extern == nil || fn.Pkg == nil || fn.Pkg.Std() {
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
