package check

import (
	"fmt"
	"go/constant"
	"go/token"
	"math"

	"ergo.local/prototype/internal/sketch/syntax"
)

// representable checks that a constant fits the basic type t (constant
// overflow is S108; types.md, Basic types 2) and rounds it to t's kind.
func (c *checker) representable(op *operand, t *Basic) bool {
	if op.val == nil {
		return true
	}
	v := op.val
	ok := true
	switch {
	case t.Kind == Bool:
		ok = v.Kind() == constant.Bool
	case t.Kind == String:
		ok = v.Kind() == constant.String
	case t.Kind >= Int && t.Kind <= Uintptr || t.Kind == Duration:
		iv := constant.ToInt(v)
		if iv.Kind() != constant.Int {
			c.errorf(op.expr, CodeType, "%s is not an integer, so it is not representable as %s", v.ExactString(), t)
			op.mode = modeInvalid
			return false
		}
		lo, hi := intBounds(t.Kind)
		if constant.Compare(iv, token.LSS, lo) || constant.Compare(iv, token.GTR, hi) {
			c.errorf(op.expr, CodeType, "the constant %s overflows %s (types.md, Basic types 2)", iv.ExactString(), t)
			op.mode = modeInvalid
			return false
		}
		op.val = iv
	case t.Kind == Float32 || t.Kind == Float64:
		fv := constant.ToFloat(v)
		if fv.Kind() != constant.Float && fv.Kind() != constant.Int {
			ok = false
			break
		}
		f, _ := constant.Float64Val(fv)
		if math.IsInf(f, 0) {
			c.errorf(op.expr, CodeType, "the constant %s overflows %s", v.ExactString(), t)
			op.mode = modeInvalid
			return false
		}
		switch t.Kind {
		case Float32:
			f32, _ := constant.Float32Val(fv)
			if math.IsInf(float64(f32), 0) {
				c.errorf(op.expr, CodeType, "the constant %s overflows float32", v.ExactString())
				op.mode = modeInvalid
				return false
			}
			// A typed constant holds its type's correctly rounded value, so
			// later constant arithmetic sees what Go sees (types2
			// roundFloat32; M12).
			fv = constant.MakeFloat64(float64(f32))
		case Float64:
			fv = constant.MakeFloat64(f)
		}
		op.val = fv
	case t.Kind == Complex64 || t.Kind == Complex128:
		cv := constant.ToComplex(v)
		if cv.Kind() != constant.Complex {
			ok = false
			break
		}
		re, _ := constant.Float64Val(constant.Real(cv))
		im, _ := constant.Float64Val(constant.Imag(cv))
		if t.Kind == Complex64 {
			re32, _ := constant.Float32Val(constant.Real(cv))
			im32, _ := constant.Float32Val(constant.Imag(cv))
			re, im = float64(re32), float64(im32)
		}
		if math.IsInf(re, 0) || math.IsInf(im, 0) {
			c.errorf(op.expr, CodeType, "the constant %s overflows %s", v.ExactString(), t)
			op.mode = modeInvalid
			return false
		}
		// Rounded per part to the type's float size, as for floats (M12).
		op.val = constant.BinaryOp(constant.MakeFloat64(re), token.ADD, constant.MakeImag(constant.MakeFloat64(im)))
	}
	if !ok {
		c.errorf(op.expr, CodeType, "the constant %s is not representable as %s", v.ExactString(), t)
		op.mode = modeInvalid
		return false
	}
	return true
}

func intBounds(k BasicKind) (constant.Value, constant.Value) {
	switch k {
	case Int8:
		return constant.MakeInt64(math.MinInt8), constant.MakeInt64(math.MaxInt8)
	case Int16:
		return constant.MakeInt64(math.MinInt16), constant.MakeInt64(math.MaxInt16)
	case Int32:
		return constant.MakeInt64(math.MinInt32), constant.MakeInt64(math.MaxInt32)
	case Uint8:
		return constant.MakeInt64(0), constant.MakeInt64(math.MaxUint8)
	case Uint16:
		return constant.MakeInt64(0), constant.MakeInt64(math.MaxUint16)
	case Uint32:
		return constant.MakeInt64(0), constant.MakeInt64(math.MaxUint32)
	case Uint, Uint64, Uintptr:
		return constant.MakeInt64(0), constant.MakeUint64(math.MaxUint64)
	}
	return constant.MakeInt64(math.MinInt64), constant.MakeInt64(math.MaxInt64)
}

// ---------------------------------------------------------------- unary

func (c *checker) unary(x *syntax.UnaryExpr, hint Type) operand {
	switch x.Op {
	case syntax.AND:
		op := c.valueExpr(x.X, nil)
		if op.invalid() {
			return op
		}
		if !op.addressable {
			c.errorf(x, CodeType, "cannot take the address of this expression; a borrow &x names a variable, field or element")
			return bad(x)
		}
		c.borrowRoot(x, x.X, "&") // D-D1: a borrow could change x
		return operand{mode: modeValue, typ: &Pointer{Elem: op.typ}}
	case syntax.ADD, syntax.SUB, syntax.NOT, syntax.XOR:
	default:
		return c.undecided(x, "the unary operator "+x.Op.String())
	}
	var op operand
	if lit, ok := unparen(x.X).(*syntax.BasicLit); ok && (x.Op == syntax.SUB || x.Op == syntax.ADD) {
		// A signed literal is one literal: -128 and -(128) are an int8
		// (literal checks the unsigned value everywhere else). Info records
		// the signed expression only; its unsigned part has no entry, since
		// 128 is no int8 (info.go, Types).
		op = c.basicLit(lit, hint)
		op.expr = lit
	} else {
		op = c.valueExpr(x.X, hint)
	}
	if op.invalid() {
		return op
	}
	t := op.typ
	switch x.Op {
	case syntax.NOT:
		if !isBoolean(t) {
			c.errorf(x, CodeType, "operator ! is not defined on %s", t)
			return bad(x)
		}
	case syntax.SUB, syntax.ADD:
		if !isNumeric(t) && !isDuration(t) {
			c.errorf(x, CodeType, "operator %s is not defined on %s", x.Op, t)
			return bad(x)
		}
	case syntax.XOR:
		if !isInteger(t) {
			c.errorf(x, CodeType, "operator ^ is not defined on %s", t)
			return bad(x)
		}
	}
	if op.mode == modeConst {
		tok := map[syntax.Token]token.Token{syntax.NOT: token.NOT, syntax.SUB: token.SUB, syntax.ADD: token.ADD, syntax.XOR: token.XOR}[x.Op]
		prec := uint(0)
		if isUnsigned(t) {
			prec = uint(basicSize(under(t).(*Basic).Kind) * 8)
		}
		op.val = constant.UnaryOp(tok, op.val, prec)
		if b, ok := under(t).(*Basic); ok {
			op.expr = x
			if !c.representable(&op, b) {
				return bad(x)
			}
		}
		return operand{mode: modeConst, typ: t, val: op.val}
	}
	return operand{mode: modeValue, typ: t}
}

func basicSize(k BasicKind) int {
	switch k {
	case Int8, Uint8:
		return 1
	case Int16, Uint16:
		return 2
	case Int32, Uint32, Float32:
		return 4
	}
	return 8
}

// ---------------------------------------------------------------- binary

var tokenOf = map[syntax.Token]token.Token{
	syntax.ADD: token.ADD, syntax.SUB: token.SUB, syntax.MUL: token.MUL, syntax.QUO: token.QUO, syntax.REM: token.REM,
	syntax.AND: token.AND, syntax.OR: token.OR, syntax.XOR: token.XOR, syntax.SHL: token.SHL, syntax.SHR: token.SHR, syntax.AND_NOT: token.AND_NOT,
	syntax.LAND: token.LAND, syntax.LOR: token.LOR,
	syntax.EQL: token.EQL, syntax.NEQ: token.NEQ, syntax.LSS: token.LSS, syntax.LEQ: token.LEQ, syntax.GTR: token.GTR, syntax.GEQ: token.GEQ,
}

func isComparison(op syntax.Token) bool {
	switch op {
	case syntax.EQL, syntax.NEQ, syntax.LSS, syntax.LEQ, syntax.GTR, syntax.GEQ:
		return true
	}
	return false
}

// binary checks Go's binary operators on Go's operand types (README Gap 10,
// types.md Operators), with exact constant arithmetic. duration takes +, -,
// comparisons and equality with another duration only (D2; #175).
func (c *checker) binary(x *syntax.BinaryExpr, hint Type) operand {
	if x.Op == syntax.SHL || x.Op == syntax.SHR {
		return c.shift(x, hint)
	}
	if x.Op == syntax.DOTDOT {
		return c.rangeExpr(x, hint)
	}
	if isComparison(x.Op) {
		hint = nil // a comparison is bool; its operands take each other's type
	}
	l, r := c.operands(x.X, x.Y, hint)
	if l.invalid() || r.invalid() {
		return bad(x)
	}
	if isRowValue(l.typ) || isRowValue(r.typ) {
		// A row value is returned, matched, caught or assigned to a union
		// that lists its types; it has no other operation (D-C3).
		c.errorf(x, CodeType, "a row value has no operator %s: return it, match it, catch it or assign it to a union that lists its types (rows.md, Catch 7)", x.Op)
		return bad(x)
	}
	if !c.matchTypes(x, &l, &r) {
		return bad(x)
	}
	t := l.typ
	if isComparison(x.Op) {
		switch x.Op {
		case syntax.EQL, syntax.NEQ:
			if !comparable(t) {
				c.errorf(x, CodeType, "%s values cannot be compared with %s", t, x.Op)
				return bad(x)
			}
		default:
			if !ordered(t) {
				c.errorf(x, CodeType, "operator %s is not defined on %s", x.Op, t)
				return bad(x)
			}
		}
		if l.mode == modeConst && r.mode == modeConst {
			return operand{mode: modeConst, typ: Typ[Bool], val: constant.MakeBool(constant.Compare(l.val, tokenOf[x.Op], r.val))}
		}
		return operand{mode: modeValue, typ: Typ[Bool]}
	}
	if !c.opDefined(x, x.Op, t) {
		return bad(x)
	}
	if l.mode == modeConst && r.mode == modeConst {
		if (x.Op == syntax.QUO || x.Op == syntax.REM) && constant.Sign(r.val) == 0 {
			c.errorf(x, CodeType, "division by zero")
			return bad(x)
		}
		tok := tokenOf[x.Op]
		if x.Op == syntax.QUO && isInteger(t) {
			tok = token.QUO_ASSIGN // integer division
		}
		var v constant.Value
		if tok == token.QUO_ASSIGN {
			v = constant.BinaryOp(constant.ToInt(l.val), token.QUO_ASSIGN, constant.ToInt(r.val))
		} else {
			v = constant.BinaryOp(l.val, tok, r.val)
		}
		// Exact arithmetic; the typed result must fit (196u rule 3).
		res := operand{mode: modeConst, typ: t, val: v, expr: x}
		if b, ok := under(t).(*Basic); ok {
			if !c.representable(&res, b) {
				return bad(x)
			}
		}
		return res
	}
	if (x.Op == syntax.QUO || x.Op == syntax.REM) && r.mode == modeConst && constant.Sign(r.val) == 0 && isInteger(t) {
		c.errorf(x, CodeType, "division by zero")
		return bad(x)
	}
	return operand{mode: modeValue, typ: t}
}

// matchTypes reports operands of two types (S108): both operands of a
// binary operator have one type (196u rule 4), and == and != also compare a
// value with an interface it is assignable to. A literal has already taken
// the other operand's type when it could (operands), so a mismatch names
// the literal form or the conversion to write.
func (c *checker) matchTypes(x *syntax.BinaryExpr, l, r *operand) bool {
	if c.identical(l.typ, r.typ) {
		return true
	}
	if x.Op == syntax.EQL || x.Op == syntax.NEQ {
		// A value that satisfies an operation type through its functions
		// (196m) is not that type's value until it is converted, so == has
		// no meaning to lower (AGENTS.md: never an unchecked passthrough).
		for _, p := range [][2]Type{{l.typ, r.typ}, {r.typ, l.typ}} {
			if asInterface(p[1]) != nil && asInterface(p[0]) == nil && len(asInterface(p[1]).Methods) > 0 {
				if ok, _ := c.assignable(p[0], p[1]); ok {
					c.errorf(x, CodeType, "cannot compare %s with %s: a %s satisfies %s through its functions, so it is not a %s value; compare two values of one type", l.typ, r.typ, p[0], p[1], p[1])
					return false
				}
			}
		}
		if ok, _ := c.assignable(l.typ, r.typ); ok {
			l.typ = r.typ
			return true
		}
		if ok, _ := c.assignable(r.typ, l.typ); ok {
			r.typ = l.typ
			return true
		}
	}
	switch ll, rl := c.isLiteral(x.X), c.isLiteral(x.Y); {
	case ll && !rl:
		if msg := literalMismatch(x.X, r.typ); msg != "" {
			c.errorf(x.X, CodeType, "%s", msg)
			return false
		}
	case rl && !ll:
		if msg := literalMismatch(x.Y, l.typ); msg != "" {
			c.errorf(x.Y, CodeType, "%s", msg)
			return false
		}
	}
	c.errorf(x, CodeType, "%s", c.mismatchText(x, l.typ, r.typ))
	return false
}

func ordered(t Type) bool {
	b, ok := under(t).(*Basic)
	return ok && b.Kind != Bool && b.Kind != Invalid && !isComplex(b)
}

func (c *checker) opDefined(x syntax.Node, op syntax.Token, t Type) bool {
	ok := false
	switch op {
	case syntax.ADD:
		ok = isNumeric(t) || isString(t) || isDuration(t)
	case syntax.SUB:
		ok = isNumeric(t) || isDuration(t)
	case syntax.MUL, syntax.QUO:
		ok = isNumeric(t)
	case syntax.REM, syntax.AND, syntax.OR, syntax.XOR, syntax.AND_NOT:
		ok = isInteger(t)
	case syntax.LAND, syntax.LOR:
		ok = isBoolean(t)
	}
	if !ok {
		c.errorf(x, CodeType, "operator %s is not defined on %s", op, t)
	}
	return ok
}

// shift checks x << n and x >> n (spec D-A3). A literal left operand takes
// the expected type when that is an integer type, else int: a shift is
// never typed from a non-integer context, so a float, string, bool or
// duration context is S108 with the conversion to write (Go types 1 << n
// from the context it would have without the shift, a known trap). The
// count is any integer type, and a literal count is an int.
func (c *checker) shift(x *syntax.BinaryExpr, hint Type) operand {
	lhint := hint
	if c.isLiteral(x.X) {
		if b, ok := under0(hint).(*Basic); ok && !isInteger(b) {
			left := "1"
			if lit, ok := unparen(x.X).(*syntax.BasicLit); ok {
				left = lit.Value
			}
			count := exprText(x.Y)
			if lit, ok := unparen(x.Y).(*syntax.BasicLit); ok {
				count = lit.Value
			} else if count == "this expression" {
				count = "n"
			}
			c.valueExpr(x.Y, nil)
			c.errorf(x.X, CodeType, "the left operand of a shift is an integer, and %s is expected here; write int(%s) %s %s, then convert the result", hint, left, x.Op, count)
			return bad(x)
		}
		if !isInteger(hint) {
			lhint = nil
		}
	}
	l := c.valueExpr(x.X, lhint)
	r := c.valueExpr(x.Y, nil)
	if l.invalid() || r.invalid() {
		return bad(x)
	}
	if !isInteger(r.typ) {
		c.errorf(x.Y, CodeType, "a shift count must be an integer, not %s", r.typ)
		return bad(x)
	}
	if r.mode == modeConst && constant.Sign(r.val) < 0 {
		c.errorf(x.Y, CodeType, "negative shift count")
		return bad(x)
	}
	lt := l.typ
	if !isInteger(lt) {
		c.errorf(x, CodeType, "operator %s is not defined on %s", x.Op, l.typ)
		return bad(x)
	}
	if l.mode == modeConst && r.mode == modeConst {
		s, _ := constant.Uint64Val(r.val)
		if s > 10000 {
			c.errorf(x.Y, CodeType, "shift count too large")
			return bad(x)
		}
		v := constant.Shift(constant.ToInt(l.val), tokenOf[x.Op], uint(s))
		res := operand{mode: modeConst, typ: lt, val: v, expr: x}
		if b, ok := under(lt).(*Basic); ok {
			if !c.representable(&res, b) {
				return bad(x)
			}
		}
		return res
	}
	return operand{mode: modeValue, typ: lt}
}

// conversion checks T(x) (Go's conversion rules over the language's
// types). A literal operand takes no expected type (D-A3): it is typed on
// its own and then converted, so float64(1 << n) converts an int shift.
func (c *checker) conversion(x *syntax.CallExpr, t Type) operand {
	if len(x.Args) != 1 || x.Trailing != nil {
		c.errorf(x, CodeType, "a conversion to %s takes one value", t)
		return bad(x)
	}
	hint := t
	if c.isLiteral(x.Args[0]) {
		hint = nil
		if msg := convertedLiteralOverflow(x.Args[0], t); msg != "" {
			c.errorf(x.Args[0], CodeType, "%s", msg)
			return bad(x)
		}
	}
	op := c.valueExpr(x.Args[0], hint)
	if op.invalid() || containsInvalid(t) || containsInvalid(op.typ) {
		// Already reported where the invalid part came from: a refused
		// definition adds nothing at its conversions (D3).
		return bad(x)
	}
	if !c.convertible(op.typ, t) && !constNumeric(op, t) {
		c.errorf(x, CodeType, "%s", c.conversionRefusal(x.Args[0], op.typ, t))
		return bad(x)
	}
	if op.mode == modeConst {
		if tb, ok := under(t).(*Basic); ok {
			ob, _ := under(op.typ).(*Basic)
			if ob == nil {
				return operand{mode: modeValue, typ: t}
			}
			res := operand{mode: modeConst, typ: t, val: op.val, expr: x}
			if isInteger(tb) && isFloat(ob) {
				if constant.ToInt(op.val).Kind() != constant.Int {
					c.errorf(x, CodeType, "the constant %s truncates to an integer", op.val.ExactString())
					return bad(x)
				}
			}
			if !c.representable(&res, tb) {
				return bad(x)
			}
			return res
		}
	}
	return operand{mode: modeValue, typ: t}
}

// convertedLiteralOverflow is the S108 message for an integer literal,
// signed or not, that a conversion to the numeric type t would take but
// its default int cannot hold: uint64(18446744073709551615). A
// conversion's literal takes its default (D-A3), so the fix writes the
// literal where t is expected. It is "" for any other operand.
func convertedLiteralOverflow(x syntax.Expr, t Type) string {
	lit, sign, ok := signedLit(x)
	tb, isBasic := under0(t).(*Basic)
	if !ok || lit.Kind != syntax.INT || !isBasic || !isNumeric(tb) || len(lit.Value) > 1 && lit.Value[0] == '0' {
		return ""
	}
	v := constant.MakeFromLiteral(lit.Value, token.INT, 0)
	if v.Kind() != constant.Int {
		return ""
	}
	if sign == "-" {
		v = constant.UnaryOp(token.SUB, v, 0)
	}
	lo, hi := intBounds(Int)
	if !constant.Compare(v, token.LSS, lo) && !constant.Compare(v, token.GTR, hi) {
		return ""
	}
	if isInteger(tb) {
		// Only a fix that fits t is named; else the plain overflow stands.
		if lo, hi := intBounds(tb.Kind); constant.Compare(v, token.LSS, lo) || constant.Compare(v, token.GTR, hi) {
			return ""
		}
	} else if !isFloat(tb) {
		return ""
	}
	return "the constant " + v.ExactString() + " overflows int, the type a conversion's literal takes; write it where " + t.String() + " is expected: let x " + t.String() + " = " + v.ExactString()
}

// convertible reports whether T(x) converts a value of type v to t (spec
// D-B7, parse-v1's round-4 ruling): between numeric types (Go's rules: an
// integer or float to an integer or float, a complex to a complex),
// between duration and int64, and between a defined type and the type it
// is defined over, in both directions; and a value to its own type.
// Nothing else converts: not two defined types over one type (convert
// through the base), not two records, not an integer to string, not a
// variant to its union or a value to an operation type (assignment does
// those). One hop is visible: `int(c)` of a Celsius over float64 is
// `int(float64(c))`.
//
// Prior art: Gleam and ReScript convert only through functions
// (int.to_float); MoonBit uses to_int64() style methods; Go converts
// between any types with one underlying type, and string(65) is a vet
// warning.
func (c *checker) convertible(v, t Type) bool {
	if c.identical(v, t) {
		return true
	}
	vb, vok := v.(*Basic)
	tb, tok := t.(*Basic)
	if vok && tok {
		switch {
		case isNumeric(vb) && isNumeric(tb) && isComplex(vb) == isComplex(tb):
			return true
		case vb.Kind == Duration && tb.Kind == Int64, tb.Kind == Duration && vb.Kind == Int64:
			return true
		}
		return false
	}
	if n, ok := t.(*Named); ok {
		if base := n.Defined(); base != nil && c.identical(base, v) {
			return true
		}
	}
	if n, ok := v.(*Named); ok {
		if base := n.Defined(); base != nil && c.identical(base, t) {
			return true
		}
	}
	return false
}

// constNumeric reports a constant of a numeric basic type converted to a
// numeric basic type: Go converts a constant to any numeric type that
// represents it, complex128(1) included (the representability check
// follows in conversion).
func constNumeric(op operand, t Type) bool {
	vb, vok := op.typ.(*Basic)
	tb, tok := t.(*Basic)
	return op.mode == modeConst && vok && tok && isNumeric(vb) && isNumeric(tb)
}

// conversionRefusal is the S108 message for a conversion convertible
// refuses, naming the Ergo form to write instead.
func (c *checker) conversionRefusal(arg syntax.Expr, v, t Type) string {
	name := exprText(arg)
	if lit, sign, ok := signedLit(arg); ok {
		name = sign + lit.Value
	}
	if name == "this expression" {
		name = "x"
	}
	switch {
	case isString(t) && isInteger(v):
		return fmt.Sprintf("an integer does not convert to string; write \"\\(%s)\" for its decimal text (types.md, Conversions)", name)
	case isUnion(t) && !isUnion(v):
		return fmt.Sprintf("%s is not converted to %s: assignment puts a variant into its union (let u %s = %s) (types.md, Conversions)", v, t, c.localTypeText(t), name)
	case asInterface(t) != nil && asInterface(v) == nil:
		return fmt.Sprintf("%s is not converted to %s: assignment puts a value into an operation type (let s %s = %s) (types.md, Conversions)", v, t, c.localTypeText(t), name)
	}
	vn, _ := v.(*Named)
	tn, _ := t.(*Named)
	if vn != nil && vn.Defined() != nil {
		if base := vn.Defined(); c.convertible(base, t) {
			return fmt.Sprintf("cannot convert %s to %s in one step; convert through the type it is defined over: %s(%s(%s)) (types.md, Conversions)", v, t, c.localTypeText(t), c.localTypeText(base), name)
		}
	}
	if tn != nil && tn.Defined() != nil {
		if base := tn.Defined(); c.convertible(v, base) {
			return fmt.Sprintf("cannot convert %s to %s in one step; convert through the type it is defined over: %s(%s(%s)) (types.md, Conversions)", v, t, c.localTypeText(t), c.localTypeText(base), name)
		}
	}
	return fmt.Sprintf("cannot convert %s to %s: a conversion is between numeric types, between duration and int64, or between a defined type and the type it is defined over (types.md, Conversions)", v, t)
}
