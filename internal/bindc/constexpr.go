package bindc

import (
	"math/big"
	"strings"
)

// A constant expression of the subset: decimal or hex literals without a
// suffix, INTn_C(lit) and UINTn_C(lit), names of earlier constants, unary
// minus, parentheses, and the binary operators *, +, -, << and |.
//
// A name of an earlier constant stands for its replacement tokens, as in
// the C preprocessor: the body is expanded first and the expansion is
// parsed, so `#define A 1 + 2` and `#define B A * 3` give B = 1 + 2 * 3 = 7.
// The bodies hold no function-like macro but INTn_C and UINTn_C, whose
// argument is a literal, and no name of a later macro, so one substitution
// of each name by the full expansion of its earlier body is the rescan of
// C (C11 6.10.3.4).
//
// The evaluator follows C11 on an LP64 target exactly: literal types, the
// integer promotions, the usual arithmetic conversions, unsigned wrap-around.
// Signed overflow, a negative left operand of <<, and a shift count out of
// range are undefined behavior in C and a diagnostic here.

type expr interface{ render() string }

type (
	eLit  struct{ tok token }
	eName struct{ tok token }
	eCall struct{ fn, arg token }
	eNeg  struct {
		op token
		x  expr
	}
	eParen struct{ x expr }
	eBin   struct {
		op   token
		x, y expr
	}
)

func (e eLit) render() string   { return e.tok.text }
func (e eName) render() string  { return e.tok.text }
func (e eCall) render() string  { return e.fn.text + "(" + e.arg.text + ")" }
func (e eParen) render() string { return "(" + e.x.render() + ")" }
func (e eBin) render() string   { return e.x.render() + " " + e.op.text + " " + e.y.render() }
func (e eNeg) render() string {
	s := e.x.render()
	if strings.HasPrefix(s, "-") {
		return "- " + s
	}
	return "-" + s
}

// cval is the value of a constant expression: its C type (Int32, Uint32,
// Int64 or Uint64) and its mathematical value, in range for the type.
type cval struct {
	typ Scalar
	v   *big.Int
}

// exprError is the one diagnostic of a refused expression.
type exprError struct {
	pos  Pos
	code Code
	msg  string
}

// failure carries one diagnostic out of the parser or the evaluator.
type failure struct{ err *exprError }

func fail(t token, c Code, msg string) {
	panic(failure{&exprError{t.pos, c, msg}})
}

// catch turns a failure panic into its diagnostic.
func catch(err **exprError) {
	if r := recover(); r != nil {
		f, ok := r.(failure)
		if !ok {
			panic(r)
		}
		*err = f.err
	}
}

type exprParser struct {
	toks []token
	i    int
	// known reports whether name is an earlier constant.
	known func(name string) bool
	// stdint reports whether <stdint.h> is included before pos.
	stdint func(pos Pos) bool
}

func (p *exprParser) peek() token {
	if p.i < len(p.toks) {
		return p.toks[p.i]
	}
	return token{kind: tEOF}
}

// parseExpr parses an expression of the subset. end is the position
// reported for a missing operand at the end of toks.
func parseExpr(toks []token, end Pos, known func(string) bool, stdint func(Pos) bool) (e expr, err *exprError) {
	defer catch(&err)
	p := &exprParser{toks: append(append([]token{}, toks...), token{kind: tEOF, pos: end}), known: known, stdint: stdint}
	e = p.or()
	if t := p.peek(); t.kind != tEOF {
		p.refuse(t)
	}
	return e, nil
}

// parseConstExpr reads the body of a #define. macro gives the replacement
// tokens of an earlier constant, already expanded. It returns the body as
// written (its render is the model's Expr), the body's own expansion (the
// replacement tokens of this constant for later bodies) and the value C
// gives the expansion. A token that comes from the expansion of a name
// takes the position of that name, so every diagnostic points into this
// body.
func parseConstExpr(toks []token, end Pos, macro func(string) ([]token, bool), stdint func(Pos) bool) (body expr, expansion []token, v cval, err *exprError) {
	known := func(name string) bool { _, ok := macro(name); return ok }
	if body, err = parseExpr(toks, end, known, stdint); err != nil {
		return nil, nil, cval{}, err
	}
	for _, t := range toks {
		r, ok := macro(t.text)
		if t.kind != tIdent || !ok {
			expansion = append(expansion, t)
			continue
		}
		for _, rt := range r {
			rt.pos, rt.bol = t.pos, false
			expansion = append(expansion, rt)
		}
	}
	// The expansion of a valid body is valid: each name stands for a whole
	// expression of operands joined by binary operators, which is again
	// such a sequence where the name was an operand.
	x, err := parseExpr(expansion, end, func(string) bool { return false }, stdint)
	if err != nil {
		return nil, nil, cval{}, err
	}
	if v, err = eval(x); err != nil {
		return nil, nil, cval{}, err
	}
	return body, expansion, v, nil
}

func (p *exprParser) refuse(t token) {
	switch t.kind {
	case tPunct:
		fail(t, CodeMacroBody, "operator "+quote(t.text)+" is outside the constant subset")
	case tEOF:
		fail(t, CodeMacroBody, "missing operand")
	}
	fail(t, CodeMacroBody, quote(t.text)+" is outside the constant subset")
}

func quote(s string) string { return "`" + s + "`" }

// binary parses operands joined by the operators of one precedence level,
// left to right.
func (p *exprParser) binary(operand func() expr, ops ...string) expr {
	x := operand()
	for {
		t := p.peek()
		found := false
		for _, op := range ops {
			found = found || t.is(op)
		}
		if !found {
			return x
		}
		p.next()
		x = eBin{t, x, operand()}
	}
}

func (p *exprParser) or() expr  { return p.binary(p.shl, "|") }
func (p *exprParser) shl() expr { return p.binary(p.add, "<<") }
func (p *exprParser) add() expr { return p.binary(p.mul, "+", "-") }
func (p *exprParser) mul() expr { return p.binary(p.unary, "*") }

func (p *exprParser) next() token { t := p.peek(); p.i++; return t }

func (p *exprParser) unary() expr {
	if t := p.peek(); t.is("-") {
		p.next()
		return eNeg{t, p.unary()}
	}
	return p.primary()
}

// intCMacros maps INTn_C and UINTn_C to the bound of their argument and the
// type of their expansion (glibc and Apple libc agree: UINT8_C, UINT16_C,
// INT8_C, INT16_C and INT32_C expand to the literal itself, which has type
// int in range; UINT32_C appends U, UINT64_C appends UL or ULL, INT64_C
// appends L or LL).
var intCMacros = map[string]struct {
	max *big.Int
	typ Scalar
}{
	"INT8_C":   {big.NewInt(1<<7 - 1), Int32},
	"INT16_C":  {big.NewInt(1<<15 - 1), Int32},
	"INT32_C":  {big.NewInt(1<<31 - 1), Int32},
	"INT64_C":  {big.NewInt(1<<63 - 1), Int64},
	"UINT8_C":  {big.NewInt(1<<8 - 1), Int32},
	"UINT16_C": {big.NewInt(1<<16 - 1), Int32},
	"UINT32_C": {big.NewInt(1<<32 - 1), Uint32},
	"UINT64_C": {new(big.Int).SetUint64(1<<64 - 1), Uint64},
}

func (p *exprParser) primary() expr {
	t := p.next()
	switch {
	case t.kind == tNumber:
		if _, typ, msg := parseLiteral(t.text); msg != "" {
			code := CodeMacroBody
			if typ == "range" {
				code = CodeConstantRange
			}
			fail(t, code, msg)
		}
		return eLit{t}
	case t.is("("):
		x := p.or()
		if c := p.next(); !c.is(")") {
			p.i--
			p.refuse(c)
		}
		return eParen{x}
	case t.kind == tIdent:
		if m, ok := intCMacros[t.text]; ok {
			if !p.stdint(t.pos) {
				fail(t, CodeMissingInclude, t.text+" needs #include <stdint.h> before it")
			}
			if o := p.next(); !o.is("(") {
				p.i--
				fail(t, CodeMacroBody, t.text+" needs one literal argument")
			}
			arg := p.next()
			if arg.kind != tNumber {
				p.i--
				fail(arg, CodeMacroBody, t.text+" takes one integer literal without a sign")
			}
			// The argument is an integer constant of the macro's type,
			// pasted to its suffix (C11 7.20.4): its value counts, not the
			// type the bare literal would have.
			v, msg := literalValue(arg.text)
			if msg != "" {
				fail(arg, CodeMacroBody, msg)
			}
			if v.Cmp(m.max) > 0 {
				fail(arg, CodeConstantRange, arg.text+" is out of range for "+t.text)
			}
			if c := p.next(); !c.is(")") {
				p.i--
				fail(c, CodeMacroBody, t.text+" takes one argument")
			}
			return eCall{t, arg}
		}
		if !p.known(t.text) {
			fail(t, CodeMacroBody, quote(t.text)+" is not the name of an earlier constant")
		}
		return eName{t}
	}
	p.i--
	p.refuse(t)
	return nil
}

// eval gives the value C gives an expression with no names (an expansion).
func eval(e expr) (v cval, err *exprError) {
	defer catch(&err)
	return evalExpr(e), nil
}

func evalExpr(e expr) cval {
	switch e := e.(type) {
	case eLit:
		v, typ, _ := parseLiteral(e.tok.text)
		return cval{typ, v}
	case eCall:
		v, _ := literalValue(e.arg.text)
		return cval{intCMacros[e.fn.text].typ, v}
	case eParen:
		return evalExpr(e.x)
	case eNeg:
		x := evalExpr(e.x)
		return fit(e.op, x.typ, new(big.Int).Neg(x.v))
	case eBin:
		x, y := evalExpr(e.x), evalExpr(e.y)
		if e.op.is("<<") {
			return shift(e.op, x, y)
		}
		return arith(e.op, x, y)
	case eName:
		fail(e.tok, CodeMacroBody, quote(e.tok.text)+" is not expanded")
	}
	panic("bindc: unknown expression")
}

// literalValue reads a decimal or hex integer literal without a suffix.
func literalValue(s string) (*big.Int, string) {
	hex := strings.HasPrefix(s, "0x") || strings.HasPrefix(s, "0X")
	digits, base := s, 10
	if hex {
		digits, base = s[2:], 16
	}
	bad := "literal " + quote(s) + " is outside the subset (decimal or hex integer literals without a suffix)"
	if digits == "" || !hex && len(digits) > 1 && digits[0] == '0' {
		return nil, bad
	}
	v, ok := new(big.Int).SetString(digits, base)
	if !ok || strings.ContainsAny(digits, "+-_") {
		return nil, bad
	}
	return v, ""
}

// parseLiteral reads a decimal or hex integer literal without a suffix and
// gives it its C type (C11 6.4.4.1). typ is "range" when msg reports a
// value too large for every type.
func parseLiteral(s string) (v *big.Int, typ Scalar, msg string) {
	v, msg = literalValue(s)
	if msg != "" {
		return nil, "", msg
	}
	hex := strings.HasPrefix(s, "0x") || strings.HasPrefix(s, "0X")
	order := []Scalar{Int32, Int64}
	if hex {
		order = []Scalar{Int32, Uint32, Int64, Uint64}
	}
	for _, t := range order {
		if inRange(v, t) {
			return v, t, ""
		}
	}
	return nil, "range", "literal " + quote(s) + " has no integer type"
}

func width(t Scalar) uint { return uint(scalarSize[t] * 8) }

func signed(t Scalar) bool { return t == Int32 || t == Int64 }

func bounds(t Scalar) (lo, hi *big.Int) {
	w := width(t)
	if signed(t) {
		hi = new(big.Int).Lsh(big.NewInt(1), w-1)
		lo = new(big.Int).Neg(hi)
		hi.Sub(hi, big.NewInt(1))
		return lo, hi
	}
	hi = new(big.Int).Lsh(big.NewInt(1), w)
	return big.NewInt(0), hi.Sub(hi, big.NewInt(1))
}

func inRange(v *big.Int, t Scalar) bool {
	lo, hi := bounds(t)
	return v.Cmp(lo) >= 0 && v.Cmp(hi) <= 0
}

// fit gives the result of an operation in type t: unsigned types wrap
// modulo 2^width, and a signed result out of range is undefined behavior.
func fit(op token, t Scalar, v *big.Int) cval {
	if !signed(t) {
		m := new(big.Int).Lsh(big.NewInt(1), width(t))
		return cval{t, new(big.Int).Mod(v, m)}
	}
	if !inRange(v, t) {
		fail(op, CodeConstantRange, "signed overflow in "+quote(op.text)+" (undefined behavior in C)")
	}
	return cval{t, v}
}

// common is the type of the usual arithmetic conversions (C11 6.3.1.8) for
// the four types a constant can have after the integer promotions.
func common(a, b Scalar) Scalar {
	if signed(a) == signed(b) {
		if width(a) >= width(b) {
			return a
		}
		return b
	}
	u, s := a, b
	if signed(a) {
		u, s = b, a
	}
	if width(u) >= width(s) {
		return u
	}
	return s
}

// convert converts v to type t (C11 6.3.1.3). Only conversions to an
// unsigned type change a value here: a signed type the usual conversions
// choose can hold every value of the other operand.
func convert(v cval, t Scalar) *big.Int {
	if !signed(t) {
		m := new(big.Int).Lsh(big.NewInt(1), width(t))
		return new(big.Int).Mod(v.v, m)
	}
	return v.v
}

func arith(op token, x, y cval) cval {
	t := common(x.typ, y.typ)
	a, b := convert(x, t), convert(y, t)
	r := new(big.Int)
	switch op.text {
	case "*":
		r.Mul(a, b)
	case "+":
		r.Add(a, b)
	case "-":
		r.Sub(a, b)
	case "|":
		w := width(t)
		r.Or(toBits(a, w), toBits(b, w))
		return cval{t, fromBits(r, t)}
	}
	return fit(op, t, r)
}

func shift(op token, x, y cval) cval {
	w := width(x.typ)
	if y.v.Sign() < 0 || y.v.Cmp(big.NewInt(int64(w))) >= 0 {
		fail(op, CodeConstantRange, "shift count out of range (undefined behavior in C)")
	}
	if signed(x.typ) && x.v.Sign() < 0 {
		fail(op, CodeConstantRange, "left shift of a negative value (undefined behavior in C)")
	}
	r := new(big.Int).Lsh(x.v, uint(y.v.Uint64()))
	return fit(op, x.typ, r)
}

func toBits(v *big.Int, w uint) *big.Int {
	m := new(big.Int).Lsh(big.NewInt(1), w)
	return new(big.Int).Mod(v, m)
}

func fromBits(b *big.Int, t Scalar) *big.Int {
	if !signed(t) {
		return b
	}
	w := width(t)
	if b.Bit(int(w-1)) == 1 {
		return new(big.Int).Sub(b, new(big.Int).Lsh(big.NewInt(1), w))
	}
	return b
}

// bits is the two's complement of v in 64 bits, as Constant.Bits holds it.
func bitsOf(v cval) uint64 {
	return toBits(v.v, width(v.typ)).Uint64()
}
