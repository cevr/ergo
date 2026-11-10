package check

import (
	"go/constant"
	"go/token"
	"strconv"
	"strings"

	"ergo.local/prototype/internal/sketch/syntax"
)

// Literals are typed where they appear (196u; spec D-A1 to D-A3). A
// literal takes the type its context expects when it may (takes), else its
// default (literalDefault): int, float64, rune, string, duration, and bool
// for true and false. There are no untyped kinds, no defaulting order and
// no update pass: Info records each literal's one type when it is checked.
//
// The expected-type sites (D-A2) pass their type as the hint of expr, and
// TestLiteralSites has a row for each:
//   - a let with a written type (letStmt, checkPkgVar);
//   - a parameter, after the type arguments that typed arguments fixed
//     (argument, callGeneric: literalArgs);
//   - the result of a function or literal with a written result: a return
//     and a block's tail value (returnStmt, yieldingBlock);
//   - a record field, a slice element, a Map key and value (structLit,
//     elements, compositeLit, index);
//   - the other operand of a binary operator or of `..` (operands);
//   - an assignment's target (assignStmt);
//   - an index and a slice bound: int (intIndex);
//   - a constant case of a match: the subject's type (switchStmt).
//
// Every other position passes no hint, so the default applies: a let
// without a type, the operand of a conversion (D-A3), an interpolated
// value, a match subject, a shift count and a comparison of two literals.

// literalDefault is the type a basic literal of kind takes where no type
// is expected (196u rule 2).
func literalDefault(kind syntax.Token) Type {
	switch kind {
	case syntax.INT:
		return Typ[Int]
	case syntax.FLOAT:
		return Typ[Float64]
	case syntax.RUNE:
		return Typ[Int32]
	case syntax.STRING:
		return Typ[String]
	case syntax.DURATION:
		return Typ[Duration]
	}
	return invalid
}

// takes reports whether a basic literal of kind may take the type t (196u
// rule 1): an integer literal any integer or float type, a float literal a
// float type, a rune literal an integer type, a string literal a string
// type and a duration literal a duration type. An integer literal is never
// a duration.
func takes(kind syntax.Token, t Type) bool {
	b, ok := under0(t).(*Basic)
	if !ok {
		return false
	}
	switch kind {
	case syntax.INT:
		return isInteger(b) || isFloat(b)
	case syntax.FLOAT:
		return isFloat(b)
	case syntax.RUNE:
		return isInteger(b)
	case syntax.STRING:
		return isString(b)
	case syntax.DURATION:
		return b.Kind == Duration
	}
	return false
}

// literal checks a basic literal where hint is expected: it takes hint
// when it may, else its default, and its value fits that type (S108).
func (c *checker) literal(x *syntax.BasicLit, hint Type) operand {
	op := c.basicLit(x, hint)
	if op.invalid() {
		return op
	}
	op.expr = x
	if !c.representable(&op, under(op.typ).(*Basic)) {
		return bad(x)
	}
	if !c.refinedConst(x, op.val, op.typ) {
		return bad(x)
	}
	return op
}

// boolLiteral gives true or false, the only constants a name denotes, the
// type a bool context expects; their default is bool.
func (c *checker) boolLiteral(op operand, hint Type) operand {
	if op.mode == modeConst && hint != nil && isBoolean(hint) {
		op.typ = hint
	}
	return op
}

// basicLit types a basic literal without the fit check, which literal and
// a negated literal (unary) make on the final value.
func (c *checker) basicLit(x *syntax.BasicLit, hint Type) operand {
	t := literalDefault(x.Kind)
	if hint != nil && takes(x.Kind, hint) {
		t = hint
	}
	switch x.Kind {
	case syntax.INT:
		if legacyOctal(x.Value) {
			c.errorf(x, CodeType, "the integer literal %s has a leading zero; Go would read it as octal, so write 0o%s for octal (grammar.md, Literals 1)", x.Value, strings.TrimLeft(x.Value, "0_"))
			return bad(x)
		}
		v := constant.MakeFromLiteral(x.Value, token.INT, 0)
		if v.Kind() == constant.Unknown {
			c.errorf(x, CodeType, "malformed integer literal %s", x.Value)
			return bad(x)
		}
		return operand{mode: modeConst, typ: t, val: v}
	case syntax.DURATION:
		i := 0
		for i < len(x.Value) && (x.Value[i] >= '0' && x.Value[i] <= '9' || x.Value[i] == '_') {
			i++
		}
		digits, unit := x.Value[:i], x.Value[i:]
		if legacyOctal(digits) {
			c.errorf(x, CodeType, "the duration literal %s has a leading zero", x.Value)
			return bad(x)
		}
		scale := map[string]int64{"ns": 1, "us": 1e3, "ms": 1e6, "s": 1e9, "m": 60e9, "h": 3600e9}[unit]
		n := constant.MakeFromLiteral(digits, token.INT, 0)
		v := constant.BinaryOp(n, token.MUL, constant.MakeInt64(scale))
		if !fitsInt64(v) {
			c.errorf(x, CodeType, "the duration %s overflows int64 nanoseconds (grammar.md, Literals 2)", x.Value)
			return bad(x)
		}
		return operand{mode: modeConst, typ: t, val: v}
	case syntax.STRING:
		s, err := strconv.Unquote(x.Value)
		if err != nil {
			c.errorf(x, CodeType, "invalid escape in the string literal %s (grammar.md, Literals 3)", x.Value)
			return bad(x)
		}
		return operand{mode: modeConst, typ: t, val: constant.MakeString(s)}
	case syntax.RUNE:
		s, err := strconv.Unquote(x.Value)
		if err != nil {
			c.errorf(x, CodeType, "malformed rune literal %s", x.Value)
			return bad(x)
		}
		r := []rune(s)
		if len(r) != 1 {
			c.errorf(x, CodeType, "malformed rune literal %s", x.Value)
			return bad(x)
		}
		return operand{mode: modeConst, typ: t, val: constant.MakeInt64(int64(r[0]))}
	case syntax.FLOAT:
		// Go's float literal forms (grammar.md, Lexical), checked by the
		// same constant rules as a float constant made by arithmetic.
		v := constant.MakeFromLiteral(x.Value, token.FLOAT, 0)
		if v.Kind() == constant.Unknown {
			c.errorf(x, CodeType, "malformed float literal %s", x.Value)
			return bad(x)
		}
		return operand{mode: modeConst, typ: t, val: v}
	}
	c.errorf(x, CodeType, "unexpected literal")
	return bad(x)
}

// legacyOctal reports whether the decimal digits lit start with a zero and
// have more than one digit (07, 0_7), which Go reads as octal (grammar.md,
// Literals 1). A literal with a base prefix (0x, 0o, 0b) is not one.
func legacyOctal(lit string) bool {
	if len(lit) < 2 || lit[0] != '0' {
		return false
	}
	switch lit[1] {
	case 'x', 'X', 'o', 'O', 'b', 'B':
		return false
	}
	return true
}

// isLiteral reports whether x is a literal expression, whose type comes
// from its context: a basic literal, true or false, a unary or arithmetic
// operation on literals, and a shift of a literal (D-A3), in parentheses
// or not. A comparison is not one: its type is bool. The answer for an
// operation is kept, so a chain of n operators costs linear work.
func (c *checker) isLiteral(x syntax.Expr) bool {
	switch x := x.(type) {
	case *syntax.BasicLit:
		return true
	case *syntax.Ident:
		_, isConst := c.lookup(x.Name).(*Const)
		return isConst
	case *syntax.ParenExpr:
		return c.isLiteral(x.X)
	case *syntax.UnaryExpr, *syntax.BinaryExpr:
	default:
		return false
	}
	if known, ok := c.literals[x]; ok {
		return known
	}
	lit := false
	switch x := x.(type) {
	case *syntax.UnaryExpr:
		lit = x.Op != syntax.AND && c.isLiteral(x.X)
	case *syntax.BinaryExpr:
		switch {
		case x.Op == syntax.SHL || x.Op == syntax.SHR:
			lit = c.isLiteral(x.X)
		case isComparison(x.Op) || x.Op == syntax.DOTDOT:
		default:
			lit = c.isLiteral(x.X) && c.isLiteral(x.Y)
		}
	}
	if c.literals == nil {
		c.literals = map[syntax.Expr]bool{}
	}
	c.literals[x] = lit
	return lit
}

// operands checks the two operands of a binary operator or a range. A
// literal operand takes the other operand's type (196u rule 4), so the
// other is checked first; two literals, or two operands that are not,
// each take hint. A composite literal of a generic type written without
// type arguments takes them from the other operand the same way, so
// f() == Some{Value: A} with f() an Option[T] is an Option[T] comparison
// (types.md, Basic types 6).
func (c *checker) operands(lx, rx syntax.Expr, hint Type) (operand, operand) {
	switch ll, rl := c.isLiteral(lx) || c.bareGeneric(lx), c.isLiteral(rx) || c.bareGeneric(rx); {
	case ll && !rl:
		r := c.valueExpr(rx, hint)
		return c.valueExpr(lx, typeOrNil(r)), r
	case rl && !ll:
		l := c.valueExpr(lx, hint)
		return l, c.valueExpr(rx, typeOrNil(l))
	}
	return c.valueExpr(lx, hint), c.valueExpr(rx, hint)
}

// bareGeneric reports a composite literal whose written type is a generic
// type with no type arguments, Some{Value: A} or pkg.Some{Value: A}: it
// takes its instance from an expected type when it has one (literalType),
// else from its field values.
func (c *checker) bareGeneric(x syntax.Expr) bool {
	cl, ok := unparen(x).(*syntax.CompositeLit)
	if !ok || cl.Base != nil || cl.Type == nil {
		return false
	}
	var o Object
	switch t := cl.Type.(type) {
	case *syntax.Ident:
		o = c.lookupType(t.Name, c.localTypeCtx())
	case *syntax.SelectorExpr:
		id, ok := t.X.(*syntax.Ident)
		if !ok {
			return false
		}
		pn, ok := c.lookupType(id.Name, nil).(*PkgName)
		if !ok || pn.broken {
			return false
		}
		o = pn.Pkg.Scope.names[t.Sel.Name]
	default:
		return false
	}
	tn, ok := o.(*TypeName)
	if !ok {
		return false
	}
	n, ok := tn.Type.(*Named)
	return ok && n.Orig == nil && len(n.TParams) > 0
}

func typeOrNil(op operand) Type {
	if op.invalid() {
		return nil
	}
	return op.typ
}

// mismatchText is the S108 message for operands of two types: two literals
// that took different defaults name the literal form to write (196u rule
// 4: `1 + 2.5` is "write 1.0 + 2.5"); a named operand names the conversion.
func (c *checker) mismatchText(x *syntax.BinaryExpr, l, r Type) string {
	msg := "mismatched types " + l.String() + " and " + r.String()
	if c.isLiteral(x.X) && c.isLiteral(x.Y) {
		lt, lok := floatForm(x.X, r)
		rt, rok := floatForm(x.Y, l)
		if lt != "" && rt != "" && (lok || rok) {
			return msg + "; write " + lt + " " + x.Op.String() + " " + rt
		}
		if isNumeric(l) && isNumeric(r) {
			return msg + "; both literals take their defaults, so convert one to the other's type, e.g. " + r.String() + "(…)"
		}
		// "a" + 'b': no conversion of one literal gives the other's type.
		return msg + "; both literals take their defaults"
	}
	if id, ok := unparen(x.X).(*syntax.Ident); ok && isNumeric(l) && isNumeric(r) {
		return msg + "; write " + r.String() + "(" + id.Name + ")"
	}
	return msg
}

// floatForm writes a basic literal as a float literal when the other
// operand is a float, so `1` beside `2.5` reads `1.0`, and reports whether
// it rewrote it; another literal is written as it is. It returns "" for
// anything but a basic literal.
func floatForm(x syntax.Expr, other Type) (string, bool) {
	lit, ok := unparen(x).(*syntax.BasicLit)
	if !ok {
		return "", false
	}
	if lit.Kind == syntax.INT && isFloat(other) && !strings.ContainsAny(lit.Value, "xXoObB") {
		return lit.Value + ".0", true
	}
	return lit.Value, false
}

// signedLit is x's basic literal under any signs and parentheses, with the
// net sign ("-" or ""): -1e3 and -(-1e3) are a float literal of value
// -1000 and 1000.
func signedLit(x syntax.Expr) (*syntax.BasicLit, string, bool) {
	sign := ""
	for {
		x = unparen(x)
		u, ok := x.(*syntax.UnaryExpr)
		if !ok || u.Op != syntax.SUB && u.Op != syntax.ADD {
			break
		}
		if u.Op == syntax.SUB {
			sign = map[string]string{"": "-", "-": ""}[sign]
		}
		x = u.X
	}
	lit, ok := x.(*syntax.BasicLit)
	return lit, sign, ok
}

// literalMismatch is the S108 message for a literal that cannot take its
// target type t (196u rule 1), or "" when x is not a basic literal, signed
// or not.
func literalMismatch(x syntax.Expr, t Type) string {
	lit, sign, ok := signedLit(x)
	if !ok {
		return ""
	}
	switch {
	case lit.Kind == syntax.INT && isDuration(t):
		return "an integer literal is not a duration: write a duration literal such as " + sign + lit.Value + "s"
	case lit.Kind == syntax.FLOAT && isInteger(t):
		fix := "write an integer literal"
		if iv := constant.ToInt(constant.MakeFromLiteral(lit.Value, token.FLOAT, 0)); iv.Kind() == constant.Int {
			fix = "write " + sign + iv.ExactString()
		}
		return "a float literal takes only a float type, not " + t.String() + "; " + fix
	case lit.Kind == syntax.RUNE && !isInteger(t):
		return "a rune literal takes only an integer type, not " + t.String()
	case lit.Kind == syntax.STRING && !isString(t):
		return "a string literal takes only a string type, not " + t.String()
	}
	return ""
}
