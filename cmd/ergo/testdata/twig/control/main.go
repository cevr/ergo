// Command control is the hand-written Go control of examples/twig: the
// same lexer, Pratt parser, resolver and tree walker, written as a Go
// programmer would write them: a tagged value struct, pointers for
// lists, records, closures and frames, and an int flow code for return,
// break and continue. It prints what the Ergo program prints, byte for
// byte, its counters included (twig run -stats), so that the receipts
// compare equal work.
//
//	control run [-stats] FILE
//	control check FILE
package main

import (
	"bufio"
	"errors"
	"io/fs"
	"math"
	"os"
	"strconv"
	"strings"
)

const usageText = "usage: twig run [-stats] FILE | twig check FILE | twig gen -seed S -n N"

// ---- tokens ----

type pos struct{ line, col int }

type tkind uint8

const (
	tIdent tkind = iota
	tKeyword
	tInt
	tFloat
	tStr
	tPunct
	tEnd
)

type token struct {
	kind  tkind
	text  string
	at    pos
	fresh bool
}

var keywords = []string{"fn", "let", "if", "else", "while", "for", "in", "return", "break", "continue", "true", "false", "nil", "and", "or", "not"}

var puncts = []string{"<=", ">=", "==", "!=", "(", ")", "{", "}", "[", "]", ",", ":", ".", "+", "-", "*", "/", "%", "<", ">", "="}

func isKeyword(w string) bool {
	for _, k := range keywords {
		if k == w {
			return true
		}
	}
	return false
}

func isDigit(c byte) bool  { return c >= '0' && c <= '9' }
func isLetter(c byte) bool { return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c == '_' }

type failure struct {
	at  pos
	msg string
}

func describe(t token) string {
	switch t.kind {
	case tEnd:
		return "the end"
	case tStr:
		return "a string"
	}
	return "\"" + t.text + "\""
}

func lex(src string) ([]token, *failure) {
	toks := make([]token, 0, len(src)/3+1)
	i, line, lineStart, fresh := 0, 1, 0, true
	for i < len(src) {
		c := src[i]
		if c == '\n' {
			i++
			line++
			lineStart = i
			fresh = true
			continue
		}
		if c == ' ' || c == '\t' || c == '\r' {
			i++
			continue
		}
		if c == '/' && i+1 < len(src) && src[i+1] == '/' {
			for i < len(src) && src[i] != '\n' {
				i++
			}
			continue
		}
		at := pos{line, i - lineStart + 1}
		start := i
		switch {
		case isLetter(c):
			for i < len(src) && (isLetter(src[i]) || isDigit(src[i])) {
				i++
			}
			w := src[start:i]
			k := tIdent
			if isKeyword(w) {
				k = tKeyword
			}
			toks = append(toks, token{k, w, at, fresh})
		case isDigit(c):
			for i < len(src) && isDigit(src[i]) {
				i++
			}
			k := tInt
			if i+1 < len(src) && src[i] == '.' && isDigit(src[i+1]) {
				i++
				for i < len(src) && isDigit(src[i]) {
					i++
				}
				k = tFloat
			}
			toks = append(toks, token{k, src[start:i], at, fresh})
		case c == '"':
			text, end, f := quoted(src, i+1, at)
			if f != nil {
				return nil, f
			}
			i = end
			toks = append(toks, token{tStr, text, at, fresh})
		default:
			p := punctAt(src, i)
			if p == "" {
				return nil, &failure{at, "unexpected character " + strconv.Quote(src[i:i+1])}
			}
			i += len(p)
			toks = append(toks, token{tPunct, p, at, fresh})
		}
		fresh = false
	}
	return append(toks, token{tEnd, "", pos{line, len(src) - lineStart + 1}, fresh}), nil
}

const brokenLine = "a string ends on the line where it starts"

// quoted reads a string literal's body from byte from: the body and the
// escape state of examples/twig's machine Quoting.
func quoted(src string, from int, at pos) (string, int, *failure) {
	var b []byte
	escape := false
	for i := from; i < len(src); i++ {
		c := src[i]
		if !escape {
			switch c {
			case '"':
				return string(b), i + 1, nil
			case '\\':
				escape = true
			case '\n':
				return "", 0, &failure{at, brokenLine}
			default:
				b = append(b, c)
			}
			continue
		}
		escape = false
		switch c {
		case '"', '\\':
			b = append(b, c)
		case '\n':
			return "", 0, &failure{at, brokenLine}
		case 'n':
			b = append(b, '\n')
		case 't':
			b = append(b, '\t')
		default:
			return "", 0, &failure{at, "unknown escape \\" + escaped(c)}
		}
	}
	return "", 0, &failure{at, "a string has no closing quote"}
}

func escaped(c byte) string {
	if c >= ' ' && c <= '~' {
		return string([]byte{c})
	}
	const hex = "0123456789abcdef"
	return string([]byte{'x', hex[c/16], hex[c%16]})
}

func punctAt(src string, i int) string {
	for _, p := range puncts {
		if i+len(p) <= len(src) && src[i:i+len(p)] == p {
			return p
		}
	}
	return ""
}

// ---- tree ----

type binop uint8

const (
	opAdd binop = iota
	opSub
	opMul
	opDiv
	opMod
	opLt
	opLe
	opGt
	opGe
	opEq
	opNe
	opAnd
	opOr
)

var symbols = [...]string{"+", "-", "*", "/", "%", "<", "<=", ">", ">=", "==", "!=", "and", "or"}

func power(op binop) int {
	switch op {
	case opOr:
		return 1
	case opAnd:
		return 2
	case opEq, opNe:
		return 3
	case opLt, opLe, opGt, opGe:
		return 4
	case opAdd, opSub:
		return 5
	}
	return 6
}

const prefixPower = 7

type ekind uint8

const (
	eInt ekind = iota
	eFloat
	eStr
	eBool
	eNil
	eName
	eNeg
	eNot
	eBinary
	eCall
	eIndex
	eField
	eList
	eRec
	eFn
)

type expr struct {
	kind   ekind
	at     pos
	n      int64
	f      float64
	s      string // string value, name, field key
	op     binop
	x, y   *expr // operand, left/right, callee/target, index
	args   []*expr
	keys   []string
	params []string
	body   []*stmt
}

type skind uint8

const (
	sFn skind = iota
	sLet
	sAssign
	sIf
	sWhile
	sFor
	sReturn
	sBreak
	sContinue
	sExpr
)

type stmt struct {
	kind   skind
	at     pos
	name   string
	params []string
	body   []*stmt
	els    []*stmt
	x, y   *expr // value/cond/over/target and value
}

// ---- parser ----

type parser struct {
	toks []token
	pos  int
}

func (p *parser) peek() token { return p.toks[p.pos] }

func (p *parser) next() token {
	t := p.toks[p.pos]
	if t.kind != tEnd {
		p.pos++
	}
	return t
}

func is(t token, w string) bool { return (t.kind == tPunct || t.kind == tKeyword) && t.text == w }

func unexpected(t token, want string) *failure {
	return &failure{t.at, "expected " + want + ", found " + describe(t)}
}

func (p *parser) expect(w string) *failure {
	t := p.next()
	if !is(t, w) {
		return unexpected(t, "\""+w+"\"")
	}
	return nil
}

func (p *parser) name(what string) (string, *failure) {
	t := p.next()
	if t.kind != tIdent {
		return "", unexpected(t, what)
	}
	return t.text, nil
}

func parse(src string) ([]*stmt, *failure) {
	toks, f := lex(src)
	if f != nil {
		return nil, f
	}
	p := &parser{toks: toks}
	var out []*stmt
	for p.peek().kind != tEnd {
		s, f := p.statement()
		if f != nil {
			return nil, f
		}
		out = append(out, s)
	}
	return out, nil
}

func (p *parser) block() ([]*stmt, *failure) {
	if f := p.expect("{"); f != nil {
		return nil, f
	}
	var out []*stmt
	for !is(p.peek(), "}") {
		if p.peek().kind == tEnd {
			return nil, unexpected(p.peek(), "\"}\"")
		}
		s, f := p.statement()
		if f != nil {
			return nil, f
		}
		out = append(out, s)
	}
	p.next()
	return out, nil
}

func (p *parser) params() ([]string, *failure) {
	if f := p.expect("("); f != nil {
		return nil, f
	}
	var out []string
	if is(p.peek(), ")") {
		p.next()
		return out, nil
	}
	for {
		at := p.peek().at
		n, f := p.name("a parameter name")
		if f != nil {
			return nil, f
		}
		for _, q := range out {
			if q == n {
				return nil, &failure{at, "the parameter " + n + " is named twice"}
			}
		}
		out = append(out, n)
		if is(p.peek(), ")") {
			p.next()
			return out, nil
		}
		if f := p.expect(","); f != nil {
			return nil, f
		}
	}
}

func (p *parser) statement() (*stmt, *failure) {
	t := p.peek()
	at := t.at
	if t.kind == tKeyword {
		switch t.text {
		case "fn":
			if p.toks[p.pos+1].kind == tIdent {
				p.next()
				n, f := p.name("a function name")
				if f != nil {
					return nil, f
				}
				ps, f := p.params()
				if f != nil {
					return nil, f
				}
				body, f := p.block()
				if f != nil {
					return nil, f
				}
				return &stmt{kind: sFn, at: at, name: n, params: ps, body: body}, nil
			}
		case "let":
			p.next()
			n, f := p.name("a variable name")
			if f != nil {
				return nil, f
			}
			if f := p.expect("="); f != nil {
				return nil, f
			}
			v, f := p.expr(0)
			if f != nil {
				return nil, f
			}
			return &stmt{kind: sLet, at: at, name: n, x: v}, nil
		case "if":
			p.next()
			return p.ifStmt(at)
		case "while":
			p.next()
			c, f := p.expr(0)
			if f != nil {
				return nil, f
			}
			body, f := p.block()
			if f != nil {
				return nil, f
			}
			return &stmt{kind: sWhile, at: at, x: c, body: body}, nil
		case "for":
			p.next()
			n, f := p.name("a variable name")
			if f != nil {
				return nil, f
			}
			if f := p.expect("in"); f != nil {
				return nil, f
			}
			over, f := p.expr(0)
			if f != nil {
				return nil, f
			}
			body, f := p.block()
			if f != nil {
				return nil, f
			}
			return &stmt{kind: sFor, at: at, name: n, x: over, body: body}, nil
		case "return":
			p.next()
			v := p.peek()
			if is(v, "}") || v.kind == tEnd || v.fresh {
				return &stmt{kind: sReturn, at: at, x: &expr{kind: eNil, at: at}}, nil
			}
			x, f := p.expr(0)
			if f != nil {
				return nil, f
			}
			return &stmt{kind: sReturn, at: at, x: x}, nil
		case "break":
			p.next()
			return &stmt{kind: sBreak, at: at}, nil
		case "continue":
			p.next()
			return &stmt{kind: sContinue, at: at}, nil
		}
	}
	x, f := p.expr(0)
	if f != nil {
		return nil, f
	}
	if !is(p.peek(), "=") {
		return &stmt{kind: sExpr, at: at, x: x}, nil
	}
	eq := p.next()
	switch x.kind {
	case eName, eIndex, eField:
		v, f := p.expr(0)
		if f != nil {
			return nil, f
		}
		return &stmt{kind: sAssign, at: eq.at, x: x, y: v}, nil
	}
	return nil, &failure{eq.at, "only a name, an element or a field can be assigned"}
}

func (p *parser) ifStmt(at pos) (*stmt, *failure) {
	c, f := p.expr(0)
	if f != nil {
		return nil, f
	}
	then, f := p.block()
	if f != nil {
		return nil, f
	}
	if !is(p.peek(), "else") {
		return &stmt{kind: sIf, at: at, x: c, body: then}, nil
	}
	p.next()
	if is(p.peek(), "if") {
		inner := p.next()
		s, f := p.ifStmt(inner.at)
		if f != nil {
			return nil, f
		}
		return &stmt{kind: sIf, at: at, x: c, body: then, els: []*stmt{s}}, nil
	}
	els, f := p.block()
	if f != nil {
		return nil, f
	}
	return &stmt{kind: sIf, at: at, x: c, body: then, els: els}, nil
}

func binOp(t token) (binop, bool) {
	if t.kind == tPunct {
		switch t.text {
		case "+":
			return opAdd, true
		case "-":
			return opSub, true
		case "*":
			return opMul, true
		case "/":
			return opDiv, true
		case "%":
			return opMod, true
		case "<":
			return opLt, true
		case "<=":
			return opLe, true
		case ">":
			return opGt, true
		case ">=":
			return opGe, true
		case "==":
			return opEq, true
		case "!=":
			return opNe, true
		}
	}
	if t.kind == tKeyword {
		switch t.text {
		case "and":
			return opAnd, true
		case "or":
			return opOr, true
		}
	}
	return 0, false
}

func (p *parser) expr(min int) (*expr, *failure) {
	left, f := p.prefix()
	if f != nil {
		return nil, f
	}
	for {
		t := p.peek()
		if is(t, "(") && !t.fresh {
			p.next()
			args, f := p.list(")")
			if f != nil {
				return nil, f
			}
			left = &expr{kind: eCall, at: t.at, x: left, args: args}
			continue
		}
		if is(t, "[") && !t.fresh {
			p.next()
			i, f := p.expr(0)
			if f != nil {
				return nil, f
			}
			if f := p.expect("]"); f != nil {
				return nil, f
			}
			left = &expr{kind: eIndex, at: t.at, x: left, y: i}
			continue
		}
		if is(t, ".") {
			p.next()
			k, f := p.name("a field name")
			if f != nil {
				return nil, f
			}
			left = &expr{kind: eField, at: t.at, x: left, s: k}
			continue
		}
		op, ok := binOp(t)
		if !ok {
			return left, nil
		}
		pw := power(op)
		if pw < min {
			return left, nil
		}
		p.next()
		right, f := p.expr(pw + 1)
		if f != nil {
			return nil, f
		}
		left = &expr{kind: eBinary, at: t.at, op: op, x: left, y: right}
	}
}

func (p *parser) list(end string) ([]*expr, *failure) {
	var out []*expr
	if is(p.peek(), end) {
		p.next()
		return out, nil
	}
	for {
		x, f := p.expr(0)
		if f != nil {
			return nil, f
		}
		out = append(out, x)
		if is(p.peek(), end) {
			p.next()
			return out, nil
		}
		if f := p.expect(","); f != nil {
			return nil, f
		}
	}
}

func (p *parser) prefix() (*expr, *failure) {
	t := p.next()
	switch t.kind {
	case tInt:
		n, err := strconv.ParseInt(t.text, 10, 64)
		if err != nil {
			return nil, &failure{t.at, "the number " + t.text + " is too large"}
		}
		return &expr{kind: eInt, at: t.at, n: n}, nil
	case tFloat:
		v, err := strconv.ParseFloat(t.text, 64)
		if err != nil {
			return nil, &failure{t.at, "the number " + t.text + " is too large"}
		}
		return &expr{kind: eFloat, at: t.at, f: v}, nil
	case tStr:
		return &expr{kind: eStr, at: t.at, s: t.text}, nil
	case tIdent:
		return &expr{kind: eName, at: t.at, s: t.text}, nil
	case tKeyword:
		switch t.text {
		case "true":
			return &expr{kind: eBool, at: t.at, n: 1}, nil
		case "false":
			return &expr{kind: eBool, at: t.at}, nil
		case "nil":
			return &expr{kind: eNil, at: t.at}, nil
		case "not":
			x, f := p.expr(prefixPower)
			if f != nil {
				return nil, f
			}
			return &expr{kind: eNot, at: t.at, x: x}, nil
		case "fn":
			ps, f := p.params()
			if f != nil {
				return nil, f
			}
			body, f := p.block()
			if f != nil {
				return nil, f
			}
			return &expr{kind: eFn, at: t.at, params: ps, body: body}, nil
		}
	case tPunct:
		switch t.text {
		case "-":
			x, f := p.expr(prefixPower)
			if f != nil {
				return nil, f
			}
			return &expr{kind: eNeg, at: t.at, x: x}, nil
		case "(":
			x, f := p.expr(0)
			if f != nil {
				return nil, f
			}
			if f := p.expect(")"); f != nil {
				return nil, f
			}
			return x, nil
		case "[":
			items, f := p.list("]")
			if f != nil {
				return nil, f
			}
			return &expr{kind: eList, at: t.at, args: items}, nil
		case "{":
			return p.record(t.at)
		}
	}
	return nil, unexpected(t, "an expression")
}

func (p *parser) record(at pos) (*expr, *failure) {
	r := &expr{kind: eRec, at: at}
	if is(p.peek(), "}") {
		p.next()
		return r, nil
	}
	for {
		kat := p.peek().at
		k, f := p.name("a field name")
		if f != nil {
			return nil, f
		}
		for _, q := range r.keys {
			if q == k {
				return nil, &failure{kat, "the field " + k + " is given twice"}
			}
		}
		if f := p.expect(":"); f != nil {
			return nil, f
		}
		r.keys = append(r.keys, k)
		v, f := p.expr(0)
		if f != nil {
			return nil, f
		}
		r.args = append(r.args, v)
		if is(p.peek(), "}") {
			p.next()
			return r, nil
		}
		if f := p.expect(","); f != nil {
			return nil, f
		}
	}
}

// ---- resolved code ----

type builtin uint8

const (
	bPrint builtin = iota
	bLen
	bPush
	bStr
	bFloat
	bInt
	bSqrt
)

var builtinNames = [...]string{"print", "len", "push", "str", "float", "int", "sqrt"}

func arityOf(b builtin) int {
	if b == bPush {
		return 2
	}
	return 1
}

type nkind uint8

const (
	nInt nkind = iota
	nFloat
	nStr
	nBool
	nNil
	nLocal
	nOuter
	nPrim
	nNeg
	nNot
	nBin
	nAnd
	nOr
	nCall
	nElem
	nGet
	nList
	nRec
	nClosure
)

type node struct {
	kind nkind
	at   pos
	val  Value // constants
	op   binop
	hops int
	slot int // slot, field key, function index
	x, y *node
	args []*node
	keys []int
	prim builtin
}

type ckind uint8

const (
	cDo ckind = iota
	cBind
	cSetVar
	cSetElem
	cSetField
	cCond
	cLoop
	cEach
	cRet
	cBrk
	cCnt
	cMakeFn
)

type code struct {
	kind       ckind
	at         pos
	hops, slot int // slot, field key; MakeFn: slot and fn
	fn         int
	x, i, val  *node
	body, els  []*code
}

type function struct {
	name     string
	arity    int
	slots    int
	body     []*code
	captured bool
	at       pos
}

type program struct {
	funcs  []*function
	fields []string
}

type binding struct {
	name        string
	slot, depth int
}

type rframe struct {
	slots    int
	captured bool
	loops    int
}

type resolver struct {
	binds  []binding
	blocks []int
	frames []rframe
	funcs  []*function
	fields []string
}

func resolveProgram(stmts []*stmt) (*program, *failure) {
	r := &resolver{frames: []rframe{{}}, funcs: []*function{{name: "main", at: pos{1, 1}}}}
	body, f := r.block(stmts)
	if f != nil {
		return nil, f
	}
	top := r.frames[0]
	r.funcs[0] = &function{name: "main", slots: top.slots, body: body, captured: top.captured, at: pos{1, 1}}
	return &program{funcs: r.funcs, fields: r.fields}, nil
}

func (r *resolver) depth() int { return len(r.frames) - 1 }

func (r *resolver) declare(name string, at pos) (int, *failure) {
	d := r.depth()
	for _, b := range r.binds[r.blocks[len(r.blocks)-1]:] {
		if b.name == name {
			return 0, &failure{at, name + " is already declared in this block"}
		}
	}
	slot := r.frames[d].slots
	r.frames[d].slots++
	r.binds = append(r.binds, binding{name, slot, d})
	return slot, nil
}

func (r *resolver) lookup(name string, at pos) (*node, *failure) {
	for i := len(r.binds) - 1; i >= 0; i-- {
		b := r.binds[i]
		if b.name == name {
			hops := r.depth() - b.depth
			if hops == 0 {
				return &node{kind: nLocal, slot: b.slot}, nil
			}
			return &node{kind: nOuter, hops: hops, slot: b.slot}, nil
		}
	}
	for i, n := range builtinNames {
		if n == name {
			return &node{kind: nPrim, prim: builtin(i)}, nil
		}
	}
	return nil, &failure{at, name + " is not declared"}
}

func (r *resolver) field(name string) int {
	for i, f := range r.fields {
		if f == name {
			return i
		}
	}
	r.fields = append(r.fields, name)
	return len(r.fields) - 1
}

func (r *resolver) block(stmts []*stmt) ([]*code, *failure) {
	r.blocks = append(r.blocks, len(r.binds))
	var out []*code
	var pending []int
	for _, s := range stmts {
		if s.kind == sFn {
			slot, f := r.declare(s.name, s.at)
			if f != nil {
				return nil, f
			}
			idx := len(r.funcs)
			r.funcs = append(r.funcs, &function{name: s.name, arity: len(s.params), at: s.at})
			pending = append(pending, idx)
			out = append(out, &code{kind: cMakeFn, slot: slot, fn: idx})
			r.frames[r.depth()].captured = true
		}
	}
	k := 0
	for _, s := range stmts {
		if s.kind == sFn {
			if f := r.function(pending[k], s.name, s.params, s.body, s.at); f != nil {
				return nil, f
			}
			k++
			continue
		}
		c, f := r.statement(s)
		if f != nil {
			return nil, f
		}
		out = append(out, c)
	}
	r.binds = r.binds[:r.blocks[len(r.blocks)-1]]
	r.blocks = r.blocks[:len(r.blocks)-1]
	return out, nil
}

func (r *resolver) function(idx int, name string, params []string, body []*stmt, at pos) *failure {
	r.frames = append(r.frames, rframe{})
	r.blocks = append(r.blocks, len(r.binds))
	for _, p := range params {
		if _, f := r.declare(p, at); f != nil {
			return f
		}
	}
	codes, f := r.block(body)
	if f != nil {
		return f
	}
	r.binds = r.binds[:r.blocks[len(r.blocks)-1]]
	r.blocks = r.blocks[:len(r.blocks)-1]
	fr := r.frames[r.depth()]
	r.frames = r.frames[:len(r.frames)-1]
	r.funcs[idx] = &function{name: name, arity: len(params), slots: fr.slots, body: codes, captured: fr.captured, at: at}
	return nil
}

func (r *resolver) statement(s *stmt) (*code, *failure) {
	switch s.kind {
	case sLet:
		v, f := r.expr(s.x)
		if f != nil {
			return nil, f
		}
		slot, f := r.declare(s.name, s.at)
		if f != nil {
			return nil, f
		}
		return &code{kind: cBind, slot: slot, val: v}, nil
	case sAssign:
		v, f := r.expr(s.y)
		if f != nil {
			return nil, f
		}
		t := s.x
		switch t.kind {
		case eName:
			n, f := r.lookup(t.s, t.at)
			if f != nil {
				return nil, f
			}
			switch n.kind {
			case nLocal:
				return &code{kind: cSetVar, slot: n.slot, val: v}, nil
			case nOuter:
				return &code{kind: cSetVar, hops: n.hops, slot: n.slot, val: v}, nil
			}
			return nil, &failure{t.at, t.s + " is not declared"}
		case eIndex:
			x, f := r.expr(t.x)
			if f != nil {
				return nil, f
			}
			i, f := r.expr(t.y)
			if f != nil {
				return nil, f
			}
			return &code{kind: cSetElem, x: x, i: i, val: v, at: t.at}, nil
		default: // eField
			x, f := r.expr(t.x)
			if f != nil {
				return nil, f
			}
			return &code{kind: cSetField, x: x, slot: r.field(t.s), val: v, at: t.at}, nil
		}
	case sIf:
		c, f := r.expr(s.x)
		if f != nil {
			return nil, f
		}
		then, f := r.block(s.body)
		if f != nil {
			return nil, f
		}
		els, f := r.block(s.els)
		if f != nil {
			return nil, f
		}
		return &code{kind: cCond, x: c, body: then, els: els, at: s.at}, nil
	case sWhile:
		c, f := r.expr(s.x)
		if f != nil {
			return nil, f
		}
		r.frames[r.depth()].loops++
		body, f := r.block(s.body)
		if f != nil {
			return nil, f
		}
		r.frames[r.depth()].loops--
		return &code{kind: cLoop, x: c, body: body, at: s.at}, nil
	case sFor:
		over, f := r.expr(s.x)
		if f != nil {
			return nil, f
		}
		r.blocks = append(r.blocks, len(r.binds))
		slot, f := r.declare(s.name, s.at)
		if f != nil {
			return nil, f
		}
		r.frames[r.depth()].loops++
		body, f := r.block(s.body)
		if f != nil {
			return nil, f
		}
		r.frames[r.depth()].loops--
		r.binds = r.binds[:r.blocks[len(r.blocks)-1]]
		r.blocks = r.blocks[:len(r.blocks)-1]
		return &code{kind: cEach, slot: slot, x: over, body: body, at: s.at}, nil
	case sReturn:
		if r.depth() == 0 {
			return nil, &failure{s.at, "return is outside a function"}
		}
		v, f := r.expr(s.x)
		if f != nil {
			return nil, f
		}
		return &code{kind: cRet, val: v}, nil
	case sBreak:
		if r.frames[r.depth()].loops == 0 {
			return nil, &failure{s.at, "break is outside a loop"}
		}
		return &code{kind: cBrk}, nil
	case sContinue:
		if r.frames[r.depth()].loops == 0 {
			return nil, &failure{s.at, "continue is outside a loop"}
		}
		return &code{kind: cCnt}, nil
	}
	x, f := r.expr(s.x)
	if f != nil {
		return nil, f
	}
	return &code{kind: cDo, x: x}, nil
}

func (r *resolver) exprs(xs []*expr) ([]*node, *failure) {
	out := make([]*node, 0, len(xs))
	for _, x := range xs {
		n, f := r.expr(x)
		if f != nil {
			return nil, f
		}
		out = append(out, n)
	}
	return out, nil
}

func (r *resolver) expr(e *expr) (*node, *failure) {
	switch e.kind {
	case eInt:
		return &node{kind: nInt, val: Value{k: kInt, n: e.n}}, nil
	case eFloat:
		return &node{kind: nFloat, val: Value{k: kFloat, f: e.f}}, nil
	case eStr:
		return &node{kind: nStr, val: Value{k: kStr, s: e.s}}, nil
	case eBool:
		return &node{kind: nBool, val: Value{k: kBool, n: e.n}}, nil
	case eNil:
		return &node{kind: nNil}, nil
	case eName:
		return r.lookup(e.s, e.at)
	case eNeg, eNot:
		x, f := r.expr(e.x)
		if f != nil {
			return nil, f
		}
		k := nNeg
		if e.kind == eNot {
			k = nNot
		}
		return &node{kind: k, x: x, at: e.at}, nil
	case eBinary:
		l, f := r.expr(e.x)
		if f != nil {
			return nil, f
		}
		rt, f := r.expr(e.y)
		if f != nil {
			return nil, f
		}
		switch e.op {
		case opAnd:
			return &node{kind: nAnd, x: l, y: rt, at: e.at}, nil
		case opOr:
			return &node{kind: nOr, x: l, y: rt, at: e.at}, nil
		}
		return &node{kind: nBin, op: e.op, x: l, y: rt, at: e.at}, nil
	case eCall:
		fn, f := r.expr(e.x)
		if f != nil {
			return nil, f
		}
		args, f := r.exprs(e.args)
		if f != nil {
			return nil, f
		}
		return &node{kind: nCall, x: fn, args: args, at: e.at}, nil
	case eIndex:
		x, f := r.expr(e.x)
		if f != nil {
			return nil, f
		}
		i, f := r.expr(e.y)
		if f != nil {
			return nil, f
		}
		return &node{kind: nElem, x: x, y: i, at: e.at}, nil
	case eField:
		x, f := r.expr(e.x)
		if f != nil {
			return nil, f
		}
		return &node{kind: nGet, x: x, slot: r.field(e.s), at: e.at}, nil
	case eList:
		items, f := r.exprs(e.args)
		if f != nil {
			return nil, f
		}
		return &node{kind: nList, args: items}, nil
	case eRec:
		keys := make([]int, 0, len(e.keys))
		for _, k := range e.keys {
			keys = append(keys, r.field(k))
		}
		vals, f := r.exprs(e.args)
		if f != nil {
			return nil, f
		}
		return &node{kind: nRec, keys: keys, args: vals}, nil
	}
	// eFn
	r.frames[r.depth()].captured = true
	idx := len(r.funcs)
	r.funcs = append(r.funcs, &function{name: "fn", arity: len(e.params), at: e.at})
	if f := r.function(idx, "fn", e.params, e.body, e.at); f != nil {
		return nil, f
	}
	return &node{kind: nClosure, slot: idx}, nil
}

// ---- values and the machine ----

type vkind uint8

const (
	kNil vkind = iota
	kBool
	kInt
	kFloat
	kStr
	kList
	kRec
	kFn
	kBuiltin
)

// Value is a twig value: a kind and its payload. An int, a bool and a
// builtin are in n; a list, a record and a function point at an object.
type Value struct {
	k vkind
	n int64
	f float64
	s string
	o *object
}

type object struct {
	items []Value // list items, record values
	keys  []int   // record field numbers
	fn    int     // function index
	env   *frame  // the frame a function was made in
}

type frame struct {
	slots  []Value
	parent *frame
}

var kindNames = [...]string{"nil", "bool", "int", "float", "string", "list", "record", "function", "function"}

func kindOf(v Value) string { return kindNames[v.k] }

const maxDepth = 2000

type flow uint8

const (
	fNormal flow = iota
	fReturn
	fBreak
	fContinue
)

type machine struct {
	prog  *program
	free  []*frame
	out   *bufio.Writer
	depth int
	nodes int
	calls int
}

func (m *machine) enter(slots int, env *frame) *frame {
	if n := len(m.free); n > 0 {
		f := m.free[n-1]
		m.free = m.free[:n-1]
		if len(f.slots) < slots {
			f.slots = make([]Value, slots)
		} else {
			clear(f.slots[:slots])
		}
		f.parent = env
		return f
	}
	return &frame{slots: make([]Value, slots), parent: env}
}

func frameOf(env *frame, hops int) *frame {
	for ; hops > 0; hops-- {
		env = env.parent
	}
	return env
}

func (m *machine) exec(body []*code, env *frame) (flow, Value, *failure) {
	for _, c := range body {
		m.nodes++
		switch c.kind {
		case cDo:
			if _, f := m.eval(c.x, env); f != nil {
				return 0, Value{}, f
			}
		case cBind:
			v, f := m.eval(c.val, env)
			if f != nil {
				return 0, Value{}, f
			}
			env.slots[c.slot] = v
		case cSetVar:
			v, f := m.eval(c.val, env)
			if f != nil {
				return 0, Value{}, f
			}
			frameOf(env, c.hops).slots[c.slot] = v
		case cSetElem:
			x, f := m.eval(c.x, env)
			if f != nil {
				return 0, Value{}, f
			}
			i, f := m.eval(c.i, env)
			if f != nil {
				return 0, Value{}, f
			}
			v, f := m.eval(c.val, env)
			if f != nil {
				return 0, Value{}, f
			}
			if f := setElem(x, i, v, c.at); f != nil {
				return 0, Value{}, f
			}
		case cSetField:
			x, f := m.eval(c.x, env)
			if f != nil {
				return 0, Value{}, f
			}
			v, f := m.eval(c.val, env)
			if f != nil {
				return 0, Value{}, f
			}
			if f := m.setField(x, c.slot, v, c.at); f != nil {
				return 0, Value{}, f
			}
		case cCond:
			t, f := m.truth(c.x, env, c.at)
			if f != nil {
				return 0, Value{}, f
			}
			branch := c.els
			if t {
				branch = c.body
			}
			if fl, v, f := m.exec(branch, env); f != nil || fl != fNormal {
				return fl, v, f
			}
		case cLoop:
			for {
				t, f := m.truth(c.x, env, c.at)
				if f != nil {
					return 0, Value{}, f
				}
				if !t {
					break
				}
				fl, v, f := m.exec(c.body, env)
				if f != nil || fl == fReturn {
					return fl, v, f
				}
				if fl == fBreak {
					break
				}
			}
		case cEach:
			over, f := m.eval(c.x, env)
			if f != nil {
				return 0, Value{}, f
			}
			if over.k != kList {
				return 0, Value{}, &failure{c.at, "for needs a list, not " + kindOf(over)}
			}
			l := over.o
			for i := 0; i < len(l.items); {
				env.slots[c.slot] = l.items[i]
				i++
				fl, v, f := m.exec(c.body, env)
				if f != nil || fl == fReturn {
					return fl, v, f
				}
				if fl == fBreak {
					break
				}
			}
		case cRet:
			v, f := m.eval(c.val, env)
			if f != nil {
				return 0, Value{}, f
			}
			return fReturn, v, nil
		case cBrk:
			return fBreak, Value{}, nil
		case cCnt:
			return fContinue, Value{}, nil
		case cMakeFn:
			env.slots[c.slot] = Value{k: kFn, o: &object{fn: c.fn, env: env}}
		}
	}
	return fNormal, Value{}, nil
}

func (m *machine) truth(n *node, env *frame, at pos) (bool, *failure) {
	v, f := m.eval(n, env)
	if f != nil {
		return false, f
	}
	if v.k != kBool {
		return false, &failure{at, "a condition must be a bool, not " + kindOf(v)}
	}
	return v.n != 0, nil
}

func boolValue(b bool) Value {
	if b {
		return Value{k: kBool, n: 1}
	}
	return Value{k: kBool}
}

func (m *machine) eval(n *node, env *frame) (Value, *failure) {
	m.nodes++
	switch n.kind {
	case nInt, nFloat, nStr, nBool, nNil:
		return n.val, nil
	case nLocal:
		return env.slots[n.slot], nil
	case nOuter:
		return frameOf(env, n.hops).slots[n.slot], nil
	case nPrim:
		return Value{k: kBuiltin, n: int64(n.prim)}, nil
	case nNeg:
		v, f := m.eval(n.x, env)
		if f != nil {
			return Value{}, f
		}
		switch v.k {
		case kInt:
			return Value{k: kInt, n: -v.n}, nil
		case kFloat:
			return Value{k: kFloat, f: -v.f}, nil
		}
		return Value{}, &failure{n.at, "- cannot take " + kindOf(v)}
	case nNot:
		v, f := m.eval(n.x, env)
		if f != nil {
			return Value{}, f
		}
		if v.k != kBool {
			return Value{}, &failure{n.at, "not cannot take " + kindOf(v)}
		}
		return boolValue(v.n == 0), nil
	case nBin:
		l, f := m.eval(n.x, env)
		if f != nil {
			return Value{}, f
		}
		r, f := m.eval(n.y, env)
		if f != nil {
			return Value{}, f
		}
		return binary(n.op, l, r, n.at)
	case nAnd, nOr:
		word := "and"
		if n.kind == nOr {
			word = "or"
		}
		l, f := m.eval(n.x, env)
		if f != nil {
			return Value{}, f
		}
		if l.k != kBool {
			return Value{}, &failure{n.at, word + " cannot take " + kindOf(l)}
		}
		if (l.n != 0) == (n.kind == nOr) {
			return l, nil
		}
		r, f := m.eval(n.y, env)
		if f != nil {
			return Value{}, f
		}
		if r.k != kBool {
			return Value{}, &failure{n.at, word + " cannot take " + kindOf(r)}
		}
		return r, nil
	case nCall:
		fn, f := m.eval(n.x, env)
		if f != nil {
			return Value{}, f
		}
		args := make([]Value, len(n.args))
		for i, a := range n.args {
			v, f := m.eval(a, env)
			if f != nil {
				return Value{}, f
			}
			args[i] = v
		}
		return m.call(fn, args, n.at)
	case nElem:
		x, f := m.eval(n.x, env)
		if f != nil {
			return Value{}, f
		}
		i, f := m.eval(n.y, env)
		if f != nil {
			return Value{}, f
		}
		return elem(x, i, n.at)
	case nGet:
		x, f := m.eval(n.x, env)
		if f != nil {
			return Value{}, f
		}
		return m.field(x, n.slot, n.at)
	case nList:
		items := make([]Value, len(n.args))
		for i, a := range n.args {
			v, f := m.eval(a, env)
			if f != nil {
				return Value{}, f
			}
			items[i] = v
		}
		return Value{k: kList, o: &object{items: items}}, nil
	case nRec:
		vals := make([]Value, len(n.args))
		for i, a := range n.args {
			v, f := m.eval(a, env)
			if f != nil {
				return Value{}, f
			}
			vals[i] = v
		}
		return Value{k: kRec, o: &object{items: vals, keys: n.keys}}, nil
	}
	// nClosure
	return Value{k: kFn, o: &object{fn: n.slot, env: env}}, nil
}

func mismatch(op binop, l, r Value, at pos) (Value, *failure) {
	return Value{}, &failure{at, symbols[op] + " cannot take " + kindOf(l) + " and " + kindOf(r)}
}

func binary(op binop, l, r Value, at pos) (Value, *failure) {
	switch op {
	case opEq:
		return boolValue(same(l, r)), nil
	case opNe:
		return boolValue(!same(l, r)), nil
	}
	switch {
	case l.k == kInt && r.k == kInt:
		a, b := l.n, r.n
		switch op {
		case opAdd:
			return Value{k: kInt, n: a + b}, nil
		case opSub:
			return Value{k: kInt, n: a - b}, nil
		case opMul:
			return Value{k: kInt, n: a * b}, nil
		case opDiv:
			if b == 0 {
				return Value{}, &failure{at, "division by zero"}
			}
			return Value{k: kInt, n: a / b}, nil
		case opMod:
			if b == 0 {
				return Value{}, &failure{at, "division by zero"}
			}
			return Value{k: kInt, n: a % b}, nil
		case opLt:
			return boolValue(a < b), nil
		case opLe:
			return boolValue(a <= b), nil
		case opGt:
			return boolValue(a > b), nil
		case opGe:
			return boolValue(a >= b), nil
		}
	case l.k == kFloat && r.k == kFloat:
		a, b := l.f, r.f
		switch op {
		case opAdd:
			return Value{k: kFloat, f: a + b}, nil
		case opSub:
			return Value{k: kFloat, f: a - b}, nil
		case opMul:
			return Value{k: kFloat, f: a * b}, nil
		case opDiv:
			return Value{k: kFloat, f: a / b}, nil
		case opLt:
			return boolValue(a < b), nil
		case opLe:
			return boolValue(a <= b), nil
		case opGt:
			return boolValue(a > b), nil
		case opGe:
			return boolValue(a >= b), nil
		}
	case l.k == kStr && r.k == kStr:
		a, b := l.s, r.s
		switch op {
		case opAdd:
			return Value{k: kStr, s: a + b}, nil
		case opLt:
			return boolValue(a < b), nil
		case opLe:
			return boolValue(a <= b), nil
		case opGt:
			return boolValue(a > b), nil
		case opGe:
			return boolValue(a >= b), nil
		}
	}
	return mismatch(op, l, r, at)
}

func same(a, b Value) bool {
	if a.k != b.k {
		return false
	}
	switch a.k {
	case kNil:
		return true
	case kBool, kInt, kBuiltin:
		return a.n == b.n
	case kFloat:
		return a.f == b.f
	case kStr:
		return a.s == b.s
	case kFn:
		return a.o.fn == b.o.fn && a.o.env == b.o.env
	}
	return a.o == b.o // a list or a record: the same one
}

func count(n int) string {
	if n == 1 {
		return "1 argument"
	}
	return strconv.Itoa(n) + " arguments"
}

func (m *machine) call(f Value, args []Value, at pos) (Value, *failure) {
	m.calls++
	switch f.k {
	case kFn:
		fn := m.prog.funcs[f.o.fn]
		if len(args) != fn.arity {
			return Value{}, &failure{at, fn.name + " takes " + count(fn.arity) + ", not " + count(len(args))}
		}
		if m.depth >= maxDepth {
			return Value{}, &failure{at, "calls nest deeper than " + strconv.Itoa(maxDepth)}
		}
		env := m.enter(fn.slots, f.o.env)
		copy(env.slots, args)
		m.depth++
		fl, v, fail := m.exec(fn.body, env)
		if fail != nil {
			m.depth--
			return Value{}, fail
		}
		m.depth--
		if !fn.captured {
			m.free = append(m.free, env)
		}
		if fl != fReturn {
			v = Value{}
		}
		return v, nil
	case kBuiltin:
		b := builtin(f.n)
		if len(args) != arityOf(b) {
			return Value{}, &failure{at, builtinNames[b] + " takes " + count(arityOf(b)) + ", not " + count(len(args))}
		}
		return m.builtin(b, args, at)
	}
	return Value{}, &failure{at, "a " + kindOf(f) + " is not a function"}
}

func (m *machine) builtin(b builtin, args []Value, at pos) (Value, *failure) {
	v := args[0]
	switch b {
	case bPrint:
		var buf []byte
		buf = m.write(buf, v, 0)
		buf = append(buf, '\n')
		m.out.Write(buf)
		return Value{}, nil
	case bLen:
		switch v.k {
		case kList:
			return Value{k: kInt, n: int64(len(v.o.items))}, nil
		case kStr:
			return Value{k: kInt, n: int64(len(v.s))}, nil
		}
		return Value{}, &failure{at, "len cannot take " + kindOf(v)}
	case bPush:
		if v.k != kList {
			return Value{}, &failure{at, "push cannot take " + kindOf(v)}
		}
		v.o.items = append(v.o.items, args[1])
		return Value{}, nil
	case bStr:
		return Value{k: kStr, s: string(m.write(nil, v, 0))}, nil
	case bFloat:
		switch v.k {
		case kInt:
			return Value{k: kFloat, f: float64(v.n)}, nil
		case kFloat:
			return v, nil
		}
		return Value{}, &failure{at, "float cannot take " + kindOf(v)}
	case bInt:
		switch v.k {
		case kFloat:
			if v.f >= -0x1p63 && v.f < 0x1p63 {
				return Value{k: kInt, n: int64(v.f)}, nil
			}
			return Value{}, &failure{at, "int cannot take a float out of range"}
		case kInt:
			return v, nil
		}
		return Value{}, &failure{at, "int cannot take " + kindOf(v)}
	}
	// bSqrt
	if v.k != kFloat {
		return Value{}, &failure{at, "sqrt cannot take " + kindOf(v)}
	}
	return Value{k: kFloat, f: math.Sqrt(v.f)}, nil
}

const showDepth = 8

func (m *machine) write(b []byte, v Value, d int) []byte {
	switch v.k {
	case kNil:
		return append(b, "nil"...)
	case kBool:
		if v.n != 0 {
			return append(b, "true"...)
		}
		return append(b, "false"...)
	case kInt:
		return strconv.AppendInt(b, v.n, 10)
	case kFloat:
		return strconv.AppendFloat(b, v.f, 'g', -1, 64)
	case kStr:
		if d == 0 {
			return append(b, v.s...)
		}
		return strconv.AppendQuote(b, v.s)
	case kList:
		if d >= showDepth {
			return append(b, "[...]"...)
		}
		b = append(b, '[')
		for i, item := range v.o.items {
			if i > 0 {
				b = append(b, ", "...)
			}
			b = m.write(b, item, d+1)
		}
		return append(b, ']')
	case kRec:
		if d >= showDepth {
			return append(b, "{...}"...)
		}
		b = append(b, '{')
		for i, k := range v.o.keys {
			if i > 0 {
				b = append(b, ", "...)
			}
			b = append(b, m.prog.fields[k]...)
			b = append(b, ": "...)
			b = m.write(b, v.o.items[i], d+1)
		}
		return append(b, '}')
	case kFn:
		return append(append(append(b, "<fn "...), m.prog.funcs[v.o.fn].name...), '>')
	}
	return append(append(append(b, "<fn "...), builtinNames[v.n]...), '>')
}

func index(i Value, at pos) (int, *failure) {
	if i.k != kInt {
		return 0, &failure{at, "an index must be an int, not " + kindOf(i)}
	}
	return int(i.n), nil
}

func elem(x, i Value, at pos) (Value, *failure) {
	k, f := index(i, at)
	if f != nil {
		return Value{}, f
	}
	switch x.k {
	case kList:
		if k < 0 || k >= len(x.o.items) {
			return Value{}, &failure{at, "index " + strconv.Itoa(k) + " is outside a list of " + strconv.Itoa(len(x.o.items))}
		}
		return x.o.items[k], nil
	case kStr:
		if k < 0 || k >= len(x.s) {
			return Value{}, &failure{at, "index " + strconv.Itoa(k) + " is outside a string of " + strconv.Itoa(len(x.s))}
		}
		return Value{k: kStr, s: x.s[k : k+1]}, nil
	}
	return Value{}, &failure{at, "a " + kindOf(x) + " has no elements"}
}

func setElem(x, i, v Value, at pos) *failure {
	k, f := index(i, at)
	if f != nil {
		return f
	}
	if x.k != kList {
		return &failure{at, "a " + kindOf(x) + " has no elements to set"}
	}
	if k < 0 || k >= len(x.o.items) {
		return &failure{at, "index " + strconv.Itoa(k) + " is outside a list of " + strconv.Itoa(len(x.o.items))}
	}
	x.o.items[k] = v
	return nil
}

func slotOf(r *object, key int) int {
	for i, k := range r.keys {
		if k == key {
			return i
		}
	}
	return -1
}

func (m *machine) field(x Value, key int, at pos) (Value, *failure) {
	if x.k != kRec {
		return Value{}, &failure{at, "a " + kindOf(x) + " has no field " + m.prog.fields[key]}
	}
	i := slotOf(x.o, key)
	if i < 0 {
		return Value{}, &failure{at, "the record has no field " + m.prog.fields[key]}
	}
	return x.o.items[i], nil
}

func (m *machine) setField(x Value, key int, v Value, at pos) *failure {
	if x.k != kRec {
		return &failure{at, "a " + kindOf(x) + " has no field " + m.prog.fields[key]}
	}
	i := slotOf(x.o, key)
	if i < 0 {
		return &failure{at, "the record has no field " + m.prog.fields[key]}
	}
	x.o.items[i] = v
	return nil
}

// ---- the command ----

func where(name string, at pos) string {
	return name + ":" + strconv.Itoa(at.line) + ":" + strconv.Itoa(at.col)
}

func compile(name, src string) (*program, string) {
	stmts, f := parse(src)
	if f == nil {
		var p *program
		p, f = resolveProgram(stmts)
		if f == nil {
			return p, ""
		}
	}
	return nil, where(name, f.at) + ": " + f.msg
}

func read(path string) (string, bool) {
	b, err := os.ReadFile(path)
	if err == nil {
		return string(b), true
	}
	reason := err.Error()
	switch {
	case errors.Is(err, fs.ErrNotExist):
		reason = "no such file"
	case errors.Is(err, fs.ErrPermission):
		reason = "permission denied"
	}
	os.Stderr.WriteString("twig: " + path + ": " + reason + "\n")
	return "", false
}

func main() {
	args := os.Args[1:]
	stats := false
	if len(args) == 3 && args[0] == "run" && args[1] == "-stats" {
		stats = true
		args = []string{"run", args[2]}
	}
	if len(args) != 2 || (args[0] != "run" && args[0] != "check") {
		os.Stderr.WriteString(usageText + "\n")
		os.Exit(2)
	}
	path := args[1]
	src, ok := read(path)
	if !ok {
		os.Exit(1)
	}
	prog, msg := compile(path, src)
	if prog == nil {
		os.Stderr.WriteString("twig: " + msg + "\n")
		os.Exit(1)
	}
	if args[0] == "check" {
		return
	}
	out := bufio.NewWriterSize(os.Stdout, 64<<10)
	m := &machine{prog: prog, out: out}
	main := prog.funcs[0]
	_, _, f := m.exec(main.body, &frame{slots: make([]Value, main.slots)})
	out.Flush()
	var errText strings.Builder
	if f != nil {
		errText.WriteString("twig: " + where(path, f.at) + ": " + f.msg + "\n")
	}
	if stats {
		errText.WriteString("nodes " + strconv.Itoa(m.nodes) + "\ncalls " + strconv.Itoa(m.calls) + "\n")
	}
	os.Stderr.WriteString(errText.String())
	if f != nil {
		os.Exit(1)
	}
}
