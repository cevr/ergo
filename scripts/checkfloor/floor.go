package main

import (
	"fmt"
)

// The floor checker: the least work a checker of the probe's language
// needs, written by hand. It checks the subset the shaped package uses,
// with no tree: functions with one optional bracket parameter list, int,
// string, bool and slice types, let and let mut, assignment, for … in over
// a slice, if, a block's final value, the operators || && == != < <= > >=
// + - * / %, calls of the package's functions (bracket parameters inferred
// from the arguments), len, and indexing. One scan collects the
// signatures and skips the bodies by their braces; a second types each
// body in one pass over its bytes, every expression once, and rejects the
// first type error. A type is an int: the basic types, a bracket parameter,
// or a slice of an element type interned in one table.

type typ int32

const (
	tBad typ = iota
	tInt
	tString
	tBool
	tParam0 // tParam0+k: the function's k-th bracket parameter
	tSlice0 = tParam0 + 8
)

type token struct {
	kind  byte // 'i' ident, 'n' int, 's' string, or the punctuation byte; two-byte operators below
	start int32
	end   int32
	nl    bool // a newline comes before it
}

// Two-byte operators.
const (
	opEq   = 'E' // ==
	opNe   = 'N' // !=
	opLe   = 'L' // <=
	opGe   = 'G' // >=
	opAnd  = 'A' // &&
	opOr   = 'O' // ||
	opEOF  = 0
	opNone = 1
)

type local struct {
	start, end int32
	t          typ
	mut        bool
}

type sig struct {
	params  []typ
	names   [][2]int32 // the parameters' names
	result  typ
	tparams int
	tnames  [8][2]int32 // the bracket parameters' names
	body    int32       // the offset of its body's '{'
}

type floor struct {
	src    []byte
	pos    int32
	tok    token
	slices []typ // slices[i] is the element of tSlice0+i
	fns    map[string]int
	sigs   []sig
	locals []local
	tnames [8][2]int32 // the current function's bracket parameter names
	ntn    int
	bind   [8]typ // a call's bracket arguments
	typed  int
	err    error
}

func (f *floor) fail(format string, args ...any) {
	if f.err == nil {
		f.err = fmt.Errorf("at %d: "+format, append([]any{f.tok.start}, args...)...)
	}
}

func (f *floor) slice(elem typ) typ {
	for i, e := range f.slices {
		if e == elem {
			return tSlice0 + typ(i)
		}
	}
	f.slices = append(f.slices, elem)
	return tSlice0 + typ(len(f.slices)-1)
}

func isLetter(c byte) bool { return c == '_' || c|0x20 >= 'a' && c|0x20 <= 'z' }
func isDigit(c byte) bool  { return c >= '0' && c <= '9' }

// next scans the next token.
func (f *floor) next() {
	s, i, nl := f.src, f.pos, false
	for int(i) < len(s) && (s[i] == ' ' || s[i] == '\t' || s[i] == '\n' || s[i] == '\r') {
		if s[i] == '\n' {
			nl = true
		}
		i++
	}
	if int(i) >= len(s) {
		f.tok, f.pos = token{kind: opEOF, start: i, end: i, nl: true}, i
		return
	}
	start, c := i, s[i]
	var k byte
	switch {
	case isLetter(c):
		for int(i) < len(s) && (isLetter(s[i]) || isDigit(s[i])) {
			i++
		}
		k = 'i'
	case isDigit(c):
		for int(i) < len(s) && isDigit(s[i]) {
			i++
		}
		k = 'n'
	case c == '"':
		i++
		for int(i) < len(s) && s[i] != '"' {
			if s[i] == '\\' {
				i++
			}
			i++
		}
		i++
		k = 's'
	default:
		i++
		k = c
		if int(i) < len(s) {
			d := s[i]
			switch {
			case c == '=' && d == '=':
				k = opEq
			case c == '!' && d == '=':
				k = opNe
			case c == '<' && d == '=':
				k = opLe
			case c == '>' && d == '=':
				k = opGe
			case c == '&' && d == '&':
				k = opAnd
			case c == '|' && d == '|':
				k = opOr
			}
			if k != c {
				i++
			}
		}
	}
	f.tok, f.pos = token{kind: k, start: start, end: i, nl: nl}, i
}

func (f *floor) is(word string) bool {
	return f.tok.kind == 'i' && string(f.src[f.tok.start:f.tok.end]) == word
}

func (f *floor) expect(k byte) {
	if f.tok.kind != k {
		f.fail("want %q", k)
	}
	f.next()
}

// typeExpr reads a type: int, string, bool, a bracket parameter, or []T.
func (f *floor) typeExpr() typ {
	if f.tok.kind == '[' {
		f.next()
		f.expect(']')
		return f.slice(f.typeExpr())
	}
	if f.tok.kind != 'i' {
		f.fail("want a type")
		return tBad
	}
	name := f.src[f.tok.start:f.tok.end]
	f.next()
	switch string(name) {
	case "int":
		return tInt
	case "string":
		return tString
	case "bool":
		return tBool
	}
	for k := 0; k < f.ntn; k++ {
		if string(f.src[f.tnames[k][0]:f.tnames[k][1]]) == string(name) {
			return tParam0 + typ(k)
		}
	}
	f.fail("unknown type %s", name)
	return tBad
}

// signatures scans the file's functions: their names, bracket parameters,
// parameter and result types, and the offset of each body, which it skips.
func (f *floor) signatures() {
	f.next()
	for f.tok.kind != opEOF && f.err == nil {
		if !f.is("func") {
			f.fail("want func")
			return
		}
		f.next()
		name := string(f.src[f.tok.start:f.tok.end])
		f.expect('i')
		f.ntn = 0
		if f.tok.kind == '[' {
			f.next()
			for f.tok.kind == 'i' {
				f.tnames[f.ntn] = [2]int32{f.tok.start, f.tok.end}
				f.ntn++
				f.next()
				if f.tok.kind == ',' {
					f.next()
				}
			}
			f.expect(']')
		}
		sg := sig{tparams: f.ntn, tnames: f.tnames}
		f.expect('(')
		for f.tok.kind == 'i' {
			sg.names = append(sg.names, [2]int32{f.tok.start, f.tok.end})
			f.next()
			sg.params = append(sg.params, f.typeExpr())
			if f.tok.kind == ',' {
				f.next()
			}
		}
		f.expect(')')
		sg.result = f.typeExpr()
		sg.body = f.tok.start
		if f.tok.kind != '{' {
			f.fail("want a body")
			return
		}
		depth := 0
		for {
			switch f.tok.kind {
			case '{':
				depth++
			case '}':
				depth--
			case opEOF:
				f.fail("unclosed body")
				return
			}
			f.next()
			if depth == 0 {
				break
			}
		}
		if _, dup := f.fns[name]; dup {
			f.fail("%s declared twice", name)
		}
		f.fns[name] = len(f.sigs)
		f.sigs = append(f.sigs, sg)
	}
}

// bodies types each function's body against its signature.
func (f *floor) bodies() {
	for i := range f.sigs {
		if f.err != nil {
			return
		}
		f.body(i)
	}
}

func (f *floor) body(i int) {
	sg := f.sigs[i]
	f.locals = f.locals[:0]
	for p, n := range sg.names {
		f.locals = append(f.locals, local{start: n[0], end: n[1], t: sg.params[p]})
	}
	f.tnames, f.ntn = sg.tnames, sg.tparams
	f.pos = sg.body
	f.next()
	if f.block() != sg.result {
		f.fail("result is not the function's")
	}
}

// block types `{ stmt … }`; its value is the final expression's type, or
// tBad when it ends in a statement.
func (f *floor) block() typ {
	f.expect('{')
	mark := len(f.locals)
	value := tBad
	for f.tok.kind != '}' && f.tok.kind != opEOF && f.err == nil {
		value = f.stmt()
	}
	f.locals = f.locals[:mark]
	f.expect('}')
	return value
}

func (f *floor) lookup(start, end int32) int {
	name := f.src[start:end]
	for i := len(f.locals) - 1; i >= 0; i-- {
		l := f.locals[i]
		if string(f.src[l.start:l.end]) == string(name) {
			return i
		}
	}
	return -1
}

// stmt types one statement; an expression statement gives its type.
func (f *floor) stmt() typ {
	switch {
	case f.is("let"):
		f.next()
		mut := false
		if f.is("mut") {
			mut = true
			f.next()
		}
		name := f.tok
		f.expect('i')
		want := tBad
		if f.tok.kind != '=' {
			want = f.typeExpr()
		}
		f.expect('=')
		t := f.expr()
		if want != tBad && t != want {
			f.fail("let of another type")
		}
		f.locals = append(f.locals, local{start: name.start, end: name.end, t: t, mut: mut})
		return tBad
	case f.is("for"):
		f.next()
		k, v := f.tok, token{}
		f.expect('i')
		f.expect(',')
		v = f.tok
		f.expect('i')
		if !f.is("in") {
			f.fail("want in")
		}
		f.next()
		xs := f.expr()
		if xs < tSlice0 {
			f.fail("range over a non-slice")
			return tBad
		}
		mark := len(f.locals)
		f.locals = append(f.locals, local{start: k.start, end: k.end, t: tInt}, local{start: v.start, end: v.end, t: f.slices[xs-tSlice0]})
		f.block()
		f.locals = f.locals[:mark]
		return tBad
	case f.is("if"):
		f.next()
		if f.expr() != tBool {
			f.fail("condition is not bool")
		}
		f.block()
		if f.is("else") {
			f.next()
			f.block()
		}
		return tBad
	}
	// An assignment or an expression.
	if f.tok.kind == 'i' {
		save, name := f.pos, f.tok
		f.next()
		if f.tok.kind == '=' {
			f.next()
			l := f.lookup(name.start, name.end)
			if l < 0 || !f.locals[l].mut {
				f.fail("assignment to an immutable or unknown name")
				return tBad
			}
			f.typed++ // the assigned name is an expression of its type
			if f.expr() != f.locals[l].t {
				f.fail("assignment of another type")
			}
			return tBad
		}
		f.pos, f.tok = save, name
	}
	return f.expr()
}

// prec is a binary operator's precedence, 0 for none.
func prec(k byte) int {
	switch k {
	case opOr:
		return 1
	case opAnd:
		return 2
	case opEq, opNe, '<', '>', opLe, opGe:
		return 3
	case '+', '-':
		return 4
	case '*', '/', '%':
		return 5
	}
	return 0
}

func (f *floor) expr() typ { return f.binary(1) }

// binary types the operators of precedence min and above (precedence
// climbing), each operation once.
func (f *floor) binary(min int) typ {
	x := f.unary()
	for !f.tok.nl {
		op := f.tok.kind
		p := prec(op)
		if p < min || p == 0 {
			return x
		}
		f.next()
		y := f.binary(p + 1)
		switch {
		case p <= 2:
			if x != tBool || y != tBool {
				f.fail("logical operands are not bool")
			}
			x = tBool
		case p == 3:
			if x != y {
				f.fail("compared operands differ")
			}
			x = tBool
		default:
			if x != y || x != tInt && !(x == tString && op == '+') {
				f.fail("arithmetic operands differ")
			}
		}
		f.typed++
	}
	return x
}

// unary types a primary and its calls and indexes.
func (f *floor) unary() typ {
	tk := f.tok
	switch tk.kind {
	case 'n':
		f.next()
		f.typed++
		return tInt
	case 's':
		f.next()
		f.typed++
		return tString
	case '(':
		f.next()
		t := f.expr()
		f.expect(')')
		f.typed++
		return t
	case 'i':
	default:
		f.fail("want an expression")
		f.next()
		return tBad
	}
	f.next()
	var t typ
	if f.tok.kind == '(' && !f.tok.nl {
		t = f.call(tk)
	} else {
		l := f.lookup(tk.start, tk.end)
		if l < 0 {
			f.fail("unknown name %s", f.src[tk.start:tk.end])
			return tBad
		}
		t = f.locals[l].t
		f.typed++
	}
	for f.tok.kind == '[' && !f.tok.nl {
		f.next()
		if f.expr() != tInt {
			f.fail("index is not int")
		}
		f.expect(']')
		if t < tSlice0 {
			f.fail("index of a non-slice")
			return tBad
		}
		t = f.slices[t-tSlice0]
		f.typed++
	}
	return t
}

// call types name(args): len, or a function of the package, its bracket
// parameters bound by the arguments.
func (f *floor) call(name token) typ {
	f.next()
	if string(f.src[name.start:name.end]) == "len" {
		a := f.expr()
		f.expect(')')
		if a != tString && a < tSlice0 {
			f.fail("len of %d", a)
		}
		f.typed++
		return tInt
	}
	i, ok := f.fns[string(f.src[name.start:name.end])]
	if !ok {
		f.fail("unknown function %s", f.src[name.start:name.end])
		return tBad
	}
	sg := f.sigs[i]
	for k := 0; k < sg.tparams; k++ {
		f.bind[k] = tBad
	}
	p := 0
	for ; f.tok.kind != ')' && f.err == nil; p++ {
		a := f.expr()
		if p >= len(sg.params) || !f.unify(sg.params[p], a) {
			f.fail("argument %d of another type", p)
		}
		if f.tok.kind == ',' {
			f.next()
		}
	}
	if p != len(sg.params) {
		f.fail("%d arguments for %d parameters", p, len(sg.params))
	}
	f.expect(')')
	f.typed += 2 // the callee, at its instance, and the call
	return f.subst(sg.result)
}

// unify binds the bracket parameters of p to a's parts.
func (f *floor) unify(p, a typ) bool {
	switch {
	case p >= tParam0 && p < tSlice0:
		k := p - tParam0
		if f.bind[k] == tBad {
			f.bind[k] = a
			return true
		}
		return f.bind[k] == a
	case p >= tSlice0:
		return a >= tSlice0 && f.unify(f.slices[p-tSlice0], f.slices[a-tSlice0])
	}
	return p == a
}

func (f *floor) subst(t typ) typ {
	switch {
	case t >= tParam0 && t < tSlice0:
		return f.bind[t-tParam0]
	case t >= tSlice0:
		return f.slice(f.subst(f.slices[t-tSlice0]))
	}
	return t
}

// checkFloor checks src: the count of functions and of typed expressions,
// or the first type error.
func checkFloor(src []byte) (int, int, error) {
	f := &floor{src: src, fns: make(map[string]int)}
	f.signatures()
	if f.err == nil {
		f.bodies()
	}
	return len(f.sigs), f.typed, f.err
}
