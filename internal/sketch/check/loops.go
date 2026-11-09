package check

import (
	"ergo.local/prototype/internal/sketch/std"
	"ergo.local/prototype/internal/sketch/syntax"
)

// One for (check-v1 stage D2; #196 fourth round, D-D4):
//
//   - `for x in e` binds the element and `for i, x in e` the position and
//     the element. The domains are a slice (position int), a Map (one
//     binding the value; two the key and the value), a prelude Range[T]
//     over an integer T and a prelude Seq[T] (one binding each). Any other
//     domain is S108, strings, integers and pointers included, with the
//     Ergo form to write.
//   - `a..b` is a Range[T] wherever it appears, for one integer T (D-A1
//     types the literals).
//   - A loop binding is immutable and must be read (S110; `_` ignores). A
//     single-name binder follows the one binder rule (namePattern).
//   - A refused domain or a refused second binding still binds its names
//     with the invalid type, so their uses add no diagnostic (D3).

// preludeOrigin returns the generic prelude type name (Range, Seq) as its
// origin, or nil inside the prelude itself, which sees only the universe.
func (c *checker) preludeOrigin(name string) *Named {
	tn, ok := c.universe.names[name].(*TypeName)
	if !ok || tn.Pkg == nil || tn.Pkg.Path != std.PreludePath {
		return nil
	}
	n, _ := tn.Type.(*Named)
	return n
}

// preludeElem returns T when t is the prelude's name[T] (Range or Seq).
func (c *checker) preludeElem(t Type, name string) (Type, bool) {
	n, ok := t.(*Named)
	if !ok || n.Orig == nil || len(n.TArgs) != 1 {
		return nil, false
	}
	origin := c.preludeOrigin(name)
	if origin == nil || n.Orig.Obj != origin.Obj {
		return nil, false
	}
	return n.TArgs[0], true
}

// rangeExpr checks `a..b`: the bounds are integers of one type, a literal
// bound takes the other's type and two literals take int (196u rules 2 and
// 4), or the T of an expected Range[T]. Its value is a Range[T] from a up to
// b, b excluded.
func (c *checker) rangeExpr(x *syntax.BinaryExpr, hint Type) operand {
	origin := c.preludeOrigin("Range")
	if origin == nil {
		c.errorf(x, CodeType, "the prelude cannot use a range")
		return bad(x)
	}
	elemHint, _ := c.preludeElem(hint, "Range")
	lo, hi := c.operands(x.X, x.Y, elemHint)
	if lo.invalid() || hi.invalid() || !c.matchTypes(x, &lo, &hi) {
		return bad(x)
	}
	t := c.typeOf(&lo)
	if isInvalid(t) || isInvalid(c.typeOf(&hi)) {
		return bad(x)
	}
	if !isInteger(under(t)) {
		c.errorf(x, CodeType, "a range's bounds are integers, not %s", t)
		return bad(x)
	}
	return operand{mode: modeValue, typ: c.instantiate(origin, []Type{t})}
}

// rangeStmt checks `for x in e` and `for i, x in e`. The decided parser
// always declares the bindings (Tok DEFINE); an assignment form is not
// produced by it (D-A5).
func (c *checker) rangeStmt(s *syntax.RangeStmt) {
	if s.Tok != syntax.DEFINE {
		c.undecided(s, "a for header that assigns")
		return
	}
	done := c.openScope()
	defer done()
	x := c.valueExpr(s.X, nil)
	l := c.loopDomain(s, x)
	pos, elem := l.Pos, l.Elem
	if l.Domain == 0 {
		pos, elem = invalid, invalid
	}
	if s.Key != nil && pos == nil {
		// Range and Seq have no position: the second binding is refused
		// and binds invalid (D3).
		c.errorf(s.Key, CodeType, "a for over %s binds one name, the element: for x in …", x.typ)
		pos = invalid
	}
	// for x in xs binds the element; for i, x in xs the position first.
	// A binding may be a pattern, matched irrefutably (spec D-B5).
	c.bindLoop(s.Key, pos, l)
	c.bindLoop(s.Value, elem, l)
	if l.Domain != 0 {
		c.loop(s, l)
	}
	pop := c.push(brLoop)
	c.block(s.Body)
	pop()
}

// loopDomain classifies a for's domain; Domain 0 is a refused domain, with
// its diagnostic.
func (c *checker) loopDomain(s *syntax.RangeStmt, x operand) *Loop {
	if x.invalid() {
		return &Loop{}
	}
	if t, ok := c.preludeElem(x.typ, "Range"); ok {
		if !isInteger(under(t)) {
			c.errorf(s.X, CodeType, "a for over a Range counts integers, not %s", t)
			return &Loop{}
		}
		return &Loop{Domain: LoopRange, Elem: t}
	}
	if t, ok := c.preludeElem(x.typ, "Seq"); ok {
		return &Loop{Domain: LoopSeq, Elem: t}
	}
	switch u := under(x.typ).(type) {
	case *Slice:
		return &Loop{Domain: LoopSlice, Elem: u.Elem, Pos: Typ[Int]}
	case *Array:
		// Go's range over an array (196y): the slice domain's form.
		return &Loop{Domain: LoopSlice, Elem: u.Elem, Pos: Typ[Int]}
	case *Map:
		return &Loop{Domain: LoopMap, Elem: u.Elem, Pos: u.Key}
	case *Pointer:
		c.errorf(s.X, CodeType, "a for does not iterate through a pointer: write for x in *p")
	case *Basic:
		switch {
		case isString(u):
			// A string's runes, at their byte offsets, as Go's range (L4).
			return &Loop{Domain: LoopString, Elem: Typ[Int32], Pos: Typ[Int]} // rune is int32
		case isInteger(u):
			c.errorf(s.X, CodeType, "%s is not iterable: count with for i in 0..n", x.typ)
		default:
			c.errorf(s.X, CodeType, "cannot loop over %s: a for takes a slice, a Map, a string, a Range or a Seq", x.typ)
		}
	default:
		c.errorf(s.X, CodeType, "cannot loop over %s: a for takes a slice, a Map, a string, a Range or a Seq", x.typ)
	}
	return &Loop{}
}

// bindLoop binds one for binding against t: a name, unless the single-name
// rule makes it a pattern (namePattern), or a pattern matched irrefutably.
// Its bindings join the loop's (Info.Loops).
func (c *checker) bindLoop(p syntax.Expr, t Type, l *Loop) {
	if p == nil {
		return
	}
	if id, ok := p.(*syntax.Ident); ok && !c.namePattern(id) {
		if v := c.declareLocal(id, t); v != nil {
			l.Bindings = append(l.Bindings, v)
		}
		return
	}
	c.forPattern(p, t, l)
}
