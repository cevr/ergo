package lower

import (
	"go/constant"
	"go/token"
	"math"
	"sync"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// Constant folding before gc (PATCH-MIN M6). A package let whose value is
// a checked constant is an IR constant where it is read (exprOf, constLet),
// and the checker folds no expression over it, so `K * 2` reaches the IR
// as a bin of two constants. Go folds such an expression of typed
// constants into one constant; the derivative's reader did the same
// (ergoir_fold.go). The builder now folds it, so the IR it writes holds no
// operation the reader would fold, and the reader's fold can leave the
// patch (MINIMAL FORK PATCH: decide before gc).
//
// The rules are Go's for typed constants, the reader's set of operators:
//   - + - * / % & | ^ &^ of two constants of the operation's type; an
//     integer / truncates; a division or a remainder by zero, and % of
//     floats, stay operations (the run's defect, or Go's own float rule);
//   - unary - + of a number and ! of a bool; unary ^ stays;
//   - a conversion between basic types of a constant: to an integer only
//     when the value is an integer, to a float rounded;
//   - + of two strings;
//   - every integer result must fit its type, or the operation stays (an
//     overflow wraps when the program runs, which a constant cannot);
//   - every float result is rounded to its type, as Go rounds each typed
//     constant operation (types2 representable). An IEEE operation is
//     correctly rounded, so the folded value is the one the operation
//     would compute when the program runs.
//
// The len of a constant string is its byte count, as Go's (INDEX-NONSLICE:
// lzpack's len(Magic)).
//
// Comparisons, logical operators and shifts are not folded, as the reader
// did not fold them. int and uint are 64 bits wide (the reader's bounds).
//
// The reference is opFoldLowering "off": no fold, the reader folds as
// before this unit.
//
// Prior art: every compiler to a host folds before the host where it can:
// Gleam folds nothing and leaves it to Erlang's or JavaScript's compiler;
// ReScript folds integer arithmetic in its Lambda passes (Lam_compile_const)
// with OCaml's wrapping int semantics; Elixir's compiler leaves it to the
// Erlang compiler's sys_core_fold. Ergo folds with the host's typed
// constant rules so the result is the one gc would have computed.

// opFoldLowering selects the fold: "" (on) or "off" (the reference),
// set by
// -ldflags=-X=ergo.local/prototype/internal/sketch/lower.opFoldLowering=off.
var opFoldLowering string

// OpFoldOn reports that the fold is on, for the property that checks
// its reach.
func OpFoldOn() bool { return opFoldLowering != "off" }

// irConsts are the values of a body's IR constants.
type irConsts map[*irExpr]constant.Value

// constValue records v as the value of the IR constant e (constant).
func (b *irBuilder) constValue(e *irExpr, v constant.Value) *irExpr {
	if b.consts == nil {
		b.consts = irConsts{}
	}
	b.consts[e] = v
	return e
}

// foldConst is e, the IR of x, folded into one constant when x is an
// operation or a conversion of constants that Go folds (above); else e.
func (b *irBuilder) foldConst(x syntax.Expr, e *irExpr) *irExpr {
	if opFoldLowering == "off" || e == nil {
		return e
	}
	var v constant.Value
	switch e.K {
	case "bin":
		if b.consts != nil {
			v = foldBin(e.Op, b.consts[e.X], b.consts[e.Y], b.scalarBasic(b.l.info.Types[x]))
		}
	case "un":
		if b.consts != nil {
			v = foldUn(e.Op, b.consts[e.X], b.scalarBasic(b.l.info.Types[x]))
		}
	case "conv":
		if b.consts != nil {
			v = foldConv(b.consts[e.X], b.scalarBasic(b.l.info.Types[x]))
		}
	case "len":
		// Go's len of a constant string or pure fixed array is a constant
		// (the spec's Length and capacity), so len(K) and K.Len() fold identically.
		if b.consts != nil {
			if s := b.consts[e.X]; s != nil && s.Kind() == constant.String {
				v = constant.MakeInt64(int64(len(constant.StringVal(s))))
			}
		}
		if v == nil {
			if val, ok := b.l.info.Values[x]; ok && val != nil && val.Kind() == constant.Int {
				v = val
			}
		}
	}
	if v == nil {
		return e
	}
	b.st.report.ConstFolds++
	return b.constant(b.l.info.Types[x], v, x, e.Pos)
}

var foldTokens = sync.OnceValue(func() map[string]token.Token {
	return map[string]token.Token{
		"+": token.ADD, "-": token.SUB, "*": token.MUL, "/": token.QUO, "%": token.REM,
		"&": token.AND, "|": token.OR, "^": token.XOR, "&^": token.AND_NOT,
	}
})

// foldBin is l op r of the basic type t, or nil.
func foldBin(op string, l, r constant.Value, t *check.Basic) constant.Value {
	tok, ok := foldTokens()[op]
	if l == nil || r == nil || t == nil || !ok {
		return nil
	}
	switch {
	case t.Kind == check.String:
		if tok != token.ADD || l.Kind() != constant.String || r.Kind() != constant.String {
			return nil
		}
		return constant.BinaryOp(l, tok, r)
	case foldInteger(t):
		if l.Kind() != constant.Int || r.Kind() != constant.Int {
			return nil
		}
		if (tok == token.QUO || tok == token.REM) && constant.Sign(r) == 0 {
			return nil
		}
		if tok == token.QUO {
			tok = token.QUO_ASSIGN // integer division
		}
		return foldFit(constant.BinaryOp(l, tok, r), t)
	case t.Kind == check.Float32 || t.Kind == check.Float64:
		switch tok {
		case token.ADD, token.SUB, token.MUL:
		case token.QUO:
			if constant.Sign(r) == 0 {
				return nil
			}
		default:
			return nil
		}
		return foldFit(constant.BinaryOp(constant.ToFloat(l), tok, constant.ToFloat(r)), t)
	}
	return nil
}

// foldUn is op x of the basic type t, or nil.
func foldUn(op string, x constant.Value, t *check.Basic) constant.Value {
	if x == nil || t == nil {
		return nil
	}
	switch {
	case op == "!" && t.Kind == check.Bool && x.Kind() == constant.Bool:
		return constant.UnaryOp(token.NOT, x, 0)
	case (op == "-" || op == "+") && (foldInteger(t) || t.Kind == check.Float32 || t.Kind == check.Float64):
		tok := token.SUB
		if op == "+" {
			tok = token.ADD
		}
		return foldFit(constant.UnaryOp(tok, x, 0), t)
	}
	return nil
}

// foldConv is t(x), or nil.
func foldConv(x constant.Value, t *check.Basic) constant.Value {
	if x == nil || t == nil {
		return nil
	}
	switch {
	case foldInteger(t):
		if v := constant.ToInt(x); v.Kind() == constant.Int {
			return foldFit(v, t)
		}
	case t.Kind == check.Float32 || t.Kind == check.Float64:
		if v := constant.ToFloat(x); v.Kind() == constant.Float || v.Kind() == constant.Int {
			return foldFit(v, t)
		}
	case t.Kind == check.String && x.Kind() == constant.String, t.Kind == check.Bool && x.Kind() == constant.Bool:
		return x
	}
	return nil
}

// foldInteger reports an integer basic type, a Duration's int64 among them.
func foldInteger(t *check.Basic) bool {
	return t.Kind >= check.Int && t.Kind <= check.Uintptr || t.Kind == check.Duration
}

// foldFit is v as a constant of t: an integer that t holds, a float
// rounded to t's precision; nil when t cannot hold it.
func foldFit(v constant.Value, t *check.Basic) constant.Value {
	if v == nil || v.Kind() == constant.Unknown {
		return nil
	}
	// A float is rounded to t where it becomes an IR constant (constant
	// records the rounded value), so a chain folds as Go rounds each
	// typed operation; here only a value past t's range stays an
	// operation.
	switch t.Kind {
	case check.Float64:
		if f, _ := constant.Float64Val(constant.ToFloat(v)); math.IsInf(f, 0) {
			return nil
		}
		return v
	case check.Float32:
		if f, _ := constant.Float32Val(constant.ToFloat(v)); math.IsInf(float64(f), 0) {
			return nil
		}
		return v
	}
	if v.Kind() != constant.Int {
		return nil
	}
	lo, hi := constant.MakeInt64(math.MinInt64), constant.MakeUint64(math.MaxInt64)
	switch t.Kind {
	case check.Int8:
		lo, hi = constant.MakeInt64(math.MinInt8), constant.MakeUint64(math.MaxInt8)
	case check.Int16:
		lo, hi = constant.MakeInt64(math.MinInt16), constant.MakeUint64(math.MaxInt16)
	case check.Int32:
		lo, hi = constant.MakeInt64(math.MinInt32), constant.MakeUint64(math.MaxInt32)
	case check.Uint8:
		lo, hi = constant.MakeInt64(0), constant.MakeUint64(math.MaxUint8)
	case check.Uint16:
		lo, hi = constant.MakeInt64(0), constant.MakeUint64(math.MaxUint16)
	case check.Uint32:
		lo, hi = constant.MakeInt64(0), constant.MakeUint64(math.MaxUint32)
	case check.Uint, check.Uint64, check.Uintptr:
		lo, hi = constant.MakeInt64(0), constant.MakeUint64(math.MaxUint64)
	}
	if constant.Compare(v, token.LSS, lo) || constant.Compare(v, token.GTR, hi) {
		return nil
	}
	return v
}
