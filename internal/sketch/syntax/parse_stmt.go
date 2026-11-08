package syntax

// ---------------------------------------------------------------- blocks and statements

func (p *parser) parseBlock() *BlockStmt {
	p.enter()
	defer p.leave()
	b := &BlockStmt{Lbrace: p.want(LBRACE)}
	saved := p.exprLev
	p.exprLev = 0
	b.List = p.parseStmtList()
	p.exprLev = saved
	if p.tok.tok != RBRACE {
		p.errorf("expected \"}\", found %s", p.found())
	}
	b.Rbrace = p.tok.pos
	p.next()
	return b
}

// parseStmtList parses statements up to a closing brace or the next case.
func (p *parser) parseStmtList() []Stmt {
	var list []Stmt
	for p.tok.tok != RBRACE && p.tok.tok != EOF && p.tok.tok != CASE && p.tok.tok != DEFAULT {
		if p.tok.tok == SEMICOLON {
			p.next()
			continue
		}
		list = append(list, p.parseStmt())
		switch p.tok.tok {
		case SEMICOLON:
			p.next()
		case RBRACE:
		default:
			p.errorf("expected end of statement, found %s", p.found())
		}
	}
	return list
}

// parseStmt parses one statement (#196 sketch: Statement). if, match and
// for at the start of a statement form the statement: no operator, selector
// or call continues them (Rust's rule; D1.8).
func (p *parser) parseStmt() Stmt {
	p.enter()
	defer p.leave()
	switch p.tok.tok {
	case LET:
		return &DeclStmt{Decl: p.parseLetDecl(nil, false).(*VarDecl)}
	case RETURN:
		s := &ReturnStmt{Return: p.tok.pos}
		p.next()
		switch p.tok.tok {
		case SEMICOLON, RBRACE, CASE, DEFAULT, EOF:
		default:
			s.Results = []Expr{p.parseExpr()}
		}
		return s
	case BREAK, CONTINUE:
		s := &BranchStmt{TokPos: p.tok.pos, Tok: p.tok.tok}
		p.next()
		if p.tok.tok == IDENT {
			p.habit(p.tok.pos, len(p.tok.lit), habitLabel)
		}
		return s
	case ASSERT:
		s := &AssertStmt{Assert: p.tok.pos}
		p.next()
		s.X = p.parseExpr()
		return s
	case IF:
		return p.parseIf()
	case MATCH:
		return p.parseMatch()
	case FOR:
		return p.parseForStmt()
	case LBRACE:
		p.errorf("unexpected \"{\": a block follows a header such as if, for or with")
	case IDENT:
		if p.peek(1).tok == COLON {
			p.habit(p.tok.pos, len(p.tok.lit)+1, habitLabel)
		}
	case TYPE, FUNC, EXTERN, TEST, IMPORT:
		if p.tok.tok != FUNC || p.peek(1).tok != LPAREN {
			p.errorf("unexpected %s in a statement list: declarations stand at the top level", p.found())
		}
	}
	if p.tok.tok.IsReserved() && !(p.tok.tok == GO && p.peek(1).tok == PERIOD) {
		p.errorf("unexpected %s", p.found())
	}
	x := p.parseExpr()
	switch p.tok.tok {
	case ASSIGN, ADD_ASSIGN, SUB_ASSIGN, MUL_ASSIGN, QUO_ASSIGN, REM_ASSIGN,
		AND_ASSIGN, OR_ASSIGN, XOR_ASSIGN, SHL_ASSIGN, SHR_ASSIGN, AND_NOT_ASSIGN:
		s := &AssignStmt{Lhs: []Expr{x}, TokPos: p.tok.pos, Tok: p.tok.tok}
		p.next()
		s.Rhs = []Expr{p.parseExpr()}
		return s
	}
	return &ExprStmt{X: x}
}

// parseForStmt parses `for { }`, `for cond { }` and `for P {, P} in e { }`.
// A Go header (three clauses, range) gets its habit message first.
func (p *parser) parseForStmt() Stmt {
	pos := p.want(FOR)
	for i := 0; ; i++ {
		t := p.peek(i)
		if t.tok == RANGE {
			p.habit(t.pos, int(t.end-t.pos), habitRange)
		}
		if t.tok == SEMICOLON && t.lit == ";" {
			p.habit(pos, 3, habitForClause)
		}
		if t.tok == SEMICOLON || t.tok == EOF || t.tok == LBRACE {
			break
		}
	}
	if p.tok.tok == LBRACE {
		return &ForStmt{For: pos, Body: p.parseBlock()}
	}
	var pats []Expr
	if p.attempt(func() {
		pats = p.parsePatternList()
		if p.tok.tok != IN {
			p.errorf("not a range header")
		}
	}) {
		s := &RangeStmt{For: pos, Tok: DEFINE, Range: p.want(IN)}
		switch len(pats) {
		case 1:
			s.Value = pats[0]
		case 2:
			s.Key, s.Value = pats[0], pats[1]
		default:
			p.errorAt(pats[2].Pos(), int(pats[2].End()-pats[2].Pos()), "for binds an element, or an index and an element")
		}
		s.X = p.parseHeader()
		if p.tok.tok != LBRACE {
			p.errorf("expected \"{\" after the range, found %s", p.found())
		}
		s.Body = p.parseBlock()
		return s
	}
	s := &ForStmt{For: pos}
	s.Cond = p.parseHeader()
	if p.tok.tok == DEFINE || p.tok.tok == ASSIGN {
		p.habit(pos, 3, habitForClause)
	}
	if p.tok.tok != LBRACE {
		p.errorf("expected \"{\" after the for condition, found %s", p.found())
	}
	s.Body = p.parseBlock()
	return s
}

// ---------------------------------------------------------------- patterns

func (p *parser) parsePatternList() []Expr {
	list := []Expr{p.parsePattern()}
	for p.got(COMMA) {
		list = append(list, p.parsePattern())
	}
	return list
}

// parsePattern parses `PatternCore [ as name ]` (#196, D1.9).
func (p *parser) parsePattern() Expr {
	p.enter()
	defer p.leave()
	x := p.parsePatternCore()
	if p.tok.tok == AS {
		as := p.tok.pos
		p.next()
		return &AsPattern{X: x, As: as, Name: p.parseIdent()}
	}
	return x
}

// parsePatternCore parses `_`, a name, a literal (a number may be negated),
// `TypeName [TypeArgs] [{ FieldPatterns }]` or a parenthesised tuple. A
// pattern is the tree of the value it matches.
func (p *parser) parsePatternCore() Expr {
	switch p.tok.tok {
	case IDENT, GO:
		if !p.startsTypeName() {
			break
		}
		x := p.parseQualifiedName()
		if p.tok.tok == LBRACK {
			x = p.parseTypeArgs(x)
		}
		if p.tok.tok == LBRACE {
			return p.parseRecordPattern(x)
		}
		return x
	case INT, FLOAT, DURATION, STRING, RUNE:
		return p.basicLit()
	case SUB:
		switch p.peek(1).tok {
		case INT, FLOAT, DURATION:
			pos := p.tok.pos
			p.next()
			return &UnaryExpr{OpPos: pos, Op: SUB, X: p.basicLit()}
		}
	case LPAREN:
		lparen := p.tok.pos
		p.next()
		saved := p.exprLev
		p.exprLev = 1
		first := p.parsePattern()
		if p.tok.tok != COMMA {
			p.exprLev = saved
			return &ParenExpr{Lparen: lparen, X: first, Rparen: p.want(RPAREN)}
		}
		p.refuseSingleton()
		t := &TupleExpr{Lparen: lparen, Elts: []Expr{first}}
		for p.got(COMMA) && p.tok.tok != RPAREN {
			t.Elts = append(t.Elts, p.parsePattern())
		}
		p.exprLev = saved
		t.Rparen = p.want(RPAREN)
		return t
	}
	if p.tok.tok == NEEDS {
		p.errorf("expected a pattern, found \"needs\": %s", needsKeywordText)
		return nil
	}
	p.errorf("expected a pattern, found %s", p.found())
	return nil
}

// parseRecordPattern parses `{ FieldPattern, … }` after a type name: keyed
// `name: Pattern` or positional patterns, not mixed.
func (p *parser) parseRecordPattern(typ Expr) *CompositeLit {
	lit := &CompositeLit{Type: typ, Lbrace: p.want(LBRACE)}
	saved := p.exprLev
	p.exprLev = 1
	keyed, positional := false, false
	for p.tok.tok != RBRACE {
		var elt Expr
		if p.tok.tok == IDENT && p.peek(1).tok == COLON {
			key := p.parseIdent()
			colon := p.want(COLON)
			elt = &KeyValueExpr{Key: key, Colon: colon, Value: p.parsePattern()}
			keyed = true
		} else {
			elt = p.parsePattern()
			positional = true
		}
		if keyed && positional {
			p.errorAt(elt.Pos(), int(elt.End()-elt.Pos()), "a record pattern mixes keyed and positional fields")
		}
		lit.Elts = append(lit.Elts, elt)
		if !p.got(COMMA) {
			break
		}
	}
	p.exprLev = saved
	if p.tok.tok != RBRACE {
		p.errorf("expected \",\" or \"}\" in a record pattern, found %s", p.found())
	}
	lit.Rbrace = p.tok.pos
	p.next()
	return lit
}
