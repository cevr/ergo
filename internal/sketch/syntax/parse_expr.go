package syntax

import "strings"

// ---------------------------------------------------------------- expressions

// parseExpr parses `[ try ] CatchExpr` (#196 sketch: Expression). try
// stands only here, at the start of an expression, and covers the whole
// expression to its right, a catch included.
func (p *parser) parseExpr() Expr {
	p.enter()
	defer p.leave()
	if p.tok.tok == TRY {
		pos := p.tok.pos
		p.next()
		switch p.tok.tok {
		case SEMICOLON, RBRACE, RPAREN, RBRACK, COMMA, COLON, EOF, CATCH:
			p.errorAt(pos, 3, "try needs an operand")
		}
		return &TryExpr{Try: pos, X: p.parseCatchExpr()}
	}
	return p.parseCatchExpr()
}

// parseCatchExpr parses `Range [ catch { cases } ]`.
func (p *parser) parseCatchExpr() Expr {
	x := p.parseRange()
	if p.tok.tok == CATCH {
		return p.parseCatch(x)
	}
	return x
}

// parseRange parses `Binary [ .. Binary ]`: `..` binds looser than `||`
// and does not chain (Rust's rule; #196, D1.4).
func (p *parser) parseRange() Expr {
	x := p.parseBinary(1)
	if p.tok.tok != DOTDOT {
		return x
	}
	pos := p.tok.pos
	p.enter()
	defer p.leave()
	p.next()
	y := p.parseBinary(1)
	if p.tok.tok == DOTDOT {
		p.errorf("a range does not chain: write the bounds of one range")
	}
	return &BinaryExpr{X: x, OpPos: pos, Op: DOTDOT, Y: y}
}

// parseBinary parses a binary expression of precedence prec or tighter. Each
// operator wraps the tree built so far in one more level: a long chain is
// as deep for every recursive consumer as the same number of parentheses.
// The loop retains the tree's height (see measure), so an operand that is
// itself deep, such as a parenthesized chain, counts in full.
func (p *parser) parseBinary(prec int) Expr {
	o := p.nest
	saved := p.measure()
	x := p.parseUnary()
	h := p.height(saved)
	defer func() { p.nest = o; p.reach(o + h) }()
	for {
		op := p.tok.tok
		opPrec := op.Precedence()
		if opPrec < prec || opPrec == 0 {
			return x
		}
		if o+h+1 > MaxNesting {
			p.tooDeep()
		}
		p.enter()
		pos := p.tok.pos
		p.next()
		inner := p.measure()
		y := p.parseBinary(opPrec + 1)
		h = max(h, p.height(inner)) + 1
		p.leave()
		x = &BinaryExpr{X: x, OpPos: pos, Op: op, Y: y}
	}
}

// parseUnary parses a unary expression. A prefix operator opens its level
// before it is consumed, so past the limit the S001 is at the operator.
func (p *parser) parseUnary() Expr {
	switch p.tok.tok {
	case ADD, SUB, NOT, XOR, AND:
		pos, op := p.tok.pos, p.tok.tok
		p.enter()
		p.next()
		x := p.parseUnary()
		p.leave()
		return &UnaryExpr{OpPos: pos, Op: op, X: x}
	case MUL:
		pos := p.tok.pos
		p.enter()
		p.next()
		x := p.parseUnary()
		p.leave()
		return &StarExpr{Star: pos, X: x}
	case TRY:
		p.errorf("unexpected \"try\": try stands at the start of an expression")
	}
	return p.parsePrimaryExpr()
}

// parseCatch parses `catch { case P, Q: … }`.
func (p *parser) parseCatch(x Expr) *CatchExpr {
	c := &CatchExpr{X: x, Catch: p.want(CATCH)}
	if p.tok.tok != LBRACE {
		p.errorf("expected \"{\" after catch, found %s", p.found())
	}
	c.Lbrace = p.tok.pos
	p.next()
	saved := p.exprLev
	p.exprLev = 0
	for p.tok.tok == CASE {
		arm := &CatchArm{Case: p.tok.pos}
		p.next()
		arm.Types = p.parseCaseList()
		arm.Colon = p.want(COLON)
		arm.Body = p.parseStmtList()
		c.Arms = append(c.Arms, arm)
	}
	p.exprLev = saved
	if p.tok.tok != RBRACE {
		p.errorf("expected \"case\" or \"}\", found %s", p.found())
	}
	c.Rbrace = p.tok.pos
	p.next()
	return c
}

// parsePrimaryExpr parses an operand and its selectors, indexes, calls and
// literals. Each one wraps the tree built so far in one more level; the
// loop retains the tree's height as parseBinary does.
func (p *parser) parsePrimaryExpr() Expr {
	o := p.nest
	saved := p.measure()
	x := p.parseOperand()
	h := p.height(saved)
	inner := 0
	defer func() { p.nest = o; p.reach(o + h) }()
	for {
		if p.nest > o {
			// The previous link's children are parsed: fold their height.
			h = max(h, p.height(inner)) + 1
			p.leave()
		}
		if p.tok.tok == LBRACE && !p.isLiteralType(x) {
			// The brace opens a body (a control clause header, a block
			// after a statement): it is a sibling, never a link.
			p.headerLiteral(x)
			return x
		}
		switch p.tok.tok {
		case PERIOD, LBRACK, LPAREN, LBRACE:
			if o+h+1 > MaxNesting {
				p.tooDeep()
			}
			p.enter()
			inner = p.measure()
		}
		switch p.tok.tok {
		case PERIOD:
			p.next()
			switch p.tok.tok {
			case IDENT:
				x = &SelectorExpr{X: x, Sel: p.parseIdent()}
			case LPAREN:
				if p.peek(1).tok == TYPE {
					p.habit(p.tok.pos, 1, habitTypeSwitch)
				}
				p.habit(p.tok.pos, 1, habitTypeAssert)
			default:
				p.errorf("expected a name after \".\", found %s", p.found())
			}
		case LBRACK:
			x = p.parseIndexOrSlice(x)
			if mapType(x) != x && (p.tok.tok != LBRACE || !p.isLiteralType(x)) {
				// `Map[K, V]` is the map type (Parser rule 8): a value
				// only as a composite literal's type (round-4 review m1).
				p.errorAt(x.Pos(), len("Map"), notAValue)
			}
		case LPAREN:
			x = p.parseCall(x)
		case LBRACE:
			x = p.parseCompositeLit(mapType(x))
		default:
			return x
		}
	}
}

// isLiteralType reports whether x, followed by `{`, starts a composite
// literal: go/parser's rule, including that a plain or qualified or
// instantiated type name in a control clause header does not.
func (p *parser) isLiteralType(x Expr) bool {
	switch t := x.(type) {
	case *Ident, *SelectorExpr, *IndexExpr:
		if s, ok := t.(*SelectorExpr); ok {
			if _, ok := s.X.(*Ident); !ok {
				return false
			}
		}
		return p.exprLev >= 0
	case *ArrayType, *MapType:
		return true
	}
	return false
}

// headerLiteral reports a composite literal written bare in a control
// clause header, `with Cfg{Port: 1} { … }`: the brace there opens the
// body, so without this the body's `Port:` would read as a label. A body
// that starts on the brace's line never starts with `name:`, nor with `}`
// followed by a `{` (round-4 review O1), nor with a positional element
// (positionalElement). A `name:` on the next line stays the label habit's.
func (p *parser) headerLiteral(x Expr) {
	if p.exprLev >= 0 || strings.Contains(p.src[p.tok.end:p.peek(1).pos], "\n") {
		return
	}
	p.exprLev = 0
	literal := p.isLiteralType(x)
	p.exprLev = -1
	if !literal {
		return
	}
	next, after := p.peek(1).tok, p.peek(2).tok
	if next == IDENT && after == COLON || next == RBRACE && after == LBRACE || p.positionalElement() {
		name := p.src[x.Pos():x.End()]
		p.errorAt(x.Pos(), int(x.End()-x.Pos()), "a composite literal in a header needs parentheses: write (%s{…}), or bind it with let first", name)
	}
}

// positionalElement reports whether the tokens after the brace p.tok
// begin a positional literal, `Point{1, 2}`, `Point{-1, 2}`,
// `Point{one(), 2}`, `Point{a.b, c[0]}` or `Box{x} {`: one complete element
// on the brace's line (a unary, call, selector, index, literal or
// parenthesised expression, any operator between them, brackets
// balanced) followed by a comma, or by the literal's `}` and the body's
// `{`. No statement starts so: an assignment has one left side, and every
// other statement that holds a comma starts with a keyword (return, let,
// case) or a label's `name:`. It scans only the brace's line (round 7
// review m1).
func (p *parser) positionalElement() bool {
	depth := 0
	prev := p.tok
	for i := 1; ; i++ {
		t := p.peek(i)
		if t.tok == EOF || strings.Contains(p.src[prev.end:t.pos], "\n") {
			return false
		}
		prev = t
		switch {
		case t.tok > keywordBeg && t.tok < keywordEnd && t.tok != FUNC:
			return false
		case t.tok == LPAREN || t.tok == LBRACK || t.tok == LBRACE:
			depth++
		case t.tok == RPAREN || t.tok == RBRACK:
			depth--
		case t.tok == RBRACE:
			if depth == 0 {
				after := p.peek(i + 1)
				return i > 1 && after.tok == LBRACE && !strings.Contains(p.src[t.end:after.pos], "\n")
			}
			depth--
		case depth > 0:
		case t.tok == COMMA:
			return i > 1
		case t.tok == COLON || t.tok == SEMICOLON || t.tok == ASSIGN || t.tok == DEFINE || t.tok >= ADD_ASSIGN && t.tok <= AND_NOT_ASSIGN:
			return false
		}
		if depth < 0 {
			return false
		}
	}
}

func (p *parser) parseOperand() Expr {
	switch p.tok.tok {
	case IDENT:
		return p.parseIdent()
	case GO:
		if p.peek(1).tok == PERIOD {
			id := &Ident{NamePos: p.tok.pos, Name: "go"}
			p.next()
			return id
		}
	case INT, FLOAT, DURATION, STRING, RUNE:
		return p.basicLit()
	case STRING_HEAD:
		return p.parseInterpolated()
	case LPAREN:
		p.enter()
		defer p.leave()
		lparen := p.tok.pos
		p.next()
		p.exprLev++
		x := p.parseExpr()
		if p.tok.tok != COMMA {
			p.exprLev--
			return &ParenExpr{Lparen: lparen, X: x, Rparen: p.want(RPAREN)}
		}
		p.refuseSingleton()
		t := &TupleExpr{Lparen: lparen, Elts: []Expr{x}}
		for p.got(COMMA) && p.tok.tok != RPAREN {
			t.Elts = append(t.Elts, p.parseExpr())
		}
		p.exprLev--
		t.Rparen = p.want(RPAREN)
		return t
	case FUNC:
		// In value position `func` always starts a literal, whose
		// signature takes the declaration spelling, `func() needs A | B
		// { … }`. A type is never a value, so there is no fallback to a
		// function type: an error inside the signature is reported where
		// it stands, and a signature with no block is a bare type.
		pos, end := p.tok.pos, p.tok.end
		t := p.parseFuncType(ctxDecl)
		if p.tok.tok != LBRACE {
			p.errorAt(pos, int(end-pos), notAValue)
		}
		p.funcLitParams(t.Params)
		return &FuncLit{Type: t, Body: p.parseBlock()}
	case LBRACK:
		// A slice type is a value only as the type of a composite
		// literal: `[]int{1, 2}`. `[]int` alone, or `[]byte(s)`, is a
		// bare type.
		pos, end := p.tok.pos, p.tok.end
		t := p.parseType(ctxList)
		if p.tok.tok != LBRACE {
			p.errorAt(pos, int(end-pos), notAValue)
		}
		return t
	case ESCAPING:
		p.errorf(notAValue)
	case IF:
		return p.parseIf()
	case MATCH:
		return p.parseMatch().(Expr)
	case WITH:
		return p.parseWith()
	case CONTEXT:
		return p.parseContextLit()
	}
	p.errorf("expected an expression, found %s", p.found())
	return nil
}

func (p *parser) basicLit() *BasicLit {
	b := &BasicLit{ValuePos: p.tok.pos, Kind: p.tok.tok, Value: p.tok.lit, ValueEnd: p.tok.end}
	p.next()
	return b
}

// parseInterpolated parses `"a \(x) b"` from its STRING_HEAD.
func (p *parser) parseInterpolated() *InterpolatedString {
	s := &InterpolatedString{Texts: []*BasicLit{p.basicLit()}}
	saved := p.exprLev
	p.exprLev = 1
	for {
		s.Exprs = append(s.Exprs, p.parseExpr())
		switch p.tok.tok {
		case STRING_MID:
			s.Texts = append(s.Texts, p.basicLit())
		case STRING_TAIL:
			s.Texts = append(s.Texts, p.basicLit())
			p.exprLev = saved
			return s
		default:
			p.errorf("expected \")\" to close the interpolation, found %s", p.found())
		}
	}
}

func (p *parser) parseIndexOrSlice(x Expr) Expr {
	lbrack := p.tok.pos
	p.next()
	p.exprLev++
	defer func() { p.exprLev-- }()
	if p.tok.tok == RBRACK {
		p.errorf("expected an index, found %s", p.found())
	}
	var index [3]Expr
	if p.tok.tok != COLON {
		index[0] = p.parseIndexValue()
	}
	if p.tok.tok == COLON {
		s := &SliceExpr{X: x, Lbrack: lbrack, Low: index[0]}
		p.next()
		if p.tok.tok != COLON && p.tok.tok != RBRACK {
			s.High = p.parseExpr()
		}
		if p.tok.tok == COLON {
			s.Slice3 = true
			p.next()
			if s.High == nil {
				p.errorf("middle index required in a 3-index slice")
			}
			if p.tok.tok == RBRACK {
				p.errorf("final index required in a 3-index slice")
			}
			s.Max = p.parseExpr()
		}
		s.Rbrack = p.want(RBRACK)
		return s
	}
	ix := &IndexExpr{X: x, Lbrack: lbrack, Indices: []Expr{index[0]}}
	for p.got(COMMA) {
		if p.tok.tok == RBRACK {
			break
		}
		ix.Indices = append(ix.Indices, p.parseIndexValue())
	}
	ix.Rbrack = p.want(RBRACK)
	return ix
}

// parseIndexValue parses an index or a type argument: a type that cannot
// start an expression (`*T`, `func(T) U`) is read as a type.
// notAValue is the message for a bare type in value position: the decided
// Primary has no type operand (round-3 review M1, m3).
const notAValue = "a type is not a value; write a literal or a conversion"

// parseIndexValue parses one index, or one type argument of an
// instantiation: `Map[string, []int]{}`, `Option[func() int]`. A type is
// taken only when a `,` or `]` follows it; else the index is an expression.
func (p *parser) parseIndexValue() Expr {
	switch {
	case p.tok.tok == MUL, p.tok.tok == LBRACK, p.tok.tok == FUNC, p.tok.tok == ESCAPING:
	case p.tok.tok == IDENT && p.tok.lit == "Map" && p.peek(1).tok == LBRACK:
	default:
		return p.parseExpr()
	}
	if p.exprLev > 0 {
		start := p.i
		var t Expr
		if p.attempt(func() { t = p.parseType(ctxList) }) && (p.tok.tok == COMMA || p.tok.tok == RBRACK) {
			return t
		}
		p.backtrack(start)
	}
	return p.parseExpr()
}

func (p *parser) parseCall(fun Expr) *CallExpr {
	c := &CallExpr{Fun: fun, Lparen: p.want(LPAREN)}
	p.exprLev++
	for p.tok.tok != RPAREN {
		c.Args = append(c.Args, p.parseExpr())
		if p.tok.tok == ELLIPSIS {
			p.errorf("a call has no spread argument: pass the slice")
		}
		if !p.got(COMMA) {
			break
		}
	}
	p.exprLev--
	if p.tok.tok != RPAREN {
		p.errorf("expected \")\" or \",\", found %s", p.found())
	}
	c.Rparen = p.tok.pos
	p.next()
	// A trailing block opens on the call's line; in a control clause header
	// the `{` opens the body instead.
	if p.tok.tok == LBRACE && p.exprLev >= 0 {
		c.Trailing = p.parseBlock()
	}
	return c
}

func (p *parser) parseCompositeLit(typ Expr) *CompositeLit {
	p.enter()
	defer p.leave()
	lit := &CompositeLit{Type: typ, Lbrace: p.want(LBRACE)}
	saved := p.exprLev
	p.exprLev = 1
	keyed, positional := -1, -1
	for p.tok.tok != RBRACE {
		elt := p.parseElement()
		if _, ok := elt.(*KeyValueExpr); ok {
			keyed = len(lit.Elts)
		} else {
			positional = len(lit.Elts)
		}
		if keyed >= 0 && positional >= 0 {
			p.errorAt(elt.Pos(), int(elt.End()-elt.Pos()), "a composite literal mixes keyed and positional elements")
		}
		lit.Elts = append(lit.Elts, elt)
		if !p.got(COMMA) {
			break
		}
	}
	p.exprLev = saved
	if p.tok.tok != RBRACE {
		p.errorf("expected \",\" or \"}\" in a composite literal, found %s", p.found())
	}
	lit.Rbrace = p.tok.pos
	p.next()
	return lit
}

func (p *parser) parseElement() Expr {
	x := p.parseElementValue()
	if p.tok.tok == COLON {
		colon := p.tok.pos
		p.next()
		return &KeyValueExpr{Key: x, Colon: colon, Value: p.parseElementValue()}
	}
	return x
}

func (p *parser) parseElementValue() Expr {
	if p.tok.tok == LBRACE {
		return p.parseCompositeLit(nil)
	}
	return p.parseExpr()
}

// ---------------------------------------------------------------- if, match, with

// parseHeader parses the expression of an if, match or with header, where
// a `{` opens the body (Go's composite-literal rule).
func (p *parser) parseHeader() Expr {
	saved := p.exprLev
	p.exprLev = -1
	x := p.parseExpr()
	p.exprLev = saved
	return x
}

// noInit reports an init statement in an if or match header (Go's
// `if x := f(); c`, `if x += 1; c`): Ergo writes the let on the line
// before. Every assignment operator starts one.
func (p *parser) noInit() {
	switch p.tok.tok {
	case DEFINE, ASSIGN, ADD_ASSIGN, SUB_ASSIGN, MUL_ASSIGN, QUO_ASSIGN, REM_ASSIGN,
		AND_ASSIGN, OR_ASSIGN, XOR_ASSIGN, SHL_ASSIGN, SHR_ASSIGN, AND_NOT_ASSIGN:
	case SEMICOLON:
		if p.tok.lit != ";" {
			return
		}
	default:
		return
	}
	p.habit(p.tok.pos, int(p.tok.end-p.tok.pos), habitIfInit)
}

// parseIf parses `if c { … } [else if … | else { … }]`.
func (p *parser) parseIf() *IfStmt {
	s := &IfStmt{If: p.want(IF), ElsePos: NoPos}
	if p.tok.tok == LET || p.tok.tok == VAR {
		p.habit(p.tok.pos, int(p.tok.end-p.tok.pos), habitIfInit)
	}
	s.Cond = p.parseHeader()
	p.noInit()
	if p.tok.tok != LBRACE {
		p.errorf("expected \"{\" after the if condition, found %s", p.found())
	}
	s.Body = p.parseBlock()
	if p.tok.tok == ELSE {
		s.ElsePos = p.tok.pos
		p.next()
		switch p.tok.tok {
		case IF:
			// Each else if nests one level deeper in the tree.
			p.enter()
			s.Else = p.parseIf()
			p.leave()
		case LBRACE:
			s.Else = p.parseBlock()
		default:
			p.errorf("expected \"if\" or \"{\" after else, found %s", p.found())
		}
	}
	return s
}

// parseMatch parses `match e { case P: … }`. Every top-level pattern a
// literal or `_`, on a subject that is not a tuple, gives a value switch
// (SwitchStmt); any other match is a TypeSwitchStmt with one subject per
// tuple element (#196, D3).
func (p *parser) parseMatch() Stmt {
	pos := p.want(MATCH)
	subject := stripParens(p.parseHeader())
	p.noInit()
	if p.tok.tok != LBRACE {
		p.errorf("expected \"{\" after the match subject, found %s", p.found())
	}
	lbrace := p.tok.pos
	p.next()
	saved := p.exprLev
	p.exprLev = 0
	var clauses []*CaseClause
	for p.tok.tok == CASE {
		c := &CaseClause{Case: p.tok.pos}
		p.next()
		c.List = p.parseCaseList()
		if len(c.List) == 1 {
			if id, ok := c.List[0].(*Ident); ok && id.Name == "_" {
				c.List = nil
			}
		}
		c.Colon = p.want(COLON)
		c.Body = p.parseStmtList()
		clauses = append(clauses, c)
	}
	p.exprLev = saved
	if p.tok.tok != RBRACE {
		p.errorf("expected \"case\" or \"}\", found %s", p.found())
	}
	rbrace := p.tok.pos
	p.next()
	tuple, isTuple := subject.(*TupleExpr)
	if !isTuple && literalCases(clauses) {
		return &SwitchStmt{Switch: pos, Tag: subject, Lbrace: lbrace, Clauses: clauses, Rbrace: rbrace}
	}
	s := &TypeSwitchStmt{Switch: pos, Lbrace: lbrace, Clauses: clauses, Rbrace: rbrace}
	subjects := []Expr{subject}
	if isTuple {
		subjects = tuple.Elts
	}
	for _, x := range subjects {
		s.Subjects = append(s.Subjects, &TypeGuard{X: x})
	}
	return s
}

// literalCases reports whether the clauses hold a pattern and every pattern
// of every clause is a literal, optionally negated. A match with no literal
// pattern (no arm, or `_` arms only) is not on literal patterns, so it is a
// type match (grammar.md, Parser 2).
func literalCases(clauses []*CaseClause) bool {
	seen := false
	for _, c := range clauses {
		for _, x := range c.List {
			if u, ok := x.(*UnaryExpr); ok && u.Op == SUB {
				x = u.X
			}
			if _, ok := x.(*BasicLit); !ok {
				return false
			}
			seen = true
		}
	}
	return seen
}

// parseCaseList parses `Pattern {, Pattern}` up to the colon. A case item
// that goes on as an expression is S001 with a pointer to if.
func (p *parser) parseCaseList() []Expr {
	var list []Expr
	for {
		list = append(list, p.parsePattern())
		switch {
		case p.tok.tok == COMMA:
			p.next()
			continue
		case p.tok.tok == COLON:
			return list
		case p.tok.tok.Precedence() > 0, p.tok.tok == LPAREN, p.tok.tok == PERIOD, p.tok.tok == LBRACK, p.tok.tok == DOTDOT:
			p.errorf("a case lists patterns, not expressions; test a condition with `if`")
		}
		return list
	}
}

// parseWith parses `with E, F { body }` into the call shape
// `with(E, F) { body }` that the checker reads (#196, D3).
func (p *parser) parseWith() *CallExpr {
	pos := p.want(WITH)
	c := &CallExpr{Fun: &Ident{NamePos: pos, Name: "with"}, Lparen: NoPos, Rparen: NoPos}
	c.Args = append(c.Args, p.parseHeader())
	for p.got(COMMA) {
		c.Args = append(c.Args, p.parseHeader())
	}
	if len(c.Args) == 1 {
		c.Args[0] = stripParens(c.Args[0])
	}
	if p.tok.tok != LBRACE {
		p.errorf("expected \"{\" after the contexts of with, found %s", p.found())
	}
	c.Trailing = p.parseBlock()
	return c
}

// stripParens removes the parentheses around a header expression that
// reads the same without them (`with (W)`, `match (x)`; #196, D4), so the
// formatter's spelling has the same tree as the source.
func stripParens(x Expr) Expr {
	for {
		par, ok := x.(*ParenExpr)
		if !ok || !headerSafe(par.X) {
			return x
		}
		x = par.X
	}
}

// headerSafe reports whether x reads the same in a header without
// parentheses: no composite literal, function literal, trailing block or
// braced expression outside parentheses or brackets.
func headerSafe(x Expr) bool {
	switch x := x.(type) {
	case *Ident, *BasicLit, *InterpolatedString, *ParenExpr, *TupleExpr, *ContextLit:
		// A context literal starts with its keyword, so its braces are never
		// read as the body (D1.8).
		return true
	case *SelectorExpr:
		return headerSafe(x.X)
	case *IndexExpr:
		return headerSafe(x.X)
	case *SliceExpr:
		return headerSafe(x.X)
	case *CallExpr:
		return x.Trailing == nil && x.Lparen.IsValid() && headerSafe(x.Fun)
	case *StarExpr:
		return headerSafe(x.X)
	case *UnaryExpr:
		return headerSafe(x.X)
	case *BinaryExpr:
		return headerSafe(x.X) && headerSafe(x.Y)
	case *TryExpr:
		return headerSafe(x.X)
	}
	return false
}
